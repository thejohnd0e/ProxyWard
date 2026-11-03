package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/thejohnd0e/ProxyWard/internal/domain"
)

// ExportMaxRecords caps one export across events, availability and speed
// results. A larger selection is refused instead of being truncated, so an
// agent never reads a silently incomplete file.
const ExportMaxRecords = 100000

// maxExportProxies matches the number of proxies the service can hold.
const maxExportProxies = domain.MaxProxies

// ErrExportTooLarge is returned when the selected range and proxies hold more
// than ExportMaxRecords records.
var ErrExportTooLarge = errors.New("export too large")

// ExportFilter selects the proxies and the inclusive time range of an export.
// Limit caps the total number of records; zero means ExportMaxRecords.
type ExportFilter struct {
	From, To time.Time
	ProxyIDs []domain.ProxyID
	Limit    int
}

// ExportData is the secret-free part of the database that an export carries.
// It never includes credentials, encrypted values, settings, session data or
// free-form event and failure messages.
type ExportData struct {
	Proxies      []ExportProxy        `json:"proxies"`
	Events       []ExportEvent        `json:"events"`
	Availability []ExportAvailability `json:"availability"`
	Episodes     []ExportEpisode      `json:"episodes"`
	Speed        []ExportSpeed        `json:"speed"`
}

// ExportProxy identifies a monitored proxy.
type ExportProxy struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
}

// ExportEvent is one recorded incident event.
type ExportEvent struct {
	ID      int64     `json:"id"`
	ProxyID int64     `json:"proxy_id"`
	Kind    string    `json:"kind"`
	At      time.Time `json:"at"`
}

// ExportAvailability is one availability check result.
type ExportAvailability struct {
	ID          int64     `json:"id"`
	ProxyID     int64     `json:"proxy_id"`
	At          time.Time `json:"at"`
	State       string    `json:"state"`
	ExitIP      string    `json:"exit_ip"`
	Endpoint    string    `json:"endpoint"`
	DurationMS  int64     `json:"duration_ms"`
	FailureCode string    `json:"failure_code"`
}

// ExportEpisode is one unbroken run of failed availability checks for a proxy,
// derived from the exported results so an agent can answer "when and why was
// this proxy failing" even when no incident event was recorded.
type ExportEpisode struct {
	ProxyID      int64      `json:"proxy_id"`
	StartedAt    time.Time  `json:"started_at"`
	EndedAt      *time.Time `json:"ended_at"` // null when no successful check followed inside the period
	Checks       int        `json:"checks"`
	States       []string   `json:"states"`
	FailureCodes []string   `json:"failure_codes"`
}

// ExportSpeed is one speed check result.
type ExportSpeed struct {
	ID         int64          `json:"id"`
	ProxyID    int64          `json:"proxy_id"`
	At         time.Time      `json:"at"`
	Provider   string         `json:"provider"`
	Method     string         `json:"method"`
	Download   ExportTransfer `json:"download"`
	Upload     ExportTransfer `json:"upload"`
	TotalBytes int64          `json:"total_bytes"`
}

// ExportTransfer is one direction of a speed check. A rate that does not exist
// is null, never zero.
type ExportTransfer struct {
	Bytes         int64    `json:"bytes"`
	DurationMS    int64    `json:"duration_ms"`
	Mbps          *float64 `json:"mbps"`
	LowConfidence bool     `json:"low_confidence"`
	FailureCode   string   `json:"failure_code"`
}

