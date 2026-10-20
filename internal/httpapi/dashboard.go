package httpapi

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/thejohnd0e/ProxyWard/internal/apierr"
	"github.com/thejohnd0e/ProxyWard/internal/domain"
	"github.com/thejohnd0e/ProxyWard/internal/geo"
	"github.com/thejohnd0e/ProxyWard/internal/providers"
	"github.com/thejohnd0e/ProxyWard/internal/store"
)

// uptimeWindow is the period the uptime figure covers.
const uptimeWindow = 24 * time.Hour

// GeoPeeker returns cached geolocation without any network access and lists
// the available sources.
type GeoPeeker interface {
	Peek(ctx context.Context, ip netip.Addr) (geo.GeoView, error)
	Sources() []providers.GeoSource
}

// WithGeo enables geolocation data in proxy views.
func WithGeo(g GeoPeeker) Option { return func(s *Server) { s.geo = g } }

type failureDTO struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func toFailure(f *domain.Failure) *failureDTO {
	if f == nil {
		return nil
	}
	return &failureDTO{Code: f.Code, Message: f.Message}
}

type statusDTO struct {
	State      string      `json:"state"`
	CheckedAt  *string     `json:"checked_at"`
	ExitIP     string      `json:"exit_ip"`
	Endpoint   string      `json:"endpoint"`
	DurationMs *int64      `json:"duration_ms"`
	Failure    *failureDTO `json:"failure"`
}

type transferDTO struct {
	Bytes         int64       `json:"bytes"`
	DurationMs    int64       `json:"duration_ms"`
	Mbps          *float64    `json:"mbps"`
	LowConfidence bool        `json:"low_confidence"`
	Failure       *failureDTO `json:"failure"`
}

type speedDTO struct {
	CheckedAt  string      `json:"checked_at"`
	Provider   string      `json:"provider"`
	Method     string      `json:"method"`
	TotalBytes int64       `json:"total_bytes"`
	Download   transferDTO `json:"download"`
	Upload     transferDTO `json:"upload"`
}

type geoDTO struct {
	Status       string  `json:"status"`
	Stale        bool    `json:"stale"`
	CountryCode  string  `json:"country_code"`
	Country      string  `json:"country"`
	City         string  `json:"city"`
	ASN          *uint32 `json:"asn"`
	Organization string  `json:"organization"`
	FetchedAt    string  `json:"fetched_at"`
}

type uptimeDTO struct {
	Percent     *float64 `json:"percent"`
	Samples     int      `json:"samples"`
	WindowHours int      `json:"window_hours"`
}

