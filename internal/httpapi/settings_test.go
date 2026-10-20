package httpapi

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/thejohnd0e/ProxyWard/internal/domain"
)

const (
	apiToken = "123456789:AAH_api-TOKEN-value-1234567890abcd"
)

func getSettings(t *testing.T, s *session) m {
	t.Helper()
	r := s.call("GET", "/api/settings", nil)
	if r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	return decode(t, r)
}

func sub(t *testing.T, v m, keys ...string) any {
	t.Helper()
	var cur any = v
	for _, k := range keys {
		mm, ok := cur.(m)
		if !ok {
			t.Fatalf("%v is not an object at %q", cur, k)
		}
		cur = mm[k]
	}
	return cur
}

func TestSettingsRoutesRequireAuthenticationAndCSRF(t *testing.T) {
	s := newSession(t)
	for _, tc := range [][2]string{
		{"GET", "/api/settings"}, {"PUT", "/api/settings"}, {"POST", "/api/settings/telegram/test"},
		{"POST", "/api/settings/speed/resume"}, {"GET", "/api/events"},
	} {
		if r := s.e.do(tc[0], tc[1], "{}", nil, ""); r.status != 401 {
			t.Errorf("%s %s: %d", tc[0], tc[1], r.status)
		}
		if tc[0] != "GET" {
			if r := s.e.do(tc[0], tc[1], "{}", nil, s.cookie); r.status != 403 {
				t.Errorf("%s %s without CSRF: %d", tc[0], tc[1], r.status)
			}
		}
	}
}

func TestDefaultSettings(t *testing.T) {
	s := newSession(t)
	v := getSettings(t, s)
	if sub(t, v, "monitoring", "availability", "enabled") != false || sub(t, v, "monitoring", "speed", "enabled") != false ||
		sub(t, v, "monitoring", "availability", "interval_minutes") != float64(5) ||
		sub(t, v, "monitoring", "speed", "interval_minutes") != float64(360) ||
		sub(t, v, "monitoring", "availability_concurrency") != float64(5) ||
		sub(t, v, "monitoring", "availability_timeout_seconds") != float64(10) ||
		sub(t, v, "monitoring", "speed_sample_mib") != float64(8) ||
		sub(t, v, "monitoring", "down_threshold") != float64(3) ||
		sub(t, v, "monitoring", "retention_days") != float64(30) {
		t.Fatalf("monitoring defaults: %v", v["monitoring"])
	}
	if sub(t, v, "geo", "source") != "auto" {
		t.Fatalf("geo: %v", v["geo"])
	}
	if list, _ := sub(t, v, "geo", "sources").([]any); len(list) != 2 || list[0].(m)["id"] != "alpha" || list[0].(m)["name"] != "Alpha" || list[0].(m)["site"] != "https://alpha.example/" {
		t.Fatalf("sources: %v", v["geo"])
	}
	if _, has := v["geo"].(m)["has_key"]; has {
		t.Fatal("there is no geolocation key any more")
	}
	if sub(t, v, "telegram", "has_token") != false || sub(t, v, "telegram", "configured") != false || sub(t, v, "telegram", "chat_id") != "" {
		t.Fatalf("telegram: %v", v["telegram"])
	}
	if sub(t, v, "speed_provider", "paused") != false || sub(t, v, "speed_provider", "name") != "cloudflare" {
		t.Fatalf("speed provider: %v", v["speed_provider"])
	}
}

