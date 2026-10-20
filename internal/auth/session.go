package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"time"
)

// SessionTTL is the fixed lifetime of a session.
const SessionTTL = 24 * time.Hour

// CookieName is the session cookie's name.
const CookieName = "pc_session"

// NewToken returns a random opaque 256-bit session token.
func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate session token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Digest returns the hex SHA-256 of token. Only digests are stored.
func Digest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CSRFToken derives the session's CSRF token from the session token, so it
// needs no storage and is only known to the holder of the session.
func CSRFToken(sessionToken string) string {
	mac := hmac.New(sha256.New, []byte(sessionToken))
	mac.Write([]byte("proxy-checker csrf v1"))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// CSRFValid compares a supplied CSRF token in constant time.
func CSRFValid(sessionToken, supplied string) bool {
	return subtle.ConstantTimeCompare([]byte(CSRFToken(sessionToken)), []byte(supplied)) == 1
}
