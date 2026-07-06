package cliauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// redirectTransport rewrites every outgoing request to hit target instead of
// its real host, so package-level clients that call hardcoded production URLs
// (like oauthHTTP against tokenURL) can be pointed at an httptest server
// without touching non-test code.
type redirectTransport struct{ target *url.URL }

func (rt redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = rt.target.Scheme
	clone.URL.Host = rt.target.Host
	clone.Host = rt.target.Host
	return http.DefaultTransport.RoundTrip(clone)
}

// withRedirectedOAuthHTTP points the package's shared oauthHTTP client at srv
// for the duration of the test, restoring the original transport afterward
// since oauthHTTP is process-wide state shared across tests in this package.
func withRedirectedOAuthHTTP(t *testing.T, srv *httptest.Server) {
	t.Helper()
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parsing httptest URL: %v", err)
	}
	orig := oauthHTTP.Transport
	oauthHTTP.Transport = redirectTransport{target: target}
	t.Cleanup(func() { oauthHTTP.Transport = orig })
}

func TestRefreshRotatesAndPersists(t *testing.T) {
	var gotForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/oauth/token" {
			t.Errorf("path = %s, want /oauth/token", r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
			t.Errorf("Content-Type = %q, want application/x-www-form-urlencoded", ct)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		gotForm = r.Form
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token_type":    "bearer",
			"access_token":  "new-access-token",
			"expires_in":    3600,
			"refresh_token": "rotated-refresh-token", // Oura rotates on every exchange
		})
	}))
	defer srv.Close()
	withRedirectedOAuthHTTP(t, srv)

	store := fileStore{dir: t.TempDir()}
	old := Credentials{
		Method:       MethodOAuth,
		AccessToken:  "old-access-token",
		RefreshToken: "old-refresh-token",
		ClientID:     "client-abc",
		ClientSecret: "secret-xyz",
		Expiry:       time.Now().Add(-time.Hour), // already expired
		SavedAt:      time.Now().Add(-2 * time.Hour),
	}

	// Discard the returned Credentials deliberately: the rotated refresh
	// token must still land in the store, or the account gets stranded on
	// the next refresh even though this call's caller ignored the result.
	if _, err := Refresh(context.Background(), store, old); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	if gotForm.Get("grant_type") != "refresh_token" {
		t.Errorf("grant_type = %q, want %q", gotForm.Get("grant_type"), "refresh_token")
	}
	if gotForm.Get("refresh_token") != "old-refresh-token" {
		t.Errorf("form refresh_token = %q, want the stored (pre-rotation) token %q", gotForm.Get("refresh_token"), "old-refresh-token")
	}
	if gotForm.Get("client_id") != "client-abc" {
		t.Errorf("form client_id = %q, want %q", gotForm.Get("client_id"), "client-abc")
	}
	if gotForm.Get("client_secret") != "secret-xyz" {
		t.Errorf("form client_secret = %q, want %q", gotForm.Get("client_secret"), "secret-xyz")
	}

	persisted, err := store.Load()
	if err != nil {
		t.Fatalf("Load after Refresh: %v", err)
	}
	if persisted.AccessToken != "new-access-token" {
		t.Errorf("persisted AccessToken = %q, want %q", persisted.AccessToken, "new-access-token")
	}
	if persisted.RefreshToken != "rotated-refresh-token" {
		t.Errorf("persisted RefreshToken = %q, want the rotated token %q", persisted.RefreshToken, "rotated-refresh-token")
	}
	if !persisted.Expiry.After(time.Now()) {
		t.Errorf("persisted Expiry %v should be in the future", persisted.Expiry)
	}
}

// TestRefreshRetainsOldRefreshTokenWhenNotRotated covers a token endpoint that
// (unusually, for Oura) omits refresh_token from the response: the existing
// refresh token must be kept rather than blanked out.
func TestRefreshRetainsOldRefreshTokenWhenNotRotated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "new-access-token",
			"expires_in":   3600,
		})
	}))
	defer srv.Close()
	withRedirectedOAuthHTTP(t, srv)

	store := fileStore{dir: t.TempDir()}
	old := Credentials{
		Method:       MethodOAuth,
		AccessToken:  "old-access-token",
		RefreshToken: "keep-me-refresh-token",
		ClientID:     "client-abc",
		ClientSecret: "secret-xyz",
	}
	got, err := Refresh(context.Background(), store, old)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got.RefreshToken != "keep-me-refresh-token" {
		t.Errorf("RefreshToken = %q, want the retained %q", got.RefreshToken, "keep-me-refresh-token")
	}
}

func TestRefreshErrorsWithoutRefreshToken(t *testing.T) {
	store := fileStore{dir: t.TempDir()}
	_, err := Refresh(context.Background(), store, Credentials{AccessToken: "x"})
	if err == nil {
		t.Fatal("Refresh with no stored refresh token should error")
	}
}

func TestRefreshSurfacesTokenEndpointError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer srv.Close()
	withRedirectedOAuthHTTP(t, srv)

	store := fileStore{dir: t.TempDir()}
	old := Credentials{RefreshToken: "old-refresh-token", ClientID: "c", ClientSecret: "s"}
	if _, err := Refresh(context.Background(), store, old); err == nil {
		t.Fatal("Refresh should surface a token-endpoint error")
	}
	// The store must remain untouched on failure.
	if _, err := store.Load(); err != ErrNotFound {
		t.Errorf("store.Load() after a failed refresh: err = %v, want ErrNotFound (nothing should have been saved)", err)
	}
}
