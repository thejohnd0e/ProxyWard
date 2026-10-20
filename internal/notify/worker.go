package notify

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/thejohnd0e/ProxyWard/internal/domain"
	"github.com/thejohnd0e/ProxyWard/internal/store"
)

// Sender delivers one message to a chat (implemented by *Telegram).
type Sender interface {
	Send(ctx context.Context, chatID, text string) error
}

// retryDelays are the waits before attempts 2 to 5.
var retryDelays = []time.Duration{30 * time.Second, 60 * time.Second, 120 * time.Second, 240 * time.Second}

const (
	defaultBatch    = 50
	defaultInterval = 5 * time.Second
	maxRounds       = 20
	// partBudget leaves room below Telegram's limit for the header line.
	partBudget = 4000
	maxLine    = 400
)

// StoreToken reads the bot token from the encrypted settings.
func StoreToken(db *store.Store) TokenSource {
	return func(ctx context.Context) (string, bool, error) {
		cfg, err := db.GetTelegram(ctx)
		if err != nil {
			return "", false, err
		}
		return cfg.Token, cfg.Token != "", nil
	}
}

// Worker delivers queued notifications. It runs on its own: a slow or broken
// Telegram can delay messages but never blocks checks or changes proxy state.
// Delivery is at-least-once: a message whose confirmation was lost is sent
// again, so a duplicate is possible.
type Worker struct {
	Store    *store.Store
	Telegram Sender
	Clock    domain.Clock
	Logger   *slog.Logger
	Batch    int           // items per run; default 50
	Interval time.Duration // delay between runs; default 5 s
}

func (w *Worker) logger() *slog.Logger {
	if w.Logger != nil {
		return w.Logger
	}
	return slog.Default()
}

// Run delivers due notifications until ctx is done.
func (w *Worker) Run(ctx context.Context) error {
	interval := w.Interval
	if interval <= 0 {
		interval = defaultInterval
	}
	for ctx.Err() == nil {
		if err := w.DeliverDue(ctx, w.Clock.Now()); err != nil && ctx.Err() == nil {
			w.logger().Error("notification run failed", "error", err)
		}
		select {
		case <-w.Clock.After(interval):
		case <-ctx.Done():
		}
	}
	return nil
}

type part struct {
	text string
	ids  []int64
}

// DeliverDue sends the notifications due at now, grouped into as few
// messages as fit Telegram's limit. It stops at the first failing message:
// that message's items are rescheduled, later ones are untouched. Without a
// configured token and chat nothing is sent and no attempt is consumed.
func (w *Worker) DeliverDue(ctx context.Context, now time.Time) error {
	cfg, err := w.Store.GetTelegram(ctx)
	if err != nil {
		return err
	}
	if !cfg.Configured() {
		return nil
	}
	batch := w.Batch
	if batch <= 0 {
		batch = defaultBatch
	}
	// A backlog is worked off in rounds; a failure ends the run, and the failed
	// items are no longer due, so this cannot spin.
	for round := 0; round < maxRounds; round++ {
		items, err := w.Store.DueOutbox(ctx, now, batch)
		if err != nil {
			return err
		}
		if len(items) == 0 {
			return nil
		}
		failed, err := w.deliverItems(ctx, cfg.ChatID, items, now)
		if err != nil || failed {
			return err
		}
		if len(items) < batch {
			return nil
		}
	}
	return nil
}

// deliverItems sends the items as messages and reports whether one failed.
func (w *Worker) deliverItems(ctx context.Context, chatID string, items []store.OutboxItem, now time.Time) (failed bool, err error) {
	for _, p := range buildParts(items) {
		err := w.Telegram.Send(ctx, chatID, p.text)
		if err == nil {
			if err := w.Store.MarkOutboxDelivered(ctx, p.ids, now); err != nil {
				return false, err
			}
			continue
		}
		var se *SendError
		if !errors.As(err, &se) {
			return false, err // not a delivery problem (for example, a settings read error)
		}
		if se.Code == "canceled" && ctx.Err() != nil {
			return false, ctx.Err() // shutting down: not a failed attempt
		}
		w.reschedule(ctx, items, p, now, se.Code, se.RetryAfter)
		return true, nil
	}
	return false, nil
}

func (w *Worker) reschedule(ctx context.Context, items []store.OutboxItem, p part, now time.Time, code string, retryAfter time.Duration) {
	byID := map[int64]store.OutboxItem{}
	for _, it := range items {
		byID[it.ID] = it
	}
	var maxAttempts int
	for _, id := range p.ids {
		next := nextAttempt(now, byID[id].Attempts+1, retryAfter)
		n, err := w.Store.MarkOutboxFailed(ctx, id, now, code, next)
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				w.logger().Error("recording a failed delivery failed", "error", err)
			}
			continue
		}
		if n > maxAttempts {
			maxAttempts = n
		}
	}
	// Only the short code is logged; the request URL and token never are.
	w.logger().Warn("notification delivery failed", "code", code, "attempt", maxAttempts,
		"items", len(p.ids), "final", maxAttempts >= store.MaxOutboxAttempts)
}

// nextAttempt returns when the attempt after the one numbered attempts-1
// should happen. Telegram's retry_after replaces the fixed delay.
func nextAttempt(now time.Time, attempts int, retryAfter time.Duration) time.Time {
	if retryAfter > 0 {
		return now.Add(retryAfter)
	}
	i := attempts - 1
	if i < 0 {
		i = 0
	}
	if i >= len(retryDelays) {
		i = len(retryDelays) - 1
	}
	return now.Add(retryDelays[i])
}

// buildParts turns items into messages of at most partBudget characters.
// A message with several items starts with a header; each item is exactly one
// line, so names cannot add lines.
func buildParts(items []store.OutboxItem) []part {
	var parts []part
	var lines []string
	var ids []int64
	size := 0
	flush := func() {
		if len(lines) == 0 {
			return
		}
		text := lines[0]
		if len(lines) > 1 {
			text = "Proxy Checker: " + itoa(len(lines)) + " updates"
			for _, l := range lines {
				text += "\n- " + l
			}
		}
		parts = append(parts, part{text: text, ids: ids})
		lines, ids, size = nil, nil, 0
	}
	for _, it := range items {
		line := lineFor(it)
		cost := utf8.RuneCountInString(line) + 3
		if size+cost > partBudget {
			flush()
		}
		lines = append(lines, line)
		ids = append(ids, it.ID)
		size += cost
	}
	flush()
	return parts
}

// lineFor renders one notification as a single sanitized line.
func lineFor(it store.OutboxItem) string {
	switch {
	case it.Kind() == "test":
		return "Test notification from Proxy Checker: delivery works."
	case it.EventMessage != "":
		return clean(it.EventMessage)
	}
	return "A proxy state change is no longer available."
}

// clean makes text safe to show as one plain-text line: control characters
// become spaces, invisible formatting characters (such as bidi overrides) are
// removed, and the length is bounded.
func clean(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsControl(r):
			return ' '
		case unicode.Is(unicode.Cf, r):
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > maxLine {
		s = string(r[:maxLine-3]) + "..."
	}
	return s
}
