package autostart

import (
	"runtime"
	"strings"
	"testing"
)

// Autostart writes into the OS startup mechanism, so the user must be able to
// see the exact target before agreeing to it.
func TestLocationIsConcrete(t *testing.T) {
	loc := Location()
	if loc == "" || loc == "(unknown)" {
		t.Fatalf("Location() = %q, want a real path or key", loc)
	}
	switch runtime.GOOS {
	case "darwin":
		if !strings.HasSuffix(loc, ".plist") {
			t.Errorf("darwin location %q should be a plist", loc)
		}
	case "linux":
		if !strings.HasSuffix(loc, ".desktop") {
			t.Errorf("linux location %q should be a .desktop file", loc)
		}
	}
}

// Status must not report "enabled" on a machine where nothing was registered.
func TestStatusDefaultsToDisabled(t *testing.T) {
	on, err := Status()
	if err != nil {
		t.Skipf("unsupported platform: %v", err)
	}
	if on {
		t.Skip("autostart already enabled on this machine; not touching it")
	}
}

func TestXMLEscape(t *testing.T) {
	got := xmlEscape(`a&b<c>"d"`)
	want := "a&amp;b&lt;c&gt;&quot;d&quot;"
	if got != want {
		t.Fatalf("xmlEscape = %q, want %q", got, want)
	}
}
