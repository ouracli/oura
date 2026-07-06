// Package cliauth stores Oura credentials out-of-band from agent context.
//
// Primary store is the OS keyring (macOS Keychain, Windows Credential
// Manager, libsecret/Secret Service on Linux) via zalando/go-keyring. Where
// no keyring is available (headless Linux, CI), it falls back to an
// AES-256-GCM encrypted file under the user config dir, keyed by a random
// key stored alongside with 0600 permissions — obfuscation-at-rest, clearly
// weaker than a real keyring, and reported as such by `oura doctor`.
package cliauth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/zalando/go-keyring"
)

const (
	service = "ouracli"
	account = "oura-credentials"
)

// Method distinguishes how the stored token was obtained.
type Method string

const (
	MethodPAT   Method = "pat"
	MethodOAuth Method = "oauth"
)

// Credentials is the JSON blob stored in the keyring.
type Credentials struct {
	Method       Method    `json:"method"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	Expiry       time.Time `json:"expiry,omitempty"`
	Scopes       []string  `json:"scopes,omitempty"`
	ClientID     string    `json:"client_id,omitempty"`
	ClientSecret string    `json:"client_secret,omitempty"`
	SavedAt      time.Time `json:"saved_at"`
}

// ErrNotFound is returned when no credentials are stored.
var ErrNotFound = errors.New("no stored credentials")

// Store abstracts the two backends so doctor can report which is active.
type Store interface {
	Save(Credentials) error
	Load() (Credentials, error)
	Delete() error
	Backend() string // "keyring" or "encrypted-file"
}

// Open returns the keyring store if the platform keyring works, otherwise
// the encrypted-file fallback. Setting OURA_KEYRING_BACKEND=file forces the
// encrypted-file backend even where a keyring exists — useful for CI, headless
// runs, and tests that must not touch (or prompt) the OS keyring.
func Open(configDir string) Store {
	if os.Getenv("OURA_KEYRING_BACKEND") == "file" {
		return fileStore{dir: configDir}
	}
	if keyringAvailable() {
		return keyringStore{}
	}
	return fileStore{dir: configDir}
}

func keyringAvailable() bool {
	// A read of a missing key exercises the backend without writing.
	_, err := keyring.Get(service, "availability-probe")
	return err == nil || errors.Is(err, keyring.ErrNotFound)
}

type keyringStore struct{}

func (keyringStore) Backend() string { return "keyring" }

func (keyringStore) Save(c Credentials) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return keyring.Set(service, account, string(b))
}

func (keyringStore) Load() (Credentials, error) {
	var c Credentials
	s, err := keyring.Get(service, account)
	if errors.Is(err, keyring.ErrNotFound) {
		return c, ErrNotFound
	}
	if err != nil {
		return c, err
	}
	return c, json.Unmarshal([]byte(s), &c)
}

func (keyringStore) Delete() error {
	err := keyring.Delete(service, account)
	if errors.Is(err, keyring.ErrNotFound) {
		return ErrNotFound
	}
	return err
}

type fileStore struct{ dir string }

func (f fileStore) Backend() string { return "encrypted-file" }

func (f fileStore) credPath() string { return filepath.Join(f.dir, "credentials.enc") }
func (f fileStore) keyPath() string  { return filepath.Join(f.dir, "credentials.key") }

func (f fileStore) loadOrCreateKey() ([]byte, error) {
	if b, err := os.ReadFile(f.keyPath()); err == nil {
		if len(b) != 32 {
			return nil, fmt.Errorf("corrupt key file %s: want 32 bytes, got %d", f.keyPath(), len(b))
		}
		return b, nil
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(f.dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(f.keyPath(), key, 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

func (f fileStore) Save(c Credentials) error {
	key, err := f.loadOrCreateKey()
	if err != nil {
		return err
	}
	plain, err := json.Marshal(c)
	if err != nil {
		return err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	sealed := gcm.Seal(nonce, nonce, plain, nil)
	return os.WriteFile(f.credPath(), sealed, 0o600)
}

func (f fileStore) Load() (Credentials, error) {
	var c Credentials
	sealed, err := os.ReadFile(f.credPath())
	if os.IsNotExist(err) {
		return c, ErrNotFound
	}
	if err != nil {
		return c, err
	}
	key, err := f.loadOrCreateKey()
	if err != nil {
		return c, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return c, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return c, err
	}
	if len(sealed) < gcm.NonceSize() {
		return c, errors.New("corrupt credentials file")
	}
	plain, err := gcm.Open(nil, sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():], nil)
	if err != nil {
		return c, fmt.Errorf("decrypt credentials: %w", err)
	}
	return c, json.Unmarshal(plain, &c)
}

func (f fileStore) Delete() error {
	err := os.Remove(f.credPath())
	if os.IsNotExist(err) {
		return ErrNotFound
	}
	return err
}
