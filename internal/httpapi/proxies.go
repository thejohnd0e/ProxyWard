package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/thejohnd0e/ProxyWard/internal/apierr"
	"github.com/thejohnd0e/ProxyWard/internal/domain"
	"github.com/thejohnd0e/ProxyWard/internal/proxy"
	"github.com/thejohnd0e/ProxyWard/internal/store"
)

// maxProxyBodyBytes caps proxy and import request bodies.
const maxProxyBodyBytes = 256 << 10

func (s *Server) registerProxyRoutes(mux *http.ServeMux) {
	protected := func(h http.HandlerFunc) http.Handler { return s.auth.Require(h) }
	mux.Handle("GET /api/proxies", protected(s.listProxies))
	mux.Handle("POST /api/proxies", protected(s.createProxy))
	mux.Handle("GET /api/proxies/{id}", protected(s.getProxy))
	mux.Handle("PUT /api/proxies/{id}", protected(s.updateProxy))
	mux.Handle("DELETE /api/proxies/{id}", protected(s.deleteProxy))
	mux.Handle("POST /api/proxies/import/preview", protected(s.importPreview))
	mux.Handle("POST /api/proxies/import", protected(s.importProxies))
}

// optional distinguishes an absent JSON field from an explicit null.
type optional[T any] struct {
	Present bool
	Null    bool
	Value   T
}

func (o *optional[T]) UnmarshalJSON(b []byte) error {
	o.Present = true
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		o.Null = true
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	return dec.Decode(&o.Value)
}

// set reports whether the field was sent with a non-null value.
func (o optional[T]) set() bool { return o.Present && !o.Null }

type scheduleIn struct {
	Enabled         *bool `json:"enabled"`
	IntervalMinutes *int  `json:"interval_minutes"`
}

type proxyInput struct {
	Name                 optional[string]     `json:"name"`
	Protocol             optional[string]     `json:"protocol"`
	Host                 optional[string]     `json:"host"`
	Port                 optional[int]        `json:"port"`
	Tags                 optional[[]string]   `json:"tags"`
	AuthEnabled          optional[bool]       `json:"auth_enabled"`
	Username             optional[string]     `json:"username"`
	Password             optional[string]     `json:"password"`
	AvailabilitySchedule optional[scheduleIn] `json:"availability_schedule"`
	SpeedSchedule        optional[scheduleIn] `json:"speed_schedule"`
}

type scheduleDTO struct {
	Enabled         bool `json:"enabled"`
	IntervalMinutes int  `json:"interval_minutes"`
}

// proxyDTO is the only proxy shape sent to clients: it never contains a password.
type proxyDTO struct {
	ID                   int64        `json:"id"`
	Name                 string       `json:"name"`
	Protocol             string       `json:"protocol"`
	Host                 string       `json:"host"`
	Port                 uint16       `json:"port"`
	Tags                 []string     `json:"tags"`
	AuthEnabled          bool         `json:"auth_enabled"`
	Username             string       `json:"username"`
	HasPassword          bool         `json:"has_password"`
	AvailabilitySchedule *scheduleDTO `json:"availability_schedule"`
	SpeedSchedule        *scheduleDTO `json:"speed_schedule"`

	// Dashboard data, filled by decorate.
	Status     statusDTO      `json:"status"`
	Speed      *speedDTO      `json:"speed"`
	Geo        *geoDTO        `json:"geo"`
	Uptime     uptimeDTO      `json:"uptime"`
	ActiveJobs []activeJobDTO `json:"active_jobs"`
	NextRuns   nextRunsDTO    `json:"next_runs"`
}

func toDTO(p domain.Proxy) proxyDTO {
	d := proxyDTO{
		ID: int64(p.ID), Name: p.Name, Protocol: string(p.Protocol), Host: p.Host, Port: p.Port,
		Tags:                 p.Tags,
		AvailabilitySchedule: scheduleToDTO(p.AvailabilitySchedule),
		SpeedSchedule:        scheduleToDTO(p.SpeedSchedule),
	}
	if d.Tags == nil {
		d.Tags = []string{}
	}
	if p.Auth != nil {
		d.AuthEnabled, d.Username, d.HasPassword = true, p.Auth.Username, p.Auth.Password != ""
	}
	return d
}

func scheduleToDTO(s *domain.Schedule) *scheduleDTO {
	if s == nil {
		return nil
	}
	return &scheduleDTO{Enabled: s.Enabled, IntervalMinutes: int(s.Interval / time.Minute)}
}

