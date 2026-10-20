package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/thejohnd0e/ProxyWard/internal/domain"
)

// AddAvailability stores one availability result. ErrNotFound means the
// proxy does not exist (it may have been deleted while the check ran).
func (s *Store) AddAvailability(ctx context.Context, a domain.Availability) error {
	switch a.State {
	case domain.StateUnknown, domain.StateUp, domain.StateDown, domain.StateIndeterminate:
	default:
		return fmt.Errorf("%w: unknown state", ErrInvalid)
	}
	var code, msg string
	if a.Failure != nil {
		code, msg = a.Failure.Code, a.Failure.Message
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO availability_results (proxy_id, checked_at, state, exit_ip, endpoint, duration_us, failure_code, failure_message)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		int64(a.ProxyID), a.CheckedAt.UTC().UnixMilli(), string(a.State), a.ExitIP, a.Endpoint,
		a.Duration.Microseconds(), code, msg)
	if err != nil {
		if strings.Contains(err.Error(), "FOREIGN KEY") {
			return ErrNotFound
		}
		return fmt.Errorf("add availability: %w", err)
	}
	return nil
}

const availabilityColumns = `proxy_id, checked_at, state, exit_ip, endpoint, duration_us, failure_code, failure_message`

func scanAvailability(r rowScanner) (domain.Availability, error) {
	var (
		a          domain.Availability
		at, durUS  int64
		state      string
		code, msg  string
		proxyID    int64
		exitIP, ep string
	)
	if err := r.Scan(&proxyID, &at, &state, &exitIP, &ep, &durUS, &code, &msg); err != nil {
		return domain.Availability{}, err
	}
	a.ProxyID = domain.ProxyID(proxyID)
	a.CheckedAt = time.UnixMilli(at).UTC()
	a.State = domain.State(state)
	a.ExitIP, a.Endpoint = exitIP, ep
	a.Duration = time.Duration(durUS) * time.Microsecond
	if code != "" || msg != "" {
		a.Failure = &domain.Failure{Code: code, Message: msg}
	}
	return a, nil
}

// LatestAvailability returns the newest result for a proxy, or ErrNotFound.
func (s *Store) LatestAvailability(ctx context.Context, id domain.ProxyID) (domain.Availability, error) {
	a, err := scanAvailability(s.db.QueryRowContext(ctx,
		`SELECT `+availabilityColumns+` FROM availability_results WHERE proxy_id = ?
		 ORDER BY checked_at DESC, id DESC LIMIT 1`, int64(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Availability{}, ErrNotFound
	}
	if err != nil {
		return domain.Availability{}, fmt.Errorf("latest availability: %w", err)
	}
	return a, nil
}

// ListAvailability returns up to limit results, newest first. Zero from/to
// leave that side of the time range open; both bounds are inclusive.
func (s *Store) ListAvailability(ctx context.Context, id domain.ProxyID, from, to time.Time, limit int) ([]domain.Availability, error) {
	if limit <= 0 {
		return nil, nil
	}
	q := `SELECT ` + availabilityColumns + ` FROM availability_results WHERE proxy_id = ?`
	args := []any{int64(id)}
	if !from.IsZero() {
		q += ` AND checked_at >= ?`
		args = append(args, from.UTC().UnixMilli())
	}
	if !to.IsZero() {
		q += ` AND checked_at <= ?`
		args = append(args, to.UTC().UnixMilli())
	}
	q += ` ORDER BY checked_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list availability: %w", err)
	}
	defer rows.Close()
	var out []domain.Availability
	for rows.Next() {
		a, err := scanAvailability(rows)
		if err != nil {
			return nil, fmt.Errorf("list availability: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
