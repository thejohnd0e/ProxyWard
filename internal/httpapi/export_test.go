package httpapi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/thejohnd0e/ProxyWard/internal/auth"
	"github.com/thejohnd0e/ProxyWard/internal/domain"
)

const (
	exportFrom = "2026-04-30T09:00:00Z"
	exportTo   = "2026-05-01T09:00:00Z"
)

func exportBody(ids ...int64) string {
	raw, _ := json.Marshal(m{"from": exportFrom, "to": exportTo, "proxy_ids": ids})
	return string(raw)
}

func exportCall(s *session, ids ...int64) resp {
	s.e.t.Helper()
	return s.call("POST", "/api/exports", exportBody(ids...))
}

func exportTimes(t *testing.T) (time.Time, time.Time) {
	t.Helper()
	from, err := time.Parse(time.RFC3339, exportFrom)
	if err != nil {
		t.Fatal(err)
	}
	to, err := time.Parse(time.RFC3339, exportTo)
	if err != nil {
		t.Fatal(err)
	}
	return from, to
}

func TestExportRequiresAuthenticationAndCSRF(t *testing.T) {
	s := newSession(t)
	if r := s.e.do("POST", "/api/exports", exportBody(1), nil, ""); r.status != 401 {
		t.Fatalf("without a session: %d %s", r.status, r.body)
	}
	if r := s.e.do("POST", "/api/exports", exportBody(1), nil, s.cookie); r.status != 403 {
		t.Fatalf("without CSRF: %d %s", r.status, r.body)
	}
}

func TestExportReturnsSelectedData(t *testing.T) {
	s := newSession(t)
	kept := s.create(m{"protocol": "http", "host": "kept.lan", "port": 8080,
		"auth_enabled": true, "username": "user", "password": "proxy-SECRET-SENTINEL"})
	other := s.create(m{"protocol": "http", "host": "other.lan", "port": 8080})
	keptID := int64(kept["id"].(float64))
	otherID := int64(other["id"].(float64))
	from, to := exportTimes(t)
	ctx := context.Background()

	if err := s.e.db.AddAvailability(ctx, domain.Availability{ProxyID: domain.ProxyID(keptID), CheckedAt: from,
		State: domain.StateUp, ExitIP: "198.51.100.9", Endpoint: "ipify", Duration: 200 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	if err := s.e.db.AddAvailability(ctx, domain.Availability{ProxyID: domain.ProxyID(otherID), CheckedAt: from, State: domain.StateUp}); err != nil {
		t.Fatal(err)
	}
	if err := s.e.db.AddSpeed(ctx, domain.Speed{ProxyID: domain.ProxyID(keptID), CheckedAt: from.Add(time.Hour),
		Provider: "cloudflare", Method: "app-stream-v1", TotalBytes: 42}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := s.e.db.RecordAvailabilityOutcome(ctx, domain.Availability{ProxyID: domain.ProxyID(keptID),
			CheckedAt: to, State: domain.StateDown, Failure: &domain.Failure{Code: "proxy_timeout", Message: "message SECRET-SENTINEL"}}, 3); err != nil {
			t.Fatal(err)
		}
	}

	r := exportCall(s, keptID)
	if r.status != 200 {
		t.Fatalf("export: %d %s", r.status, r.body)
	}
	if ct := r.header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content type: %q", ct)
	}
	if cd := r.header.Get("Content-Disposition"); !strings.Contains(cd, "attachment") || !strings.Contains(cd, "proxyward-export.json") {
		t.Fatalf("content disposition: %q", cd)
	}
	if cc := r.header.Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("exports must never be cached: %q", cc)
	}
	if strings.Contains(r.body, "SECRET-SENTINEL") || strings.Contains(r.body, "password") {
		t.Fatalf("the export leaked a secret: %s", r.body)
	}

	got := decode(t, r)
	if got["schema_version"] != float64(1) {
		t.Fatalf("schema version: %v", got["schema_version"])
	}
	if got["generated_at"] != "2026-05-01T09:00:00Z" || got["from"] != exportFrom || got["to"] != exportTo {
		t.Fatalf("metadata: %v %v %v", got["generated_at"], got["from"], got["to"])
	}
	proxies := got["proxies"].([]any)
	if len(proxies) != 1 || proxies[0].(m)["id"] != float64(keptID) || proxies[0].(m)["host"] != "kept.lan" {
		t.Fatalf("proxies: %v", proxies)
	}
	if avail := got["availability"].([]any); len(avail) != 1 || avail[0].(m)["proxy_id"] != float64(keptID) {
		t.Fatalf("availability: %v", avail)
	}
	if speed := got["speed"].([]any); len(speed) != 1 || speed[0].(m)["total_bytes"] != float64(42) {
		t.Fatalf("speed: %v", speed)
	}
	events := got["events"].([]any)
	if len(events) != 1 || events[0].(m)["kind"] != "proxy_down" || events[0].(m)["proxy_id"] != float64(keptID) {
		t.Fatalf("events: %v", events)
	}
	if _, leaked := events[0].(m)["message"]; leaked {
		t.Fatal("free-form event messages are not exported")
	}
}

