// Package store persists application state in SQLite.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

var (
	// ErrNotFound is returned when a record does not exist.
	ErrNotFound = errors.New("not found")
	// ErrConflict is returned when a record would violate a uniqueness rule.
	ErrConflict = errors.New("conflict")
	// ErrInvalid is returned when a record fails a storage constraint.
	ErrInvalid = errors.New("invalid record")
	// ErrAuthUnsupported is returned until proxy credentials can be stored encrypted.
	ErrAuthUnsupported = errors.New("proxy authentication storage is not available yet")
)

// Store is the SQLite-backed persistent state. Short writes are serialized by
// a mutex; callers must never hold a transaction across network calls.
type Store struct {
	db      *sql.DB
	writeMu sync.Mutex
	sealer  atomic.Pointer[sealerBox]
}

// Open opens the database at path, creating it when missing. It does not
// migrate; call Migrate before use.
func Open(ctx context.Context, path string) (*Store, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve database path: %w", err)
	}
	db, err := sql.Open("sqlite", dsn(abs))
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("open database: %w", err)
	}
	return &Store{db: db}, nil
}

func dsn(path string) string {
	p := filepath.ToSlash(path)
	p = strings.NewReplacer("%", "%25", "?", "%3F", "#", "%23").Replace(p)
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "synchronous(NORMAL)")
	return "file:" + p + "?" + q.Encode()
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Ping reports whether the database is reachable.
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }
