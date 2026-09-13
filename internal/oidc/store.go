package oidc

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zalando/go-keyring"
)

// keyringService is the account namespace used in the OS credential store.
const keyringService = "aikey"

// Store persists tokens, preferring the OS keyring and falling back to a 0600
// file.
//
// The fallback is not laziness: headless Linux (containers, LXC, CI, SSH
// sessions with no D-Bus) frequently has no Secret Service at all, and a
// keyring-only implementation would simply refuse to run there. We therefore
// probe the keyring once and remember the verdict.
type Store struct {
	profile  string
	filePath string
	backend  Backend
}

// Backend reports where a token actually lives.
type Backend string

const (
	// BackendKeyring means the OS credential store (Keychain, Secret Service,
	// Credential Manager).
	BackendKeyring Backend = "keyring"
	// BackendFile means a 0600 file next to the config.
	BackendFile Backend = "file"
)

// NewStore returns the token store for a profile. It probes the OS keyring and
// silently falls back to a file when unavailable.
func NewStore(configPath, profile string) *Store {
	s := &Store{
		profile:  profile,
		filePath: filepath.Join(filepath.Dir(configPath), fmt.Sprintf("token-%s.json", profile)),
		backend:  BackendFile,
	}
	if keyringUsable() {
		s.backend = BackendKeyring
	}
	return s
}

// NewFileStore forces the file backend, for users who prefer it or whose
// keyring prompts on every access.
func NewFileStore(configPath, profile string) *Store {
	return &Store{
		profile:  profile,
		filePath: filepath.Join(filepath.Dir(configPath), fmt.Sprintf("token-%s.json", profile)),
		backend:  BackendFile,
	}
}

// keyringUsable round-trips a probe value. We cannot just call Get: a missing
// key and a broken keyring both return errors, and only a write proves the
// backend really works.
func keyringUsable() bool {
	const probe = "__aikey_probe__"
	if err := keyring.Set(keyringService, probe, "1"); err != nil {
		return false
	}
	v, err := keyring.Get(keyringService, probe)
	_ = keyring.Delete(keyringService, probe)
	return err == nil && v == "1"
}

// Backend reports the active storage backend, for `aikey status`.
func (s *Store) Backend() Backend { return s.backend }

// Location describes where the token is kept, for display.
func (s *Store) Location() string {
	if s.backend == BackendKeyring {
		return fmt.Sprintf("OS keyring (service %q, account %q)", keyringService, s.profile)
	}
	return s.filePath
}

// Path returns the file path used by the file backend.
func (s *Store) Path() string { return s.filePath }

// Load returns the stored token, or nil when there is none.
//
// When the keyring is active we still read a leftover file, so an existing
// install keeps working after upgrading; Save then migrates it.
func (s *Store) Load() (*Token, error) {
	if s.backend == BackendKeyring {
		v, err := keyring.Get(keyringService, s.profile)
		if err == nil {
			var t Token
			if err := json.Unmarshal([]byte(v), &t); err != nil {
				return nil, fmt.Errorf("parse keyring entry: %w", err)
			}
			return &t, nil
		}
		if !errors.Is(err, keyring.ErrNotFound) {
			return nil, fmt.Errorf("read keyring: %w", err)
		}
		// Not in the keyring: fall through to the legacy file.
	}
	return s.loadFile()
}

func (s *Store) loadFile() (*Token, error) {
	b, err := os.ReadFile(s.filePath)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var t Token
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, fmt.Errorf("parse %s: %w", s.filePath, err)
	}
	return &t, nil
}

// Save writes the token. With the keyring backend it also removes any legacy
// file, so the secret does not linger in two places after migration.
func (s *Store) Save(t *Token) error {
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	if s.backend == BackendKeyring {
		if err := keyring.Set(keyringService, s.profile, string(b)); err != nil {
			return fmt.Errorf("write keyring: %w", err)
		}
		_ = os.Remove(s.filePath)
		return nil
	}
	return s.saveFile(b)
}

func (s *Store) saveFile(b []byte) error {
	if err := os.MkdirAll(filepath.Dir(s.filePath), 0o700); err != nil {
		return err
	}
	// Write-then-rename: a crash mid-write must not leave a truncated file
	// that would force a pointless re-login.
	tmp := s.filePath + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.filePath)
}

// Delete removes the token from both backends, so `aikey logout` cannot leave a
// copy behind.
func (s *Store) Delete() error {
	var firstErr error
	if s.backend == BackendKeyring {
		if err := keyring.Delete(keyringService, s.profile); err != nil &&
			!errors.Is(err, keyring.ErrNotFound) {
			firstErr = err
		}
	}
	if err := os.Remove(s.filePath); err != nil && !os.IsNotExist(err) && firstErr == nil {
		firstErr = err
	}
	return firstErr
}
