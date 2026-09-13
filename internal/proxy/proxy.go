// Package proxy is the loopback HTTP proxy that injects a fresh bearer token.
//
// Clients point OPENAI_BASE_URL at this process and use any dummy API key. The
// real token never reaches the client, and is refreshed per request, which is
// what makes short-lived tokens usable by long-running tools.
package proxy

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/dmetzler/aikey/internal/oidc"
)

// TokenSource yields a valid access token, refreshing when needed.
type TokenSource struct {
	mu       sync.Mutex
	ep       *oidc.Endpoints
	clientID string
	store    *oidc.Store
	tok      *oidc.Token
}

// NewTokenSource loads any persisted token; a missing one is not fatal so the
// proxy can start and return a clear 401 telling the user to log in.
func NewTokenSource(ep *oidc.Endpoints, clientID string, store *oidc.Store) (*TokenSource, error) {
	t, err := store.Load()
	if err != nil {
		return nil, err
	}
	return &TokenSource{ep: ep, clientID: clientID, store: store, tok: t}, nil
}

// ErrNeedsLogin means interactive re-authentication is required.
var ErrNeedsLogin = fmt.Errorf("not logged in")

// Token returns a valid access token, refreshing with a 60s safety margin so a
// long streaming response cannot start on a token that dies mid-flight.
func (ts *TokenSource) Token(ctx context.Context) (string, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	if ts.tok == nil || ts.tok.RefreshToken == "" && ts.tok.AccessToken == "" {
		return "", ErrNeedsLogin
	}
	if !ts.tok.Expired(60 * time.Second) {
		return ts.tok.AccessToken, nil
	}
	if ts.tok.RefreshToken == "" {
		return "", ErrNeedsLogin
	}

	nt, terminal, err := oidc.Refresh(ctx, ts.ep, ts.clientID, ts.tok.RefreshToken)
	if err != nil {
		if terminal {
			// Revoked or disabled upstream: real revocation, by design.
			return "", ErrNeedsLogin
		}
		return "", fmt.Errorf("refresh failed: %w", err)
	}
	if nt.RefreshToken == "" {
		nt.RefreshToken = ts.tok.RefreshToken
	}
	ts.tok = nt
	if err := ts.store.Save(nt); err != nil {
		log.Printf("warning: could not persist refreshed token: %v", err)
	}
	return nt.AccessToken, nil
}

// Expiry reports the current token expiry, for `aikey status`.
func (ts *TokenSource) Expiry() (time.Time, bool) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.tok == nil {
		return time.Time{}, false
	}
	return ts.tok.ExpiresAt, true
}

// Server is the loopback proxy.
type Server struct {
	listen string
	proxy  *httputil.ReverseProxy
	ts     *TokenSource
}

// New builds the reverse proxy for upstream.
func New(listen, upstream string, ts *TokenSource) (*Server, error) {
	u, err := url.Parse(upstream)
	if err != nil {
		return nil, fmt.Errorf("parse upstream: %w", err)
	}

	rp := &httputil.ReverseProxy{
		// FlushInterval -1 flushes every write immediately. Without it, SSE
		// token streams are buffered and the client appears to hang until the
		// response ends -- the classic failure of hand-rolled LLM proxies.
		FlushInterval: -1,
		Director: func(r *http.Request) {
			r.URL.Scheme = u.Scheme
			r.URL.Host = u.Host
			r.URL.Path = singleJoin(u.Path, r.URL.Path)
			r.Host = u.Host
			// Strip any client-supplied credential: the dummy key must never
			// be forwarded, and we substitute the real bearer below.
			r.Header.Del("Authorization")
			r.Header.Del("X-Api-Key")
			if tok, ok := r.Context().Value(tokenKey{}).(string); ok {
				r.Header.Set("Authorization", "Bearer "+tok)
			}
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("upstream error: %v", err)
			writeJSONError(w, http.StatusBadGateway, "upstream_error", err.Error())
		},
	}

	return &Server{listen: listen, proxy: rp, ts: ts}, nil
}

type tokenKey struct{}

func singleJoin(a, b string) string {
	a = strings.TrimRight(a, "/")
	if b == "" {
		return a
	}
	if !strings.HasPrefix(b, "/") {
		b = "/" + b
	}
	return a + b
}

func writeJSONError(w http.ResponseWriter, code int, kind, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	fmt.Fprintf(w, `{"error":{"type":%q,"message":%q}}`+"\n", kind, msg)
}

// ServeHTTP injects the token, then proxies.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/_aikey/health" {
		w.Header().Set("Content-Type", "application/json")
		exp, ok := s.ts.Expiry()
		if !ok {
			fmt.Fprint(w, `{"status":"logged_out"}`+"\n")
			return
		}
		fmt.Fprintf(w, `{"status":"ok","expires_at":%q,"expires_in_seconds":%d}`+"\n",
			exp.Format(time.RFC3339), int(time.Until(exp).Seconds()))
		return
	}

	tok, err := s.ts.Token(r.Context())
	if err != nil {
		if err == ErrNeedsLogin {
			// Explicit, actionable message: tools surface the body to the user.
			writeJSONError(w, http.StatusUnauthorized, "auth_error",
				"aikey: re-authentication required -- run `aikey login`")
			return
		}
		writeJSONError(w, http.StatusServiceUnavailable, "auth_error", err.Error())
		return
	}

	ctx := contextWithToken(r.Context(), tok)
	s.proxy.ServeHTTP(w, r.WithContext(ctx))
}

func contextWithToken(ctx context.Context, tok string) context.Context {
	return context.WithValue(ctx, tokenKey{}, tok)
}

// ListenAndServe binds and serves. It refuses any non-loopback bind: the proxy
// hands out credentials to whoever can reach it, so exposing it on a LAN
// interface would turn it into an open relay for your identity.
func (s *Server) ListenAndServe() error {
	host, _, err := net.SplitHostPort(s.listen)
	if err != nil {
		return fmt.Errorf("invalid listen address %q: %w", s.listen, err)
	}
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf(
			"refusing to listen on %q: aikey must bind a loopback address "+
				"(127.0.0.1 or ::1); anyone who can reach this port can spend your tokens", s.listen)
	}

	srv := &http.Server{
		Addr:    s.listen,
		Handler: s,
		// No WriteTimeout: streaming completions can legitimately run for
		// minutes and a write deadline would truncate them mid-stream.
		ReadHeaderTimeout: 30 * time.Second,
	}
	log.Printf("aikey listening on http://%s", s.listen)
	return srv.ListenAndServe()
}

var _ = io.Discard
