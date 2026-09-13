// Package oidc implements the Authorization Code + PKCE flow (RFC 8252) against
// a Keycloak realm, plus silent refresh. Stdlib only: no external OIDC library,
// so the spike stays auditable and dependency-free.
package oidc

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Endpoints is the subset of the discovery document we need.
type Endpoints struct {
	Authorization string `json:"authorization_endpoint"`
	Token         string `json:"token_endpoint"`
	DeviceAuth    string `json:"device_authorization_endpoint"`
}

// Discover fetches .well-known/openid-configuration for the issuer.
func Discover(ctx context.Context, issuer string) (*Endpoints, error) {
	u := strings.TrimRight(issuer, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("discovery: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("discovery: %s returned %s", u, resp.Status)
	}
	var e Endpoints
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
		return nil, fmt.Errorf("discovery: decode: %w", err)
	}
	if e.Authorization == "" || e.Token == "" {
		return nil, fmt.Errorf("discovery: incomplete document from %s", u)
	}
	return &e, nil
}

// Token is a token response, normalised with an absolute expiry.
type Token struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// Expired reports whether the access token is within skew of expiry. Callers
// refresh proactively so a request never leaves with a token about to die.
func (t *Token) Expired(skew time.Duration) bool {
	return t == nil || t.AccessToken == "" || time.Now().Add(skew).After(t.ExpiresAt)
}

type tokenResponse struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	ExpiresIn        int    `json:"expires_in"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

func postForm(ctx context.Context, endpoint string, form url.Values) (*tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return nil, fmt.Errorf("token endpoint: decode: %w", err)
	}
	if tr.Error != "" {
		return &tr, fmt.Errorf("token endpoint: %s: %s", tr.Error, tr.ErrorDescription)
	}
	if resp.StatusCode != http.StatusOK {
		return &tr, fmt.Errorf("token endpoint: %s", resp.Status)
	}
	return &tr, nil
}

func toToken(tr *tokenResponse) *Token {
	return &Token{
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		ExpiresAt:    time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second),
	}
}

func randomURLSafe(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Login runs the browser flow: loopback listener on an ephemeral port, PKCE
// S256, strict state check.
func Login(ctx context.Context, ep *Endpoints, clientID string) (*Token, error) {
	verifier, err := randomURLSafe(48)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	state, err := randomURLSafe(24)
	if err != nil {
		return nil, err
	}

	// Port 0 lets the OS pick a free port, per RFC 8252. The Keycloak client
	// must therefore allow a trailing wildcard: http://127.0.0.1:*
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("loopback listener: %w", err)
	}
	defer ln.Close()
	redirect := fmt.Sprintf("http://127.0.0.1:%d/callback", ln.Addr().(*net.TCPAddr).Port)

	type result struct {
		code string
		err  error
	}
	results := make(chan result, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if e := q.Get("error"); e != "" {
			http.Error(w, "login failed: "+e, http.StatusBadRequest)
			results <- result{err: fmt.Errorf("authorization failed: %s: %s", e, q.Get("error_description"))}
			return
		}
		if q.Get("state") != state {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			results <- result{err: fmt.Errorf("state mismatch: possible CSRF, aborting")}
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, "<html><body><h3>aikey: login complete</h3>"+
			"<p>You can close this tab and return to the terminal.</p></body></html>")
		results <- result{code: q.Get("code")}
	})

	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	defer srv.Close()

	auth, _ := url.Parse(ep.Authorization)
	q := auth.Query()
	q.Set("client_id", clientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", redirect)
	q.Set("scope", "openid email profile")
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	auth.RawQuery = q.Encode()

	fmt.Println("Opening your browser to sign in...")
	fmt.Println("If it does not open, visit:")
	fmt.Println("  " + auth.String())
	_ = openBrowser(auth.String())

	var res result
	select {
	case res = <-results:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(5 * time.Minute):
		return nil, fmt.Errorf("timed out waiting for the browser callback")
	}
	if res.err != nil {
		return nil, res.err
	}

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", clientID)
	form.Set("code", res.code)
	form.Set("redirect_uri", redirect)
	form.Set("code_verifier", verifier)

	tr, err := postForm(ctx, ep.Token, form)
	if err != nil {
		return nil, err
	}
	return toToken(tr), nil
}

// Refresh exchanges a refresh token. The bool reports whether the failure is
// terminal (re-login required) rather than transient (network).
func Refresh(ctx context.Context, ep *Endpoints, clientID, refreshToken string) (*Token, bool, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", clientID)
	form.Set("refresh_token", refreshToken)

	tr, err := postForm(ctx, ep.Token, form)
	if err != nil {
		// invalid_grant means the session is gone: revoked, expired, or the
		// user was disabled in Keycloak. That is the whole point of this
		// design, so surface it as "needs login", not as an outage.
		terminal := tr != nil && (tr.Error == "invalid_grant" || tr.Error == "invalid_token")
		return nil, terminal, err
	}
	return toToken(tr), false, nil
}

// DeviceCode is the device-flow initiation response.
type DeviceCode struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	Complete        string `json:"verification_uri_complete"`
	Interval        int    `json:"interval"`
	ExpiresIn       int    `json:"expires_in"`
}

// StartDevice begins the device flow, for headless hosts (SSH, CI).
func StartDevice(ctx context.Context, ep *Endpoints, clientID string) (*DeviceCode, error) {
	if ep.DeviceAuth == "" {
		return nil, fmt.Errorf("this realm does not advertise a device authorization endpoint")
	}
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("scope", "openid email profile")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep.DeviceAuth,
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("device authorization: %s", resp.Status)
	}
	var dc DeviceCode
	if err := json.NewDecoder(resp.Body).Decode(&dc); err != nil {
		return nil, err
	}
	if dc.Interval == 0 {
		dc.Interval = 5
	}
	return &dc, nil
}

// PollDevice polls until the user approves, or the code expires.
func PollDevice(ctx context.Context, ep *Endpoints, clientID string, dc *DeviceCode) (*Token, error) {
	deadline := time.Now().Add(time.Duration(dc.ExpiresIn) * time.Second)
	interval := time.Duration(dc.Interval) * time.Second

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}

		form := url.Values{}
		form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
		form.Set("client_id", clientID)
		form.Set("device_code", dc.DeviceCode)

		tr, err := postForm(ctx, ep.Token, form)
		if err == nil {
			return toToken(tr), nil
		}
		if tr == nil {
			return nil, err
		}
		switch tr.Error {
		case "authorization_pending":
			continue
		case "slow_down":
			interval += 5 * time.Second
		default:
			return nil, err
		}
	}
	return nil, fmt.Errorf("device code expired before approval")
}

func openBrowser(u string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", u).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", u).Start()
	default:
		return exec.Command("xdg-open", u).Start()
	}
}
