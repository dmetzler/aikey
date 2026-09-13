package main

import (
	"strings"
	"testing"
)

// Finder launches CFBundleExecutable with no arguments, and on older systems
// appends -psn_<n>. Both must reach `serve` rather than printing usage, which
// is what made the first .app open and do nothing.
func TestBundleArgDefaults(t *testing.T) {
	cases := []struct {
		name     string
		raw      []string
		inBundle bool
		want     string // "" means: print usage and exit
	}{
		{"cli no args prints usage", nil, false, ""},
		{"bundle no args serves", nil, true, "serve"},
		{"bundle psn flag serves", []string{"-psn_0_12345"}, true, "serve"},
		{"cli explicit command wins", []string{"status"}, false, "status"},
		{"bundle explicit command wins", []string{"login"}, true, "login"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := resolveArgs(c.raw, c.inBundle)
			if c.want == "" {
				if ok {
					t.Fatalf("expected usage, got command %q", got[0])
				}
				return
			}
			if !ok {
				t.Fatal("expected a command, got usage")
			}
			if got[0] != c.want {
				t.Fatalf("command = %q, want %q", got[0], c.want)
			}
		})
	}
}

func TestUsageMentionsEveryCommand(t *testing.T) {
	for _, cmd := range []string{"init", "login", "serve", "status", "logout", "profiles", "autostart"} {
		if !strings.Contains(usage, "aikey "+cmd) {
			t.Errorf("usage does not document %q", cmd)
		}
	}
}
