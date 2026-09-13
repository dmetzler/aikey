package oidc

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func tempStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	// Force the file backend: unit tests must not touch the developer's real
	// Keychain, and CI has no Secret Service at all.
	return NewFileStore(filepath.Join(dir, "config.json"), "default")
}

func TestFileStoreRoundTrip(t *testing.T) {
	s := tempStore(t)

	got, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatal("expected no token in a fresh store")
	}

	want := &Token{
		AccessToken:  "access-value",
		RefreshToken: "refresh-value",
		ExpiresAt:    time.Now().Add(15 * time.Minute).Truncate(time.Second),
	}
	if err := s.Save(want); err != nil {
		t.Fatal(err)
	}
	got, err = s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != want.AccessToken || got.RefreshToken != want.RefreshToken {
		t.Fatalf("round trip mismatch: %+v", got)
	}
}

// A refresh token readable by other local users defeats the whole design.
func TestFileStoreIsNotWorldReadable(t *testing.T) {
	s := tempStore(t)
	if err := s.Save(&Token{AccessToken: "a", RefreshToken: "r"}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("token file mode = %#o, want 0600", perm)
	}
}

// Logout must leave nothing behind, or "signed out" is a lie.
func TestDeleteRemovesToken(t *testing.T) {
	s := tempStore(t)
	if err := s.Save(&Token{AccessToken: "a", RefreshToken: "r"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatal("token survived Delete")
	}
	if _, err := os.Stat(s.Path()); !os.IsNotExist(err) {
		t.Fatal("token file still on disk after Delete")
	}
}

func TestDeleteOnEmptyStoreIsNotAnError(t *testing.T) {
	if err := tempStore(t).Delete(); err != nil {
		t.Fatalf("Delete on empty store: %v", err)
	}
}
