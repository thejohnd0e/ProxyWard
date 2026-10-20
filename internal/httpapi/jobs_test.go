package httpapi

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"proxy-checker/internal/domain"
)

type fakeCanceler struct {
	mu  sync.Mutex
	ids []int64
}

func (f *fakeCanceler) CancelRunning(id int64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ids = append(f.ids, id)
	return true
}

func (f *fakeCanceler) got() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.ids...)
}

func jobSession(t *testing.T) (*session, *fakeCanceler, []int64) {
	t.Helper()
	s := newSession(t)
	var ids []int64
	for i := 0; i < 3; i++ {
		p := s.create(m{"protocol": "http", "host": fmt.Sprintf("j%d.lan", i), "port": 1})
		ids = append(ids, int64(p["id"].(float64)))
	}
	return s, s.e.canceler, ids
}

func jobList(t *testing.T, r resp, key string) []any {
	t.Helper()
	v, _ := decode(t, r)[key].([]any)
	return v
}

func TestJobRoutesRequireAuthenticationAndCSRF(t *testing.T) {
	s, _, _ := jobSession(t)
	for _, tc := range [][2]string{{"POST", "/api/jobs"}, {"GET", "/api/jobs"}, {"POST", "/api/jobs/1/cancel"}} {
		if r := s.e.do(tc[0], tc[1], "{}", nil, ""); r.status != 401 {
			t.Errorf("%s %s: %d", tc[0], tc[1], r.status)
		}
		if tc[0] == "POST" {
			if r := s.e.do(tc[0], tc[1], "{}", nil, s.cookie); r.status != 403 {
				t.Errorf("%s %s without CSRF: %d", tc[0], tc[1], r.status)
			}
		}
	}
}

func TestEnqueueReturns202WithNewAndExistingJobs(t *testing.T) {
	s, _, ids := jobSession(t)
	r := s.call("POST", "/api/jobs", m{"kind": "availability", "proxy_ids": ids[:2]})
	if r.status != 202 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	created := jobList(t, r, "new")
	if len(created) != 2 || len(jobList(t, r, "existing")) != 0 {
		t.Fatalf("%s", r.body)
	}
	first := created[0].(m)
	if first["kind"] != "availability" || first["status"] != "pending" || first["source"] != "manual" ||
		first["proxy_id"] == nil || first["id"] == nil || first["due_at"] == nil || first["started_at"] != nil {
		t.Fatalf("job DTO: %v", first)
	}
	if _, err := time.Parse(time.RFC3339, first["due_at"].(string)); err != nil {
		t.Fatalf("due_at is not RFC 3339: %v", first["due_at"])
	}

	// The same request again returns the existing jobs and creates none.
	r = s.call("POST", "/api/jobs", m{"kind": "availability", "proxy_ids": ids[:2]})
	if r.status != 202 || len(jobList(t, r, "new")) != 0 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	existing := jobList(t, r, "existing")
	if len(existing) != 2 || existing[0].(m)["id"] != first["id"] && existing[1].(m)["id"] != first["id"] {
		t.Fatalf("existing job IDs not returned: %s", r.body)
	}

	// "all" covers every proxy: the third is new, the first two already queued.
	r = s.call("POST", "/api/jobs", m{"kind": "availability", "all": true})
	if r.status != 202 || len(jobList(t, r, "new")) != 1 || len(jobList(t, r, "existing")) != 2 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	// A different kind is a different job.
	r = s.call("POST", "/api/jobs", m{"kind": "speed", "proxy_ids": ids[:1]})
	if r.status != 202 || len(jobList(t, r, "new")) != 1 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	list := s.call("GET", "/api/jobs", nil)
	if list.status != 200 || len(jobList(t, list, "jobs")) != 4 {
		t.Fatalf("%d %s", list.status, list.body)
	}
}

func TestEnqueueValidation(t *testing.T) {
	s, _, ids := jobSession(t)
	many := make([]int64, 101)
	for i := range many {
		many[i] = int64(i + 1)
	}
	for name, tc := range map[string]struct {
		body  m
		field string
	}{
		"no kind":      {m{"proxy_ids": ids}, "kind"},
		"bad kind":     {m{"kind": "ping", "proxy_ids": ids}, "kind"},
		"no target":    {m{"kind": "availability"}, "proxy_ids"},
		"empty ids":    {m{"kind": "availability", "proxy_ids": []int64{}}, "proxy_ids"},
		"ids and all":  {m{"kind": "availability", "proxy_ids": ids, "all": true}, "proxy_ids"},
		"zero id":      {m{"kind": "availability", "proxy_ids": []int64{0}}, "proxy_ids"},
		"negative id":  {m{"kind": "availability", "proxy_ids": []int64{-4}}, "proxy_ids"},
		"too many ids": {m{"kind": "availability", "proxy_ids": many}, "proxy_ids"},
	} {
		r := s.call("POST", "/api/jobs", tc.body)
		if r.status != 422 {
			t.Errorf("%s: %d %s", name, r.status, r.body)
			continue
		}
		fieldError(t, r, 422, "validation_failed", tc.field)
	}
	if r := s.call("POST", "/api/jobs", `{"kind":"availability","proxy_ids":[1],"bogus":1}`); r.status != 400 {
		t.Errorf("unknown field: %d", r.status)
	}
	if r := s.call("POST", "/api/jobs", `{"kind":"availability","proxy_ids":["1"]}`); r.status != 400 {
		t.Errorf("string id: %d", r.status)
	}
}

