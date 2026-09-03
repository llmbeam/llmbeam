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
	"time"

	"github.com/llmbeam/llmbeam/internal/backend"
	"github.com/llmbeam/llmbeam/internal/pair"
)

const sessionCookieName = "sc_session"

// Server owns the HTTP routing layer for the gateway.
type Server struct {
	pairs      *pair.Manager
	connectors *pair.ConnectorManager
	registry   *backend.Registry
	limiter    *pair.RateLimiter
	static     fs.FS
	upstream   *http.Client
}

// New constructs a gateway server. static may be nil in API-only tests and
// development builds where the web UI has not been compiled yet.
func New(pairs *pair.Manager, registry *backend.Registry, limiter *pair.RateLimiter, static fs.FS) *Server {
	return NewWithConnector(pairs, registry, limiter, static, pair.NewConnectorManager())
}

// NewWithConnector constructs a gateway with an explicit connector manager.
// It is useful for tests and for applications that need to manage connector
// credentials independently from browser sessions.
func NewWithConnector(pairs *pair.Manager, registry *backend.Registry, limiter *pair.RateLimiter, static fs.FS, connectors *pair.ConnectorManager) *Server {
	if connectors == nil {
		connectors = pair.NewConnectorManager()
	}
	return &Server{
		pairs:      pairs,
		connectors: connectors,
		registry:   registry,
		limiter:    limiter,
		static:     static,
		upstream:   newUpstreamClient(),
	}
}

// ConnectorCode returns the current one-time code for native connectors.
func (s *Server) ConnectorCode() string {
	return s.connectors.Code()
}

// ConnectorCodeExpiry returns the expiry of the current native connector code.
func (s *Server) ConnectorCodeExpiry() time.Time {
	return s.connectors.CodeExpiry()
}

// Handler builds the complete HTTP handler with security middleware.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("POST /api/pair", s.handlePair)
	mux.Handle("GET /api/session", s.auth(http.HandlerFunc(s.handleSession)))
	mux.Handle("GET /api/models", s.auth(http.HandlerFunc(s.handleModels)))
	mux.Handle("POST /api/chat", s.auth(http.HandlerFunc(s.handleChat)))
	mux.HandleFunc("GET /api/connector/info", s.handleConnectorInfo)
	mux.HandleFunc("POST /api/connector/pair", s.handleConnectorPair)
	mux.Handle("POST /api/connector/refresh", s.connectorAuth(http.HandlerFunc(s.handleConnectorRefresh)))
	mux.Handle("POST /api/connector/revoke", s.connectorAuth(http.HandlerFunc(s.handleConnectorRevoke)))
	mux.Handle("GET /v1/models", s.connectorAuth(http.HandlerFunc(s.handleOpenAIModels)))
	mux.Handle("POST /v1/chat/completions", s.connectorAuth(http.HandlerFunc(s.handleOpenAIChat)))
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
