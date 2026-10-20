package jobs

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/thejohnd0e/ProxyWard/internal/check"
	"github.com/thejohnd0e/ProxyWard/internal/domain"
	"github.com/thejohnd0e/ProxyWard/internal/notify"
)

// occupancy records, across both check kinds, how many checks run at once
// and whether any proxy was ever checked by two workers at the same time.
type occupancy struct {
	mu       sync.Mutex
	cur, max int
	perKind  map[domain.CheckKind]int
	maxKind  map[domain.CheckKind]int
	perProxy map[domain.ProxyID]int
	overlap  bool
	calls    int
}

func newOccupancy() *occupancy {
	return &occupancy{perKind: map[domain.CheckKind]int{}, maxKind: map[domain.CheckKind]int{}, perProxy: map[domain.ProxyID]int{}}
}

func (o *occupancy) enter(kind domain.CheckKind, id domain.ProxyID) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls++
	o.cur++
	o.perKind[kind]++
	o.perProxy[id]++
	if o.cur > o.max {
		o.max = o.cur
	}
	if o.perKind[kind] > o.maxKind[kind] {
		o.maxKind[kind] = o.perKind[kind]
	}
	if o.perProxy[id] > 1 {
		o.overlap = true
	}
}

func (o *occupancy) leave(kind domain.CheckKind, id domain.ProxyID) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.cur--
	o.perKind[kind]--
	o.perProxy[id]--
}

func (o *occupancy) snapshot() (running, calls int, maxKind map[domain.CheckKind]int, overlap bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	mk := map[domain.CheckKind]int{}
	for k, v := range o.maxKind {
		mk[k] = v
	}
	return o.cur, o.calls, mk, o.overlap
}

type fakeChecks struct {
	occ     *occupancy
	hold    chan struct{} // when non-nil, checks wait for it to close
	pause   time.Duration
	state   func(domain.Proxy) domain.State
	paused  bool
	pausedM sync.Mutex
}

func (f *fakeChecks) wait(ctx context.Context) {
	if f.pause > 0 {
		select {
		case <-time.After(f.pause):
		case <-ctx.Done():
		}
	}
	if f.hold != nil {
		select {
		case <-f.hold:
		case <-ctx.Done():
		}
	}
}

func (f *fakeChecks) Check(ctx context.Context, p domain.Proxy, _ *http.Client) domain.Availability {
	f.occ.enter(domain.CheckAvailability, p.ID)
	defer f.occ.leave(domain.CheckAvailability, p.ID)
	f.wait(ctx)
	a := domain.Availability{ProxyID: p.ID, CheckedAt: time.Now(), State: domain.StateUp, ExitIP: "198.51.100.7", Endpoint: "ipify"}
	if ctx.Err() != nil {
		return domain.Availability{ProxyID: p.ID, CheckedAt: time.Now(), State: domain.StateIndeterminate, Failure: check.NewFailure(check.CodeCanceled)}
	}
	if f.state != nil {
		a.State = f.state(p)
		if a.State == domain.StateDown {
			a.ExitIP, a.Failure = "", check.NewFailure(check.CodeAuthFailed)
		}
	}
	return a
}

func (f *fakeChecks) Measure(ctx context.Context, p domain.Proxy, _ *http.Client, cfg check.SpeedConfig) domain.Speed {
	f.occ.enter(domain.CheckSpeed, p.ID)
	defer f.occ.leave(domain.CheckSpeed, p.ID)
	f.wait(ctx)
	mbps := 42.0
	return domain.Speed{ProxyID: p.ID, CheckedAt: time.Now(), Provider: "fake", Method: cfg.Method(),
		Download: domain.Transfer{Bytes: cfg.SampleBytes, Duration: time.Second, Mbps: &mbps},
		Upload:   domain.Transfer{Bytes: cfg.SampleBytes, Duration: time.Second, Mbps: &mbps}, TotalBytes: 2 * cfg.SampleBytes}
}

func (f *fakeChecks) Gate(context.Context) (check.Gate, error) {
	f.pausedM.Lock()
	defer f.pausedM.Unlock()
	if f.paused {
		return check.Gate{Paused: true, Reason: "rate_limited"}, nil
	}
	return check.Gate{}, nil
}

func (f *fakeChecks) setPaused(v bool) {
	f.pausedM.Lock()
	f.paused = v
	f.pausedM.Unlock()
}

type rig struct {
	*env
	svc    *Service
	checks *fakeChecks
	occ    *occupancy
	stop   context.CancelFunc
	done   chan struct{}
	hooked chan domain.Availability
}

