package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/thejohnd0e/ProxyWard/internal/domain"
)

// Incident transitions.
const (
	TransitionDown      = "down"
	TransitionRecovered = "recovered"
)

// notificationDelay is how long a new outbox row waits so that changes
// happening close together can be sent as one message.
const notificationDelay = 10 * time.Second

// IncidentOutcome describes what recording a result changed.
type IncidentOutcome struct {
	Transition string // "", TransitionDown or TransitionRecovered
	EventID    int64  // the event written for a transition
	Streak     int    // consecutive definite failures after this result
}

// RecordAvailabilityOutcome applies one availability result to the proxy's
// incident state and, on a transition, writes the incident, the event and the
// notification outbox row in a single transaction.
//
//   - up closes an open incident (recovery) and resets the failure streak;
//   - down extends the streak and, once it reaches threshold, opens an
//     incident (once: further failures only extend it);
//   - indeterminate, unknown, canceled and interrupted results change nothing.
func (s *Store) RecordAvailabilityOutcome(ctx context.Context, a domain.Availability, threshold int) (IncidentOutcome, error) {
	if threshold < 1 || threshold > 10 {
		return IncidentOutcome{}, fmt.Errorf("%w: threshold must be between 1 and 10", ErrInvalid)
	}
	ignored := a.Failure != nil && (a.Failure.Code == "canceled" || a.Failure.Code == "interrupted")

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return IncidentOutcome{}, fmt.Errorf("record outcome: %w", err)
	}
	defer tx.Rollback()

	var name string
	if err := tx.QueryRowContext(ctx, `SELECT name FROM proxies WHERE id = ?`, int64(a.ProxyID)).Scan(&name); errors.Is(err, sql.ErrNoRows) {
		return IncidentOutcome{}, ErrNotFound
	} else if err != nil {
		return IncidentOutcome{}, fmt.Errorf("record outcome: %w", err)
	}
	var streak int
	if err := tx.QueryRowContext(ctx, `SELECT failure_streak FROM proxy_state WHERE proxy_id = ?`, int64(a.ProxyID)).Scan(&streak); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return IncidentOutcome{}, fmt.Errorf("record outcome: %w", err)
	}
	out := IncidentOutcome{Streak: streak}
	if ignored || (a.State != domain.StateUp && a.State != domain.StateDown) {
		return out, nil
	}

	var incidentID int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM incidents WHERE proxy_id = ? AND ended_at IS NULL`, int64(a.ProxyID)).Scan(&incidentID)
	open := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return IncidentOutcome{}, fmt.Errorf("record outcome: %w", err)
	}
	at := a.CheckedAt.UTC()

	switch a.State {
	case domain.StateUp:
		out.Streak = 0
		if open {
			if _, err := tx.ExecContext(ctx, `UPDATE incidents SET ended_at = ? WHERE id = ?`, at.UnixMilli(), incidentID); err != nil {
				return IncidentOutcome{}, fmt.Errorf("record outcome: %w", err)
			}
			msg := fmt.Sprintf("Proxy %q is up again.", name)
			if out.EventID, err = writeEvent(ctx, tx, a.ProxyID, "proxy_recovered", msg, at); err != nil {
				return IncidentOutcome{}, err
			}
			out.Transition = TransitionRecovered
		}
	case domain.StateDown:
		out.Streak = streak + 1
		switch {
		case open:
			if _, err := tx.ExecContext(ctx, `UPDATE incidents SET consecutive_failures = ? WHERE id = ?`, out.Streak, incidentID); err != nil {
				return IncidentOutcome{}, fmt.Errorf("record outcome: %w", err)
			}
		case out.Streak >= threshold:
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO incidents (proxy_id, started_at, consecutive_failures) VALUES (?, ?, ?)`,
				int64(a.ProxyID), at.UnixMilli(), out.Streak); err != nil {
				return IncidentOutcome{}, fmt.Errorf("record outcome: %w", err)
			}
			code := "unknown"
			if a.Failure != nil && a.Failure.Code != "" {
				code = a.Failure.Code
			}
			msg := fmt.Sprintf("Proxy %q is down after %d consecutive failures (last reason: %s).", name, out.Streak, code)
			if out.EventID, err = writeEvent(ctx, tx, a.ProxyID, "proxy_down", msg, at); err != nil {
				return IncidentOutcome{}, err
			}
			out.Transition = TransitionDown
		}
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO proxy_state (proxy_id, failure_streak, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(proxy_id) DO UPDATE SET failure_streak = excluded.failure_streak, updated_at = excluded.updated_at`,
		int64(a.ProxyID), out.Streak, at.UnixMilli()); err != nil {
		return IncidentOutcome{}, fmt.Errorf("record outcome: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return IncidentOutcome{}, fmt.Errorf("record outcome: %w", err)
	}
	return out, nil
}

// writeEvent inserts an event and its outbox row.
func writeEvent(ctx context.Context, tx *sql.Tx, id domain.ProxyID, kind, message string, at time.Time) (int64, error) {
	res, err := tx.ExecContext(ctx,
		`INSERT INTO events (proxy_id, kind, message, created_at) VALUES (?, ?, ?, ?)`, int64(id), kind, message, at.UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("write event: %w", err)
	}
	eventID, _ := res.LastInsertId()
	payload, err := json.Marshal(map[string]any{"kind": kind, "event_id": eventID, "proxy_id": int64(id)})
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO notification_outbox (event_id, payload, next_attempt_at, created_at) VALUES (?, ?, ?, ?)`,
		eventID, string(payload), at.Add(notificationDelay).UnixMilli(), at.UnixMilli()); err != nil {
		return 0, fmt.Errorf("write outbox: %w", err)
	}
	return eventID, nil
}