func TestUpdateMonitoringSettingsAppliesAndReconciles(t *testing.T) {
	s := newSession(t)
	before := s.e.reconciles.Load()
	r := s.call("PUT", "/api/settings", m{"monitoring": m{
		"availability":             m{"enabled": true, "interval_minutes": 90},
		"speed":                    m{"enabled": true, "interval_minutes": 1440},
		"availability_concurrency": 8, "availability_timeout_seconds": 20, "speed_sample_mib": 16,
		"speed_direction_timeout_seconds": 45, "down_threshold": 2, "retention_days": 14,
	}})
	if r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	v := decode(t, r)
	if sub(t, v, "monitoring", "availability", "interval_minutes") != float64(90) || sub(t, v, "monitoring", "availability", "enabled") != true ||
		sub(t, v, "monitoring", "speed", "interval_minutes") != float64(1440) || sub(t, v, "monitoring", "speed_sample_mib") != float64(16) {
		t.Fatalf("%v", v["monitoring"])
	}
	got, _ := s.e.db.GetSettings(context.Background())
	if got.Availability.Interval != 90*time.Minute || got.Speed.Interval != 24*time.Hour || got.AvailabilityConcurrency != 8 ||
		got.AvailabilityTimeout != 20*time.Second || got.SpeedSampleBytes != 16<<20 || got.SpeedDirectionTimeout != 45*time.Second ||
		got.DownThreshold != 2 || got.RetentionDays != 14 || got.SpeedWarmupBytes != 256<<10 {
		t.Fatalf("%+v", got)
	}
	if s.e.reconciles.Load() != before+1 {
		t.Fatal("schedule changes were not applied immediately")
	}
	// Partial updates keep everything else.
	s.call("PUT", "/api/settings", m{"monitoring": m{"down_threshold": 5}})
	got, _ = s.e.db.GetSettings(context.Background())
	if got.DownThreshold != 5 || got.Availability.Interval != 90*time.Minute || !got.Availability.Enabled {
		t.Fatalf("%+v", got)
	}
	// Hours are an input convenience only: intervals are exchanged in minutes.
	s.call("PUT", "/api/settings", m{"monitoring": m{"availability": m{"interval_minutes": 120}}})
	got, _ = s.e.db.GetSettings(context.Background())
	if got.Availability.Interval != 2*time.Hour || !got.Availability.Enabled {
		t.Fatalf("an interval-only update must keep the enabled switch: %+v", got)
	}
}

func TestSettingsValidation(t *testing.T) {
	s := newSession(t)
	for name, tc := range map[string]struct {
		body  m
		field string
	}{
		"interval zero":      {m{"monitoring": m{"availability": m{"interval_minutes": 0}}}, "availability_interval"},
		"interval too long":  {m{"monitoring": m{"speed": m{"interval_minutes": 10081}}}, "speed_interval"},
		"concurrency 0":      {m{"monitoring": m{"availability_concurrency": 0}}, "availability_concurrency"},
		"concurrency 21":     {m{"monitoring": m{"availability_concurrency": 21}}, "availability_concurrency"},
		"timeout 0":          {m{"monitoring": m{"availability_timeout_seconds": 0}}, "availability_timeout"},
		"timeout 61":         {m{"monitoring": m{"availability_timeout_seconds": 61}}, "availability_timeout"},
		"sample 0":           {m{"monitoring": m{"speed_sample_mib": 0}}, "speed_sample_bytes"},
		"sample 65":          {m{"monitoring": m{"speed_sample_mib": 65}}, "speed_sample_bytes"},
		"direction timeout":  {m{"monitoring": m{"speed_direction_timeout_seconds": 121}}, "speed_direction_timeout"},
		"threshold 0":        {m{"monitoring": m{"down_threshold": 0}}, "down_threshold"},
		"threshold 11":       {m{"monitoring": m{"down_threshold": 11}}, "down_threshold"},
		"retention 0":        {m{"monitoring": m{"retention_days": 0}}, "retention_days"},
		"retention 366":      {m{"monitoring": m{"retention_days": 366}}, "retention_days"},
		"null number":        {m{"monitoring": m{"retention_days": nil}}, "retention_days"},
		"null switch":        {m{"monitoring": m{"availability": m{"enabled": nil}}}, "availability_enabled"},
		"bad token":          {m{"telegram": m{"bot_token": "not a token"}}, "bot_token"},
		"token path inject":  {m{"telegram": m{"bot_token": "123:abc/../x"}}, "bot_token"},
		"null token":         {m{"telegram": m{"bot_token": nil}}, "bot_token"},
		"bad chat":           {m{"telegram": m{"chat_id": "12/34"}}, "chat_id"},
		"null chat":          {m{"telegram": m{"chat_id": nil}}, "chat_id"},
		"null geo source":    {m{"geo": m{"source": nil}}, "geo_source"},
		"unknown geo source": {m{"geo": m{"source": "evil"}}, "geo_source"},
		"empty geo source":   {m{"geo": m{"source": ""}}, "geo_source"},
		"long geo source":    {m{"geo": m{"source": strings.Repeat("k", 300)}}, "geo_source"},
	} {
		r := s.call("PUT", "/api/settings", tc.body)
		if r.status != 422 {
			t.Errorf("%s: %d %s", name, r.status, r.body)
			continue
		}
		fieldError(t, r, 422, "validation_failed", tc.field)
	}
	before, _ := s.e.db.GetSettings(context.Background())
	if before != domain.DefaultSettings() {
		t.Fatal("a rejected update changed the settings")
	}
	if r := s.call("PUT", "/api/settings", `{"monitoring":{"bogus":1}}`); r.status != 400 {
		t.Errorf("unknown field: %d", r.status)
	}
	// Nothing is applied when any field is invalid.
	r := s.call("PUT", "/api/settings", m{"monitoring": m{"down_threshold": 4}, "telegram": m{"chat_id": "bad/chat"}})
	if r.status != 422 {
		t.Fatalf("%d", r.status)
	}
	if got, _ := s.e.db.GetSettings(context.Background()); got.DownThreshold != 3 {
		t.Fatal("a partially valid update was partially applied")
	}
}