// Export reads the selected events and check results in one read transaction,
// so all arrays describe the same database snapshot. Events of deleted proxies
// are not part of the result: they cannot be attributed to a selected proxy.
func (s *Store) Export(ctx context.Context, f ExportFilter) (ExportData, error) {
	if err := validateExportFilter(f); err != nil {
		return ExportData{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ExportData{}, fmt.Errorf("begin export: %w", err)
	}
	defer tx.Rollback()

	data := ExportData{
		Proxies:      []ExportProxy{},
		Events:       []ExportEvent{},
		Availability: []ExportAvailability{},
		Episodes:     []ExportEpisode{},
		Speed:        []ExportSpeed{},
	}
	limit := ExportMaxRecords
	if f.Limit > 0 && f.Limit < limit {
		limit = f.Limit
	}
	ids := exportIDs(f.ProxyIDs)
	in := placeholders(len(ids))
	from, to := f.From.UTC().UnixMilli(), f.To.UTC().UnixMilli()

	if err := exportProxies(ctx, tx, in, ids, &data); err != nil {
		return ExportData{}, err
	}
	if len(data.Proxies) != len(ids) {
		return ExportData{}, ErrNotFound
	}
	if err := exportEvents(ctx, tx, in, ids, from, to, limit, &data); err != nil {
		return ExportData{}, err
	}
	if err := exportAvailability(ctx, tx, in, ids, from, to, limit, &data); err != nil {
		return ExportData{}, err
	}
	data.Episodes = buildEpisodes(data.Availability)
	if err := exportSpeed(ctx, tx, in, ids, from, to, limit, &data); err != nil {
		return ExportData{}, err
	}
	if total := len(data.Events) + len(data.Availability) + len(data.Speed); total > limit {
		return ExportData{}, ErrExportTooLarge
	}
	return data, nil
}

func validateExportFilter(f ExportFilter) error {
	if f.From.IsZero() || f.To.IsZero() || !f.From.Before(f.To) {
		return fmt.Errorf("%w: the time range must start before it ends", ErrInvalid)
	}
	if len(f.ProxyIDs) == 0 || len(f.ProxyIDs) > maxExportProxies {
		return fmt.Errorf("%w: select between 1 and %d proxies", ErrInvalid, maxExportProxies)
	}
	seen := make(map[domain.ProxyID]bool, len(f.ProxyIDs))
	for _, id := range f.ProxyIDs {
		if id <= 0 || seen[id] {
			return fmt.Errorf("%w: proxy ids must be positive and unique", ErrInvalid)
		}
		seen[id] = true
	}
	return nil
}

// buildEpisodes derives runs of failed availability checks from the exported
// results. A run ends at the next successful check of the same proxy and stays
// open when no success follows inside the selected period.
func buildEpisodes(rows []ExportAvailability) []ExportEpisode {
	out := []ExportEpisode{}
	type open struct {
		index int
		last  time.Time
	}
	running := map[int64]open{}
	for _, a := range rows {
		if a.State == string(domain.StateUp) {
			if o, ok := running[a.ProxyID]; ok {
				end := o.last
				out[o.index].EndedAt = &end
				delete(running, a.ProxyID)
			}
			continue
		}
		o, ok := running[a.ProxyID]
		if !ok {
			out = append(out, ExportEpisode{ProxyID: a.ProxyID, StartedAt: a.At, States: []string{}, FailureCodes: []string{}})
			o = open{index: len(out) - 1}
		}
		out[o.index].Checks++
		out[o.index].States = addUnique(out[o.index].States, a.State)
		if a.FailureCode != "" {
			out[o.index].FailureCodes = addUnique(out[o.index].FailureCodes, a.FailureCode)
		}
		o.last = a.At
		running[a.ProxyID] = o
	}
	for i := range out {
		sort.Strings(out[i].States)
		sort.Strings(out[i].FailureCodes)
	}
	return out
}

func addUnique(list []string, v string) []string {
	for _, have := range list {
		if have == v {
			return list
		}
	}
	return append(list, v)
}

func exportIDs(ids []domain.ProxyID) []any {
	out := make([]any, len(ids))
	for i, id := range ids {
		out[i] = int64(id)
	}
	return out
}

func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

func exportProxies(ctx context.Context, tx *sql.Tx, in string, args []any, data *ExportData) error {
	rows, err := tx.QueryContext(ctx,
		`SELECT id, name, protocol, host, port FROM proxies WHERE id IN (`+in+`) ORDER BY id`, args...)
	if err != nil {
		return fmt.Errorf("export proxies: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var p ExportProxy
		if err := rows.Scan(&p.ID, &p.Name, &p.Protocol, &p.Host, &p.Port); err != nil {
			return fmt.Errorf("export proxies: %w", err)
		}
		data.Proxies = append(data.Proxies, p)
	}
	return rows.Err()
}

func exportEvents(ctx context.Context, tx *sql.Tx, in string, ids []any, from, to int64, limit int, data *ExportData) error {
	args := append(ids, from, to, limit+1)
	rows, err := tx.QueryContext(ctx,
		`SELECT id, proxy_id, kind, created_at FROM events
		  WHERE proxy_id IN (`+in+`) AND created_at >= ? AND created_at <= ?
		  ORDER BY created_at, id LIMIT ?`, args...)
	if err != nil {
		return fmt.Errorf("export events: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			e    ExportEvent
			at   int64
			kind string
		)
		if err := rows.Scan(&e.ID, &e.ProxyID, &kind, &at); err != nil {
			return fmt.Errorf("export events: %w", err)
		}
		e.Kind = kind
		e.At = time.UnixMilli(at).UTC()
		data.Events = append(data.Events, e)
	}
	return rows.Err()
}

func exportAvailability(ctx context.Context, tx *sql.Tx, in string, ids []any, from, to int64, limit int, data *ExportData) error {
	args := append(ids, from, to, limit+1)
	rows, err := tx.QueryContext(ctx,
		`SELECT id, proxy_id, checked_at, state, exit_ip, endpoint, duration_us, failure_code
		   FROM availability_results
		  WHERE proxy_id IN (`+in+`) AND checked_at >= ? AND checked_at <= ?
		  ORDER BY checked_at, id LIMIT ?`, args...)
	if err != nil {
		return fmt.Errorf("export availability: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			a     ExportAvailability
			at    int64
			durUS int64
		)
		if err := rows.Scan(&a.ID, &a.ProxyID, &at, &a.State, &a.ExitIP, &a.Endpoint, &durUS, &a.FailureCode); err != nil {
			return fmt.Errorf("export availability: %w", err)
		}
		a.At = time.UnixMilli(at).UTC()
		a.DurationMS = durUS / 1000
		data.Availability = append(data.Availability, a)
	}
	return rows.Err()
}

func exportSpeed(ctx context.Context, tx *sql.Tx, in string, ids []any, from, to int64, limit int, data *ExportData) error {
	args := append(ids, from, to, limit+1)
	rows, err := tx.QueryContext(ctx,
		`SELECT id, proxy_id, checked_at, provider, method,
		        download_bytes, download_us, download_mbps, download_low_conf, download_fail_code,
		        upload_bytes, upload_us, upload_mbps, upload_low_conf, upload_fail_code, total_bytes
		   FROM speed_results
		  WHERE proxy_id IN (`+in+`) AND checked_at >= ? AND checked_at <= ?
		  ORDER BY checked_at, id LIMIT ?`, args...)
	if err != nil {
		return fmt.Errorf("export speed: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			sp           ExportSpeed
			at           int64
			dMbps, uMbps sql.NullFloat64
			dUS, uUS     int64
			dLow, uLow   int64
		)
		if err := rows.Scan(&sp.ID, &sp.ProxyID, &at, &sp.Provider, &sp.Method,
			&sp.Download.Bytes, &dUS, &dMbps, &dLow, &sp.Download.FailureCode,
			&sp.Upload.Bytes, &uUS, &uMbps, &uLow, &sp.Upload.FailureCode, &sp.TotalBytes); err != nil {
			return fmt.Errorf("export speed: %w", err)
		}
		sp.At = time.UnixMilli(at).UTC()
		sp.Download.DurationMS, sp.Download.LowConfidence = dUS/1000, dLow == 1
		sp.Upload.DurationMS, sp.Upload.LowConfidence = uUS/1000, uLow == 1
		if dMbps.Valid {
			v := dMbps.Float64
			sp.Download.Mbps = &v
		}
		if uMbps.Valid {
			v := uMbps.Float64
			sp.Upload.Mbps = &v
		}
		data.Speed = append(data.Speed, sp)
	}
	return rows.Err()
}
