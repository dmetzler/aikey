package oidc

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Store persists tokens on disk with 0600 permissions.
//
// The spike deliberately uses a file rather than the OS keyring: keyring access
// pulls in cgo and platform libraries, which would make the "does the loopback
// proxy hold up?" question harder to answer. Moving to the OS keyring is a
// follow-up, and the file path is already per-profile so that swap is local.
type Store struct{ path string }

// NewStore returns the token store for a profile, next to the config file.
func NewStore(configPath, profile string) *Store {
	return &Store{path: filepath.Join(filepath.Dir(configPath), fmt.Sprintf("token-%s.json", profile))}
}

// Path exposes the on-disk location, for `aikey status` and logout.
func (s *Store) Path() string { return s.path }

// Load returns the stored token, or nil if there is none.
func (s *Store) Load() (*Token, error) {
	b, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var t Token
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, fmt.Errorf("parse %s: %w", s.path, err)
	}
	return &t, nil
}

// Save writes the token atomically with 0600 so a crash cannot leave a
// half-written file that would force a needless re-login.
func (s *Store) Save(t *Token) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Delete removes the stored token.
func (s *Store) Delete() error {
	err := os.Remove(s.path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
