package cliauth

import (
	"testing"
	"time"
)

func TestConfigDirHonorsEnvOverride(t *testing.T) {
	t.Setenv("OURA_CONFIG_DIR", "/tmp/some-ouracli-test-dir")
	if got := ConfigDir(); got != "/tmp/some-ouracli-test-dir" {
		t.Errorf("ConfigDir() = %q, want %q", got, "/tmp/some-ouracli-test-dir")
	}
}

func TestConfigDirFallsBackWithoutEnv(t *testing.T) {
	t.Setenv("OURA_CONFIG_DIR", "")
	got := ConfigDir()
	if got == "" {
		t.Error("ConfigDir() returned empty string without OURA_CONFIG_DIR")
	}
}

// TestResolveTokenPrecedence exercises OURA_TOKEN > stored credentials, using
// only the file-backed store (via OURA_CONFIG_DIR + OURA_KEYRING_BACKEND=file)
// so it never touches a real OS keyring.
func TestResolveTokenPrecedence(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OURA_CONFIG_DIR", dir)
	t.Setenv("OURA_KEYRING_BACKEND", "file")

	t.Run("nothing configured yields empty token and source", func(t *testing.T) {
		t.Setenv("OURA_TOKEN", "")
		token, source := ResolveToken()
		if token != "" || source != "" {
			t.Errorf("ResolveToken() = (%q, %q), want (\"\", \"\")", token, source)
		}
	})

	t.Run("stored credentials are used when no env token", func(t *testing.T) {
		t.Setenv("OURA_TOKEN", "")
		store := Open(dir)
		if err := store.Save(Credentials{
			Method:      MethodPAT,
			AccessToken: "stored-token",
			SavedAt:     time.Now(),
		}); err != nil {
			t.Fatalf("Save: %v", err)
		}
		token, source := ResolveToken()
		if token != "stored-token" {
			t.Errorf("token = %q, want %q", token, "stored-token")
		}
		if source != "encrypted-file" {
			t.Errorf("source = %q, want %q", source, "encrypted-file")
		}
	})

	t.Run("env token takes precedence over stored credentials", func(t *testing.T) {
		t.Setenv("OURA_TOKEN", "env-token")
		token, source := ResolveToken()
		if token != "env-token" {
			t.Errorf("token = %q, want %q", token, "env-token")
		}
		if source != "env" {
			t.Errorf("source = %q, want %q", source, "env")
		}
	})
}
