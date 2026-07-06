package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/akeemjenkins/ouracli/internal/cliauth"
	"github.com/akeemjenkins/ouracli/internal/envelope"
	"github.com/akeemjenkins/ouracli/internal/output"
)

// methodToken labels credentials that are a raw bearer token supplied
// out-of-band (--token-stdin or OURA_TOKEN), as opposed to an OAuth grant.
const methodToken = "token"

func newAuthCmd() *cobra.Command {
	auth := &cobra.Command{
		Use:   "auth",
		Short: "Manage Oura credentials (login, status, logout)",
		Long: "auth stores Oura credentials out-of-band from agent context, in the OS\n" +
			"keyring where available or an encrypted file otherwise. `login` runs the\n" +
			"OAuth2 browser flow; `--token-stdin` accepts a legacy bearer token.",
		Annotations: map[string]string{annStdout: "json", annExitCodes: "0,3"},
		RunE: func(cmd *cobra.Command, args []string) error {
			return envelope.New(envelope.KindUsage, "missing_subcommand",
				"auth requires a subcommand",
				"run 'oura auth login', 'oura auth status', or 'oura auth logout'")
		},
	}
	auth.AddCommand(newAuthLoginCmd(), newAuthStatusCmd(), newAuthLogoutCmd())
	return auth
}

type authLoginOpts struct {
	clientID     string
	clientSecret string
	scopes       string
	port         int
	tokenStdin   bool
	noBrowser    bool
	loginTimeout int
}

func newAuthLoginCmd() *cobra.Command {
	var opts authLoginOpts
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Authorize oura via OAuth2, or store a legacy bearer token",
		Long: "login runs Oura's OAuth2 authorization-code flow: it opens the consent\n" +
			"screen in your browser and captures the redirect on a local loopback\n" +
			"server. Register the redirect URI it prints (http://localhost:PORT/callback)\n" +
			"on your app first. Use --token-stdin to store a pre-existing bearer token\n" +
			"instead.",
		Args:        cobra.NoArgs,
		Annotations: map[string]string{annStdout: "json", annExitCodes: "0,2,3,4,6"},
		RunE:        func(cmd *cobra.Command, args []string) error { return runAuthLogin(cmd, &opts) },
	}
	f := cmd.Flags()
	f.StringVar(&opts.clientID, "client-id", "", "OAuth client ID from your Oura app (env OURA_CLIENT_ID)")
	f.StringVar(&opts.clientSecret, "client-secret", "", "OAuth client secret (env OURA_CLIENT_SECRET; avoids argv exposure; prompted securely if omitted on a TTY)")
	f.StringVar(&opts.scopes, "scopes", "", "scopes, comma or space separated; empty requests all scopes")
	f.IntVar(&opts.port, "port", 8989, "loopback port for the OAuth callback (must match the registered redirect URI)")
	f.BoolVar(&opts.tokenStdin, "token-stdin", false, "read a raw bearer token from stdin instead of running OAuth")
	f.BoolVar(&opts.noBrowser, "no-browser", false, "do not launch a browser; print the URL to open manually")
	f.IntVar(&opts.loginTimeout, "login-timeout", 300,
		"seconds to wait for the browser authorization before failing with a typed error")
	return cmd
}

// classifyLoginError turns OAuth-flow failures into envelopes whose reason and
// hint are precise enough for an LLM caller to relay the exact fix to a human.
func classifyLoginError(err error, opts *authLoginOpts) error {
	redirectURI := fmt.Sprintf("http://localhost:%d/callback", opts.port)
	var rejected *cliauth.AuthorizeRejectedError
	if errors.As(err, &rejected) {
		return envelope.New(envelope.KindConfig, "redirect_uri_not_registered", err.Error(),
			fmt.Sprintf("tell the user to open https://cloud.ouraring.com/oauth/applications, edit app %s, "+
				"add this exact redirect URI: %s — then rerun 'oura auth login'",
				opts.clientID, redirectURI))
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return envelope.New(envelope.KindAuth, "authorization_timeout",
			fmt.Sprintf("no authorization callback arrived within %d seconds", opts.loginTimeout),
			"the user has to approve access in their browser; ask them to complete the Oura consent "+
				"screen, then rerun 'oura auth login' (raise --login-timeout to allow more time)")
	}
	if strings.Contains(err.Error(), "authorization denied") {
		return envelope.New(envelope.KindAuth, "authorization_denied", err.Error(),
			"the user declined the Oura consent screen; rerun 'oura auth login' once they are ready to approve")
	}
	return envelope.New(envelope.KindAuth, "oauth_failed", err.Error(),
		fmt.Sprintf("verify %s is registered on your Oura app and the client credentials are correct", redirectURI))
}