// newRig starts a Service with fake checkers on a real clock.
func newRig(t *testing.T, proxies int, mod func(*domain.Settings), checks *fakeChecks) *rig {
	t.Helper()
	e := newEnv(t, proxies)
	occ := newOccupancy()
	if checks == nil {
		checks = &fakeChecks{}
	}
	checks.occ = occ
	e.settings(func(s *domain.Settings) {
		if mod != nil {
			mod(s)
		}
	})
	svc := NewService(Config{
		Store: e.db, Clock: domain.SystemClock{},
		Availability: checks, Speed: checks,
		Incidents: &notify.IncidentTracker{Store: e.db},
		NewClient: func(domain.Proxy, time.Duration) (*http.Client, error) { return http.DefaultClient, nil },
		Poll:      5 * time.Millisecond, TickEvery: 10 * time.Millisecond,
	})
	r := &rig{env: e, svc: svc, checks: checks, occ: occ, hooked: make(chan domain.Availability, 256)}
	svc.Runner.OnAvailability = func(a domain.Availability) { r.hooked <- a }
	return r
}

func (r *rig) start() {
	r.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r.stop = cancel
	r.done = make(chan struct{})
	go func() {
		defer close(r.done)
		_ = r.svc.Run(ctx)
	}()
	r.t.Cleanup(r.shutdown)
}

func (r *rig) shutdown() {
	if r.stop == nil {
		return
	}
	r.stop()
	select {
	case <-r.done:
	case <-time.After(5 * time.Second):
		r.t.Error("service did not stop")
	}
	r.stop = nil
}

