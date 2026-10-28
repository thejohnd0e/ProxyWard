package jobs

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/thejohnd0e/ProxyWard/internal/domain"
	"github.com/thejohnd0e/ProxyWard/internal/store"
	"github.com/thejohnd0e/ProxyWard/internal/testkit"
)

var epoch = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

func TestPausePreservesRunningAndManualJobs(t *testing.T) {
	e := newEnv(t, 2)
	ctx := context.Background()
	e.settings(func(s *domain.Settings) { s.Availability.Enabled = true; s.Speed.Enabled = true })
	e.tick()
	e.clock.Advance(10 * time.Second)
	running, ok, err := e.db.ClaimJob(ctx, domain.CheckAvailability, e.clock.Now())
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	manual, err := e.db.EnqueueJobs(ctx, []domain.ProxyID{running.ProxyID}, domain.CheckSpeed, domain.SourceManual, e.clock.Now(), e.clock.Now())
	if err != nil || len(manual.Existing) != 1 {
		t.Fatalf("manual promotion: %+v %v", manual, err)
	}
	if err := e.db.SetMonitoringPaused(ctx, running.ProxyID, true, e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	e.tick()
	got, err := e.db.GetJob(ctx, running.ID)
	if err != nil || got.Status != domain.JobRunning {
		t.Fatalf("running job changed: %+v %v", got, err)
	}
	got, err = e.db.GetJob(ctx, manual.Existing[0].ID)
	if err != nil || got.Status != domain.JobPending || got.Source != domain.SourceManual {
		t.Fatalf("manual job changed: %+v %v", got, err)
	}
	if len(e.active(domain.CheckAvailability)) != 2 || len(e.active(domain.CheckSpeed)) != 2 {
		t.Fatal("pause changed other proxy jobs")
	}
}

type env struct {
	t     *testing.T
	db    *store.Store
	path  string
	clock *testkit.Clock
	sched *Scheduler
	ids   []domain.ProxyID
}

func newEnv(t *testing.T, proxies int) *env {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, db: db, path: path, clock: testkit.NewClock(epoch)}
	for i := 0; i < proxies; i++ {
		id, err := db.InsertProxy(ctx, domain.Proxy{Name: "p", Protocol: domain.ProtocolHTTP, Host: "h" + string(rune('a'+i)) + ".lan", Port: 1})
		if err != nil {
			t.Fatal(err)
		}
		e.ids = append(e.ids, id)
	}
	e.sched = &Scheduler{Store: db, Clock: e.clock, Jitter: func() time.Duration { return 10 * time.Second }}
	return e
}

