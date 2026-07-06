package cliauth

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"
)

// newTestFileStore returns a fileStore rooted at a fresh temp dir, bypassing
// Open() entirely so these tests never touch the real OS keyring.
func newTestFileStore(t *testing.T) fileStore {
	t.Helper()
	return fileStore{dir: t.TempDir()}
}

func sampleCreds() Credentials {
	return Credentials{
		Method:       MethodOAuth,
		AccessToken:  "access-123",
		RefreshToken: "refresh-456",
		Expiry:       time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
		Scopes:       []string{"personal", "daily"},
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		SavedAt:      time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC),
	}
}

func TestFileStoreRoundtrip(t *testing.T) {
	fs := newTestFileStore(t)
	want := sampleCreds()

	if err := fs.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := fs.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Load() = %+v, want %+v", got, want)
	}

	if err := fs.Delete(); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := fs.Load(); err != ErrNotFound {
		t.Errorf("Load after Delete: err = %v, want ErrNotFound", err)
	}
}

func TestFileStoreLoadMissingReturnsErrNotFound(t *testing.T) {
	fs := newTestFileStore(t)
	if _, err := fs.Load(); err != ErrNotFound {
		t.Errorf("Load on empty store: err = %v, want ErrNotFound", err)
	}
}

func TestFileStoreDeleteMissingReturnsErrNotFound(t *testing.T) {
	fs := newTestFileStore(t)
	if err := fs.Delete(); err != ErrNotFound {
		t.Errorf("Delete on empty store: err = %v, want ErrNotFound", err)
	}
}

func TestFileStoreBackend(t *testing.T) {
	fs := newTestFileStore(t)
	if got := fs.Backend(); got != "encrypted-file" {
		t.Errorf("Backend() = %q, want %q", got, "encrypted-file")
	}
}

func TestFileStoreCorruptCredentialsFile(t *testing.T) {
	fs := newTestFileStore(t)
	// Establish the key first (Save would create it, but we want to write a
	// bogus ciphertext directly).
	if err := fs.Save(sampleCreds()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// Overwrite the encrypted blob with garbage shorter than a valid nonce+tag.
	if err := os.WriteFile(fs.credPath(), []byte("not-encrypted-data"), 0o600); err != nil {
		t.Fatalf("corrupting credentials file: %v", err)
	}
	if _, err := fs.Load(); err == nil {
		t.Error("Load on a corrupted credentials file should error, got nil")
	}
}

func TestFileStoreCorruptKeyFile(t *testing.T) {
	fs := newTestFileStore(t)
	if err := os.MkdirAll(fs.dir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// A key file that isn't 32 bytes is corrupt and must be rejected rather
	// than silently used (which would produce cipher.NewCipher errors anyway).
	if err := os.WriteFile(fs.keyPath(), []byte("too-short"), 0o600); err != nil {
		t.Fatalf("writing bogus key: %v", err)
	}
	if err := fs.Save(sampleCreds()); err == nil {
		t.Error("Save with a corrupt (wrong-length) key file should error, got nil")
	}
}

func TestFileStoreKeyFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits are not enforced on Windows")
	}
	if os.Getuid() == 0 {
		t.Skip("running as root; file permission bits are not enforced")
	}
	fs := newTestFileStore(t)
	if err := fs.Save(sampleCreds()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(fs.keyPath())
	if err != nil {
		t.Fatalf("Stat key file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("key file permissions = %o, want %o", perm, 0o600)
	}
	credInfo, err := os.Stat(fs.credPath())
	if err != nil {
		t.Fatalf("Stat credentials file: %v", err)
	}
	if perm := credInfo.Mode().Perm(); perm != 0o600 {
		t.Errorf("credentials file permissions = %o, want %o", perm, 0o600)
	}
}

func TestFileStoreKeyIsStableAcrossSaves(t *testing.T) {
	fs := newTestFileStore(t)
	if err := fs.Save(sampleCreds()); err != nil {
		t.Fatalf("first Save: %v", err)
	}
	key1, err := os.ReadFile(fs.keyPath())
	if err != nil {
		t.Fatalf("reading key after first save: %v", err)
	}
	c2 := sampleCreds()
	c2.AccessToken = "different-token"
	if err := fs.Save(c2); err != nil {
		t.Fatalf("second Save: %v", err)
	}
	key2, err := os.ReadFile(fs.keyPath())
	if err != nil {
		t.Fatalf("reading key after second save: %v", err)
	}
	if string(key1) != string(key2) {
		t.Error("key file changed between saves; it should be created once and reused")
	}
	got, err := fs.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.AccessToken != "different-token" {
		t.Errorf("AccessToken = %q, want %q", got.AccessToken, "different-token")
	}
}

func TestOpenHonorsFileBackendOverride(t *testing.T) {
	t.Setenv("OURA_KEYRING_BACKEND", "file")
	dir := t.TempDir()
	store := Open(dir)
	if store.Backend() != "encrypted-file" {
		t.Errorf("Open with OURA_KEYRING_BACKEND=file: Backend() = %q, want %q", store.Backend(), "encrypted-file")
	}
	// Sanity: it actually persists under dir.
	if err := store.Save(sampleCreds()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "credentials.enc")); err != nil {
		t.Errorf("expected credentials.enc under %s: %v", dir, err)
	}
}
