package auth

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/thejohnd0e/ProxyWard/internal/apierr"
	"github.com/thejohnd0e/ProxyWard/internal/domain"
	"github.com/thejohnd0e/ProxyWard/internal/store"
)

// CSRFHeader carries the CSRF token on mutating requests.
const CSRFHeader = "X-CSRF-Token"

// Authenticator validates sessions and protects mutations.
type Authenticator struct {
	Store  *store.Store
	Clock  domain.Clock
	Secure bool // mark cookies Secure (HTTPS deployments)
}

type ctxKey struct{}

// Session is the authenticated session attached to a request.
type Session struct {
	Token  string
	Digest string
}

// SessionFrom returns the session attached by Require.
func SessionFrom(ctx context.Context) (Session, bool) {
	s, ok := ctx.Value(ctxKey{}).(Session)
	return s, ok
}

// Identify returns the active session for the request cookie, if any.
func (a *Authenticator) Identify(r *http.Request) (Session, bool) {
	c, err := r.Cookie(CookieName)
	if err != nil || c.Value == "" || len(c.Value) > 128 {
		return Session{}, false
	}
	s := Session{Token: c.Value, Digest: Digest(c.Value)}
	if err := a.Store.SessionActive(r.Context(), s.Digest, a.Clock.Now()); err != nil {
		return Session{}, false
	}
	return s, true
}

// Require rejects requests without a valid session. Mutating requests also
// need a same-origin Origin header (when present) and a valid CSRF token.
// Until the administrator password is set, protected routes are unavailable.
func (a *Authenticator) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := a.Store.AdminPasswordHash(r.Context()); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				apierr.Write(w, http.StatusServiceUnavailable, "not_initialized",
					"The administrator password has not been set.", nil)
				return
			}
			apierr.Write(w, http.StatusInternalServerError, "internal_error", "Internal error.", nil)
			return
		}
		sess, ok := a.Identify(r)
		if !ok {
			apierr.Write(w, http.StatusUnauthorized, "unauthenticated", "Authentication is required.", nil)
			return
		}
		if isMutation(r.Method) {
			if !OriginAllowed(r) {
				apierr.Write(w, http.StatusForbidden, "forbidden_origin", "Cross-origin request rejected.", nil)
				return
			}
			if !CSRFValid(sess.Token, r.Header.Get(CSRFHeader)) {
				apierr.Write(w, http.StatusForbidden, "csrf_failed", "Missing or invalid CSRF token.", nil)
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, sess)))
	})
}

// SetCookie issues the session cookie.
func (a *Authenticator) SetCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		Expires:  a.Clock.Now().Add(SessionTTL),
		MaxAge:   int(SessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   a.Secure,
		SameSite: http.SameSiteStrictMode,
	})
}

// ClearCookie expires the session cookie.
func (a *Authenticator) ClearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   a.Secure,
		SameSite: http.SameSiteStrictMode,
	})
}

func isMutation(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}

// OriginAllowed accepts requests without an Origin header (non-browser
// clients, still bound by CSRF tokens) and requests whose Origin host equals
// the request Host.
func OriginAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}
