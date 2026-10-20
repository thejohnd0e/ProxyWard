package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// AdminPasswordHash returns the encoded administrator password hash, or
// ErrNotFound when the administrator has not been set up.
func (s *Store) AdminPasswordHash(ctx context.Context) (string, error) {
	var hash string
	err := s.db.QueryRowContext(ctx, `SELECT password_hash FROM admin WHERE id = 1`).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("read admin: %w", err)
	}
	return hash, nil
}

// SetAdminPasswordHash stores the administrator password hash and revokes all
// sessions in one transaction.
func (s *Store) SetAdminPasswordHash(ctx context.Context, hash string, now time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("set admin password: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO admin (id, password_hash, updated_at) VALUES (1, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET password_hash = excluded.password_hash, updated_at = excluded.updated_at`,
		hash, now.UTC().UnixMilli()); err != nil {
		return fmt.Errorf("set admin password: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions`); err != nil {
		return fmt.Errorf("revoke sessions: %w", err)
	}
	return tx.Commit()
}

// CreateSession records a session by the digest of its token.
func (s *Store) CreateSession(ctx context.Context, tokenDigest string, createdAt, expiresAt time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (token_digest, created_at, expires_at) VALUES (?, ?, ?)`,
		tokenDigest, createdAt.UTC().UnixMilli(), expiresAt.UTC().UnixMilli()); err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

// SessionActive returns ErrNotFound unless the session exists and has not expired at now.
func (s *Store) SessionActive(ctx context.Context, tokenDigest string, now time.Time) error {
	var expires int64
	err := s.db.QueryRowContext(ctx,
		`SELECT expires_at FROM sessions WHERE token_digest = ?`, tokenDigest).Scan(&expires)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("read session: %w", err)
	}
	if expires <= now.UTC().UnixMilli() {
		return ErrNotFound
	}
	return nil
}

// DeleteSession revokes one session. Missing sessions are not an error.
func (s *Store) DeleteSession(ctx context.Context, tokenDigest string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_digest = ?`, tokenDigest); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// DeleteExpiredSessions removes sessions that expired at or before now.
func (s *Store) DeleteExpiredSessions(ctx context.Context, now time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE expires_at <= ?`, now.UTC().UnixMilli()); err != nil {
		return fmt.Errorf("delete expired sessions: %w", err)
	}
	return nil
}

// DeleteAllSessions revokes every session.
func (s *Store) DeleteAllSessions(ctx context.Context) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions`); err != nil {
		return fmt.Errorf("delete sessions: %w", err)
	}
	return nil
}
