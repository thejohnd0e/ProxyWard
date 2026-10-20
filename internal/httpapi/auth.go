package httpapi

import (
	"errors"
	"net"
	"net/http"
	"strconv"

	"github.com/thejohnd0e/ProxyWard/internal/apierr"
	"github.com/thejohnd0e/ProxyWard/internal/auth"
	"github.com/thejohnd0e/ProxyWard/internal/store"
)

type loginRequest struct {
	Password string `json:"password"`
}

// peerAddr returns the TCP peer host. Forwarded headers are deliberately
// ignored: any client could forge them.
func peerAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !auth.OriginAllowed(r) {
		apierr.Write(w, http.StatusForbidden, "forbidden_origin", "Cross-origin request rejected.", nil)
		return
	}
	peer := peerAddr(r)
	if ok, wait := s.limiter.Allow(peer); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		apierr.Write(w, http.StatusTooManyRequests, "rate_limited", "Too many login attempts. Try again later.", nil)
		return
	}
	var req loginRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	hash, err := s.db.AdminPasswordHash(r.Context())
	if errors.Is(err, store.ErrNotFound) {
		apierr.Write(w, http.StatusServiceUnavailable, "not_initialized",
			"The administrator password has not been set.", nil)
		return
	}
	if err != nil {
		s.internalError(w, "read admin", err)
		return
	}
	if !auth.Verify(hash, req.Password) {
		s.logger.Warn("login failed", "peer", peer)
		apierr.Write(w, http.StatusUnauthorized, "invalid_credentials", "Invalid password.", nil)
		return
	}

	s.startSession(w, r)
}

// startSession creates a session, sets its cookie and returns the CSRF token.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request) {
	token, err := auth.NewToken()
	if err != nil {
		s.internalError(w, "new session token", err)
		return
	}
	now := s.clock.Now()
	if err := s.db.DeleteExpiredSessions(r.Context(), now); err != nil {
		s.internalError(w, "purge sessions", err)
		return
	}
	if err := s.db.CreateSession(r.Context(), auth.Digest(token), now, now.Add(auth.SessionTTL)); err != nil {
		s.internalError(w, "create session", err)
		return
	}
	s.auth.SetCookie(w, token)
	apierr.JSON(w, http.StatusOK, map[string]any{"csrf_token": auth.CSRFToken(token)})
}

// setup creates the administrator password on first run and signs the caller
// in. It works only while no administrator exists; afterwards it answers 409.
func (s *Server) setup(w http.ResponseWriter, r *http.Request) {
	if !auth.OriginAllowed(r) {
		apierr.Write(w, http.StatusForbidden, "forbidden_origin", "Cross-origin request rejected.", nil)
		return
	}
	peer := peerAddr(r)
	if ok, wait := s.limiter.Allow(peer); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		apierr.Write(w, http.StatusTooManyRequests, "rate_limited", "Too many attempts. Try again later.", nil)
		return
	}
	var req loginRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	alreadySet := func() {
		apierr.Write(w, http.StatusConflict, "already_initialized", "The administrator password is already set.", nil)
	}
	if _, err := s.db.AdminPasswordHash(r.Context()); err == nil {
		alreadySet()
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		s.internalError(w, "read admin", err)
		return
	}
	if err := auth.ValidatePassword(req.Password); err != nil {
		apierr.Write(w, http.StatusUnprocessableEntity, "validation_failed", "Password is not acceptable.",
			map[string]string{"password": err.Error()})
		return
	}
	hash, err := auth.Hash(req.Password)
	if err != nil {
		s.internalError(w, "hash password", err)
		return
	}
	created, err := s.db.CreateAdminIfAbsent(r.Context(), hash, s.clock.Now())
	if err != nil {
		s.internalError(w, "create admin", err)
		return
	}
	if !created {
		alreadySet()
		return
	}
	s.logger.Info("administrator password created", "peer", peer)
	s.startSession(w, r)
}

func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.auth.Identify(r)
	if !ok {
		_, err := s.db.AdminPasswordHash(r.Context())
		apierr.JSON(w, http.StatusOK, map[string]any{
			"authenticated": false,
			"needs_setup":   errors.Is(err, store.ErrNotFound),
		})
		return
	}
	apierr.JSON(w, http.StatusOK, map[string]any{
		"authenticated": true,
		"csrf_token":    auth.CSRFToken(sess.Token),
	})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	sess, _ := auth.SessionFrom(r.Context())
	if err := s.db.DeleteSession(r.Context(), sess.Digest); err != nil {
		s.internalError(w, "delete session", err)
		return
	}
	s.auth.ClearCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

type passwordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	var req passwordRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	hash, err := s.db.AdminPasswordHash(r.Context())
	if err != nil {
		s.internalError(w, "read admin", err)
		return
	}
	if !auth.Verify(hash, req.CurrentPassword) {
		apierr.Write(w, http.StatusUnauthorized, "invalid_credentials", "Current password is incorrect.",
			map[string]string{"current_password": "Incorrect password."})
		return
	}
	if err := auth.ValidatePassword(req.NewPassword); err != nil {
		apierr.Write(w, http.StatusUnprocessableEntity, "validation_failed", "New password is not acceptable.",
			map[string]string{"new_password": err.Error()})
		return
	}
	newHash, err := auth.Hash(req.NewPassword)
	if err != nil {
		s.internalError(w, "hash password", err)
		return
	}
	if err := s.db.SetAdminPasswordHash(r.Context(), newHash, s.clock.Now()); err != nil {
		s.internalError(w, "store password", err)
		return
	}
	s.auth.ClearCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

// internalError logs the operation and error class, never request content,
// and answers with a generic message.
func (s *Server) internalError(w http.ResponseWriter, op string, err error) {
	s.logger.Error("request failed", "op", op, "error", err)
	apierr.Write(w, http.StatusInternalServerError, "internal_error", "Internal error.", nil)
}