// buildProxy validates input. existing is nil when creating; when updating it
// supplies the values for omitted fields. Problems are returned per field.
func buildProxy(in proxyInput, existing *domain.Proxy) (domain.Proxy, store.CredentialUpdate, map[string]string) {
	fields := map[string]string{}
	var p domain.Proxy
	var cu store.CredentialUpdate

	for name, f := range map[string]bool{
		"name": in.Name.Present && in.Name.Null, "protocol": in.Protocol.Present && in.Protocol.Null,
		"host": in.Host.Present && in.Host.Null, "port": in.Port.Present && in.Port.Null,
		"tags": in.Tags.Present && in.Tags.Null, "auth_enabled": in.AuthEnabled.Present && in.AuthEnabled.Null,
		"username": in.Username.Present && in.Username.Null, "password": in.Password.Present && in.Password.Null,
	} {
		if f {
			fields[name] = "Must not be null."
		}
	}

	if proto := domain.Protocol(in.Protocol.Value); in.Protocol.set() && proto.Valid() {
		p.Protocol = proto
	} else if _, bad := fields["protocol"]; !bad {
		fields["protocol"] = "Must be one of http, https or socks5."
	}
	if in.Host.set() {
		if h, err := proxy.NormalizeHost(in.Host.Value); err == nil {
			p.Host = h
		} else {
			fields["host"] = "Must be a valid hostname or IP address without scheme or port."
		}
	} else if _, bad := fields["host"]; !bad {
		fields["host"] = "Host is required."
	}
	if in.Port.set() && in.Port.Value >= 1 && in.Port.Value <= 65535 {
		p.Port = uint16(in.Port.Value)
	} else if _, bad := fields["port"]; !bad {
		fields["port"] = "Must be a number from 1 to 65535."
	}

	switch {
	case in.Name.set() && in.Name.Value != "" && strings.TrimSpace(in.Name.Value) != "":
		if n, err := proxy.ValidateName(in.Name.Value); err == nil {
			p.Name = n
		} else {
			fields["name"] = err.Error()
		}
	case in.Name.set() || existing == nil:
		if p.Host != "" && p.Port != 0 {
			p.Name = net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port)))
		}
	default:
		p.Name = existing.Name
	}

	switch {
	case in.Tags.set():
		if tags, err := proxy.ValidateTags(in.Tags.Value); err == nil {
			p.Tags = tags
		} else {
			fields["tags"] = err.Error()
		}
	case existing != nil:
		p.Tags = existing.Tags
	default:
		p.Tags = []string{}
	}

	p.AvailabilitySchedule = buildSchedule("availability_schedule", in.AvailabilitySchedule, existingSchedule(existing, false), fields)
	p.SpeedSchedule = buildSchedule("speed_schedule", in.SpeedSchedule, existingSchedule(existing, true), fields)

	buildAuth(in, existing, &cu, fields)
	return p, cu, fields
}

func existingSchedule(existing *domain.Proxy, speed bool) *domain.Schedule {
	switch {
	case existing == nil:
		return nil
	case speed:
		return existing.SpeedSchedule
	}
	return existing.AvailabilitySchedule
}

func buildSchedule(field string, in optional[scheduleIn], current *domain.Schedule, fields map[string]string) *domain.Schedule {
	switch {
	case !in.Present:
		return current
	case in.Null:
		return nil
	}
	v := in.Value
	if v.Enabled == nil || v.IntervalMinutes == nil {
		fields[field] = "Both enabled and interval_minutes are required, or send null to inherit."
		return nil
	}
	if *v.IntervalMinutes < 1 || *v.IntervalMinutes > 10080 {
		fields[field] = "interval_minutes must be from 1 to 10080."
		return nil
	}
	return &domain.Schedule{Enabled: *v.Enabled, Interval: time.Duration(*v.IntervalMinutes) * time.Minute}
}

// buildAuth applies the credential rules: omitted fields keep stored values,
// auth_enabled=false removes credentials, and a string password (even empty)
// replaces the stored one. Credentials are rejected unless authentication is on.
func buildAuth(in proxyInput, existing *domain.Proxy, cu *store.CredentialUpdate, fields map[string]string) {
	hadAuth := existing != nil && existing.Auth != nil
	enabled := hadAuth
	if in.AuthEnabled.set() {
		enabled = in.AuthEnabled.Value
	}
	if in.Username.Null || in.Password.Null {
		return // already reported as null
	}
	if !enabled {
		if in.Username.Present || in.Password.Present {
			fields["username"] = "Credentials require authentication to be enabled."
		}
		cu.Remove = hadAuth
		return
	}
	switch {
	case in.Username.Present && in.Username.Value == "":
		fields["username"] = "Username is required when authentication is enabled."
	case !hadAuth && !in.Username.Present:
		fields["username"] = "Username is required when authentication is enabled."
	case in.Username.Present:
		u := in.Username.Value
		cu.Username = &u
	}
	switch {
	case in.Password.Present:
		pw := in.Password.Value
		cu.Password = &pw
	case !hadAuth:
		empty := ""
		cu.Password = &empty
	}
}