func TestExportValidation(t *testing.T) {
	s := newSession(t)
	p := s.create(m{"protocol": "http", "host": "only.lan", "port": 8080})
	id := int64(p["id"].(float64))
	cases := map[string]struct {
		body   string
		status int
		code   string
	}{
		"not json":       {`nope`, 400, "invalid_json"},
		"unknown field":  {`{"from":"` + exportFrom + `","to":"` + exportTo + `","proxy_ids":[1],"x":1}`, 400, "invalid_json"},
		"missing from":   {`{"to":"` + exportTo + `","proxy_ids":[1]}`, 422, "validation_failed"},
		"bad date":       {`{"from":"yesterday","to":"` + exportTo + `","proxy_ids":[1]}`, 422, "validation_failed"},
		"reversed range": {`{"from":"` + exportTo + `","to":"` + exportFrom + `","proxy_ids":[1]}`, 422, "validation_failed"},
		"no proxies":     {`{"from":"` + exportFrom + `","to":"` + exportTo + `","proxy_ids":[]}`, 422, "validation_failed"},
		"duplicate ids":  {exportBody(id, id), 422, "validation_failed"},
		"unknown proxy":  {exportBody(9999), 404, "not_found"},
	}
	for name, c := range cases {
		r := s.call("POST", "/api/exports", c.body)
		if r.status != c.status || !strings.Contains(r.body, c.code) {
			t.Errorf("%s: %d %s", name, r.status, r.body)
		}
		if strings.Contains(r.header.Get("Content-Disposition"), "attachment") {
			t.Errorf("%s: a failed export must not look like a download", name)
		}
	}
}

func TestExportTooLarge(t *testing.T) {
	e := newEnv(t, true, WithExportLimit(2))
	cookie, csrf, r := e.login(adminPassword)
	if r.status != 200 {
		t.Fatalf("login: %d", r.status)
	}
	handler := &session{e: e, cookie: cookie, csrf: csrf}
	// Three results exceed the configured limit of two.
	p := handler.create(m{"protocol": "http", "host": "busy.lan", "port": 8080})
	id := domain.ProxyID(p["id"].(float64))
	from, _ := exportTimes(t)
	for i := 0; i < 3; i++ {
		if err := e.db.AddAvailability(context.Background(), domain.Availability{ProxyID: id, CheckedAt: from.Add(time.Duration(i) * time.Minute), State: domain.StateUp}); err != nil {
			t.Fatal(err)
		}
	}
	res := e.do("POST", "/api/exports", exportBody(int64(id)), map[string]string{auth.CSRFHeader: csrf}, cookie)
	if res.status != 413 || !strings.Contains(res.body, "export_too_large") {
		t.Fatalf("too large: %d %s", res.status, res.body)
	}
	if res.header.Get("Content-Disposition") != "" {
		t.Fatal("a refused export must not be sent as an attachment")
	}
}