func TestSecretsAreWriteOnlyAndMasked(t *testing.T) {
	s := newSession(t)
	r := s.call("PUT", "/api/settings", m{
		"telegram": m{"bot_token": apiToken, "chat_id": "-1001234"},
	})
	if r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	v := decode(t, r)
	if sub(t, v, "telegram", "has_token") != true || sub(t, v, "telegram", "configured") != true || sub(t, v, "telegram", "chat_id") != "-1001234" {
		t.Fatalf("%v", v)
	}
	got := getSettings(t, s)
	if sub(t, got, "telegram", "has_token") != true {
		t.Fatalf("%v", got)
	}
	cfg, _ := s.e.db.GetTelegram(context.Background())
	if cfg.Token != apiToken {
		t.Fatal("secrets were not stored")
	}

	// Omitted keeps, a new value replaces, an empty string clears.
	s.call("PUT", "/api/settings", m{"telegram": m{"chat_id": "@new_channel"}})
	if cfg, _ := s.e.db.GetTelegram(context.Background()); cfg.Token != apiToken || cfg.ChatID != "@new_channel" {
		t.Fatalf("omitting the token must keep it: %+v", cfg)
	}
	r = s.call("PUT", "/api/settings", m{"telegram": m{"bot_token": ""}})
	v = decode(t, r)
	if sub(t, v, "telegram", "has_token") != false || sub(t, v, "telegram", "configured") != false {
		t.Fatalf("%v", v)
	}
	for _, b := range s.e.bodies {
		for _, secret := range []string{apiToken, "AAH_api"} {
			if strings.Contains(b, secret) {
				t.Fatalf("a response contains a secret: %s", b)
			}
		}
	}
	if strings.Contains(s.e.logs.String(), apiToken) {
		t.Fatal("logs contain a secret")
	}
}

func TestTelegramTestMessageIsQueuedNotClaimedDelivered(t *testing.T) {
	s := newSession(t)
	r := s.call("POST", "/api/settings/telegram/test", nil)
	fieldError(t, r, 422, "telegram_not_configured", "")
	if n, _ := s.e.db.OutboxSummary(context.Background()); n.Pending != 0 {
		t.Fatalf("%+v", n)
	}
	s.call("PUT", "/api/settings", m{"telegram": m{"bot_token": apiToken, "chat_id": "-1001234"}})
	r = s.call("POST", "/api/settings/telegram/test", nil)
	if r.status != 202 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	out := decode(t, r)
	if out["message_id"] == nil || out["message_id"].(float64) <= 0 || out["status"] != "queued" {
		t.Fatalf("%v", out)
	}
	if strings.Contains(r.body, "delivered") {
		t.Fatalf("the response claims delivery: %s", r.body)
	}
	v := getSettings(t, s)
	if sub(t, v, "telegram", "delivery", "pending") != float64(1) || sub(t, v, "telegram", "delivery", "last_delivered_at") != nil {
		t.Fatalf("%v", v["telegram"])
	}
	// Delivery state is observable afterwards.
	items, _ := s.e.db.DueOutbox(context.Background(), time.Now().Add(time.Hour), 10)
	if len(items) != 1 || int64(out["message_id"].(float64)) != items[0].ID {
		t.Fatalf("%+v", items)
	}
	_ = s.e.db.MarkOutboxDelivered(context.Background(), []int64{items[0].ID}, time.Now())
	v = getSettings(t, s)
	if sub(t, v, "telegram", "delivery", "pending") != float64(0) || sub(t, v, "telegram", "delivery", "last_delivered_at") == nil {
		t.Fatalf("%v", v["telegram"])
	}
}