type activeJobDTO struct {
	ID     int64  `json:"id"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
}

type nextRunsDTO struct {
	Availability *string `json:"availability"`
	Speed        *string `json:"speed"`
}

func toTransferDTO(t domain.Transfer) transferDTO {
	return transferDTO{Bytes: t.Bytes, DurationMs: t.Duration.Milliseconds(), Mbps: t.Mbps, LowConfidence: t.LowConfidence, Failure: toFailure(t.Failure)}
}

func toSpeedDTO(sp domain.Speed) speedDTO {
	return speedDTO{
		CheckedAt: ts(sp.CheckedAt), Provider: sp.Provider, Method: sp.Method, TotalBytes: sp.TotalBytes,
		Download: toTransferDTO(sp.Download), Upload: toTransferDTO(sp.Upload),
	}
}

// decorate converts proxies to views and adds the latest results, geolocation,
// uptime and queue state. Values that do not exist stay null: a proxy that was
// never checked is "unknown", not down, and a failed direction has no rate.
func (s *Server) decorate(ctx context.Context, proxies []domain.Proxy) ([]proxyDTO, error) {
	latest, err := s.db.LatestAvailabilityAll(ctx)
	if err != nil {
		return nil, err
	}
	speeds, err := s.db.LatestSpeedAll(ctx)
	if err != nil {
		return nil, err
	}
	now := s.clock.Now()
	uptime, err := s.db.UptimeSince(ctx, now.Add(-uptimeWindow))
	if err != nil {
		return nil, err
	}
	active, err := s.db.ListActiveJobs(ctx)
	if err != nil {
		return nil, err
	}
	schedules, err := s.db.ListSchedules(ctx)
	if err != nil {
		return nil, err
	}
	nextRuns := map[domain.ProxyID]store.ScheduleState{}
	for _, st := range schedules {
		nextRuns[st.ProxyID] = st
	}
	jobsBy := map[domain.ProxyID][]activeJobDTO{}
	for _, j := range active {
		jobsBy[j.ProxyID] = append(jobsBy[j.ProxyID], activeJobDTO{ID: j.ID, Kind: string(j.Kind), Status: string(j.Status)})
	}

	out := make([]proxyDTO, len(proxies))
	for i, p := range proxies {
		d := toDTO(p)
		d.Status = statusDTO{State: string(domain.StateUnknown)}
		if a, ok := latest[p.ID]; ok {
			ms := a.Duration.Milliseconds()
			d.Status = statusDTO{State: string(a.State), CheckedAt: tsPtr(&a.CheckedAt), ExitIP: a.ExitIP, Endpoint: a.Endpoint,
				DurationMs: &ms, Failure: toFailure(a.Failure)}
			d.Geo = s.geoFor(ctx, a.ExitIP)
		}
		if sp, ok := speeds[p.ID]; ok {
			v := toSpeedDTO(sp)
			d.Speed = &v
		}
		u := uptime[p.ID]
		d.Uptime = uptimeDTO{Percent: u.Percent(), Samples: u.Samples(), WindowHours: int(uptimeWindow / time.Hour)}
		d.ActiveJobs = jobsBy[p.ID]
		if d.ActiveJobs == nil {
			d.ActiveJobs = []activeJobDTO{}
		}
		if st, ok := nextRuns[p.ID]; ok {
			d.NextRuns = nextRunsDTO{Availability: tsPtr(st.NextAvailability), Speed: tsPtr(st.NextSpeed)}
		}
		out[i] = d
	}
	return out, nil
}

func (s *Server) geoFor(ctx context.Context, exitIP string) *geoDTO {
	if s.geo == nil || exitIP == "" {
		return nil
	}
	ip, err := netip.ParseAddr(exitIP)
	if err != nil {
		return nil
	}
	v, err := s.geo.Peek(ctx, ip)
	if err != nil || !v.HasGeo {
		return nil
	}
	return &geoDTO{
		Status: v.Status, Stale: v.Stale, CountryCode: v.Geo.CountryCode, Country: v.Geo.Country, City: v.Geo.City,
		ASN: v.Geo.ASN, Organization: v.Geo.Organization, FetchedAt: ts(v.Geo.FetchedAt),
	}
}

func (s *Server) registerHistoryRoutes(mux *http.ServeMux) {
	protected := func(h http.HandlerFunc) http.Handler { return s.auth.Require(h) }
	mux.Handle("GET /api/proxies/{id}/history", protected(s.proxyHistory))
	mux.Handle("GET /api/proxies/{id}/chart", protected(s.proxyChart))
}

const (
	defaultHistoryLimit = 50
	maxHistoryLimit     = 200
	defaultChartPoints  = 96
	maxChartRange       = 366 * 24 * time.Hour
)

func encodeHistoryCursor(c *store.HistoryCursor) any {
	if c == nil {
		return nil
	}
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(c.At.UnixMilli(), 10) + ":" + strconv.FormatInt(c.ID, 10)))
}

func decodeHistoryCursor(v string) (*store.HistoryCursor, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(v)
	if err != nil {
		return nil, false
	}
	ms, id, ok := strings.Cut(string(raw), ":")
	if !ok {
		return nil, false
	}
	msN, e1 := strconv.ParseInt(ms, 10, 64)
	idN, e2 := strconv.ParseInt(id, 10, 64)
	if e1 != nil || e2 != nil || idN <= 0 || msN < 0 {
		return nil, false
	}
	return &store.HistoryCursor{At: time.UnixMilli(msN).UTC(), ID: idN}, true
}

type rangeQuery struct {
	kind     domain.CheckKind
	from, to time.Time
	limit    int
	points   int
	cursor   *store.HistoryCursor
}

// parseRange reads kind, from and to. Absent from/to stay zero.
func parseRange(r *http.Request, fields map[string]string) rangeQuery {
	q := r.URL.Query()
	rq := rangeQuery{kind: domain.CheckAvailability, limit: defaultHistoryLimit, points: defaultChartPoints}
	if v, ok := q["kind"]; ok {
		switch k := domain.CheckKind(v[0]); k {
		case domain.CheckAvailability, domain.CheckSpeed:
			rq.kind = k
		default:
			fields["kind"] = "Must be availability or speed."
		}
	}
	parse := func(name string) time.Time {
		v, ok := q[name]
		if !ok {
			return time.Time{}
		}
		t, err := time.Parse(time.RFC3339, v[0])
		if err != nil {
			fields[name] = "Must be an RFC 3339 time such as 2026-10-20T12:00:00Z."
		}
		return t.UTC()
	}
	rq.from, rq.to = parse("from"), parse("to")
	if !rq.from.IsZero() && !rq.to.IsZero() && !rq.from.Before(rq.to) {
		fields["from"] = "Must be before to."
	}
	return rq
}

func (s *Server) proxyExists(w http.ResponseWriter, r *http.Request) (domain.ProxyID, bool) {
	id, ok := parseID(r)
	if !ok {
		s.storeError(w, "history", store.ErrNotFound)
		return 0, false
	}
	if _, err := s.db.GetProxy(r.Context(), id); err != nil {
		s.storeError(w, "history", err)
		return 0, false
	}
	return id, true
}

func (s *Server) proxyHistory(w http.ResponseWriter, r *http.Request) {
	id, ok := s.proxyExists(w, r)
	if !ok {
		return
	}
	fields := map[string]string{}
	rq := parseRange(r, fields)
	q := r.URL.Query()
	if v, ok := q["limit"]; ok {
		n, err := strconv.Atoi(v[0])
		if err != nil || n < 1 || n > maxHistoryLimit {
			fields["limit"] = "Must be a number from 1 to 200."
		} else {
			rq.limit = n
		}
	}
	if v, ok := q["cursor"]; ok {
		c, valid := decodeHistoryCursor(v[0])
		if !valid {
			fields["cursor"] = "Not a valid cursor."
		}
		rq.cursor = c
	}
	if len(fields) > 0 {
		writeValidation(w, fields)
		return
	}

	if rq.kind == domain.CheckSpeed {
		rows, next, err := s.db.PageSpeed(r.Context(), id, rq.from, rq.to, rq.cursor, rq.limit)
		if err != nil {
			s.internalError(w, "speed history", err)
			return
		}
		items := make([]map[string]any, len(rows))
		for i, rec := range rows {
			v := toSpeedDTO(rec.Speed)
			items[i] = map[string]any{"id": rec.ID, "checked_at": v.CheckedAt, "provider": v.Provider, "method": v.Method,
				"total_bytes": v.TotalBytes, "download": v.Download, "upload": v.Upload}
		}
		apierr.JSON(w, http.StatusOK, map[string]any{"kind": "speed", "items": items, "next_cursor": encodeHistoryCursor(next)})
		return
	}
	rows, next, err := s.db.PageAvailability(r.Context(), id, rq.from, rq.to, rq.cursor, rq.limit)
	if err != nil {
		s.internalError(w, "availability history", err)
		return
	}
	items := make([]map[string]any, len(rows))
	for i, rec := range rows {
		a := rec.Availability
		items[i] = map[string]any{"id": rec.ID, "checked_at": ts(a.CheckedAt), "state": string(a.State), "exit_ip": a.ExitIP,
			"endpoint": a.Endpoint, "duration_ms": a.Duration.Milliseconds(), "failure": toFailure(a.Failure)}
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"kind": "availability", "items": items, "next_cursor": encodeHistoryCursor(next)})
}

func (s *Server) proxyChart(w http.ResponseWriter, r *http.Request) {
	id, ok := s.proxyExists(w, r)
	if !ok {
		return
	}
	fields := map[string]string{}
	rq := parseRange(r, fields)
	if v, ok := r.URL.Query()["points"]; ok {
		n, err := strconv.Atoi(v[0])
		if err != nil || n < 1 || n > store.MaxChartBuckets {
			fields["points"] = "Must be a number from 1 to 500."
		} else {
			rq.points = n
		}
	}
	now := s.clock.Now().UTC()
	to, from := rq.to, rq.from
	if to.IsZero() {
		to = now
	}
	if from.IsZero() {
		from = to.Add(-24 * time.Hour)
	}
	if len(fields) == 0 {
		switch {
		case !from.Before(to):
			fields["from"] = "Must be before to."
		case to.Sub(from) > maxChartRange:
			fields["from"] = "The range can be at most 366 days."
		}
	}
	if len(fields) > 0 {
		writeValidation(w, fields)
		return
	}
	interval := (to.Sub(from) + time.Duration(rq.points) - 1) / time.Duration(rq.points)
	interval = interval.Truncate(time.Millisecond)
	if interval < time.Millisecond {
		interval = time.Millisecond
	}
	resp := map[string]any{"kind": string(rq.kind), "from": ts(from), "to": ts(to), "interval_seconds": interval.Seconds()}

	if rq.kind == domain.CheckSpeed {
		b, err := s.db.ChartSpeed(r.Context(), id, from, to, rq.points)
		if err != nil {
			s.internalError(w, "speed chart", err)
			return
		}
		pts := make([]map[string]any, len(b))
		for i, x := range b {
			pts[i] = map[string]any{"t": ts(x.Start), "samples": x.Samples, "download_mbps": x.Download, "upload_mbps": x.Upload,
				"download_samples": x.DownloadSamples, "upload_samples": x.UploadSamples}
		}
		resp["points"] = pts
		apierr.JSON(w, http.StatusOK, resp)
		return
	}
	b, err := s.db.ChartAvailability(r.Context(), id, from, to, rq.points)
	if err != nil {
		s.internalError(w, "availability chart", err)
		return
	}
	pts := make([]map[string]any, len(b))
	for i, x := range b {
		pts[i] = map[string]any{"t": ts(x.Start), "samples": x.Samples, "up": x.Up, "down": x.Down, "indeterminate": x.Indeterminate,
			"avg_duration_ms": x.AvgDurationMs}
	}
	resp["points"] = pts
	apierr.JSON(w, http.StatusOK, resp)
}
