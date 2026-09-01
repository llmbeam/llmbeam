// Package server composes pairing, backend discovery, and the embedded UI into
// one HTTP handler.
package server

import (
	"context"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/shao-hua-li/llmbeam/internal/backend"
	"github.com/shao-hua-li/llmbeam/internal/pair"
)

const sessionCookieName = "sc_session"

// Server owns the HTTP routing layer for the gateway.
type Server struct {
	pairs    *pair.Manager
	registry *backend.Registry
	limiter  *pair.RateLimiter
	static   fs.FS
	upstream *http.Client
}

// New constructs a gateway server. static may be nil in API-only tests and
// development builds where the web UI has not been compiled yet.
func New(pairs *pair.Manager, registry *backend.Registry, limiter *pair.RateLimiter, static fs.FS) *Server {
	return &Server{
		pairs:    pairs,
		registry: registry,
		limiter:  limiter,
		static:   static,
		upstream: newUpstreamClient(),
	}
}

// Handler builds the complete HTTP handler with security middleware.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("POST /api/pair", s.handlePair)
	mux.Handle("GET /api/session", s.auth(http.HandlerFunc(s.handleSession)))
	mux.Handle("GET /api/models", s.auth(http.HandlerFunc(s.handleModels)))
	mux.Handle("POST /api/chat", s.auth(http.HandlerFunc(s.handleChat)))
	if s.static != nil {
		mux.Handle("GET /", http.FileServerFS(s.static))
	}
	return securityHeaders(originCheck(mux))
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers := w.Header()
		headers.Set("X-Content-Type-Options", "nosniff")
		headers.Set("X-Frame-Options", "DENY")
		headers.Set("Referrer-Policy", "no-referrer")
		headers.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		headers.Set("Content-Security-Policy", strings.Join([]string{
			"default-src 'self'",
			"connect-src 'self'",
			"img-src 'self' data:",
			"style-src 'self' 'unsafe-inline'",
			"object-src 'none'",
			"base-uri 'none'",
			"frame-ancestors 'none'",
			"form-action 'self'",
		}, "; "))
		next.ServeHTTP(w, r)
	})
}

func originCheck(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			if origin := r.Header.Get("Origin"); origin != "" && !sameOrigin(origin, r.Host) {
				jsonError(w, http.StatusForbidden, "cross_origin_forbidden")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func sameOrigin(rawOrigin, requestHost string) bool {
	origin, err := url.Parse(rawOrigin)
	if err != nil || (origin.Scheme != "http" && origin.Scheme != "https") {
		return false
	}
	if origin.User != nil || origin.Host == "" || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return false
	}
	return strings.EqualFold(origin.Host, requestHost)
}

type sessionContextKey struct{}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil {
			jsonError(w, http.StatusUnauthorized, "unauthenticated")
			return
		}
		session, ok := s.pairs.Session(cookie.Value)
		if !ok {
			jsonError(w, http.StatusUnauthorized, "unauthenticated")
			return
		}
		ctx := context.WithValue(r.Context(), sessionContextKey{}, session)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func sessionFromRequest(r *http.Request) (pair.Session, bool) {
	session, ok := r.Context().Value(sessionContextKey{}).(pair.Session)
	return session, ok
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

var logger = slog.Default()
