package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
)

// ErrOpen is returned for any ciphertext that fails authentication or is malformed.
var ErrOpen = errors.New("cannot decrypt secret")

// Cipher seals and opens secrets with AES-256-GCM and computes keyed
// fingerprints for duplicate detection.
type Cipher struct {
	aead  cipher.AEAD
	fpKey []byte // derived from, but distinct from, the encryption key
}

// Fingerprint returns a keyed digest of proxy credentials. It lets the
// database enforce uniqueness without storing a password hash that could be
// brute-forced offline without the key. Without credentials callers use "".
func (c *Cipher) Fingerprint(username, password string) string {
	mac := hmac.New(sha256.New, c.fpKey)
	// Length-prefix the username so ("ab","c") differs from ("a","bc").
	mac.Write([]byte{byte(len(username) >> 24), byte(len(username) >> 16), byte(len(username) >> 8), byte(len(username))})
	mac.Write([]byte(username))
	mac.Write([]byte(password))
	return hex.EncodeToString(mac.Sum(nil))
}

func newCipher(key []byte) (*Cipher, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}
	kdf := hmac.New(sha256.New, key)
	kdf.Write([]byte("proxy-checker credential fingerprint v1"))
	return &Cipher{aead: aead, fpKey: kdf.Sum(nil)}, nil
}

// Seal encrypts plain with a fresh random nonce. The result is nonce||ciphertext.
func (c *Cipher) Seal(plain []byte) ([]byte, error) {
	nonce := make([]byte, c.aead.NonceSize(), c.aead.NonceSize()+len(plain)+c.aead.Overhead())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	return c.aead.Seal(nonce, nonce, plain, nil), nil
}

// Open authenticates and decrypts the output of Seal.
func (c *Cipher) Open(sealed []byte) ([]byte, error) {
	n := c.aead.NonceSize()
	if len(sealed) < n+c.aead.Overhead() {
		return nil, ErrOpen
	}
	plain, err := c.aead.Open(nil, sealed[:n], sealed[n:], nil)
	if err != nil {
		return nil, ErrOpen
	}
	return plain, nil
}