func (e *env) settings(mod func(*domain.Settings)) {
	e.t.Helper()
	s := domain.DefaultSettings()
	mod(&s)
	if err := e.db.PutSettings(context.Background(), s, e.clock.Now()); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) tick() {
	e.t.Helper()
	if err := e.sched.Tick(context.Background(), e.clock.Now()); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) active(kind domain.CheckKind) map[domain.ProxyID]domain.Job {
	e.t.Helper()
	jobs, err := e.db.ListActiveJobs(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	out := map[domain.ProxyID]domain.Job{}
	for _, j := range jobs {
		if j.Kind == kind {
			out[j.ProxyID] = j
		}
	}
	return out
}

// run claims and completes every due job of kind at the current time, like a
// worker would, and lets the scheduler record the next run.
func (e *env) run(kind domain.CheckKind) int {
	e.t.Helper()
	ctx := context.Background()
	n := 0
	for {
		j, ok, err := e.db.ClaimJob(ctx, kind, e.clock.Now())
		if err != nil {
			e.t.Fatal(err)
		}
		if !ok {
			return n
		}
		if err := e.db.FinishJob(ctx, j.ID, domain.JobDone, e.clock.Now()); err != nil {
			e.t.Fatal(err)
		}
		if err := e.sched.Completed(ctx, j.ProxyID, kind, e.clock.Now()); err != nil {
			e.t.Fatal(err)
		}
		n++
	}
}

func (e *env) next(id domain.ProxyID, kind domain.CheckKind) *time.Time {
	e.t.Helper()
	rows, err := e.db.ListSchedules(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	for _, r := range rows {
		if r.ProxyID == id {
			if kind == domain.CheckAvailability {
				return r.NextAvailability
			}
			return r.NextSpeed
		}
	}
	e.t.Fatalf("no proxy %d", id)
	return nil
}

func TestDisabledSchedulesCreateNothing(t *testing.T) {
	e := newEnv(t, 3)
	e.tick()
	if jobs, _ := e.db.ListActiveJobs(context.Background()); len(jobs) != 0 {
		t.Fatalf("both schedules start disabled, got %d jobs", len(jobs))
	}
}

func TestEnabledScheduleEnqueuesOncePerProxyWithJitter(t *testing.T) {
	e := newEnv(t, 3)
	e.settings(func(s *domain.Settings) { s.Availability.Enabled = true })
	e.tick()
	got := e.active(domain.CheckAvailability)
	if len(got) != 3 {
		t.Fatalf("%d jobs", len(got))
	}
	for id, j := range got {
		if j.Source != domain.SourceScheduled || !j.DueAt.Equal(epoch.Add(10*time.Second)) || j.Status != domain.JobPending {
			t.Errorf("proxy %d: %+v", id, j)
		}
	}
	if len(e.active(domain.CheckSpeed)) != 0 {
		t.Fatal("the speed schedule is independent and still disabled")
	}
	for i := 0; i < 5; i++ { // repeated ticks neither duplicate nor move jobs
		e.clock.Advance(time.Second)
		e.tick()
	}
	if after := e.active(domain.CheckAvailability); len(after) != 3 {
		t.Fatalf("repeated ticks created duplicates: %d", len(after))
	}
}

func TestNextRunIsMeasuredFromCompletion(t *testing.T) {
	e := newEnv(t, 1)
	id := e.ids[0]
	e.settings(func(s *domain.Settings) { s.Availability = domain.Schedule{Enabled: true, Interval: 90 * time.Minute} })
	e.tick()
	e.clock.Advance(10 * time.Second)
	e.clock.Advance(40 * time.Second) // the check itself takes a while
	if n := e.run(domain.CheckAvailability); n != 1 {
		t.Fatalf("ran %d", n)
	}
	done := e.clock.Now()
	next := e.next(id, domain.CheckAvailability)
	if next == nil || !next.Equal(done.Add(90*time.Minute)) {
		t.Fatalf("next = %v, want %v (completion + 90 min)", next, done.Add(90*time.Minute))
	}
	e.clock.Advance(89 * time.Minute)
	e.tick()
	if len(e.active(domain.CheckAvailability)) != 0 {
		t.Fatal("job enqueued before the interval elapsed")
	}
	e.clock.Advance(time.Minute)
	e.tick()
	j := e.active(domain.CheckAvailability)[id]
	if j.ID == 0 || !j.DueAt.Equal(*next) {
		t.Fatalf("on-time job: %+v, want due %v", j, *next)
	}
}

func TestEffectiveScheduleOverrides(t *testing.T) {
	e := newEnv(t, 3)
	ctx := context.Background()
	off := &domain.Schedule{Enabled: false, Interval: time.Hour}
	on := &domain.Schedule{Enabled: true, Interval: 2 * time.Hour}

	// Global enabled: an explicit disabled override wins.
	e.settings(func(s *domain.Settings) { s.Availability.Enabled = true })
	p, _ := e.db.GetProxy(ctx, e.ids[0])
	p.AvailabilitySchedule = off
	if err := e.db.UpdateProxy(ctx, e.ids[0], p, store.CredentialUpdate{}); err != nil {
		t.Fatal(err)
	}
	e.tick()
	got := e.active(domain.CheckAvailability)
	if _, ok := got[e.ids[0]]; ok || len(got) != 2 {
		t.Fatalf("disabled override ignored: %v", got)
	}

	// Global disabled: an enabled override still runs and uses its own interval.
	e.settings(func(s *domain.Settings) {})
	e.tick()
	if len(e.active(domain.CheckAvailability)) != 0 {
		t.Fatal("jobs survived the global disable")
	}
	p, _ = e.db.GetProxy(ctx, e.ids[1])
	p.AvailabilitySchedule = on
	_ = e.db.UpdateProxy(ctx, e.ids[1], p, store.CredentialUpdate{})
	e.tick()
	got = e.active(domain.CheckAvailability)
	if len(got) != 1 || got[e.ids[1]].ID == 0 {
		t.Fatalf("enabled override not honoured: %v", got)
	}
	e.clock.Advance(10 * time.Second)
	e.run(domain.CheckAvailability)
	if next := e.next(e.ids[1], domain.CheckAvailability); next == nil || !next.Equal(e.clock.Now().Add(2*time.Hour)) {
		t.Fatalf("override interval not used: %v", next)
	}
	// Proxies without a schedule never get a next run.
	if next := e.next(e.ids[2], domain.CheckAvailability); next != nil {
		t.Fatalf("%v", next)
	}
}

func TestKindsAreIndependent(t *testing.T) {
	e := newEnv(t, 2)
	e.settings(func(s *domain.Settings) {
		s.Speed = domain.Schedule{Enabled: true, Interval: 6 * time.Hour}
	})
	e.tick()
	if len(e.active(domain.CheckSpeed)) != 2 || len(e.active(domain.CheckAvailability)) != 0 {
		t.Fatalf("speed=%d availability=%d", len(e.active(domain.CheckSpeed)), len(e.active(domain.CheckAvailability)))
	}
	e.clock.Advance(10 * time.Second)
	e.run(domain.CheckSpeed)
	if next := e.next(e.ids[0], domain.CheckSpeed); next == nil || !next.Equal(e.clock.Now().Add(6*time.Hour)) {
		t.Fatalf("%v", next)
	}
	if next := e.next(e.ids[0], domain.CheckAvailability); next != nil {
		t.Fatalf("a speed run advanced the availability schedule: %v", next)
	}
}

func TestManualRunAdvancesTheNextDueTime(t *testing.T) {
	e := newEnv(t, 1)
	id := e.ids[0]
	e.settings(func(s *domain.Settings) { s.Availability.Enabled = true })
	e.tick()
	e.clock.Advance(10 * time.Second)
	e.run(domain.CheckAvailability)
	first := *e.next(id, domain.CheckAvailability)

	// A manual check two minutes later pushes the scheduled one back.
	e.clock.Advance(2 * time.Minute)
	if _, err := e.db.EnqueueJobs(context.Background(), []domain.ProxyID{id}, domain.CheckAvailability, domain.SourceManual, e.clock.Now(), e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	e.run(domain.CheckAvailability)
	second := *e.next(id, domain.CheckAvailability)
	if !second.After(first) || !second.Equal(e.clock.Now().Add(5*time.Minute)) {
		t.Fatalf("first=%v second=%v", first, second)
	}
	// Without a schedule a manual run leaves next_run_at unset.
	e.settings(func(s *domain.Settings) {})
	e.tick()
	_, _ = e.db.EnqueueJobs(context.Background(), []domain.ProxyID{id}, domain.CheckAvailability, domain.SourceManual, e.clock.Now(), e.clock.Now())
	e.run(domain.CheckAvailability)
	if n := e.next(id, domain.CheckAvailability); n != nil {
		t.Fatalf("a disabled schedule got a next run: %v", n)
	}
}

func TestDowntimeDoesNotReplayMissedIntervals(t *testing.T) {
	e := newEnv(t, 4)
	e.settings(func(s *domain.Settings) { s.Availability.Enabled = true })
	e.tick()
	e.clock.Advance(10 * time.Second)
	e.run(domain.CheckAvailability)

	// The process is down for three days: every proxy is overdue by ~860 intervals.
	e.clock.Advance(72 * time.Hour)
	n := 0
	e.sched.Jitter = func() time.Duration { n++; return time.Duration(n) * 7 * time.Second }
	for i := 0; i < 10; i++ {
		e.tick()
	}
	got := e.active(domain.CheckAvailability)
	if len(got) != 4 {
		t.Fatalf("catch-up storm: %d jobs for 4 proxies", len(got))
	}
	now := e.clock.Now()
	dues := map[time.Time]bool{}
	for _, j := range got {
		if j.DueAt.Before(now) || j.DueAt.After(now.Add(30*time.Second)) {
			t.Errorf("overdue work must be spread over 0-30 s, got due %v (now %v)", j.DueAt, now)
		}
		dues[j.DueAt] = true
	}
	if len(dues) < 3 {
		t.Fatalf("jobs were not spread out: %v", dues)
	}
}

func TestJitterIsBoundedByThirtySeconds(t *testing.T) {
	s := NewScheduler(nil, testkit.NewClock(epoch))
	for i := 0; i < 2000; i++ {
		if d := s.Jitter(); d < 0 || d >= 30*time.Second {
			t.Fatalf("jitter %v", d)
		}
	}
}

func TestDisablingRemovesOnlyScheduledPendingJobs(t *testing.T) {
	e := newEnv(t, 3)
	ctx := context.Background()
	e.settings(func(s *domain.Settings) { s.Availability.Enabled = true })
	e.tick()
	// One proxy also has a manual job queued (promoting its scheduled one) and one is running.
	_, _ = e.db.EnqueueJobs(ctx, e.ids[:1], domain.CheckAvailability, domain.SourceManual, epoch, epoch)
	e.clock.Advance(10 * time.Second)
	running, _, _ := e.db.ClaimJob(ctx, domain.CheckAvailability, e.clock.Now()) // the manual one is oldest

	e.settings(func(s *domain.Settings) {}) // availability disabled again
	e.tick()
	got := e.active(domain.CheckAvailability)
	if len(got) != 1 || got[e.ids[0]].ID != running.ID || got[e.ids[0]].Status != domain.JobRunning {
		t.Fatalf("only the manual/running job may remain: %v", got)
	}
	// Re-enabling starts from scratch with jitter, not from a stale next_run_at.
	e.settings(func(s *domain.Settings) { s.Availability.Enabled = true })
	e.tick()
	if len(e.active(domain.CheckAvailability)) != 3 {
		t.Fatalf("%v", e.active(domain.CheckAvailability))
	}
}

func TestDisabledScheduleClearsNextRun(t *testing.T) {
	e := newEnv(t, 1)
	e.settings(func(s *domain.Settings) { s.Availability.Enabled = true })
	e.tick()
	e.clock.Advance(10 * time.Second)
	e.run(domain.CheckAvailability)
	if e.next(e.ids[0], domain.CheckAvailability) == nil {
		t.Fatal("expected a next run")
	}
	e.settings(func(s *domain.Settings) {})
	e.tick()
	if n := e.next(e.ids[0], domain.CheckAvailability); n != nil {
		t.Fatalf("stale next run kept after disabling: %v", n)
	}
	e.clock.Advance(24 * time.Hour)
	e.settings(func(s *domain.Settings) { s.Availability.Enabled = true })
	e.tick()
	j := e.active(domain.CheckAvailability)[e.ids[0]]
	if j.ID == 0 || j.DueAt.Before(e.clock.Now()) || j.DueAt.After(e.clock.Now().Add(30*time.Second)) {
		t.Fatalf("%+v", j)
	}
}

func TestScheduledEnqueueNeverOverridesManualJobs(t *testing.T) {
	e := newEnv(t, 1)
	ctx := context.Background()
	_, _ = e.db.EnqueueJobs(ctx, e.ids, domain.CheckAvailability, domain.SourceManual, epoch, epoch)
	e.settings(func(s *domain.Settings) { s.Availability.Enabled = true })
	e.tick()
	j := e.active(domain.CheckAvailability)[e.ids[0]]
	if j.Source != domain.SourceManual || !j.DueAt.Equal(epoch) {
		t.Fatalf("%+v", j)
	}
}

func TestSchedulerFollowsNewProxiesAndDeletions(t *testing.T) {
	e := newEnv(t, 2)
	ctx := context.Background()
	e.settings(func(s *domain.Settings) { s.Availability.Enabled = true })
	e.tick()
	if err := e.db.DeleteProxy(ctx, e.ids[0]); err != nil {
		t.Fatal(err)
	}
	e.tick()
	if got := e.active(domain.CheckAvailability); len(got) != 1 {
		t.Fatalf("%v", got)
	}
	id, _ := e.db.InsertProxy(ctx, domain.Proxy{Name: "new", Protocol: domain.ProtocolHTTP, Host: "new.lan", Port: 1})
	e.tick()
	if _, ok := e.active(domain.CheckAvailability)[id]; !ok {
		t.Fatal("a new proxy is not scheduled")
	}
}

func TestTickPropagatesContextCancellation(t *testing.T) {
	e := newEnv(t, 1)
	e.settings(func(s *domain.Settings) { s.Availability.Enabled = true })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := e.sched.Tick(ctx, epoch); err == nil {
		t.Fatal("a canceled context was ignored")
	}
}

// count runs a COUNT query on a separate connection to the same database.
func (e *env) count(query string, args ...any) int {
	e.t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(e.path))
	if err != nil {
		e.t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}