func runAuthLogin(cmd *cobra.Command, opts *authLoginOpts) error {
	store := cliauth.Open(cliauth.ConfigDir())

	if opts.tokenStdin {
		return runTokenStdinLogin(store)
	}

	// Onboarding guidance to stderr — this is the human's first real step.
	output.Progressf(os.Stderr, "Oura OAuth2 login")
	output.Progressf(os.Stderr, "  1. Create an app at https://cloud.ouraring.com/oauth/applications")
	output.Progressf(os.Stderr, "  2. Register this exact redirect URI: http://localhost:%d/callback", opts.port)
	output.Progressf(os.Stderr, "  3. Copy its client ID and client secret into this command.")

	// Environment variables are the off-argv path: they keep the client secret
	// out of the process table (ps/procfs) and give non-TTY automation, which
	// cannot use the interactive prompt below, a way to supply it.
	if opts.clientID == "" {
		opts.clientID = os.Getenv("OURA_CLIENT_ID")
	}
	if opts.clientSecret == "" {
		opts.clientSecret = os.Getenv("OURA_CLIENT_SECRET")
	}

	isTTY := term.IsTerminal(int(os.Stdin.Fd()))
	if opts.clientID == "" {
		if !isTTY {
			return envelope.New(envelope.KindUsage, "missing_client_id",
				"no --client-id given and stdin is not a terminal to prompt on",
				"pass --client-id/--client-secret, or use --token-stdin for a legacy bearer token")
		}
		fmt.Fprint(os.Stderr, "Oura client ID: ")
		line, err := readLine(os.Stdin)
		if err != nil {
			return envelope.New(envelope.KindUsage, "read_client_id", err.Error(), "pass --client-id instead")
		}
		opts.clientID = strings.TrimSpace(line)
		if opts.clientID == "" {
			return envelope.New(envelope.KindUsage, "empty_client_id", "no client ID entered", "pass --client-id")
		}
	}
	if opts.clientSecret == "" {
		if !isTTY {
			return envelope.New(envelope.KindUsage, "missing_client_secret",
				"no --client-secret given and stdin is not a terminal to prompt on",
				"pass --client-secret, or use --token-stdin for a legacy bearer token")
		}
		fmt.Fprint(os.Stderr, "Oura client secret (hidden): ")
		secret, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return envelope.New(envelope.KindUsage, "read_client_secret", err.Error(), "pass --client-secret instead")
		}
		opts.clientSecret = strings.TrimSpace(string(secret))
		if opts.clientSecret == "" {
			return envelope.New(envelope.KindUsage, "empty_client_secret", "no client secret entered", "pass --client-secret")
		}
	}

	var openBrowser func(string) error
	if !opts.noBrowser {
		openBrowser = openInBrowser
	}
	ctx := cmd.Context()
	if opts.loginTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(opts.loginTimeout)*time.Second)
		defer cancel()
	}
	creds, err := cliauth.LoginOAuth(ctx, opts.clientID, opts.clientSecret, parseScopes(opts.scopes), opts.port, openBrowser)
	if err != nil {
		return classifyLoginError(err, opts)
	}
	if err := store.Save(creds); err != nil {
		return envelope.New(envelope.KindConfig, "credential_save", err.Error(), "run 'oura doctor'")
	}
	output.Progressf(os.Stderr, "Authorized. Credentials saved to the %s.", store.Backend())
	return printResult(loginResult(creds, store.Backend()))
}

func runTokenStdinLogin(store cliauth.Store) error {
	line, err := readLine(os.Stdin)
	if err != nil {
		return envelope.New(envelope.KindUsage, "read_token", err.Error(), "pipe a token: printf %s \"$TOKEN\" | oura auth login --token-stdin")
	}
	token := strings.TrimSpace(line)
	if token == "" {
		return envelope.New(envelope.KindUsage, "empty_token", "no token read from stdin",
			"pipe a token: printf %s \"$TOKEN\" | oura auth login --token-stdin")
	}
	creds := cliauth.Credentials{
		Method:      cliauth.Method(methodToken),
		AccessToken: token,
		SavedAt:     time.Now(),
	}
	if err := store.Save(creds); err != nil {
		return envelope.New(envelope.KindConfig, "credential_save", err.Error(), "run 'oura doctor'")
	}
	output.Progressf(os.Stderr, "Stored bearer token in the %s.", store.Backend())
	return printResult(loginResult(creds, store.Backend()))
}

