package cliauth

// OAuth2 authorization-code flow for Oura. Personal Access Tokens were
// deprecated in December 2025, so this is the only way to mint fresh
// credentials; raw bearer tokens are still accepted out-of-band. Oura has two
// quirks worth remembering here: the authorize and token endpoints live on
// DIFFERENT hosts (cloud. vs api.), and refresh tokens are single-use and
// rotate, so we persist the rotated token before ever using the new access
// token.

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	authorizeURL = "https://cloud.ouraring.com/oauth/authorize" // cloud host
	tokenURL     = "https://api.ouraring.com/oauth/token"       // api host
	revokeURL    = "https://api.ouraring.com/oauth/revoke"
)

// oauthHTTP is a dedicated client for the token/revoke endpoints so they are
// not affected by a caller's API timeout choices.
var oauthHTTP = &http.Client{Timeout: 30 * time.Second}

// tokenResponse is the JSON body from the token endpoint for both the
// authorization_code and refresh_token grants.
type tokenResponse struct {
	TokenType    string `json:"token_type"`
	AccessToken  string `json:"access_token"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
}

// LoginOAuth runs the loopback authorization-code flow. It binds a tiny HTTP
// server on 127.0.0.1:port, opens the browser to Oura's consent screen (and
// always prints the URL to stderr so headless users can paste it), waits for
// the redirect back to /callback, validates the state parameter, and exchanges
// the returned code for tokens. The granted scope set (which may be a subset of
// what was requested, since users can untick scopes) and the derived expiry are
// recorded on the returned Credentials. Saving is left to the caller.
func LoginOAuth(ctx context.Context, clientID, clientSecret string, scopes []string, port int, openBrowser func(url string) error) (Credentials, error) {
	var creds Credentials

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return creds, fmt.Errorf("cannot listen on 127.0.0.1:%d for the OAuth callback: %w", port, err)
	}
	defer ln.Close()

	state, err := randomState()
	if err != nil {
		return creds, err
	}

	// The redirect URI must match one registered on the Oura app exactly and be
	// identical between the authorize and token calls.
	redirectURI := fmt.Sprintf("http://localhost:%d/callback", port)
	authURL := buildAuthorizeURL(clientID, redirectURI, state, scopes)

	// Fail fast, before any browser is involved, when Oura would reject the
	// request anyway (unregistered redirect URI). Without this the browser
	// shows a bare 400 and the loopback wait below would never resolve.
	if err := preflightAuthorize(ctx, authURL, clientID, redirectURI); err != nil {
		return creds, err
	}

	resultCh := make(chan callbackResult, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		res := handleCallback(w, r, state)
		select {
		case resultCh <- res:
		default: // a duplicate hit (e.g. a reload) must not block the handler
		}
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	defer srv.Close()

	// Always surface the URL so a headless or SSH user can paste it manually.
	fmt.Fprintln(os.Stderr, "Open this URL in your browser to authorize oura:")
	fmt.Fprintln(os.Stderr, "  "+authURL)
	if openBrowser != nil {
		if err := openBrowser(authURL); err != nil {
			fmt.Fprintf(os.Stderr, "Could not open a browser automatically (%v); paste the URL above.\n", err)
		}
	}
	fmt.Fprintln(os.Stderr, "Waiting for the authorization redirect...")

	select {
	case <-ctx.Done():
		return creds, ctx.Err()
	case res := <-resultCh:
		if res.err != nil {
			return creds, res.err
		}
		tok, err := exchangeToken(ctx, url.Values{
			"grant_type":    {"authorization_code"},
			"code":          {res.code},
			"redirect_uri":  {redirectURI},
			"client_id":     {clientID},
			"client_secret": {clientSecret},
		})
		if err != nil {
			return creds, err
		}
		granted := scopes
		if res.scope != "" {
			granted = strings.Fields(res.scope)
		}
		now := time.Now()
		return Credentials{
			Method:       MethodOAuth,
			AccessToken:  tok.AccessToken,
			RefreshToken: tok.RefreshToken,
			Expiry:       now.Add(time.Duration(tok.ExpiresIn) * time.Second),
			Scopes:       granted,
			ClientID:     clientID,
			ClientSecret: clientSecret,
			SavedAt:      now,
		}, nil
	}
}

// AuthorizeRejectedError reports that Oura's authorize endpoint rejected the
// request outright (HTTP 400) before any user interaction — in practice this
// means the redirect URI is not registered on the OAuth app. Detected by the
// preflight probe so callers can fail fast with exact remediation instead of
// waiting for a browser callback that will never arrive.
type AuthorizeRejectedError struct {
	RedirectURI string
	ClientID    string
	Status      int
}

func (e *AuthorizeRejectedError) Error() string {
	return fmt.Sprintf("Oura rejected the authorization request (HTTP %d); redirect URI %q is not registered on app %s",
		e.Status, e.RedirectURI, e.ClientID)
}

// preflightAuthorize probes the authorize URL without following redirects. A
// well-formed request is answered with a redirect (to Oura's login/consent
// page); a 4xx means Oura refused the parameters themselves and the browser
// flow is guaranteed to dead-end.
func preflightAuthorize(ctx context.Context, authURL, clientID, redirectURI string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, authURL, nil)
	if err != nil {
		return err
	}
	client := &http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		// Preflight is advisory: on network trouble let the real flow surface
		// the error rather than blocking login on the probe.
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return &AuthorizeRejectedError{RedirectURI: redirectURI, ClientID: clientID, Status: resp.StatusCode}
	}
	return nil
}

// callbackResult carries the outcome of the loopback redirect back to the
// waiting flow.
type callbackResult struct {
	code  string
	scope string
	err   error
}

func handleCallback(w http.ResponseWriter, r *http.Request, wantState string) callbackResult {
	q := r.URL.Query()
	// Validate state FIRST, even on an error response (RFC 6749 §10.12): a local
	// process that cannot forge the state must not be able to steer the flow —
	// e.g. by racing in a /callback?error=access_denied to abort a real login.
	if q.Get("state") != wantState {
		writeCallbackPage(w, "State mismatch — this request was rejected.")
		return callbackResult{err: errors.New("state parameter mismatch (possible CSRF); aborting")}
	}
	if e := q.Get("error"); e != "" {
		msg := e
		if d := q.Get("error_description"); d != "" {
			msg = d
		}
		writeCallbackPage(w, "Authorization failed.")
		return callbackResult{err: fmt.Errorf("authorization denied by Oura: %s", msg)}
	}
	code := q.Get("code")
	if code == "" {
		writeCallbackPage(w, "No authorization code was returned.")
		return callbackResult{err: errors.New("callback contained no authorization code")}
	}
	writeCallbackPage(w, "Authorization complete.")
	return callbackResult{code: code, scope: q.Get("scope")}
}

func writeCallbackPage(w http.ResponseWriter, heading string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, "<!doctype html><meta charset=utf-8><title>oura</title>"+
		"<body style=\"font-family:system-ui,sans-serif;text-align:center;padding-top:4rem\">"+
		"<h2>%s</h2><p>You can close this tab and return to your terminal.</p></body>", heading)
}

// Refresh exchanges the stored refresh token for a new access token. Oura's
// refresh tokens are single-use and rotate on every exchange, so the rotated
// credentials are saved to the store BEFORE this returns — a caller that used
// the new access token without persisting the new refresh token would strand
// the account on the next refresh.
func Refresh(ctx context.Context, store Store, c Credentials) (Credentials, error) {
	if c.RefreshToken == "" {
		return c, errors.New("no refresh token stored; re-run 'oura auth login'")
	}
	tok, err := exchangeToken(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {c.RefreshToken},
		"client_id":     {c.ClientID},
		"client_secret": {c.ClientSecret},
	})
	if err != nil {
		return c, err
	}
	now := time.Now()
	c.AccessToken = tok.AccessToken
	if tok.RefreshToken != "" {
		c.RefreshToken = tok.RefreshToken // rotate
	}
	c.Expiry = now.Add(time.Duration(tok.ExpiresIn) * time.Second)
	c.SavedAt = now
	if err := store.Save(c); err != nil {
		return c, fmt.Errorf("refreshed the token but could not persist the rotated refresh token: %w", err)
	}
	return c, nil
}

// Revoke asks Oura to invalidate a token. The token is passed as a query
// parameter, matching Oura's revoke endpoint.
func Revoke(ctx context.Context, token string) error {
	u := revokeURL + "?" + url.Values{"access_token": {token}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
	if err != nil {
		return err
	}
	resp, err := oauthHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode >= 400 {
		return fmt.Errorf("revoke endpoint returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func exchangeToken(ctx context.Context, form url.Values) (tokenResponse, error) {
	var tr tokenResponse
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return tr, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := oauthHTTP.Do(req)
	if err != nil {
		return tr, fmt.Errorf("reaching the Oura token endpoint: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return tr, err
	}
	if resp.StatusCode != http.StatusOK {
		return tr, fmt.Errorf("token endpoint returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, &tr); err != nil {
		return tr, fmt.Errorf("unparseable token response: %w", err)
	}
	if tr.AccessToken == "" {
		return tr, errors.New("token endpoint returned no access_token")
	}
	return tr, nil
}

func buildAuthorizeURL(clientID, redirectURI, state string, scopes []string) string {
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("state", state)
	// Omit the scope param entirely to request all scopes: Oura treats an
	// absent scope as "everything", but an empty scope= value is malformed.
	if len(scopes) > 0 {
		q.Set("scope", strings.Join(scopes, " "))
	}
	return authorizeURL + "?" + q.Encode()
}

func randomState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating OAuth state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
