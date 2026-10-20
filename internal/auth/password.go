// Package auth implements administrator passwords, sessions and request protection.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters for new hashes.
const (
	argonMemoryKiB = 64 * 1024
	argonTime      = 3
	argonThreads   = 1
	argonKeyLen    = 32
	argonSaltLen   = 16
)

// Bounds applied to parameters read from stored hashes so a malformed or
// hostile value cannot trigger unbounded memory or CPU use.
const (
	maxMemoryKiB = 256 * 1024
	maxTime      = 10
	maxThreads   = 8
	minSaltLen   = 8
	maxSaltLen   = 64
	minKeyLen    = 16
	maxKeyLen    = 64
)

const (
	// MinPasswordLength is the minimum number of characters in a new password.
	MinPasswordLength = 12
	// MaxPasswordBytes bounds password size to keep hashing cost predictable.
	MaxPasswordBytes = 1024
)

var (
	ErrPasswordTooShort = errors.New("password must be at least 12 characters")
	ErrPasswordTooLong  = errors.New("password must be at most 1024 bytes")
)

// ValidatePassword checks the rules for a new password.
func ValidatePassword(password string) error {
	if len(password) > MaxPasswordBytes {
		return ErrPasswordTooLong
	}
	if utf8.RuneCountInString(password) < MinPasswordLength {
		return ErrPasswordTooShort
	}
	return nil
}

// Hash returns a salted Argon2id hash of password in PHC string format.
func Hash(password string) (string, error) {
	if err := ValidatePassword(password); err != nil {
		return "", err
	}
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemoryKiB, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemoryKiB, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// Verify reports whether password matches encoded. Malformed or out-of-bounds
// hashes never match.
func Verify(encoded, password string) bool {
	if len(password) > MaxPasswordBytes {
		return false
	}
	p, ok := parseHash(encoded)
	if !ok {
		return false
	}
	got := argon2.IDKey([]byte(password), p.salt, p.time, p.memory, p.threads, uint32(len(p.key)))
	return subtle.ConstantTimeCompare(got, p.key) == 1
}

type hashParams struct {
	memory  uint32
	time    uint32
	threads uint8
	salt    []byte
	key     []byte
}

func parseHash(encoded string) (hashParams, bool) {
	if len(encoded) > 512 {
		return hashParams{}, false
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return hashParams{}, false
	}
	var p hashParams
	fields := strings.Split(parts[3], ",")
	if len(fields) != 3 {
		return hashParams{}, false
	}
	vals := map[string]uint64{}
	for _, f := range fields {
		k, v, ok := strings.Cut(f, "=")
		if !ok {
			return hashParams{}, false
		}
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			return hashParams{}, false
		}
		vals[k] = n
	}
	m, t, th := vals["m"], vals["t"], vals["p"]
	if m < 8 || m > maxMemoryKiB || t < 1 || t > maxTime || th < 1 || th > maxThreads {
		return hashParams{}, false
	}
	p.memory, p.time, p.threads = uint32(m), uint32(t), uint8(th)

	var err error
	if p.salt, err = base64.RawStdEncoding.DecodeString(parts[4]); err != nil ||
		len(p.salt) < minSaltLen || len(p.salt) > maxSaltLen {
		return hashParams{}, false
	}
	if p.key, err = base64.RawStdEncoding.DecodeString(parts[5]); err != nil ||
		len(p.key) < minKeyLen || len(p.key) > maxKeyLen {
		return hashParams{}, false
	}
	return p, true
}
