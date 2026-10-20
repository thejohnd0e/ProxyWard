package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Setting is one stored setting. For secret settings Value holds ciphertext
// (base64) produced by the caller; the store never sees plaintext secrets.
type Setting struct {
	Value  string
	Secret bool
}

// GetSetting returns a setting, or ErrNotFound.
func (s *Store) GetSetting(ctx context.Context, key string) (Setting, error) {
	var (
		v      Setting
		secret int
	)
	err := s.db.QueryRowContext(ctx, `SELECT value, secret FROM settings WHERE key = ?`, key).Scan(&v.Value, &secret)
	if errors.Is(err, sql.ErrNoRows) {
		return Setting{}, ErrNotFound
	}
	if err != nil {
		return Setting{}, fmt.Errorf("read setting: %w", err)
	}
	v.Secret = secret == 1
	return v, nil
}

// SetSetting creates or replaces a setting.
func (s *Store) SetSetting(ctx context.Context, key string, v Setting, now time.Time) error {
	secret := 0
	if v.Secret {
		secret = 1
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO settings (key, value, secret, updated_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, secret = excluded.secret, updated_at = excluded.updated_at`,
		key, v.Value, secret, now.UTC().UnixMilli()); err != nil {
		return fmt.Errorf("write setting: %w", err)
	}
	return nil
}

// DeleteSetting removes a setting. Missing settings are not an error.
func (s *Store) DeleteSetting(ctx context.Context, key string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, key); err != nil {
		return fmt.Errorf("delete setting: %w", err)
	}
	return nil
}

// HasEncryptedRecords reports whether any stored record needs the secrets key
// to be read: a secret setting or a proxy password.
func (s *Store) HasEncryptedRecords(ctx context.Context) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT (SELECT COUNT(*) FROM settings WHERE secret = 1)
		      + (SELECT COUNT(*) FROM proxies WHERE password_enc IS NOT NULL)`).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("check encrypted records: %w", err)
	}
	return n > 0, nil
}
