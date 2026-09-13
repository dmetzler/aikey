package proxy

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dmetzler/aikey/internal/oidc"
)

// fakeIDP serves just enough of a token endpoint to drive refresh paths.
func fakeIDP(t *testing.T, behave func(refreshCount int32) (int, string)) (*oidc.Endpoints, *int32) {
	t.Helper()
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := atomic.AddInt32(&n, 1)
		code, body := behave(c)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return &oidc.Endpoints{Token: srv.URL, Authorization: srv.URL + "/auth"}, &n
}

func storeWith(t *testing.T, tok *oidc.Token) *oidc.Store {
	t.Helper()
	s := oidc.NewStore(filepath.Join(t.TempDir(), "config.json"), "test")
	if tok != nil {
		if err := s.Save(tok); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

// A token near expiry must be refreshed BEFORE the request goes out, otherwise
// a long stream can start on a token that dies mid-flight.
func TestRefreshesBeforeExpiry(t *testing.T) {
	ep, calls := fakeIDP(t, func(int32) (int, string) {
		return 200, `{"access_token":"fresh","refresh_token":"r2","expires_in":900}`
	})
	st := storeWith(t, &oidc.Token{
		AccessToken:  "stale",
		RefreshToken: "r1",
		ExpiresAt:    time.Now().Add(30 * time.Second), // inside the 60s margin
	})
	ts, err := NewTokenSource(ep, "cid", st)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ts.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "fresh" {
		t.Fatalf("expected a refreshed token, got %q", got)
	}
	if *calls != 1 {
		t.Fatalf("expected exactly 1 refresh, got %d", *calls)
	}
}

// A comfortably valid token must NOT trigger a refresh on every request.
func TestNoRefreshWhenValid(t *testing.T) {
	ep, calls := fakeIDP(t, func(int32) (int, string) {
		return 200, `{"access_token":"should-not-happen","expires_in":900}`
	})
	st := storeWith(t, &oidc.Token{
		AccessToken: "good", RefreshToken: "r1",
		ExpiresAt: time.Now().Add(10 * time.Minute),
	})
	ts, _ := NewTokenSource(ep, "cid", st)
	for i := 0; i < 3; i++ {
		if got, err := ts.Token(context.Background()); err != nil || got != "good" {
			t.Fatalf("got %q err %v", got, err)
		}
	}
	if *calls != 0 {
		t.Fatalf("expected no refresh, got %d", *calls)
	}
}

// invalid_grant means the Keycloak session is gone (user disabled/revoked).
// That must surface as "log in again", not as a generic outage -- it is the
// revocation story that justifies this whole design.
func TestRevokedSessionNeedsLogin(t *testing.T) {
	ep, _ := fakeIDP(t, func(int32) (int, string) {
		return 400, `{"error":"invalid_grant","error_description":"Session not active"}`
	})
	st := storeWith(t, &oidc.Token{
		AccessToken: "old", RefreshToken: "dead",
		ExpiresAt: time.Now().Add(-time.Minute),
	})
	ts, _ := NewTokenSource(ep, "cid", st)
	if _, err := ts.Token(context.Background()); err != ErrNeedsLogin {
		t.Fatalf("expected ErrNeedsLogin, got %v", err)
	}
}

// The dummy client key must never reach upstream, and the real bearer must.
func TestInjectsTokenAndStripsClientKey(t *testing.T) {
	var gotAuth string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer up.Close()

	st := storeWith(t, &oidc.Token{AccessToken: "real-token", RefreshToken: "r",
		ExpiresAt: time.Now().Add(time.Hour)})
	ts, _ := NewTokenSource(&oidc.Endpoints{Token: "http://unused"}, "cid", st)
	s, err := New("127.0.0.1:0", up.URL, ts)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer dummy-client-key")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if gotAuth != "Bearer real-token" {
		t.Fatalf("upstream saw %q", gotAuth)
	}
}

// Logged out must yield an actionable 401, since tools show the body.
func TestLoggedOutGivesActionable401(t *testing.T) {
	st := storeWith(t, nil)
	ts, _ := NewTokenSource(&oidc.Endpoints{Token: "http://unused"}, "cid", st)
	s, _ := New("127.0.0.1:0", "http://unused", ts)

	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader("{}")))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	var body map[string]map[string]string
	json.Unmarshal(rec.Body.Bytes(), &body)
	if !strings.Contains(body["error"]["message"], "aikey login") {
		t.Fatalf("401 body should tell the user what to do, got %q", rec.Body.String())
	}
}

