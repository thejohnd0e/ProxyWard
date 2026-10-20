package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"proxy-checker/internal/domain"
)

// MaxChartBuckets bounds the points of one chart.
const MaxChartBuckets = 500

// LatestAvailabilityAll returns the newest availability result of every proxy
// that has one.
func (s *Store) LatestAvailabilityAll(ctx context.Context) (map[domain.ProxyID]domain.Availability, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+availabilityColumns+` FROM availability_results
		 WHERE id IN (SELECT MAX(id) FROM availability_results GROUP BY proxy_id)`)
	if err != nil {
		return nil, fmt.Errorf("latest availability: %w", err)
	}
	defer rows.Close()
	out := map[domain.ProxyID]domain.Availability{}
	for rows.Next() {
		a, err := scanAvailability(rows)
		if err != nil {
			return nil, fmt.Errorf("latest availability: %w", err)
		}
		out[a.ProxyID] = a
	}
	return out, rows.Err()
}

// LatestSpeedAll returns the newest speed result of every proxy that has one.
func (s *Store) LatestSpeedAll(ctx context.Context) (map[domain.ProxyID]domain.Speed, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+speedColumns+` FROM speed_results WHERE id IN (SELECT MAX(id) FROM speed_results GROUP BY proxy_id)`)
	if err != nil {
		return nil, fmt.Errorf("latest speed: %w", err)
	}
	defer rows.Close()
	out := map[domain.ProxyID]domain.Speed{}
	for rows.Next() {
		sp, err := scanSpeed(rows)
		if err != nil {
			return nil, fmt.Errorf("latest speed: %w", err)
		}
		out[sp.ProxyID] = sp
	}
	return out, rows.Err()
}

// Uptime counts definite results (up or down) in a window. Indeterminate,
// canceled and interrupted results are not samples.
type Uptime struct{ Up, Down int }

// Samples is the number of definite results.
func (u Uptime) Samples() int { return u.Up + u.Down }

// Percent is the share of definite results that were up, or nil without samples.
func (u Uptime) Percent() *float64 {
	if u.Samples() == 0 {
		return nil
	}
	p := float64(u.Up) * 100 / float64(u.Samples())
	return &p
}

// UptimeSince returns Uptime per proxy for results at or after since.
func (s *Store) UptimeSince(ctx context.Context, since time.Time) (map[domain.ProxyID]Uptime, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT proxy_id, SUM(state = 'up'), SUM(state = 'down') FROM availability_results
		 WHERE checked_at >= ? AND state IN ('up','down') GROUP BY proxy_id`, since.UTC().UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("uptime: %w", err)
	}
	defer rows.Close()
	out := map[domain.ProxyID]Uptime{}
	for rows.Next() {
		var id int64
		var u Uptime
		if err := rows.Scan(&id, &u.Up, &u.Down); err != nil {
			return nil, fmt.Errorf("uptime: %w", err)
		}
		out[domain.ProxyID(id)] = u
	}
	return out, rows.Err()
}

// HistoryCursor continues a history listing after a given (checked_at, id).
type HistoryCursor struct {
	At time.Time
	ID int64
}

// AvailabilityRecord is a stored availability result with its row ID.
type AvailabilityRecord struct {
	ID           int64
	Availability domain.Availability
}

// SpeedRecord is a stored speed result with its row ID.
type SpeedRecord struct {
	ID    int64
	Speed domain.Speed
}

func historyWhere(id domain.ProxyID, from, to time.Time, before *HistoryCursor) (string, []any) {
	q := ` WHERE proxy_id = ?`
	args := []any{int64(id)}
	if !from.IsZero() {
		q += ` AND checked_at >= ?`
		args = append(args, from.UTC().UnixMilli())
	}
	if !to.IsZero() {
		q += ` AND checked_at <= ?`
		args = append(args, to.UTC().UnixMilli())
	}
	if before != nil {
		q += ` AND (checked_at < ? OR (checked_at = ? AND id < ?))`
		ms := before.At.UTC().UnixMilli()
		args = append(args, ms, ms, before.ID)
	}
	return q, args
}

// PageAvailability returns up to limit results, newest first, and a cursor
// when more remain.
func (s *Store) PageAvailability(ctx context.Context, id domain.ProxyID, from, to time.Time, before *HistoryCursor, limit int) ([]AvailabilityRecord, *HistoryCursor, error) {
	if limit <= 0 {
		return nil, nil, nil
	}
	where, args := historyWhere(id, from, to, before)
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, `+availabilityColumns+` FROM availability_results`+where+` ORDER BY checked_at DESC, id DESC LIMIT ?`,
		append(args, limit+1)...)
	if err != nil {
		return nil, nil, fmt.Errorf("page availability: %w", err)
	}
	defer rows.Close()
	var out []AvailabilityRecord
	for rows.Next() {
		var rid int64
		a, err := scanAvailability(idScanner{rows, &rid})
		if err != nil {
			return nil, nil, fmt.Errorf("page availability: %w", err)
		}
		out = append(out, AvailabilityRecord{ID: rid, Availability: a})
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	var next *HistoryCursor
	if len(out) > limit {
		out = out[:limit]
		last := out[len(out)-1]
		next = &HistoryCursor{At: last.Availability.CheckedAt, ID: last.ID}
	}
	return out, next, nil
}

