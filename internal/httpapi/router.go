// Package httpapi serves the authenticated JSON API.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/thejohnd0e/ProxyWard/internal/apierr"
	"github.com/thejohnd0e/ProxyWard/internal/auth"
	"github.com/thejohnd0e/ProxyWard/internal/domain"
	"github.com/thejohnd0e/ProxyWard/internal/secrets"
	"github.com/thejohnd0e/ProxyWard/internal/store"
)

// Default request body cap; proxy routes use maxProxyBodyBytes.
const maxBodyBytes = 64 << 10

// Server holds the API's dependencies.
type Server struct {
	db        *store.Store
	cipher    *secrets.Cipher
	clock     domain.Clock
	logger    *slog.Logger
	secure    bool
	auth      *auth.Authenticator
	jobs      JobQueue
	geo       GeoPeeker
	reconcile func(context.Context) error
	limiter   *auth.LoginLimiter
}

// Option customizes the API server.
type Option func(*Server)

// WithClock replaces the wall clock (used by tests).
func WithClock(c domain.Clock) Option { return func(s *Server) { s.clock = c } }

// WithSecureCookies marks session cookies Secure, for HTTPS deployments.
func WithSecureCookies(secure bool) Option { return func(s *Server) { s.secure = secure } }

// WithLogger sets the structured logger.
func WithLogger(l *slog.Logger) Option { return func(s *Server) { s.logger = l } }

// New builds the API handler. Routes are registered under /api/.
func New(db *store.Store, cipher *secrets.Cipher, opts ...Option) http.Handler {
	s := &Server{db: db, cipher: cipher, clock: domain.SystemClock{}, logger: slog.Default()}
	for _, o := range opts {
		o(s)
	}
	s.auth = &auth.Authenticator{Store: db, Clock: s.clock, Secure: s.secure}
	s.limiter = auth.NewLoginLimiter(s.clock)
	if cipher != nil {
		db.UseSecrets(cipher)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/auth/login", s.login)
	mux.HandleFunc("POST /api/auth/setup", s.setup)
	mux.HandleFunc("GET /api/auth/session", s.session)
	mux.Handle("POST /api/auth/logout", s.auth.Require(http.HandlerFunc(s.logout)))
	mux.Handle("PUT /api/auth/password", s.auth.Require(http.HandlerFunc(s.changePassword)))
	s.registerProxyRoutes(mux)
	s.registerJobRoutes(mux)
	s.registerSettingsRoutes(mux)
	s.registerHistoryRoutes(mux)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		apierr.Write(w, http.StatusNotFound, "not_found", "Not found.", nil)
	})
	return noStore(mux)
}

func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// decodeJSON reads a bounded JSON body into v, rejecting unknown fields.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	return decodeJSONLimit(w, r, v, maxBodyBytes)
}

func decodeJSONLimit(w http.ResponseWriter, r *http.Request, v any, limit int64) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			apierr.Write(w, http.StatusRequestEntityTooLarge, "payload_too_large", "Request body is too large.", nil)
			return false
		}
		apierr.Write(w, http.StatusBadRequest, "invalid_json", "Request body is not valid.", nil)
		return false
	}
	if _, err := dec.Token(); err != io.EOF {
		apierr.Write(w, http.StatusBadRequest, "invalid_json", "Request body is not valid.", nil)
		return false
	}
	return true
}
