package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"proxy-checker/internal/domain"
)

// MaxOutboxAttempts is the number of delivery attempts before a notification
// is given up on. Failed items stay visible; nothing is silently dropped.
const MaxOutboxAttempts = 5

// OutboxItem is a notification waiting for, or having had, delivery.
type OutboxItem struct {
	ID            int64
	EventID       *int64 // nil for test messages
	Payload       string
	Attempts      int
	NextAttemptAt time.Time
	DeliveredAt   *time.Time
	LastError     string // short code, never upstream text
	CreatedAt     time.Time

	EventKind    string // from the joined event; empty for test messages
	EventMessage string
	ProxyID      *int64
}

// Kind returns the notification kind recorded in the payload.
func (o OutboxItem) Kind() string {
	var p struct {
		Kind string `json:"kind"`
	}
	_ = json.Unmarshal([]byte(o.Payload), &p)
	return p.Kind
}

const outboxColumns = `o.id, o.event_id, o.payload, o.attempts, o.next_attempt_at, o.delivered_at, o.last_error, o.created_at,
	COALESCE(e.kind, ''), COALESCE(e.message, ''), e.proxy_id`

func scanOutbox(r rowScanner) (OutboxItem, error) {
	var (
		o             OutboxItem
		event, proxy  sql.NullInt64
		next, created int64
		delivered     sql.NullInt64
	)
	if err := r.Scan(&o.ID, &event, &o.Payload, &o.Attempts, &next, &delivered, &o.LastError, &created,
		&o.EventKind, &o.EventMessage, &proxy); err != nil {
		return OutboxItem{}, err
	}
	if event.Valid {
		v := event.Int64
		o.EventID = &v
	}
	if proxy.Valid {
		v := proxy.Int64
		o.ProxyID = &v
	}
	o.NextAttemptAt, o.CreatedAt = time.UnixMilli(next).UTC(), time.UnixMilli(created).UTC()
	if delivered.Valid {
		t := time.UnixMilli(delivered.Int64).UTC()
		o.DeliveredAt = &t
	}
	return o, nil
}

