package config

import (
	"os"
	"path/filepath"
	"testing"
)

func withTempConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	t.Setenv("AIKEY_CONFIG", p)
	for _, k := range []string{"AIKEY_ISSUER", "AIKEY_CLIENT_ID", "AIKEY_UPSTREAM", "AIKEY_LISTEN", "AIKEY_PROFILE"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	return p
}

// The whole point of the repo rule: an empty config must fail loudly rather
// than silently falling back to somebody's real deployment.
func TestResolveRefusesWithoutEndpoints(t *testing.T) {
	withTempConfig(t)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Resolve(""); err == nil {
		t.Fatal("expected an error when no endpoints are configured")
	}
}

func TestEnvOverridesFile(t *testing.T) {
	withTempConfig(t)
	c := &Config{Current: "default", Profiles: map[string]*Profile{
		"default": {Issuer: "https://file/realms/x", ClientID: "cid", Upstream: "https://file/ai"},
	}}
	t.Setenv("AIKEY_UPSTREAM", "https://env/ai")

	p, _, err := c.Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if p.Upstream != "https://env/ai" {
		t.Fatalf("env should win, got %q", p.Upstream)
	}
	if c.Profiles["default"].Upstream != "https://file/ai" {
		t.Fatal("Resolve must not mutate the stored profile")
	}
}

func TestTrailingSlashTrimmed(t *testing.T) {
	p := &Profile{Issuer: "https://h/realms/r/", ClientID: "c", Upstream: "https://h/ai/"}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if p.Issuer != "https://h/realms/r" || p.Upstream != "https://h/ai" {
		t.Fatalf("trailing slashes not trimmed: %+v", p)
	}
}

func TestRelativeURLRejected(t *testing.T) {
	p := &Profile{Issuer: "keycloak/realms/r", ClientID: "c", Upstream: "https://h/ai"}
	if err := p.Validate(); err == nil {
		t.Fatal("expected a relative issuer to be rejected")
	}
}

func TestProfilesAreIsolated(t *testing.T) {
	withTempConfig(t)
	c := &Config{Current: "home", Profiles: map[string]*Profile{
		"home": {Issuer: "https://home/realms/h", ClientID: "h", Upstream: "https://home/ai"},
		"work": {Issuer: "https://work/realms/w", ClientID: "w", Upstream: "https://work/ai"},
	}}
	p, name, err := c.Resolve("work")
	if err != nil {
		t.Fatal(err)
	}
	if name != "work" || p.Upstream != "https://work/ai" {
		t.Fatalf("wrong profile resolved: %s %+v", name, p)
	}
}

func TestSavePermissions(t *testing.T) {
	p := withTempConfig(t)
	c := &Config{Current: "d", Profiles: map[string]*Profile{
		"d": {Issuer: "https://h/realms/r", ClientID: "c", Upstream: "https://h/ai"},
	}}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("config must be 0600, got %o", fi.Mode().Perm())
	}
}
