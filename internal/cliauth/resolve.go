package cliauth

import (
	"os"
	"path/filepath"
)

// ConfigDir returns the ouracli config directory (~/.config/ouracli or the
// platform equivalent), honoring OURA_CONFIG_DIR for tests and containers.
func ConfigDir() string {
	if d := os.Getenv("OURA_CONFIG_DIR"); d != "" {
		return d
	}
	base, err := os.UserConfigDir()
	if err != nil {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "ouracli")
}

// ResolveToken returns the access token to use, and where it came from.
// Precedence: OURA_TOKEN env > stored credentials. Returns source
// "env", "keyring", "encrypted-file", or "" when nothing is available.
// OAuth refresh, when needed, is handled by the caller via Load().
func ResolveToken() (token, source string) {
	if t := os.Getenv("OURA_TOKEN"); t != "" {
		return t, "env"
	}
	store := Open(ConfigDir())
	creds, err := store.Load()
	if err != nil {
		return "", ""
	}
	return creds.AccessToken, store.Backend()
}