func loginResult(c cliauth.Credentials, backend string) map[string]any {
	scopes := c.Scopes
	if scopes == nil {
		scopes = []string{}
	}
	var expiresAt any
	if !c.Expiry.IsZero() {
		expiresAt = c.Expiry.UTC().Format(time.RFC3339)
	}
	return map[string]any{
		"stored":     true,
		"backend":    backend,
		"method":     string(c.Method),
		"scopes":     scopes,
		"expires_at": expiresAt,
	}
}

func newAuthStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:         "status",
		Short:       "Report whether credentials are stored and how (no secrets)",
		Args:        cobra.NoArgs,
		Annotations: map[string]string{annStdout: "json", annExitCodes: "0"},
		RunE:        func(cmd *cobra.Command, args []string) error { return runAuthStatus() },
	}
}

func runAuthStatus() error {
	envOverride := os.Getenv("OURA_TOKEN") != ""
	store := cliauth.Open(cliauth.ConfigDir())
	creds, loadErr := store.Load()

	out := map[string]any{
		"authenticated": false,
		"method":        nil,
		"backend":       store.Backend(),
		"scopes":        []string{},
		"expires_at":    nil,
		"expired":       false,
		"env_override":  envOverride,
	}
	if envOverride {
		out["authenticated"] = true
		out["method"] = "env"
	}
	if loadErr == nil && creds.AccessToken != "" {
		out["authenticated"] = true
		if !envOverride {
			out["method"] = string(creds.Method)
		}
		if creds.Scopes != nil {
			out["scopes"] = creds.Scopes
		}
		if !creds.Expiry.IsZero() {
			out["expires_at"] = creds.Expiry.UTC().Format(time.RFC3339)
			out["expired"] = time.Now().After(creds.Expiry)
		}
	}
	return printResult(out)
}

func newAuthLogoutCmd() *cobra.Command {
	var revoke bool
	cmd := &cobra.Command{
		Use:         "logout",
		Short:       "Remove stored credentials, optionally revoking them at Oura",
		Args:        cobra.NoArgs,
		Annotations: map[string]string{annStdout: "json", annExitCodes: "0,4"},
		RunE:        func(cmd *cobra.Command, args []string) error { return runAuthLogout(cmd, revoke) },
	}
	cmd.Flags().BoolVar(&revoke, "revoke", false, "also revoke the token at Oura before removing it")
	return cmd
}

func runAuthLogout(cmd *cobra.Command, revoke bool) error {
	store := cliauth.Open(cliauth.ConfigDir())
	creds, loadErr := store.Load()

	revoked := false
	if revoke && loadErr == nil && creds.AccessToken != "" {
		if err := cliauth.Revoke(cmd.Context(), creds.AccessToken); err != nil {
			output.Progressf(os.Stderr, "warning: could not revoke the token at Oura: %v", err)
		} else {
			revoked = true
		}
	}

	err := store.Delete()
	removed := err == nil
	if err != nil && err != cliauth.ErrNotFound {
		return envelope.New(envelope.KindConfig, "credential_delete", err.Error(), "run 'oura doctor'")
	}
	return printResult(map[string]any{
		"removed": removed,
		"revoked": revoked,
		"backend": store.Backend(),
	})
}

// parseScopes splits a comma- or whitespace-separated scope string. An empty
// or whitespace-only input yields no scopes, which requests all scopes.
func parseScopes(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	})
	return fields
}

// readLine reads a single line from r, tolerating a missing trailing newline.
func readLine(r io.Reader) (string, error) {
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return line, nil
}

// openInBrowser launches the platform's default handler for url, invoking the
// opener directly (never via a shell) so url is not interpreted.
func openInBrowser(url string) error {
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name, args = "open", []string{url}
	case "windows":
		name, args = "rundll32", []string{"url.dll,FileProtocolHandler", url}
	default:
		name, args = "xdg-open", []string{url}
	}
	return exec.Command(name, args...).Start()
}