// PageSpeed returns up to limit speed results, newest first, and a cursor
// when more remain.
func (s *Store) PageSpeed(ctx context.Context, id domain.ProxyID, from, to time.Time, before *HistoryCursor, limit int) ([]SpeedRecord, *HistoryCursor, error) {
	if limit <= 0 {
		return nil, nil, nil
	}
	where, args := historyWhere(id, from, to, before)
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, `+speedColumns+` FROM speed_results`+where+` ORDER BY checked_at DESC, id DESC LIMIT ?`,
		append(args, limit+1)...)
	if err != nil {
		return nil, nil, fmt.Errorf("page speed: %w", err)
	}
	defer rows.Close()
	var out []SpeedRecord
	for rows.Next() {
		var rid int64
		sp, err := scanSpeed(idScanner{rows, &rid})
		if err != nil {
			return nil, nil, fmt.Errorf("page speed: %w", err)
		}
		out = append(out, SpeedRecord{ID: rid, Speed: sp})
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	var next *HistoryCursor
	if len(out) > limit {
		out = out[:limit]
		last := out[len(out)-1]
		next = &HistoryCursor{At: last.Speed.CheckedAt, ID: last.ID}
	}
	return out, next, nil
}

// idScanner reads a leading id column into *id and passes the rest on.
type idScanner struct {
	r  rowScanner
	id *int64
}

func (s idScanner) Scan(dest ...any) error {
	return s.r.Scan(append([]any{s.id}, dest...)...)
}

// AvailabilityBucket aggregates the results of one chart interval.
type AvailabilityBucket struct {
	Start                   time.Time
	Samples                 int
	Up, Down, Indeterminate int
	AvgDurationMs           *float64 // mean duration of up results; nil without any
}

// SpeedBucket aggregates the completed samples of one chart interval. A
// direction without a completed sample has no value, not zero.
type SpeedBucket struct {
	Start                          time.Time
	Samples                        int
	Download, Upload               *float64
	DownloadSamples, UploadSamples int
}

func bucketParams(from, to time.Time, buckets int) (widthMs int64, err error) {
	if buckets < 1 || buckets > MaxChartBuckets {
		return 0, fmt.Errorf("%w: buckets must be between 1 and %d", ErrInvalid, MaxChartBuckets)
	}
	span := to.Sub(from).Milliseconds()
	if span <= 0 {
		return 0, fmt.Errorf("%w: empty time range", ErrInvalid)
	}
	w := (span + int64(buckets) - 1) / int64(buckets)
	if w < 1 {
		w = 1
	}
	return w, nil
}

// ChartAvailability returns exactly `buckets` consecutive intervals covering
// [from, to). Intervals without results are present with zero samples.
func (s *Store) ChartAvailability(ctx context.Context, id domain.ProxyID, from, to time.Time, buckets int) ([]AvailabilityBucket, error) {
	width, err := bucketParams(from, to, buckets)
	if err != nil {
		return nil, err
	}
	fromMs := from.UTC().UnixMilli()
	rows, err := s.db.QueryContext(ctx,
		`SELECT (checked_at - ?) / ?, COUNT(*), SUM(state = 'up'), SUM(state = 'down'), SUM(state = 'indeterminate'),
		        AVG(CASE WHEN state = 'up' THEN duration_us END)
		 FROM availability_results WHERE proxy_id = ? AND checked_at >= ? AND checked_at < ?
		 GROUP BY 1`, fromMs, width, int64(id), fromMs, fromMs+width*int64(buckets))
	if err != nil {
		return nil, fmt.Errorf("chart availability: %w", err)
	}
	defer rows.Close()
	out := make([]AvailabilityBucket, buckets)
	for i := range out {
		out[i].Start = time.UnixMilli(fromMs + int64(i)*width).UTC()
	}
	for rows.Next() {
		var (
			idx   int
			b     AvailabilityBucket
			avgUS sql.NullFloat64
		)
		if err := rows.Scan(&idx, &b.Samples, &b.Up, &b.Down, &b.Indeterminate, &avgUS); err != nil {
			return nil, fmt.Errorf("chart availability: %w", err)
		}
		if idx < 0 || idx >= buckets {
			continue
		}
		b.Start = out[idx].Start
		if avgUS.Valid {
			ms := avgUS.Float64 / 1000
			b.AvgDurationMs = &ms
		}
		out[idx] = b
	}
	return out, rows.Err()
}

// ChartSpeed is ChartAvailability for speed results: it averages the
// completed samples of each direction.
func (s *Store) ChartSpeed(ctx context.Context, id domain.ProxyID, from, to time.Time, buckets int) ([]SpeedBucket, error) {
	width, err := bucketParams(from, to, buckets)
	if err != nil {
		return nil, err
	}
	fromMs := from.UTC().UnixMilli()
	rows, err := s.db.QueryContext(ctx,
		`SELECT (checked_at - ?) / ?, COUNT(*), AVG(download_mbps), COUNT(download_mbps), AVG(upload_mbps), COUNT(upload_mbps)
		 FROM speed_results WHERE proxy_id = ? AND checked_at >= ? AND checked_at < ?
		 GROUP BY 1`, fromMs, width, int64(id), fromMs, fromMs+width*int64(buckets))
	if err != nil {
		return nil, fmt.Errorf("chart speed: %w", err)
	}
	defer rows.Close()
	out := make([]SpeedBucket, buckets)
	for i := range out {
		out[i].Start = time.UnixMilli(fromMs + int64(i)*width).UTC()
	}
	for rows.Next() {
		var (
			idx    int
			b      SpeedBucket
			dl, ul sql.NullFloat64
		)
		if err := rows.Scan(&idx, &b.Samples, &dl, &b.DownloadSamples, &ul, &b.UploadSamples); err != nil {
			return nil, fmt.Errorf("chart speed: %w", err)
		}
		if idx < 0 || idx >= buckets {
			continue
		}
		b.Start = out[idx].Start
		if dl.Valid {
			v := dl.Float64
			b.Download = &v
		}
		if ul.Valid {
			v := ul.Float64
			b.Upload = &v
		}
		out[idx] = b
	}
	return out, rows.Err()
}