// THE spike question: does an SSE stream arrive incrementally, or does the
// reverse proxy buffer it until the upstream closes? Buffering is the classic
// failure of hand-rolled LLM proxies.
func TestSSEStreamsIncrementally(t *testing.T) {
	release := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"chunk\":1}\n\n")
		fl.Flush()
		<-release // hold the response open
		fmt.Fprint(w, "data: [DONE]\n\n")
		fl.Flush()
	}))
	defer up.Close()

	st := storeWith(t, &oidc.Token{AccessToken: "t", RefreshToken: "r",
		ExpiresAt: time.Now().Add(time.Hour)})
	ts, _ := NewTokenSource(&oidc.Endpoints{Token: "http://unused"}, "cid", st)
	s, _ := New("127.0.0.1:0", up.URL, ts)

	front := httptest.NewServer(s)
	defer front.Close()

	resp, err := http.Post(front.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	type readResult struct {
		line string
		err  error
	}
	ch := make(chan readResult, 1)
	go func() {
		l, err := bufio.NewReader(resp.Body).ReadString('\n')
		ch <- readResult{l, err}
	}()

	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("read error: %v", r.err)
		}
		if !strings.Contains(r.line, "chunk") {
			t.Fatalf("unexpected first line %q", r.line)
		}
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("first SSE chunk was buffered: the proxy is not streaming")
	}
	close(release)
}

// Chunked JSON streaming (Anthropic-style), as opposed to SSE above.
//
// HONESTY NOTE: both streaming tests still pass when FlushInterval is set to
// 30s, so neither has been proven to catch a buffering regression -- Go's
// httptest stack appears to flush small writes on its own. They assert the
// user-visible behaviour (first chunk arrives promptly) but must NOT be trusted
// as a guard on FlushInterval. Verify real streaming against a live upstream
// before removing that setting.
func TestChunkedJSONStreamsIncrementally(t *testing.T) {
	release := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fl := w.(http.Flusher)
		fmt.Fprint(w, "{\"chunk\":1}\n")
		fl.Flush()
		<-release
		fmt.Fprint(w, "{\"done\":true}\n")
		fl.Flush()
	}))
	defer up.Close()

	st := storeWith(t, &oidc.Token{AccessToken: "t", RefreshToken: "r",
		ExpiresAt: time.Now().Add(time.Hour)})
	ts, _ := NewTokenSource(&oidc.Endpoints{Token: "http://unused"}, "cid", st)
	s, _ := New("127.0.0.1:0", up.URL, ts)

	front := httptest.NewServer(s)
	defer front.Close()

	resp, err := http.Post(front.URL+"/v1/messages", "application/json",
		strings.NewReader(`{"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	type readResult struct {
		line string
		err  error
	}
	ch := make(chan readResult, 1)
	go func() {
		l, err := bufio.NewReader(resp.Body).ReadString('\n')
		ch <- readResult{l, err}
	}()

	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("read error: %v", r.err)
		}
		if !strings.Contains(r.line, "chunk") {
			t.Fatalf("unexpected first line %q", r.line)
		}
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("first chunk was buffered: FlushInterval is not -1")
	}
	close(release)
}

// Binding a non-loopback address would let anything on the LAN spend tokens.
func TestRefusesNonLoopbackBind(t *testing.T) {
	st := storeWith(t, nil)
	ts, _ := NewTokenSource(&oidc.Endpoints{Token: "http://unused"}, "cid", st)
	s, _ := New("0.0.0.0:4001", "http://unused", ts)
	err := s.ListenAndServe()
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("expected a loopback refusal, got %v", err)
	}
}