func parseID(r *http.Request) (domain.ProxyID, bool) {
	n, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return domain.ProxyID(n), true
}

func writeValidation(w http.ResponseWriter, fields map[string]string) {
	apierr.Write(w, http.StatusUnprocessableEntity, "validation_failed", "Some fields are not valid.", fields)
}

// storeError maps store errors to fixed, secret-free API responses.
func (s *Server) storeError(w http.ResponseWriter, op string, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		apierr.Write(w, http.StatusNotFound, "not_found", "Not found.", nil)
	case errors.Is(err, store.ErrConflict):
		apierr.Write(w, http.StatusConflict, "duplicate", "A proxy with the same endpoint and credentials already exists.", nil)
	case errors.Is(err, store.ErrLimit):
		apierr.Write(w, http.StatusUnprocessableEntity, "record_limit", "At most 100 proxies can be stored.", nil)
	case errors.Is(err, store.ErrInvalid):
		writeValidation(w, nil)
	default:
		s.internalError(w, op, err)
	}
}

func (s *Server) listProxies(w http.ResponseWriter, r *http.Request) {
	list, err := s.db.ListProxies(r.Context())
	if err != nil {
		s.internalError(w, "list proxies", err)
		return
	}
	out, err := s.decorate(r.Context(), list)
	if err != nil {
		s.internalError(w, "list proxies", err)
		return
	}
	_, geoConfigured, err := s.db.GeoKey(r.Context())
	if err != nil {
		s.internalError(w, "read geolocation key", err)
		return
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"proxies": out, "limit": domain.MaxProxies, "geo_configured": geoConfigured})
}

func (s *Server) createProxy(w http.ResponseWriter, r *http.Request) {
	var in proxyInput
	if !decodeJSONLimit(w, r, &in, maxProxyBodyBytes) {
		return
	}
	p, cu, fields := buildProxy(in, nil)
	if len(fields) > 0 {
		writeValidation(w, fields)
		return
	}
	if cu.Username != nil {
		p.Auth = &domain.Credentials{Username: *cu.Username, Password: derefOr(cu.Password, "")}
	}
	id, err := s.db.InsertProxy(r.Context(), p)
	if err != nil {
		s.storeError(w, "create proxy", err)
		return
	}
	p.ID = id
	s.respondProxy(w, r, http.StatusCreated, p)
}

func derefOr(s *string, def string) string {
	if s == nil {
		return def
	}
	return *s
}

func (s *Server) getProxy(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(r)
	if !ok {
		s.storeError(w, "get proxy", store.ErrNotFound)
		return
	}
	p, err := s.db.GetProxy(r.Context(), id)
	if err != nil {
		s.storeError(w, "get proxy", err)
		return
	}
	s.respondProxy(w, r, http.StatusOK, p)
}

func (s *Server) updateProxy(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(r)
	if !ok {
		s.storeError(w, "update proxy", store.ErrNotFound)
		return
	}
	var in proxyInput
	if !decodeJSONLimit(w, r, &in, maxProxyBodyBytes) {
		return
	}
	existing, err := s.db.GetProxy(r.Context(), id)
	if err != nil {
		s.storeError(w, "update proxy", err)
		return
	}
	p, cu, fields := buildProxy(in, &existing)
	if len(fields) > 0 {
		writeValidation(w, fields)
		return
	}
	if err := s.db.UpdateProxy(r.Context(), id, p, cu); err != nil {
		s.storeError(w, "update proxy", err)
		return
	}
	updated, err := s.db.GetProxy(r.Context(), id)
	if err != nil {
		s.storeError(w, "update proxy", err)
		return
	}
	s.respondProxy(w, r, http.StatusOK, updated)
}

