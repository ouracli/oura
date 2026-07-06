package main

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/akeemjenkins/ouracli/internal/cliauth"
	"github.com/akeemjenkins/ouracli/internal/envelope"
	"github.com/akeemjenkins/ouracli/internal/ouraapi"
)

// refreshWindow is how far before expiry an OAuth access token is proactively
// refreshed, so a token that is technically valid but about to lapse mid-call
// is renewed first.
const refreshWindow = 2 * time.Minute

// apiClient builds the ouraapi.Client for a data command, resolving
// credentials in precedence order: --sandbox short-circuits to the
// credential-free sandbox; otherwise the explicit --token flag wins, then the
// OURA_TOKEN env var; otherwise stored credentials are loaded and, for OAuth,
// refreshed (rotation-safe) when expired or near expiry. With nothing available
// it returns a KindAuth envelope pointing at the ways to get credentials.
func apiClient(ctx context.Context) (*ouraapi.Client, error) {
	timeout := time.Duration(globals.timeout) * time.Second
	if globals.sandbox {
		return ouraapi.NewSandbox(timeout), nil
	}
	if globals.token != "" {
		return ouraapi.New(globals.token, timeout), nil
	}
	if t := os.Getenv("OURA_TOKEN"); t != "" {
		return ouraapi.New(t, timeout), nil
	}

	store := cliauth.Open(cliauth.ConfigDir())
	creds, err := store.Load()
	if err != nil {
		if errors.Is(err, cliauth.ErrNotFound) {
			return nil, envelope.New(envelope.KindAuth, "no_credentials",
				"no Oura credentials are configured",
				"run 'oura auth login', set OURA_TOKEN, or use --sandbox for fake data")
		}
		return nil, envelope.New(envelope.KindConfig, "credential_load",
			err.Error(), "run 'oura doctor'")
	}

	if creds.Method == cliauth.MethodOAuth && needsRefresh(creds) {
		refreshed, rerr := cliauth.Refresh(ctx, store, creds)
		if rerr != nil {
			return nil, envelope.New(envelope.KindAuth, "refresh_failed",
				rerr.Error(), "run 'oura auth login' to re-authorize")
		}
		creds = refreshed
	}

	if creds.AccessToken == "" {
		return nil, envelope.New(envelope.KindAuth, "no_credentials",
			"stored credentials contain no access token",
			"run 'oura auth login', set OURA_TOKEN, or use --sandbox for fake data")
	}
	return ouraapi.New(creds.AccessToken, timeout), nil
}

// needsRefresh reports whether an OAuth credential should be refreshed before
// use: it has a rotation-capable refresh token and is expired or within the
// refresh window of expiry.
func needsRefresh(c cliauth.Credentials) bool {
	if c.RefreshToken == "" || c.Expiry.IsZero() {
		return false
	}
	return time.Now().Add(refreshWindow).After(c.Expiry)
}

// storedAuthSummary reports whether the tool currently has usable credentials
// and by which method, without touching the network. root.go uses it for the
// bare-`oura` welcome object.
func storedAuthSummary() (authed bool, method string) {
	if globals.token != "" {
		return true, "flag"
	}
	if os.Getenv("OURA_TOKEN") != "" {
		return true, "env"
	}
	creds, err := cliauth.Open(cliauth.ConfigDir()).Load()
	if err != nil || creds.AccessToken == "" {
		return false, ""
	}
	return true, string(creds.Method)
}