func TestSpeedProviderPauseAndResume(t *testing.T) {
	s := newSession(t)
	ctx := context.Background()
	until := time.Now().Add(time.Hour)
	if err := s.e.db.SetProviderPause(ctx, "cloudflare", "rate_limited", until); err != nil {
		t.Fatal(err)
	}
	v := getSettings(t, s)
	if sub(t, v, "speed_provider", "paused") != true || sub(t, v, "speed_provider", "reason") != "rate_limited" || sub(t, v, "speed_provider", "until") == nil {
		t.Fatalf("%v", v["speed_provider"])
	}
	if err := s.e.db.SetProviderPause(ctx, "cloudflare", "forbidden", time.Time{}); err != nil {
		t.Fatal(err)
	}
	v = getSettings(t, s)
	if sub(t, v, "speed_provider", "reason") != "forbidden" || sub(t, v, "speed_provider", "until") != nil {
		t.Fatalf("%v", v["speed_provider"])
	}
	if r := s.call("POST", "/api/settings/speed/resume", nil); r.status != 204 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	if sub(t, getSettings(t, s), "speed_provider", "paused") != false {
		t.Fatal("the provider is still paused after an explicit resume")
	}
	if r := s.call("POST", "/api/settings/speed/resume", nil); r.status != 204 {
		t.Fatalf("resuming when not paused: %d", r.status)
	}
}

func TestGeoSourceCanBeChosenAndIsKept(t *testing.T) {
	s := newSession(t)
	for _, choice := range []string{"beta", "off", "auto", "alpha"} {
		r := s.call("PUT", "/api/settings", m{"geo": m{"source": choice}})
		if r.status != 200 || sub(t, decode(t, r), "geo", "source") != choice {
			t.Fatalf("%s: %d %s", choice, r.status, r.body)
		}
		if got, _ := s.e.db.GetSettings(context.Background()); got.GeoSource != choice {
			t.Fatalf("stored %q, want %q", got.GeoSource, choice)
		}
	}
	// Other settings are untouched by a geo-only update, and vice versa.
	s.call("PUT", "/api/settings", m{"monitoring": m{"down_threshold": 7}})
	got, _ := s.e.db.GetSettings(context.Background())
	if got.DownThreshold != 7 || got.GeoSource != "alpha" {
		t.Fatalf("%+v", got)
	}
	if r := s.call("PUT", "/api/settings", m{"geo": m{"source": "ipapi-is"}}); r.status != 422 {
		t.Fatalf("unknown source accepted: %d", r.status)
	}
	if got, _ := s.e.db.GetSettings(context.Background()); got.GeoSource != "alpha" {
		t.Fatalf("a rejected update changed the setting to %q", got.GeoSource)
	}
}

func seedEvents(t *testing.T, s *session, n int) []int64 {
	t.Helper()
	var ids []int64
	for i := 0; i < n; i++ {
		p := s.create(m{"name": fmt.Sprintf("Evt %d", i), "protocol": "http", "host": fmt.Sprintf("e%d.lan", i), "port": 1})
		id := int64(p["id"].(float64))
		ids = append(ids, id)
		at := s.e.clock.Now().Add(time.Duration(i) * time.Minute)
		if _, err := s.e.db.RecordAvailabilityOutcome(context.Background(),
			domain.Availability{ProxyID: domain.ProxyID(id), CheckedAt: at, State: domain.StateDown, Failure: &domain.Failure{Code: "x"}}, 1); err != nil {
			t.Fatal(err)
		}
	}
	return ids
}

