package auth

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/thejohnd0e/ProxyWard/internal/testkit"
)

const goodPassword = "correct horse battery"

func TestHashAndVerify(t *testing.T) {
	h, err := Hash(goodPassword)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=3,p=1$") {
		t.Fatalf("unexpected hash format: %s", h)
	}
	if strings.Contains(h, goodPassword) {
		t.Fatal("hash contains the password")
	}
	if !Verify(h, goodPassword) {
		t.Fatal("correct password rejected")
	}
	if Verify(h, goodPassword+"x") || Verify(h, "") {
		t.Fatal("wrong password accepted")
	}
	h2, _ := Hash(goodPassword)
	if h == h2 {
		t.Fatal("hashes are not salted")
	}
}

func TestPasswordRules(t *testing.T) {
	if _, err := Hash("short"); err != ErrPasswordTooShort {
		t.Fatalf("short: %v", err)
	}
	if _, err := Hash("12345678901"); err != ErrPasswordTooShort {
		t.Fatalf("11 chars: %v", err)
	}
	// 12 multi-byte characters count as 12, not by byte length.
	if err := ValidatePassword(strings.Repeat("é", 12)); err != nil {
		t.Fatalf("12 runes: %v", err)
	}
	if err := ValidatePassword(strings.Repeat("a", MaxPasswordBytes+1)); err != ErrPasswordTooLong {
		t.Fatalf("too long: %v", err)
	}
	if Verify("whatever", strings.Repeat("a", MaxPasswordBytes+1)) {
		t.Fatal("oversized password accepted")
	}
}

func TestVerifyRejectsMalformedAndHostileHashes(t *testing.T) {
	valid, _ := Hash(goodPassword)
	parts := strings.Split(valid, "$")
	salt, key := parts[4], parts[5]
	for name, enc := range map[string]string{
		"empty":         "",
		"garbage":       "not a hash",
		"wrong algo":    "$argon2i$v=19$m=65536,t=3,p=1$" + salt + "$" + key,
		"wrong version": "$argon2id$v=16$m=65536,t=3,p=1$" + salt + "$" + key,
		"huge memory":   "$argon2id$v=19$m=4194304,t=3,p=1$" + salt + "$" + key,
		"huge time":     "$argon2id$v=19$m=65536,t=4000000,p=1$" + salt + "$" + key,
		"zero threads":  "$argon2id$v=19$m=65536,t=3,p=0$" + salt + "$" + key,
		"many threads":  "$argon2id$v=19$m=65536,t=3,p=200$" + salt + "$" + key,
		"short salt":    "$argon2id$v=19$m=65536,t=3,p=1$AAAA$" + key,
		"short key":     "$argon2id$v=19$m=65536,t=3,p=1$" + salt + "$AAAA",
		"bad base64":    "$argon2id$v=19$m=65536,t=3,p=1$!!!$" + key,
		"missing field": "$argon2id$v=19$m=65536,t=3$" + salt + "$" + key,
		"overlong":      valid + strings.Repeat("A", 600),
	} {
		start := time.Now()
		if Verify(enc, goodPassword) {
			t.Errorf("%s: accepted", name)
		}
		if time.Since(start) > 2*time.Second {
			t.Errorf("%s: rejection took %v", name, time.Since(start))
		}
	}
}

func TestTokensAndCSRF(t *testing.T) {
	a, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewToken()
	if a == b || len(a) != 43 {
		t.Fatalf("tokens: %q %q", a, b)
	}
	if Digest(a) == Digest(b) || Digest(a) == a || len(Digest(a)) != 64 {
		t.Fatal("bad digest")
	}
	if !CSRFValid(a, CSRFToken(a)) {
		t.Fatal("own CSRF token rejected")
	}
	if CSRFValid(a, CSRFToken(b)) || CSRFValid(a, "") || CSRFValid(a, a) {
		t.Fatal("foreign CSRF token accepted")
	}
}

func TestLoginLimiter(t *testing.T) {
	clock := testkit.NewClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	l := NewLoginLimiter(clock)

	for i := 0; i < LoginPerPeerPerMinute; i++ {
		if ok, _ := l.Allow("10.0.0.1"); !ok {
			t.Fatalf("attempt %d denied", i+1)
		}
	}
	ok, wait := l.Allow("10.0.0.1")
	if ok || wait <= 0 || wait > time.Minute {
		t.Fatalf("sixth attempt: ok=%v wait=%v", ok, wait)
	}
	if ok, _ := l.Allow("10.0.0.2"); !ok {
		t.Fatal("other peer was throttled")
	}
	clock.Advance(61 * time.Second)
	if ok, _ := l.Allow("10.0.0.1"); !ok {
		t.Fatal("peer still throttled after the window")
	}
}

func TestLoginLimiterGlobalAndBounded(t *testing.T) {
	clock := testkit.NewClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	l := NewLoginLimiter(clock)
	allowed := 0
	for i := 0; i < 100; i++ {
		if ok, _ := l.Allow("peer-" + string(rune('A'+i%26)) + string(rune('a'+i/26))); ok {
			allowed++
		}
	}
	if allowed != LoginGlobalPerMinute {
		t.Fatalf("allowed %d, want %d", allowed, LoginGlobalPerMinute)
	}
	clock.Advance(2 * time.Minute)
	if ok, _ := l.Allow("fresh"); !ok {
		t.Fatal("global limit did not expire")
	}
	l.mu.Lock()
	n := len(l.peers)
	l.mu.Unlock()
	if n > maxTrackedPeers {
		t.Fatalf("tracked peers = %d", n)
	}
}

func TestOriginAllowed(t *testing.T) {
	for origin, want := range map[string]bool{
		"":                          true,
		"http://panel.lan:8080":     true,
		"https://PANEL.lan:8080":    true,
		"http://evil.example":       false,
		"http://panel.lan:9999":     false,
		"null":                      false,
		"ftp://panel.lan:8080":      false,
		"http://panel.lan:8080/../": true,
	} {
		r, _ := http.NewRequest(http.MethodPost, "http://panel.lan:8080/api/x", nil)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if got := OriginAllowed(r); got != want {
			t.Errorf("origin %q: got %v want %v", origin, got, want)
		}
	}
}
