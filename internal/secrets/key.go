// Package secrets manages the persistent encryption key and AEAD sealing.
package secrets

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// KeyFileName is the key's file name inside the data directory.
const KeyFileName = "secrets.key"

const keySize = 32

// ErrBadKey is returned when the key file exists but is not a valid key.
var ErrBadKey = errors.New("secrets key file is invalid")

// KeyPath returns the key file location for dataDir.
func KeyPath(dataDir string) string { return filepath.Join(dataDir, KeyFileName) }

// Exists reports whether the key file is present.
func Exists(dataDir string) (bool, error) {
	_, err := os.Stat(KeyPath(dataDir))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

// Load returns a Cipher using the persistent key, creating the key if it does
// not exist. Creation is atomic: the key is written to a temporary file and
// hard-linked into place, so concurrent callers all end up with the same key
// and an existing key is never overwritten. Callers must refuse to call Load
// when encrypted records exist but the key is missing.
func Load(dataDir string) (*Cipher, error) {
	path := KeyPath(dataDir)
	key, err := readKey(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := createKey(dataDir, path); err != nil {
			return nil, err
		}
		key, err = readKey(path)
	}
	if err != nil {
		return nil, err
	}
	return newCipher(key)
}

func readKey(path string) ([]byte, error) {
	key, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(key) != keySize {
		return nil, fmt.Errorf("%w: expected %d bytes", ErrBadKey, keySize)
	}
	return key, nil
}

func createKey(dataDir, path string) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	key := make([]byte, keySize)
	if _, err := rand.Read(key); err != nil {
		return fmt.Errorf("generate key: %w", err)
	}
	tmp, err := os.CreateTemp(dataDir, "secrets.key.tmp-*")
	if err != nil {
		return fmt.Errorf("create key file: %w", err)
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil && !errors.Is(err, os.ErrPermission) {
		tmp.Close()
		return fmt.Errorf("restrict key file: %w", err)
	}
	if _, err := tmp.Write(key); err != nil {
		tmp.Close()
		return fmt.Errorf("write key file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync key file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close key file: %w", err)
	}
	// Link fails if another process created the key first; that key wins.
	if err := os.Link(tmp.Name(), path); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("install key file: %w", err)
	}
	return nil
}
