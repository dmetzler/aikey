package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dmetzler/aikey/internal/config"
)

type fakeSession struct {
	exp time.Time
	ok  bool
}

func (f fakeSession) Expiry() (time.Time, bool) { return f.exp, f.ok }
func (f fakeSession) Backend() string           { return "file" }

func newTestUI() *UI {
	p := &config.Profile{
		Issuer:   "https://example.invalid/realms/test",
		ClientID: "test-cli",
		Upstream: "https://example.invalid/ai",
		Listen:   "127.0.0.1:4001",
	}
	return New("default",
		fakeSession{exp: time.Now().Add(10 * time.Minute), ok: true},
		func() error { return nil },
		func() error { return nil },
		func() (*config.Profile, error) { return p, nil },
		func(*config.Profile) error { return nil },
	)
}

// A page you visit can POST to 127.0.0.1. Without a CSRF token that would let
// any website reconfigure the proxy or sign you out.
func TestMutationRequiresCSRF(t *testing.T) {
	ui := newTestUI()
	h := ui.Handler()

	r := httptest.NewRequest("POST", "/_aikey/api/logout", nil)
	r.Host = "127.0.0.1:4001"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("POST without CSRF token = %d, want 403", w.Code)
	}

	r = httptest.NewRequest("POST", "/_aikey/api/logout", nil)
	r.Host = "127.0.0.1:4001"
	r.Header.Set("X-Aikey-CSRF", ui.csrf)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("POST with CSRF token = %d, want 200", w.Code)
	}
}

// A hostile name resolving to 127.0.0.1 would otherwise reach this UI with the
// attacker's page as same-origin. The Host header is the defence.
func TestDNSRebindingRejected(t *testing.T) {
	h := newTestUI().Handler()
	for _, host := range []string{"evil.example.com", "evil.example.com:4001"} {
		r := httptest.NewRequest("GET", "/_aikey/api/state", nil)
		r.Host = host
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Errorf("Host %q = %d, want 403", host, w.Code)
		}
	}
}

func TestLoopbackHostsAccepted(t *testing.T) {
	h := newTestUI().Handler()
	for _, host := range []string{"127.0.0.1:4001", "localhost:4001", "[::1]:4001"} {
		r := httptest.NewRequest("GET", "/_aikey/api/state", nil)
		r.Host = host
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Errorf("Host %q = %d, want 200", host, w.Code)
		}
	}
}

func TestCrossOriginRejected(t *testing.T) {
	ui := newTestUI()
	r := httptest.NewRequest("POST", "/_aikey/api/logout", nil)
	r.Host = "127.0.0.1:4001"
	r.Header.Set("Origin", "https://evil.example.com")
	r.Header.Set("X-Aikey-CSRF", ui.csrf)
	w := httptest.NewRecorder()
	ui.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-origin POST = %d, want 403", w.Code)
	}
}

// The UI shows session metadata; leaking the bearer itself into a web page
// would undo the point of keyring storage.
func TestStateNeverExposesTokens(t *testing.T) {
	r := httptest.NewRequest("GET", "/_aikey/api/state", nil)
	r.Host = "127.0.0.1:4001"
	w := httptest.NewRecorder()
	newTestUI().Handler().ServeHTTP(w, r)

	var st map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"access_token", "refresh_token", "token"} {
		if _, found := st[k]; found {
			t.Errorf("state exposes %q", k)
		}
	}
	body := strings.ToLower(w.Body.String())
	if strings.Contains(body, "bearer ") {
		t.Error("state body contains a bearer token")
	}
	if st["logged_in"] != true {
		t.Error("expected logged_in true")
	}
}

// Saving settings must go through the same validation as the CLI, or the UI
// becomes a way to write a config that refuses to start.
func TestSettingsRejectsInvalidProfile(t *testing.T) {
	ui := newTestUI()
	body := strings.NewReader(`{"issuer":"","client_id":"x","upstream":"https://e.invalid/ai"}`)
	r := httptest.NewRequest("POST", "/_aikey/api/settings", body)
	r.Host = "127.0.0.1:4001"
	r.Header.Set("X-Aikey-CSRF", ui.csrf)
	w := httptest.NewRecorder()
	ui.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid settings = %d, want 400", w.Code)
	}
}
