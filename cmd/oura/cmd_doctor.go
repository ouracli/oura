package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/ouracli/oura/internal/cliauth"
	"github.com/ouracli/oura/internal/ouraapi"
)

// doctorCheck is one onboarding diagnostic. Doctor always exits 0 and reports
// the pass/fail of each check in JSON so an agent can branch on results.
type doctorCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	Hint   string `json:"hint,omitempty"`
}

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Run onboarding diagnostics (config, keyring, network, token) as JSON",
		Long: "doctor checks everything needed to use oura and reports each result as a\n" +
			"JSON object. It always exits 0 — read {\"ok\":...} and the per-check hints\n" +
			"rather than the exit code.",
		Args:        cobra.NoArgs,
		Annotations: map[string]string{annStdout: "json", annExitCodes: "0"},
		RunE:        func(cmd *cobra.Command, args []string) error { return runDoctor(cmd.Context()) },
	}
}

func runDoctor(ctx context.Context) error {
	checks := []doctorCheck{checkConfigDir()}

	backendCheck, store := checkBackend()
	checks = append(checks, backendCheck)

	creds, credErr := store.Load()
	envToken := os.Getenv("OURA_TOKEN")
	checks = append(checks, checkCredentialsPresent(creds, credErr, envToken))

	if token, _ := cliauth.ResolveToken(); token != "" {
		checks = append(checks, checkTokenValid(ctx, token))
	}
	if credErr == nil && creds.Method == cliauth.MethodOAuth && !creds.Expiry.IsZero() {
		checks = append(checks, checkTokenExpiry(creds))
	}

	checks = append(checks, checkNetwork(ctx), checkVersion())

	all := true
	for _, c := range checks {
		if !c.OK {
			all = false
		}
	}
	return printResult(map[string]any{"ok": all, "checks": checks})
}

func checkConfigDir() doctorCheck {
	dir := cliauth.ConfigDir()
	c := doctorCheck{Name: "config_dir"}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		c.Detail = fmt.Sprintf("cannot create %s: %v", dir, err)
		c.Hint = "fix directory permissions or set OURA_CONFIG_DIR"
		return c
	}
	probe := filepath.Join(dir, ".doctor-probe")
	if err := os.WriteFile(probe, []byte("ok"), 0o600); err != nil {
		c.Detail = fmt.Sprintf("%s is not writable: %v", dir, err)
		c.Hint = "fix directory permissions or set OURA_CONFIG_DIR"
		return c
	}
	_ = os.Remove(probe)
	c.OK = true
	c.Detail = dir + " is writable"
	return c
}

func checkBackend() (doctorCheck, cliauth.Store) {
	store := cliauth.Open(cliauth.ConfigDir())
	c := doctorCheck{Name: "credential_backend", OK: true, Detail: "using OS keyring"}
	if store.Backend() == "encrypted-file" {
		c.Detail = "using encrypted-file fallback (no OS keyring available)"
		c.Hint = "an OS keyring (Keychain/Credential Manager/libsecret) is stronger; " +
			"the file fallback is obfuscation-at-rest only"
	}
	return c, store
}

func checkCredentialsPresent(creds cliauth.Credentials, credErr error, envToken string) doctorCheck {
	c := doctorCheck{Name: "credentials_present"}
	switch {
	case envToken != "":
		c.OK = true
		c.Detail = "OURA_TOKEN is set (overrides any stored credentials)"
	case credErr == nil && creds.AccessToken != "":
		c.OK = true
		c.Detail = fmt.Sprintf("stored credentials found (method %q)", creds.Method)
	default:
		c.Detail = "no stored credentials and no OURA_TOKEN set"
		c.Hint = "run 'oura auth login' (or use --sandbox for fake data with no credentials)"
	}
	return c
}

func checkTokenValid(ctx context.Context, token string) doctorCheck {
	c := doctorCheck{Name: "token_valid"}
	status, body, err := authedGet(ctx, ouraapi.BaseURL+"/usercollection/personal_info", token)
	if err != nil {
		c.Detail = "could not reach Oura to validate the token: " + err.Error()
		c.Hint = "check network connectivity, then run 'oura doctor' again"
		return c
	}
	switch status {
	case http.StatusOK:
		c.OK = true
		c.Detail = "token accepted by Oura"
		var pi map[string]any
		if json.Unmarshal(body, &pi) == nil {
			if email, ok := pi["email"].(string); ok && email != "" {
				c.Detail = "token valid; authenticated as " + email
			}
		}
	case http.StatusUnauthorized:
		c.Detail = "Oura rejected the token (HTTP 401)"
		c.Hint = "run 'oura auth login' to obtain a fresh token"
	case http.StatusForbidden:
		c.Detail = "token authenticated but lacks scope, or the Oura membership has lapsed (HTTP 403)"
		c.Hint = "re-run 'oura auth login' granting the 'personal' and 'email' scopes, or check your Oura subscription"
	default:
		c.Detail = fmt.Sprintf("unexpected HTTP %d while validating the token", status)
		c.Hint = "run 'oura doctor' again; if it persists inspect with 'oura profile'"
	}
	return c
}

func checkTokenExpiry(creds cliauth.Credentials) doctorCheck {
	c := doctorCheck{Name: "token_expiry"}
	canRefresh := creds.RefreshToken != ""
	expiresAt := creds.Expiry.UTC().Format(time.RFC3339)
	if time.Now().Before(creds.Expiry) {
		c.OK = true
		c.Detail = fmt.Sprintf("access token valid until %s (in %s)", expiresAt, time.Until(creds.Expiry).Round(time.Second))
		if canRefresh {
			c.Detail += "; auto-refresh available"
		} else {
			c.Hint = "no refresh token stored; run 'oura auth login' again when it expires"
		}
		return c
	}
	if canRefresh {
		c.OK = true
		c.Detail = fmt.Sprintf("access token expired at %s; will auto-refresh on the next call", expiresAt)
		return c
	}
	c.Detail = fmt.Sprintf("access token expired at %s and no refresh token is stored", expiresAt)
	c.Hint = "run 'oura auth login' to re-authorize"
	return c
}

func checkNetwork(ctx context.Context) doctorCheck {
	c := doctorCheck{Name: "network"}
	// A dummy token satisfies the sandbox's "any non-empty token" rule, so this
	// proves both reachability of api.ouraring.com and that sandbox mode works.
	status, _, err := authedGet(ctx, ouraapi.SandboxBaseURL+"/usercollection/daily_sleep", "sandbox")
	if err != nil {
		c.Detail = "cannot reach api.ouraring.com: " + err.Error()
		c.Hint = "check your internet connection, DNS, or proxy settings"
		return c
	}
	c.OK = true
	if status == http.StatusOK {
		c.Detail = "reached api.ouraring.com; sandbox responded 200"
	} else {
		c.Detail = fmt.Sprintf("reached api.ouraring.com (sandbox HTTP %d)", status)
	}
	return c
}

func checkVersion() doctorCheck {
	return doctorCheck{
		Name:   "version",
		OK:     true,
		Detail: fmt.Sprintf("oura %s (commit %s, built %s)", version, commit, date),
	}
}

// authedGet performs a bare authenticated GET and returns the status code and
// body. It exists so doctor can classify raw HTTP statuses (401 vs 403 vs ok)
// that the typed client folds together.
func authedGet(ctx context.Context, url, token string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: time.Duration(globals.timeout) * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, body, nil
}
