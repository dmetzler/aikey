// Package config loads aikey's on-disk configuration.
//
// There are deliberately NO built-in defaults for the Keycloak issuer or the
// LiteLLM endpoint. This repository is public: shipping a default would leak a
// private deployment's URLs to anyone who clones it. Both values must come from
// the user's own config file (or the matching environment variables), and aikey
// refuses to start without them.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Profile is one named endpoint set. Profiles let a single install talk to
// several deployments (work, home, staging) without editing files by hand.
type Profile struct {
	Issuer   string `json:"issuer"`    // Keycloak realm URL
	ClientID string `json:"client_id"` // public OIDC client
	Upstream string `json:"upstream"`  // LiteLLM base URL
	Listen   string `json:"listen,omitempty"`
}

// Config is the whole file: a set of profiles plus the active one.
type Config struct {
	Current  string              `json:"current"`
	Profiles map[string]*Profile `json:"profiles"`
}

const (
	// DefaultListen is a loopback bind. This is the ONE default we keep,
	// because the safe value is the restrictive one.
	DefaultListen = "127.0.0.1:4001"
	// DefaultProfile names the profile created by `aikey init`.
	DefaultProfile = "default"
)

// Path returns the config file location, honouring AIKEY_CONFIG.
func Path() (string, error) {
	if p := os.Getenv("AIKEY_CONFIG"); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate user config dir: %w", err)
	}
	return filepath.Join(dir, "aikey", "config.json"), nil
}

// Load reads the config file. A missing file is not an error here: callers
// decide whether that is fatal, so `aikey init` can run on a clean machine.
func Load() (*Config, error) {
	p, err := Path()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return &Config{Profiles: map[string]*Profile{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", p, err)
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", p, err)
	}
	if c.Profiles == nil {
		c.Profiles = map[string]*Profile{}
	}
	return &c, nil
}

// Save writes the config file with 0600, creating parents as needed.
func (c *Config) Save() error {
	p, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, append(b, '\n'), 0o600)
}

// Resolve returns the profile to use, applying env overrides on top.
// name == "" means "the current profile".
func (c *Config) Resolve(name string) (*Profile, string, error) {
	if name == "" {
		name = os.Getenv("AIKEY_PROFILE")
	}
	if name == "" {
		name = c.Current
	}
	if name == "" {
		name = DefaultProfile
	}

	p := c.Profiles[name]
	if p == nil {
		p = &Profile{}
	} else {
		cp := *p
		p = &cp
	}

	// Environment wins over the file, so CI and one-off runs need no file.
	if v := os.Getenv("AIKEY_ISSUER"); v != "" {
		p.Issuer = v
	}
	if v := os.Getenv("AIKEY_CLIENT_ID"); v != "" {
		p.ClientID = v
	}
	if v := os.Getenv("AIKEY_UPSTREAM"); v != "" {
		p.Upstream = v
	}
	if v := os.Getenv("AIKEY_LISTEN"); v != "" {
		p.Listen = v
	}
	if p.Listen == "" {
		p.Listen = DefaultListen
	}

	if err := p.Validate(); err != nil {
		return nil, name, err
	}
	return p, name, nil
}

// Validate enforces that the endpoints were actually supplied. The error text
// points at `aikey init` because an empty config is the expected first-run
// state, not a corruption.
func (p *Profile) Validate() error {
	var missing []string
	if p.Issuer == "" {
		missing = append(missing, "issuer")
	}
	if p.ClientID == "" {
		missing = append(missing, "client_id")
	}
	if p.Upstream == "" {
		missing = append(missing, "upstream")
	}
	if len(missing) > 0 {
		return fmt.Errorf(
			"missing required setting(s): %s\n\nRun `aikey init` to configure, "+
				"or set AIKEY_ISSUER / AIKEY_CLIENT_ID / AIKEY_UPSTREAM.\n"+
				"aikey ships no default endpoints on purpose.",
			strings.Join(missing, ", "))
	}
	if !strings.HasPrefix(p.Issuer, "https://") && !strings.HasPrefix(p.Issuer, "http://") {
		return fmt.Errorf("issuer must be an absolute URL, got %q", p.Issuer)
	}
	if !strings.HasPrefix(p.Upstream, "https://") && !strings.HasPrefix(p.Upstream, "http://") {
		return fmt.Errorf("upstream must be an absolute URL, got %q", p.Upstream)
	}
	p.Issuer = strings.TrimRight(p.Issuer, "/")
	p.Upstream = strings.TrimRight(p.Upstream, "/")
	return nil
}
