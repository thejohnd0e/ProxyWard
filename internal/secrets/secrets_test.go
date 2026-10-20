package secrets

import (
	"bytes"
	"errors"
	"os"
	"runtime"
	"sync"
	"testing"
)

func TestCipherRejectsTampering(t *testing.T) {
	c, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := c.Seal([]byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	sealed[len(sealed)-1] ^= 1
	if _, err := c.Open(sealed); err == nil {
		t.Fatal("tampered ciphertext accepted")
	}
}

func TestSealOpenRoundTripAndFreshNonce(t *testing.T) {
	c, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a, _ := c.Seal([]byte("same"))
	b, _ := c.Seal([]byte("same"))
	if bytes.Equal(a, b) {
		t.Fatal("two seals of the same plaintext are identical")
	}
	if bytes.Contains(a, []byte("same")) {
		t.Fatal("ciphertext contains plaintext")
	}
	got, err := c.Open(a)
	if err != nil || string(got) != "same" {
		t.Fatalf("round trip: %q %v", got, err)
	}
	empty, err := c.Seal(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := c.Open(empty); err != nil || len(got) != 0 {
		t.Fatalf("empty round trip: %q %v", got, err)
	}
}

func TestOpenRejectsShortAndForeignKeyData(t *testing.T) {
	c1, _ := Load(t.TempDir())
	c2, _ := Load(t.TempDir())
	if _, err := c1.Open([]byte("short")); !errors.Is(err, ErrOpen) {
		t.Fatalf("short input err = %v", err)
	}
	sealed, _ := c1.Seal([]byte("x"))
	if _, err := c2.Open(sealed); !errors.Is(err, ErrOpen) {
		t.Fatalf("foreign key err = %v", err)
	}
}

func TestKeyIsCreatedOnceAndReused(t *testing.T) {
	dir := t.TempDir()
	c1, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	sealed, _ := c1.Seal([]byte("persist"))
	first, err := os.ReadFile(KeyPath(dir))
	if err != nil || len(first) != 32 {
		t.Fatalf("key file: len=%d err=%v", len(first), err)
	}
	c2, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := c2.Open(sealed); err != nil || string(got) != "persist" {
		t.Fatalf("reloaded key cannot open: %q %v", got, err)
	}
	second, _ := os.ReadFile(KeyPath(dir))
	if !bytes.Equal(first, second) {
		t.Fatal("key changed on reload")
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(KeyPath(dir))
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("key mode = %v, want 0600", info.Mode().Perm())
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temporary files left behind: %v", entries)
	}
}

func TestConcurrentLoadYieldsOneKey(t *testing.T) {
	dir := t.TempDir()
	const n = 16
	sealed := make([][]byte, n)
	ciphers := make([]*Cipher, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := Load(dir)
			if err != nil {
				t.Error(err)
				return
			}
			ciphers[i] = c
			sealed[i], _ = c.Seal([]byte("v"))
		}()
	}
	wg.Wait()
	for i := 0; i < n; i++ {
		if ciphers[i] == nil {
			t.Fatal("a loader failed")
		}
		if _, err := ciphers[0].Open(sealed[i]); err != nil {
			t.Fatalf("loader %d used a different key: %v", i, err)
		}
	}
}

func TestLoadRejectsInvalidKeyFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(KeyPath(dir), []byte("too short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); !errors.Is(err, ErrBadKey) {
		t.Fatalf("err = %v, want ErrBadKey", err)
	}
	if b, _ := os.ReadFile(KeyPath(dir)); string(b) != "too short" {
		t.Fatal("invalid key file was overwritten")
	}
}

func TestExists(t *testing.T) {
	dir := t.TempDir()
	if ok, err := Exists(dir); err != nil || ok {
		t.Fatalf("before: %v %v", ok, err)
	}
	if _, err := Load(dir); err != nil {
		t.Fatal(err)
	}
	if ok, err := Exists(dir); err != nil || !ok {
		t.Fatalf("after: %v %v", ok, err)
	}
}

func TestFingerprint(t *testing.T) {
	dir := t.TempDir()
	c, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	fp := c.Fingerprint("alice", "pw")
	if len(fp) != 64 || fp != c.Fingerprint("alice", "pw") {
		t.Fatalf("fingerprint not stable or wrong size: %q", fp)
	}
	for name, other := range map[string]string{
		"other password": c.Fingerprint("alice", "pw2"),
		"other user":     c.Fingerprint("bob", "pw"),
		"split shift":    c.Fingerprint("alic", "epw"),
		"empty":          c.Fingerprint("", ""),
	} {
		if other == fp {
			t.Errorf("%s collides", name)
		}
	}
	if c.Fingerprint("ab", "c") == c.Fingerprint("a", "bc") {
		t.Error("username/password boundary is ambiguous")
	}
	again, _ := Load(dir)
	if again.Fingerprint("alice", "pw") != fp {
		t.Error("fingerprint changes across reloads")
	}
	foreign, _ := Load(t.TempDir())
	if foreign.Fingerprint("alice", "pw") == fp {
		t.Error("fingerprint is not key dependent")
	}
}