func TestEnqueueMissingProxies(t *testing.T) {
	s, _, ids := jobSession(t)
	r := s.call("POST", "/api/jobs", m{"kind": "availability", "proxy_ids": []int64{9999}})
	fieldError(t, r, 404, "not_found", "")
	r = s.call("POST", "/api/jobs", m{"kind": "availability", "proxy_ids": []int64{ids[0], 9999}})
	if r.status != 202 || len(jobList(t, r, "new")) != 1 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	missing := jobList(t, r, "missing_proxy_ids")
	if len(missing) != 1 || missing[0] != float64(9999) {
		t.Fatalf("%s", r.body)
	}
	// "all" with no proxies is a valid no-op.
	for _, id := range ids {
		s.call("DELETE", fmt.Sprintf("/api/proxies/%d", id), nil)
	}
	r = s.call("POST", "/api/jobs", m{"kind": "speed", "all": true})
	if r.status != 202 || len(jobList(t, r, "new")) != 0 {
		t.Fatalf("%d %s", r.status, r.body)
	}
}

func TestCancelJobs(t *testing.T) {
	s, canceler, ids := jobSession(t)
	ctx := context.Background()
	r := s.call("POST", "/api/jobs", m{"kind": "availability", "proxy_ids": ids[:2]})
	created := jobList(t, r, "new")
	pendingID := int64(created[0].(m)["id"].(float64))

	got := s.call("POST", fmt.Sprintf("/api/jobs/%d/cancel", pendingID), nil)
	if got.status != 200 || decode(t, got)["status"] != "canceled" || len(canceler.got()) != 0 {
		t.Fatalf("pending: %d %s canceler=%v", got.status, got.body, canceler.got())
	}
	// A running job is stopped by its runner: the API only asks.
	runningJob, ok, err := s.e.db.ClaimJob(ctx, domain.CheckAvailability, time.Now().Add(time.Minute))
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	got = s.call("POST", fmt.Sprintf("/api/jobs/%d/cancel", runningJob.ID), nil)
	if got.status != 202 || decode(t, got)["status"] != "running" {
		t.Fatalf("running: %d %s", got.status, got.body)
	}
	if ids := canceler.got(); len(ids) != 1 || ids[0] != runningJob.ID {
		t.Fatalf("canceler calls: %v", ids)
	}
	// Finished jobs cannot be canceled again; unknown or malformed IDs are 404.
	_ = s.e.db.FinishJob(ctx, runningJob.ID, domain.JobDone, time.Now())
	if r := s.call("POST", fmt.Sprintf("/api/jobs/%d/cancel", runningJob.ID), nil); r.status != 409 {
		t.Errorf("finished job: %d %s", r.status, r.body)
	}
	for _, path := range []string{"/api/jobs/9999/cancel", "/api/jobs/abc/cancel", "/api/jobs/0/cancel"} {
		if r := s.call("POST", path, nil); r.status != 404 {
			t.Errorf("%s: %d", path, r.status)
		}
	}
}

func TestDeletingAProxyCancelsItsJobs(t *testing.T) {
	s, canceler, ids := jobSession(t)
	ctx := context.Background()
	s.call("POST", "/api/jobs", m{"kind": "availability", "proxy_ids": ids[:2]})
	s.call("POST", "/api/jobs", m{"kind": "speed", "proxy_ids": ids[:1]})
	running, ok, _ := s.e.db.ClaimJob(ctx, domain.CheckAvailability, time.Now().Add(time.Minute))
	if !ok {
		t.Fatal("no job to claim")
	}
	if r := s.call("DELETE", fmt.Sprintf("/api/proxies/%d", running.ProxyID), nil); r.status != 204 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	if got := canceler.got(); len(got) != 1 || got[0] != running.ID {
		t.Fatalf("the running job of the deleted proxy was not canceled: %v", got)
	}
	jobs, _ := s.e.db.ListActiveJobs(ctx)
	for _, j := range jobs {
		if j.ProxyID == running.ProxyID {
			t.Fatalf("job of the deleted proxy survived: %+v", j)
		}
	}
}