func (r *rig) enqueue(kind domain.CheckKind, ids ...domain.ProxyID) {
	r.t.Helper()
	if _, err := r.svc.Queue.Enqueue(context.Background(), ids, kind, domain.SourceManual); err != nil {
		r.t.Fatal(err)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func (r *rig) statuses() map[domain.JobStatus]int {
	r.t.Helper()
	rows, err := r.db.ListActiveJobs(context.Background())
	if err != nil {
		r.t.Fatal(err)
	}
	out := map[domain.JobStatus]int{}
	for _, j := range rows {
		out[j.Status]++
	}
	return out
}

func (r *rig) idle() bool {
	s := r.statuses()
	return s[domain.JobPending] == 0 && s[domain.JobRunning] == 0
}

func (r *rig) availCount(id domain.ProxyID) int {
	list, _ := r.db.ListAvailability(context.Background(), id, time.Time{}, time.Time{}, 100)
	return len(list)
}

func TestAvailabilityConcurrencyIsBounded(t *testing.T) {
	hold := make(chan struct{})
	r := newRig(t, 12, func(s *domain.Settings) { s.AvailabilityConcurrency = 3 }, &fakeChecks{hold: hold})
	r.start()
	r.enqueue(domain.CheckAvailability, r.ids...)
	waitFor(t, "three concurrent checks", func() bool { c, _, _, _ := r.occ.snapshot(); return c == 3 })
	time.Sleep(100 * time.Millisecond) // give a wrongly unbounded runner the chance to exceed 3
	if c, _, mk, _ := r.occ.snapshot(); c != 3 || mk[domain.CheckAvailability] != 3 {
		t.Fatalf("running=%d max=%d, want 3", c, mk[domain.CheckAvailability])
	}
	close(hold)
	waitFor(t, "all jobs done", r.idle)
	if _, calls, mk, _ := r.occ.snapshot(); calls != 12 || mk[domain.CheckAvailability] != 3 {
		t.Fatalf("calls=%d max=%d", calls, mk[domain.CheckAvailability])
	}
}

func TestDefaultAvailabilityConcurrencyIsFive(t *testing.T) {
	hold := make(chan struct{})
	r := newRig(t, 9, nil, &fakeChecks{hold: hold})
	r.start()
	r.enqueue(domain.CheckAvailability, r.ids...)
	waitFor(t, "five concurrent checks", func() bool { c, _, _, _ := r.occ.snapshot(); return c == 5 })
	time.Sleep(60 * time.Millisecond)
	if c, _, _, _ := r.occ.snapshot(); c != 5 {
		t.Fatalf("running=%d", c)
	}
	close(hold)
	waitFor(t, "done", r.idle)
}

func TestExactlyOneSpeedWorker(t *testing.T) {
	hold := make(chan struct{})
	r := newRig(t, 5, nil, &fakeChecks{hold: hold})
	r.start()
	r.enqueue(domain.CheckSpeed, r.ids...)
	waitFor(t, "a speed check", func() bool { c, _, _, _ := r.occ.snapshot(); return c == 1 })
	time.Sleep(100 * time.Millisecond)
	if c, _, mk, _ := r.occ.snapshot(); c != 1 || mk[domain.CheckSpeed] != 1 {
		t.Fatalf("running=%d max=%d: speed checks must be serialized", c, mk[domain.CheckSpeed])
	}
	close(hold)
	waitFor(t, "all speed jobs done", r.idle)
	if _, calls, _, _ := r.occ.snapshot(); calls != 5 {
		t.Fatalf("calls=%d", calls)
	}
	for _, id := range r.ids {
		if _, err := r.db.LatestSpeed(context.Background(), id); err != nil {
			t.Errorf("no speed result for proxy %d: %v", id, err)
		}
	}
}

func TestAProxyIsNeverCheckedTwiceAtOnce(t *testing.T) {
	r := newRig(t, 6, nil, &fakeChecks{pause: 15 * time.Millisecond})
	r.start()
	for i := 0; i < 4; i++ {
		r.enqueue(domain.CheckAvailability, r.ids...)
		r.enqueue(domain.CheckSpeed, r.ids...)
		waitFor(t, "round done", r.idle)
	}
	if _, calls, _, overlap := r.occ.snapshot(); overlap || calls != 48 {
		t.Fatalf("overlap=%v calls=%d", overlap, calls)
	}
}

func TestResultsIncidentsAndNextRunAreRecorded(t *testing.T) {
	r := newRig(t, 1, func(s *domain.Settings) { s.Availability.Interval = time.Hour }, &fakeChecks{state: func(domain.Proxy) domain.State { return domain.StateDown }})
	r.start()
	id := r.ids[0]
	for i := 1; i <= 3; i++ {
		r.enqueue(domain.CheckAvailability, id)
		waitFor(t, "check done", func() bool { return r.availCount(id) == i && r.idle() })
		if i < 3 {
			if n := r.count(`SELECT COUNT(*) FROM events`); n != 0 {
				t.Fatalf("event after %d failures", i)
			}
		}
	}
	if n := r.count(`SELECT COUNT(*) FROM events WHERE kind = 'proxy_down'`); n != 1 {
		t.Fatalf("down events = %d", n)
	}
	latest, _ := r.db.LatestAvailability(context.Background(), id)
	if latest.State != domain.StateDown || latest.Failure == nil || latest.Failure.Code != "auth_failed" {
		t.Fatalf("%+v", latest)
	}
	if len(r.hooked) != 3 {
		t.Fatalf("hook calls = %d", len(r.hooked))
	}
	// The schedule is disabled, so manual runs leave no next run behind.
	if n := r.next(id, domain.CheckAvailability); n != nil {
		t.Fatalf("%v", n)
	}
}

func TestManualRunAdvancesAnEnabledSchedule(t *testing.T) {
	r := newRig(t, 1, func(s *domain.Settings) { s.Availability = domain.Schedule{Enabled: true, Interval: 6 * time.Hour} }, nil)
	r.svc.Scheduler.Jitter = func() time.Duration { return time.Hour } // keep the scheduler's own job far away
	r.start()
	id := r.ids[0]
	waitFor(t, "scheduled job queued", func() bool { return r.statuses()[domain.JobPending] == 1 })
	r.enqueue(domain.CheckAvailability, id) // promotes the scheduled job to run now
	waitFor(t, "run", func() bool { return r.availCount(id) == 1 && r.idle() })
	next := r.next(id, domain.CheckAvailability)
	if next == nil || next.Before(time.Now().Add(5*time.Hour+50*time.Minute)) {
		t.Fatalf("next = %v", next)
	}
}

func TestCancelRunningJobStopsItWithoutAResult(t *testing.T) {
	r := newRig(t, 1, nil, &fakeChecks{hold: make(chan struct{})})
	r.start()
	id := r.ids[0]
	r.enqueue(domain.CheckAvailability, id)
	waitFor(t, "check running", func() bool { c, _, _, _ := r.occ.snapshot(); return c == 1 })
	jobs, _ := r.db.ListActiveJobs(context.Background())
	if len(jobs) != 1 || jobs[0].Status != domain.JobRunning {
		t.Fatalf("%+v", jobs)
	}
	got, err := r.svc.Queue.Cancel(context.Background(), jobs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != jobs[0].ID {
		t.Fatalf("%+v", got)
	}
	waitFor(t, "job canceled", func() bool {
		j, _ := r.db.GetJob(context.Background(), jobs[0].ID)
		return j.Status == domain.JobCanceled
	})
	if r.availCount(id) != 0 || r.count(`SELECT COUNT(*) FROM proxy_state`) != 0 {
		t.Fatal("a canceled check left a result or failure state behind")
	}
	// The proxy can be checked again afterwards.
	close(r.checks.hold)
	r.enqueue(domain.CheckAvailability, id)
	waitFor(t, "second check", func() bool { return r.availCount(id) == 1 && r.idle() })
}

func TestCancelPendingJob(t *testing.T) {
	hold := make(chan struct{})
	r := newRig(t, 2, func(s *domain.Settings) { s.AvailabilityConcurrency = 1 }, &fakeChecks{hold: hold})
	r.start()
	r.enqueue(domain.CheckAvailability, r.ids...)
	waitFor(t, "one running", func() bool { return r.statuses()[domain.JobRunning] == 1 })
	var pending domain.Job
	jobs, _ := r.db.ListActiveJobs(context.Background())
	for _, j := range jobs {
		if j.Status == domain.JobPending {
			pending = j
		}
	}
	got, err := r.svc.Queue.Cancel(context.Background(), pending.ID)
	if err != nil || got.Status != domain.JobCanceled {
		t.Fatalf("%+v %v", got, err)
	}
	close(hold)
	waitFor(t, "idle", r.idle)
	if _, calls, _, _ := r.occ.snapshot(); calls != 1 {
		t.Fatalf("calls = %d, want 1: the canceled job must not run", calls)
	}
	if _, err := r.svc.Queue.Cancel(context.Background(), 9999); err == nil {
		t.Fatal("cancelling an unknown job succeeded")
	}
}

func TestShutdownInterruptsRunningJobsWithoutFailures(t *testing.T) {
	r := newRig(t, 2, nil, &fakeChecks{hold: make(chan struct{}), state: func(domain.Proxy) domain.State { return domain.StateDown }})
	r.start()
	r.enqueue(domain.CheckAvailability, r.ids...)
	waitFor(t, "both running", func() bool { c, _, _, _ := r.occ.snapshot(); return c == 2 })
	jobs, _ := r.db.ListActiveJobs(context.Background())
	r.shutdown()
	for _, j := range jobs {
		got, err := r.db.GetJob(context.Background(), j.ID)
		if err != nil || got.Status != domain.JobInterrupted || got.FinishedAt == nil {
			t.Fatalf("%+v %v", got, err)
		}
	}
	for _, id := range r.ids {
		if r.availCount(id) != 0 {
			t.Fatal("shutdown recorded a result")
		}
	}
	if r.count(`SELECT COUNT(*) FROM proxy_state`) != 0 || r.count(`SELECT COUNT(*) FROM events`) != 0 {
		t.Fatal("shutdown counted as a proxy failure")
	}
}

func TestStartupInterruptsJobsLeftRunningByACrash(t *testing.T) {
	r := newRig(t, 2, nil, nil)
	ctx := context.Background()
	r.enqueue(domain.CheckAvailability, r.ids[0])
	crashed, ok, _ := r.db.ClaimJob(ctx, domain.CheckAvailability, time.Now()) // "the process died while running it"
	if !ok {
		t.Fatal("no job")
	}
	r.enqueue(domain.CheckAvailability, r.ids[1])
	r.start()
	waitFor(t, "pending job run", func() bool { return r.availCount(r.ids[1]) == 1 })
	got, _ := r.db.GetJob(ctx, crashed.ID)
	if got.Status != domain.JobInterrupted {
		t.Fatalf("%+v", got)
	}
	if r.availCount(r.ids[0]) != 0 || r.count(`SELECT COUNT(*) FROM proxy_state WHERE proxy_id = ?`, int64(r.ids[0])) != 0 {
		t.Fatal("an interrupted job was recorded as a result or failure")
	}
	// The interrupted proxy can be queued again.
	r.enqueue(domain.CheckAvailability, r.ids[0])
	waitFor(t, "rerun", func() bool { return r.availCount(r.ids[0]) == 1 && r.idle() })
}

func TestPausedSpeedProviderKeepsJobsPending(t *testing.T) {
	checks := &fakeChecks{paused: true}
	r := newRig(t, 2, nil, checks)
	r.start()
	r.enqueue(domain.CheckSpeed, r.ids...)
	time.Sleep(150 * time.Millisecond)
	if _, calls, _, _ := r.occ.snapshot(); calls != 0 {
		t.Fatalf("speed checks ran during the provider pause: %d", calls)
	}
	if r.statuses()[domain.JobPending] != 2 {
		t.Fatalf("%v", r.statuses())
	}
	// Availability work is unaffected by the speed provider's pause.
	r.enqueue(domain.CheckAvailability, r.ids[0])
	waitFor(t, "availability still runs", func() bool { return r.availCount(r.ids[0]) == 1 })
	checks.setPaused(false)
	waitFor(t, "speed resumed", r.idle)
	if _, err := r.db.LatestSpeed(context.Background(), r.ids[0]); err != nil {
		t.Fatal(err)
	}
}

func TestDeletedProxyJobsAreCanceled(t *testing.T) {
	hold := make(chan struct{})
	r := newRig(t, 3, func(s *domain.Settings) { s.AvailabilityConcurrency = 1 }, &fakeChecks{hold: hold})
	r.start()
	ctx := context.Background()
	r.enqueue(domain.CheckAvailability, r.ids...)
	waitFor(t, "one running", func() bool { return r.statuses()[domain.JobRunning] == 1 })
	running, _ := r.db.ListActiveJobs(ctx)
	var runningProxy domain.ProxyID
	for _, j := range running {
		if j.Status == domain.JobRunning {
			runningProxy = j.ProxyID
		}
	}
	// Delete the running proxy and one that is only queued.
	var queued domain.ProxyID
	for _, id := range r.ids {
		if id != runningProxy {
			queued = id
			break
		}
	}
	for _, id := range []domain.ProxyID{runningProxy, queued} {
		if err := r.svc.Queue.CancelProxy(ctx, id); err != nil {
			t.Fatal(err)
		}
		if err := r.db.DeleteProxy(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	close(hold)
	waitFor(t, "idle", r.idle)
	for _, id := range []domain.ProxyID{runningProxy, queued} {
		if r.availCount(id) != 0 {
			t.Fatalf("result stored for deleted proxy %d", id)
		}
	}
	// The worker may have started the second proxy before it was deleted, but
	// nothing may be stored for it (checked above), and at most the three
	// original jobs ever ran.
	if _, calls, _, _ := r.occ.snapshot(); calls > 3 {
		t.Fatalf("calls = %d", calls)
	}
	// The runner is healthy afterwards.
	var left domain.ProxyID
	for _, id := range r.ids {
		if id != runningProxy && id != queued {
			left = id
		}
	}
	r.enqueue(domain.CheckAvailability, left)
	waitFor(t, "survivor checked", func() bool { return r.availCount(left) >= 1 })
}

func TestQueueReportsExistingJobs(t *testing.T) {
	r := newRig(t, 2, nil, nil)
	ctx := context.Background()
	first, err := r.svc.Queue.Enqueue(ctx, r.ids, domain.CheckAvailability, domain.SourceManual)
	if err != nil || len(first.New) != 2 {
		t.Fatalf("%+v %v", first, err)
	}
	again, err := r.svc.Queue.Enqueue(ctx, r.ids[:1], domain.CheckAvailability, domain.SourceManual)
	if err != nil || len(again.New) != 0 || len(again.Existing) != 1 || again.Existing[0].ID != first.New[0].ID {
		t.Fatalf("%+v %v", again, err)
	}
	all, err := r.svc.Queue.EnqueueAll(ctx, domain.CheckSpeed, domain.SourceManual)
	if err != nil || len(all.New) != 2 {
		t.Fatalf("%+v %v", all, err)
	}
	if _, err := r.svc.Queue.Enqueue(ctx, r.ids, "bogus", domain.SourceManual); err == nil {
		t.Fatal("unknown kind accepted")
	}
}

func TestServiceRunsSchedulesOnItsOwn(t *testing.T) {
	r := newRig(t, 3, func(s *domain.Settings) { s.Availability = domain.Schedule{Enabled: true, Interval: time.Hour} }, nil)
	r.svc.Scheduler.Jitter = func() time.Duration { return 0 }
	r.start()
	waitFor(t, "all proxies checked by the schedule", func() bool {
		for _, id := range r.ids {
			// The next run is recorded just after the result, so wait for both.
			if r.availCount(id) < 1 || r.next(id, domain.CheckAvailability) == nil {
				return false
			}
		}
		return true
	})
	for _, id := range r.ids {
		next := r.next(id, domain.CheckAvailability)
		if next == nil || next.Before(time.Now().Add(59*time.Minute)) {
			t.Fatalf("proxy %d next = %v", id, next)
		}
	}
	time.Sleep(60 * time.Millisecond) // many ticks later nothing re-ran early
	for _, id := range r.ids {
		if n := r.availCount(id); n != 1 {
			t.Fatalf("proxy %d checked %d times within its interval", id, n)
		}
	}
}
