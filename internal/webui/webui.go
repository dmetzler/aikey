// Package webui serves aikey's local settings page.
//
// Threat model: this listens on loopback, but a web page you visit CAN issue
// requests to 127.0.0.1, and a hostile DNS name can be made to resolve there
// (DNS rebinding). So the UI is not "safe because it is local":
//
//   - every mutating request requires a CSRF token read from a same-origin GET,
//   - the Host header must be a loopback literal (blocks DNS rebinding),
//   - Origin/Referer, when present, must be loopback too,
//   - tokens are never rendered; only expiry and status are shown.
package webui

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dmetzler/aikey/internal/autostart"
	"github.com/dmetzler/aikey/internal/config"
)

// Session exposes what the UI needs from the running proxy.
type Session interface {
	Expiry() (time.Time, bool)
	Backend() string
}

// UI is the settings handler.
type UI struct {
	profile   string
	csrf      string
	session   Session
	onLogin   func() error
	onLogout  func() error
	reloadCfg func() (*config.Profile, error)
	saveCfg   func(*config.Profile) error
}

// New builds the settings UI.
func New(profile string, s Session,
	onLogin, onLogout func() error,
	reload func() (*config.Profile, error),
	save func(*config.Profile) error) *UI {

	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return &UI{
		profile:   profile,
		csrf:      base64.RawURLEncoding.EncodeToString(b),
		session:   s,
		onLogin:   onLogin,
		onLogout:  onLogout,
		reloadCfg: reload,
		saveCfg:   save,
	}
}

// isLoopbackHost reports whether a Host header points at loopback.
func isLoopbackHost(h string) bool {
	host := h
	if s, _, err := net.SplitHostPort(h); err == nil {
		host = s
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// guard enforces the anti-rebinding and CSRF rules.
func (u *UI) guard(w http.ResponseWriter, r *http.Request) bool {
	if !isLoopbackHost(r.Host) {
		http.Error(w, "aikey: refusing a non-loopback Host header (possible DNS rebinding)",
			http.StatusForbidden)
		return false
	}
	if o := r.Header.Get("Origin"); o != "" {
		if pu, err := url.Parse(o); err != nil || !isLoopbackHost(pu.Host) {
			http.Error(w, "aikey: cross-origin request refused", http.StatusForbidden)
			return false
		}
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}
	got := r.Header.Get("X-Aikey-CSRF")
	if subtle.ConstantTimeCompare([]byte(got), []byte(u.csrf)) != 1 {
		http.Error(w, "aikey: bad or missing CSRF token", http.StatusForbidden)
		return false
	}
	return true
}

// Handler returns the mux mounted under /_aikey/.
func (u *UI) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/_aikey/", func(w http.ResponseWriter, r *http.Request) {
		if !u.guard(w, r) {
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// No inline event handlers; a strict CSP keeps injected markup inert.
		w.Header().Set("Content-Security-Policy",
			"default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		fmt.Fprint(w, page)
	})

	mux.HandleFunc("/_aikey/api/state", func(w http.ResponseWriter, r *http.Request) {
		if !u.guard(w, r) {
			return
		}
		p, err := u.reloadCfg()
		if err != nil {
			writeJSON(w, 500, map[string]any{"error": err.Error()})
			return
		}
		auto, _ := autostart.Status()

		st := map[string]any{
			"profile":            u.profile,
			"issuer":             p.Issuer,
			"client_id":          p.ClientID,
			"upstream":           p.Upstream,
			"listen":             p.Listen,
			"csrf":               u.csrf,
			"autostart":          auto,
			"autostart_location": autostart.Location(),
			"token_backend":      u.session.Backend(),
		}
		if exp, ok := u.session.Expiry(); ok {
			st["logged_in"] = true
			st["expires_at"] = exp.Format(time.RFC3339)
			st["expires_in_seconds"] = int(time.Until(exp).Seconds())
		} else {
			st["logged_in"] = false
		}
		writeJSON(w, 200, st)
	})

	mux.HandleFunc("/_aikey/api/settings", func(w http.ResponseWriter, r *http.Request) {
		if !u.guard(w, r) {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var in config.Profile
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSON(w, 400, map[string]any{"error": err.Error()})
			return
		}
		if err := in.Validate(); err != nil {
			writeJSON(w, 400, map[string]any{"error": err.Error()})
			return
		}
		if err := u.saveCfg(&in); err != nil {
			writeJSON(w, 500, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]any{
			"ok":   true,
			"note": "Saved. Restart aikey for a changed listen address or upstream to take effect.",
		})
	})

	mux.HandleFunc("/_aikey/api/login", func(w http.ResponseWriter, r *http.Request) {
		if !u.guard(w, r) {
			return
		}
		if err := u.onLogin(); err != nil {
			writeJSON(w, 500, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	})

	mux.HandleFunc("/_aikey/api/logout", func(w http.ResponseWriter, r *http.Request) {
		if !u.guard(w, r) {
			return
		}
		if err := u.onLogout(); err != nil {
			writeJSON(w, 500, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	})

	mux.HandleFunc("/_aikey/api/autostart", func(w http.ResponseWriter, r *http.Request) {
		if !u.guard(w, r) {
			return
		}
		var in struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSON(w, 400, map[string]any{"error": err.Error()})
			return
		}
		var err error
		if in.Enabled {
			err = autostart.Enable(u.profile)
		} else {
			err = autostart.Disable()
		}
		if err != nil {
			writeJSON(w, 500, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	})

	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