// DueOutbox returns undelivered items that are due at now and have attempts
// left, oldest first.
func (s *Store) DueOutbox(ctx context.Context, now time.Time, limit int) ([]OutboxItem, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+outboxColumns+` FROM notification_outbox o LEFT JOIN events e ON e.id = o.event_id
		 WHERE o.delivered_at IS NULL AND o.attempts < ? AND o.next_attempt_at <= ?
		 ORDER BY o.created_at, o.id LIMIT ?`, MaxOutboxAttempts, now.UTC().UnixMilli(), limit)
	if err != nil {
		return nil, fmt.Errorf("due outbox: %w", err)
	}
	defer rows.Close()
	var out []OutboxItem
	for rows.Next() {
		o, err := scanOutbox(rows)
		if err != nil {
			return nil, fmt.Errorf("due outbox: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// MarkOutboxDelivered records successful delivery of the items.
func (s *Store) MarkOutboxDelivered(ctx context.Context, ids []int64, now time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	q := `UPDATE notification_outbox SET delivered_at = ?, last_error = '' WHERE delivered_at IS NULL AND id IN (` +
		strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + `)`
	args := []any{now.UTC().UnixMilli()}
	for _, id := range ids {
		args = append(args, id)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.ExecContext(ctx, q, args...); err != nil {
		return fmt.Errorf("mark delivered: %w", err)
	}
	return nil
}

// MarkOutboxFailed counts a failed attempt, stores its short error code and
// schedules the next attempt. It returns the attempts used so far. Items
// already delivered report ErrNotFound.
func (s *Store) MarkOutboxFailed(ctx context.Context, id int64, now time.Time, code string, next time.Time) (int, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("mark failed: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx,
		`UPDATE notification_outbox SET attempts = attempts + 1, last_error = ?, next_attempt_at = ?
		 WHERE id = ? AND delivered_at IS NULL`, code, next.UTC().UnixMilli(), id)
	if err != nil {
		return 0, fmt.Errorf("mark failed: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, ErrNotFound
	}
	var attempts int
	if err := tx.QueryRowContext(ctx, `SELECT attempts FROM notification_outbox WHERE id = ?`, id).Scan(&attempts); err != nil {
		return 0, fmt.Errorf("mark failed: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("mark failed: %w", err)
	}
	return attempts, nil
}

// EnqueueTestMessage queues a test notification that is due immediately. It
// returns the outbox ID; delivery happens later and is reported separately.
func (s *Store) EnqueueTestMessage(ctx context.Context, now time.Time) (int64, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO notification_outbox (event_id, payload, next_attempt_at, created_at) VALUES (NULL, '{"kind":"test"}', ?, ?)`,
		now.UTC().UnixMilli(), now.UTC().UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("enqueue test message: %w", err)
	}
	return res.LastInsertId()
}

// OutboxSummary is the delivery state shown in the settings.
type OutboxSummary struct {
	Pending         int // waiting for delivery or a retry
	Failed          int // used all attempts without delivery
	LastDeliveredAt *time.Time
	LastError       string // code of the latest failure still unresolved
}

// OutboxSummary returns counts and the latest delivery information.
func (s *Store) OutboxSummary(ctx context.Context) (OutboxSummary, error) {
	var sum OutboxSummary
	if err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(delivered_at IS NULL AND attempts < ?), 0), COALESCE(SUM(delivered_at IS NULL AND attempts >= ?), 0)
		 FROM notification_outbox`, MaxOutboxAttempts, MaxOutboxAttempts).Scan(&sum.Pending, &sum.Failed); err != nil {
		return sum, fmt.Errorf("outbox summary: %w", err)
	}
	var last sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT MAX(delivered_at) FROM notification_outbox`).Scan(&last); err != nil {
		return sum, fmt.Errorf("outbox summary: %w", err)
	}
	if last.Valid {
		t := time.UnixMilli(last.Int64).UTC()
		sum.LastDeliveredAt = &t
	}
	err := s.db.QueryRowContext(ctx,
		`SELECT last_error FROM notification_outbox WHERE delivered_at IS NULL AND last_error <> '' ORDER BY id DESC LIMIT 1`).Scan(&sum.LastError)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return sum, fmt.Errorf("outbox summary: %w", err)
	}
	return sum, nil
}

// TelegramConfig is the bot token and destination chat.
type TelegramConfig struct {
	Token  string
	ChatID string
}

// Configured reports whether messages can be sent.
func (c TelegramConfig) Configured() bool { return c.Token != "" && c.ChatID != "" }

// TelegramUpdate changes Telegram settings. A nil field is kept; an empty
// string clears the value.
type TelegramUpdate struct {
	Token  *string
	ChatID *string
}

const (
	telegramTokenKey = "telegram.bot_token"
	telegramChatKey  = "telegram.chat_id"
)

// SetTelegram stores Telegram settings. The token is encrypted.
func (s *Store) SetTelegram(ctx context.Context, u TelegramUpdate, now time.Time) error {
	if u.Token != nil {
		tok := strings.TrimSpace(*u.Token)
		if tok == "" {
			if err := s.DeleteSetting(ctx, telegramTokenKey); err != nil {
				return err
			}
		} else {
			sealer := s.secretsSealer()
			if sealer == nil {
				return ErrSecretsUnavailable
			}
			sealed, err := sealer.Seal([]byte(tok))
			if err != nil {
				return fmt.Errorf("encrypt token: %w", err)
			}
			if err := s.SetSetting(ctx, telegramTokenKey, Setting{Value: base64.StdEncoding.EncodeToString(sealed), Secret: true}, now); err != nil {
				return err
			}
		}
	}
	if u.ChatID != nil {
		chat := strings.TrimSpace(*u.ChatID)
		if chat == "" {
			return s.DeleteSetting(ctx, telegramChatKey)
		}
		return s.SetSetting(ctx, telegramChatKey, Setting{Value: chat}, now)
	}
	return nil
}

// GetTelegram returns the decrypted Telegram settings.
func (s *Store) GetTelegram(ctx context.Context) (TelegramConfig, error) {
	var cfg TelegramConfig
	if v, err := s.GetSetting(ctx, telegramChatKey); err == nil {
		cfg.ChatID = v.Value
	} else if !errors.Is(err, ErrNotFound) {
		return cfg, err
	}
	v, err := s.GetSetting(ctx, telegramTokenKey)
	if errors.Is(err, ErrNotFound) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	sealer := s.secretsSealer()
	if sealer == nil {
		return cfg, ErrSecretsUnavailable
	}
	sealed, err := base64.StdEncoding.DecodeString(v.Value)
	if err != nil {
		return cfg, errors.New("stored telegram token is corrupt")
	}
	plain, err := sealer.Open(sealed)
	if err != nil {
		return cfg, fmt.Errorf("decrypt telegram token: %w", err)
	}
	cfg.Token = string(plain)
	return cfg, nil
}

// Delivery states of an event's notification.
const (
	DeliveryNone      = "none"
	DeliveryPending   = "pending"
	DeliveryDelivered = "delivered"
	DeliveryFailed    = "failed"
)

// Delivery is the notification state of one event.
type Delivery struct {
	Status      string
	Attempts    int
	LastError   string
	DeliveredAt *time.Time
}

// EventRow is an event with its delivery state.
type EventRow struct {
	ID        int64
	ProxyID   *int64
	ProxyName string // current name; empty if the proxy was deleted
	Kind      string
	Message   string
	CreatedAt time.Time
	Delivery  Delivery
}

// EventCursor continues a listing after a given (created_at, id).
type EventCursor struct {
	CreatedAt time.Time
	ID        int64
}

// EventFilter selects events. Limit is capped by the caller.
type EventFilter struct {
	ProxyID *domain.ProxyID
	Kinds   []string
	Before  *EventCursor
	Limit   int
}

// ListEvents returns events newest first, plus a cursor when more remain.
func (s *Store) ListEvents(ctx context.Context, f EventFilter) ([]EventRow, *EventCursor, error) {
	if f.Limit <= 0 {
		return nil, nil, nil
	}
	q := `SELECT e.id, e.proxy_id, COALESCE(p.name, ''), e.kind, e.message, e.created_at,
	        o.id, o.attempts, o.last_error, o.delivered_at
	      FROM events e LEFT JOIN proxies p ON p.id = e.proxy_id LEFT JOIN notification_outbox o ON o.event_id = e.id WHERE 1 = 1`
	var args []any
	if f.ProxyID != nil {
		q += ` AND e.proxy_id = ?`
		args = append(args, int64(*f.ProxyID))
	}
	if len(f.Kinds) > 0 {
		q += ` AND e.kind IN (` + strings.TrimSuffix(strings.Repeat("?,", len(f.Kinds)), ",") + `)`
		for _, k := range f.Kinds {
			args = append(args, k)
		}
	}
	if f.Before != nil {
		q += ` AND (e.created_at < ? OR (e.created_at = ? AND e.id < ?))`
		ms := f.Before.CreatedAt.UTC().UnixMilli()
		args = append(args, ms, ms, f.Before.ID)
	}
	q += ` ORDER BY e.created_at DESC, e.id DESC LIMIT ?`
	args = append(args, f.Limit+1)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("list events: %w", err)
	}
	defer rows.Close()
	var out []EventRow
	for rows.Next() {
		var (
			e         EventRow
			proxy     sql.NullInt64
			created   int64
			outbox    sql.NullInt64
			attempts  sql.NullInt64
			lastErr   sql.NullString
			delivered sql.NullInt64
		)
		if err := rows.Scan(&e.ID, &proxy, &e.ProxyName, &e.Kind, &e.Message, &created, &outbox, &attempts, &lastErr, &delivered); err != nil {
			return nil, nil, fmt.Errorf("list events: %w", err)
		}
		if proxy.Valid {
			v := proxy.Int64
			e.ProxyID = &v
		}
		e.CreatedAt = time.UnixMilli(created).UTC()
		switch {
		case !outbox.Valid:
			e.Delivery = Delivery{Status: DeliveryNone}
		case delivered.Valid:
			t := time.UnixMilli(delivered.Int64).UTC()
			e.Delivery = Delivery{Status: DeliveryDelivered, Attempts: int(attempts.Int64), DeliveredAt: &t}
		case int(attempts.Int64) >= MaxOutboxAttempts:
			e.Delivery = Delivery{Status: DeliveryFailed, Attempts: int(attempts.Int64), LastError: lastErr.String}
		default:
			e.Delivery = Delivery{Status: DeliveryPending, Attempts: int(attempts.Int64), LastError: lastErr.String}
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	var next *EventCursor
	if len(out) > f.Limit {
		out = out[:f.Limit]
		last := out[len(out)-1]
		next = &EventCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	return out, next, nil
}