func TestEventsListFiltersAndPaginates(t *testing.T) {
	s := newSession(t)
	ids := seedEvents(t, s, 5)
	// A recovery for the first proxy.
	_, _ = s.e.db.RecordAvailabilityOutcome(context.Background(),
		domain.Availability{ProxyID: domain.ProxyID(ids[0]), CheckedAt: s.e.clock.Now().Add(time.Hour), State: domain.StateUp}, 1)

	r := s.call("GET", "/api/events?limit=4", nil)
	if r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	page := decode(t, r)
	events := page["events"].([]any)
	if len(events) != 4 || page["next_cursor"] == nil {
		t.Fatalf("%s", r.body)
	}
	first := events[0].(m)
	if first["kind"] != "proxy_recovered" || first["proxy_name"] != "Evt 0" || first["message"] == "" || first["created_at"] == nil ||
		sub(t, first, "delivery", "status") != "pending" {
		t.Fatalf("newest first: %v", first)
	}
	r = s.call("GET", "/api/events?limit=4&cursor="+page["next_cursor"].(string), nil)
	rest := decode(t, r)
	if len(rest["events"].([]any)) != 2 || rest["next_cursor"] != nil {
		t.Fatalf("second page: %s", r.body)
	}
	seen := map[float64]bool{}
	for _, e := range append(events, rest["events"].([]any)...) {
		id := e.(m)["id"].(float64)
		if seen[id] {
			t.Fatalf("event %v listed twice", id)
		}
		seen[id] = true
	}
	if len(seen) != 6 {
		t.Fatalf("%d distinct events", len(seen))
	}

	down := decode(t, s.call("GET", "/api/events?kind=proxy_down", nil))["events"].([]any)
	if len(down) != 5 {
		t.Fatalf("kind filter: %d", len(down))
	}
	both := decode(t, s.call("GET", "/api/events?kind=proxy_down&kind=proxy_recovered", nil))["events"].([]any)
	if len(both) != 6 {
		t.Fatalf("two kinds: %d", len(both))
	}
	one := decode(t, s.call("GET", fmt.Sprintf("/api/events?proxy_id=%d", ids[0]), nil))["events"].([]any)
	if len(one) != 2 {
		t.Fatalf("proxy filter: %d", len(one))
	}
	// Deleting a proxy keeps its history, now without a name.
	s.call("DELETE", fmt.Sprintf("/api/proxies/%d", ids[1]), nil)
	after := decode(t, s.call("GET", "/api/events", nil))["events"].([]any)
	var orphan m
	for _, e := range after {
		if e.(m)["message"].(string) != "" && e.(m)["proxy_id"] == nil {
			orphan = e.(m)
		}
	}
	if orphan == nil || orphan["proxy_name"] != nil {
		t.Fatalf("history of a deleted proxy: %v", orphan)
	}
}

func TestEventsInputValidation(t *testing.T) {
	s := newSession(t)
	seedEvents(t, s, 2)
	for name, q := range map[string]string{
		"limit zero":      "limit=0",
		"limit negative":  "limit=-1",
		"limit text":      "limit=abc",
		"limit too large": "limit=201",
		"bad cursor":      "cursor=!!!",
		"forged cursor":   "cursor=" + "Zm9yZ2Vk",
		"bad proxy":       "proxy_id=abc",
		"zero proxy":      "proxy_id=0",
		"bad kind":        "kind=DROP%20TABLE",
		"empty kind":      "kind=",
	} {
		r := s.call("GET", "/api/events?"+q, nil)
		if r.status != 422 {
			t.Errorf("%s: %d %s", name, r.status, r.body)
		}
	}
	// The maximum page size is accepted.
	if r := s.call("GET", "/api/events?limit=200", nil); r.status != 200 {
		t.Errorf("limit 200: %d", r.status)
	}
	// An unknown proxy simply has no events.
	got := decode(t, s.call("GET", "/api/events?proxy_id=9999", nil))["events"].([]any)
	if len(got) != 0 {
		t.Fatalf("%v", got)
	}
}