func (s *Server) deleteProxy(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(r)
	if !ok {
		s.storeError(w, "delete proxy", store.ErrNotFound)
		return
	}
	if s.jobs != nil {
		// Stop pending and running checks first so none writes results for a proxy that is going away.
		if err := s.jobs.CancelProxy(r.Context(), id); err != nil {
			s.internalError(w, "cancel proxy jobs", err)
			return
		}
	}
	if err := s.db.DeleteProxy(r.Context(), id); err != nil {
		s.storeError(w, "delete proxy", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type importRequest struct {
	Text            string `json:"text"`
	DefaultProtocol string `json:"default_protocol"`
	SelectedLines   []int  `json:"selected_lines"`
}

type importRowDTO struct {
	Line      int    `json:"line"`
	Masked    string `json:"masked"`
	Valid     bool   `json:"valid"`
	Duplicate bool   `json:"duplicate"`
	ErrorCode string `json:"error_code"`
}

// parseImport parses the pasted list and marks rows that already exist in the
// database as duplicates. It writes the error response itself on failure.
func (s *Server) parseImport(w http.ResponseWriter, r *http.Request, req importRequest) ([]proxy.ParsedRow, bool) {
	proto := domain.Protocol(req.DefaultProtocol)
	if !proto.Valid() {
		writeValidation(w, map[string]string{"default_protocol": "Must be one of http, https or socks5."})
		return nil, false
	}
	rows := proxy.ParseList(req.Text, proto)
	if len(rows) > proxy.MaxImportLines {
		apierr.Write(w, http.StatusUnprocessableEntity, "too_many_lines", "The list has too many lines.", nil)
		return nil, false
	}
	for i := range rows {
		if !rows[i].Row.Valid || rows[i].Row.Duplicate {
			continue
		}
		exists, err := s.db.ProxyExists(r.Context(), rows[i].Proxy)
		if err != nil {
			s.internalError(w, "check duplicates", err)
			return nil, false
		}
		rows[i].Row.Duplicate = exists
	}
	return rows, true
}

func (s *Server) importPreview(w http.ResponseWriter, r *http.Request) {
	var req importRequest
	if !decodeJSONLimit(w, r, &req, maxProxyBodyBytes) {
		return
	}
	rows, ok := s.parseImport(w, r, req)
	if !ok {
		return
	}
	existing, err := s.db.CountProxies(r.Context())
	if err != nil {
		s.internalError(w, "count proxies", err)
		return
	}
	out := make([]importRowDTO, len(rows))
	importable := 0
	for i, p := range rows {
		out[i] = importRowDTO{Line: p.Row.Line, Masked: p.Row.Masked, Valid: p.Row.Valid, Duplicate: p.Row.Duplicate, ErrorCode: p.Row.ErrorCode}
		if p.Row.Valid && !p.Row.Duplicate {
			importable++
		}
	}
	apierr.JSON(w, http.StatusOK, map[string]any{
		"rows": out, "existing_count": existing, "limit": domain.MaxProxies, "importable_count": importable,
	})
}

func (s *Server) importProxies(w http.ResponseWriter, r *http.Request) {
	var req importRequest
	if !decodeJSONLimit(w, r, &req, maxProxyBodyBytes) {
		return
	}
	rows, ok := s.parseImport(w, r, req)
	if !ok {
		return
	}
	byLine := make(map[int]proxy.ParsedRow, len(rows))
	for _, p := range rows {
		byLine[p.Row.Line] = p
	}
	selected := dedupeInts(req.SelectedLines)
	if len(selected) == 0 {
		writeValidation(w, map[string]string{"selected_lines": "Select at least one line."})
		return
	}
	batch := make([]domain.Proxy, 0, len(selected))
	for _, line := range selected {
		p, found := byLine[line]
		if !found || !p.Row.Valid || p.Row.Duplicate {
			writeValidation(w, map[string]string{"selected_lines": "Line " + strconv.Itoa(line) + " cannot be imported."})
			return
		}
		batch = append(batch, p.Proxy)
	}
	ids, err := s.db.ImportProxies(r.Context(), batch)
	if err != nil {
		s.storeError(w, "import proxies", err)
		return
	}
	out := make([]int64, len(ids))
	for i, id := range ids {
		out[i] = int64(id)
	}
	apierr.JSON(w, http.StatusCreated, map[string]any{"imported": len(ids), "ids": out})
}

func dedupeInts(in []int) []int {
	seen := map[int]bool{}
	out := make([]int, 0, len(in))
	for _, n := range in {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Ints(out)
	return out
}

// respondProxy sends one proxy with its dashboard data.
func (s *Server) respondProxy(w http.ResponseWriter, r *http.Request, status int, p domain.Proxy) {
	out, err := s.decorate(r.Context(), []domain.Proxy{p})
	if err != nil || len(out) != 1 {
		s.internalError(w, "build proxy view", err)
		return
	}
	apierr.JSON(w, status, out[0])
}
