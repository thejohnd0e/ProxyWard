package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/thejohnd0e/ProxyWard/internal/domain"
)

func addProxies(t *testing.T, db *Store, n int) []domain.ProxyID {
	t.Helper()
	ids := make([]domain.ProxyID, n)
	for i := range ids {
		id, err := db.InsertProxy(context.Background(), domain.Proxy{
			Name: "p", Protocol: domain.ProtocolHTTP, Host: "h" + string(rune('a'+i)) + ".lan", Port: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = id
	}
	return ids
}

func enableSchedules(t *testing.T, db *Store, availability, speed bool) {
	t.Helper()
	settings := domain.DefaultSettings()
	settings.Availability.Enabled = availability
	settings.Speed.Enabled = speed
	if err := db.PutSettings(context.Background(), settings, t0); err != nil {
		t.Fatal(err)
	}
}

func TestCancelWinsBeforeOutcomeCommit(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	id := addProxies(t, db, 1)[0]
	queued, err := db.EnqueueJobs(ctx, []domain.ProxyID{id}, domain.CheckAvailability, domain.SourceManual, t0, t0)
	if err != nil || len(queued.New) != 1 {
		t.Fatalf("enqueue: %+v %v", queued, err)
	}
	job, ok, err := db.ClaimJob(ctx, domain.CheckAvailability, t0)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if _, err := db.RequestJobCancel(ctx, job.ID, t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	result := domain.Availability{ProxyID: id, CheckedAt: t0.Add(2 * time.Second), State: domain.StateDown, Failure: &domain.Failure{Code: "timeout"}}
	_, err = db.CompleteAvailability(ctx, domain.AvailabilityCompletion{JobID: job.ID, Result: result, CompletedAt: t0.Add(2 * time.Second), ScheduleRevision: job.ScheduleRevision})
	if !errors.Is(err, ErrJobCanceled) {
		t.Fatalf("canceled outcome error=%v, want ErrJobCanceled", err)
	}
	got, err := db.GetJob(ctx, job.ID)
	if err != nil || got.Status != domain.JobCanceled {
		t.Fatalf("job after canceled outcome: %+v %v", got, err)
	}
	for _, query := range []string{
		`SELECT COUNT(*) FROM availability_results`,
		`SELECT COUNT(*) FROM proxy_state`,
		`SELECT COUNT(*) FROM incidents`,
		`SELECT COUNT(*) FROM events`,
		`SELECT COUNT(*) FROM notification_outbox`,
	} {
		if n := countRows(t, db, query); n != 0 {
			t.Fatalf("canceled outcome left %d rows in %s", n, query)
		}
	}
}

func TestCompletionRollbackLeavesNoPartialHistory(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	id := addProxies(t, db, 1)[0]
	settings := domain.DefaultSettings()
	settings.DownThreshold = 1
	if err := db.PutSettings(ctx, settings, t0); err != nil {
		t.Fatal(err)
	}
	queued, err := db.EnqueueJobs(ctx, []domain.ProxyID{id}, domain.CheckAvailability, domain.SourceManual, t0, t0)
	if err != nil || len(queued.New) != 1 {
		t.Fatalf("enqueue: %+v %v", queued, err)
	}
	job, ok, err := db.ClaimJob(ctx, domain.CheckAvailability, t0)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if _, err := db.db.ExecContext(ctx, `CREATE TRIGGER reject_outbox BEFORE INSERT ON notification_outbox BEGIN SELECT RAISE(ABORT, 'test outbox failure'); END`); err != nil {
		t.Fatal(err)
	}
	result := domain.Availability{ProxyID: id, CheckedAt: t0.Add(time.Second), State: domain.StateDown, Failure: &domain.Failure{Code: "timeout"}}
	if _, err := db.CompleteAvailability(ctx, domain.AvailabilityCompletion{JobID: job.ID, Result: result, CompletedAt: t0.Add(time.Second), ScheduleRevision: job.ScheduleRevision}); err == nil {
		t.Fatal("expected outbox insert to fail")
	}
	for _, query := range []string{
		`SELECT COUNT(*) FROM availability_results`,
		`SELECT COUNT(*) FROM proxy_state`,
		`SELECT COUNT(*) FROM incidents`,
		`SELECT COUNT(*) FROM events`,
		`SELECT COUNT(*) FROM notification_outbox`,
	} {
		if n := countRows(t, db, query); n != 0 {
			t.Fatalf("failed outcome left %d rows in %s", n, query)
		}
	}
	got, err := db.GetJob(ctx, job.ID)
	if err != nil || got.Status != domain.JobRunning {
		t.Fatalf("failed transaction changed job state: %+v %v", got, err)
	}
}

func TestCompletionWinsBeforeLateCancel(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	id := addProxies(t, db, 1)[0]
	queued, err := db.EnqueueJobs(ctx, []domain.ProxyID{id}, domain.CheckAvailability, domain.SourceManual, t0, t0)
	if err != nil || len(queued.New) != 1 {
		t.Fatalf("enqueue: %+v %v", queued, err)
	}
	job, ok, err := db.ClaimJob(ctx, domain.CheckAvailability, t0)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	result := domain.Availability{ProxyID: id, CheckedAt: t0.Add(time.Second), State: domain.StateUp}
	if _, err := db.CompleteAvailability(ctx, domain.AvailabilityCompletion{JobID: job.ID, Result: result, CompletedAt: t0.Add(time.Second), ScheduleRevision: job.ScheduleRevision}); err != nil {
		t.Fatalf("commit outcome: %v", err)
	}
	if _, err := db.RequestJobCancel(ctx, job.ID, t0.Add(2*time.Second)); !errors.Is(err, ErrConflict) {
		t.Fatalf("cancel completed job error = %v, want conflict", err)
	}
}

func TestCompletionRetryDoesNotDoubleCountAndRetainedResultKeepsJobIdentity(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	id := addProxies(t, db, 1)[0]
	settings := domain.DefaultSettings()
	settings.DownThreshold = 1
	if err := db.PutSettings(ctx, settings, t0); err != nil {
		t.Fatal(err)
	}
	queued, err := db.EnqueueJobs(ctx, []domain.ProxyID{id}, domain.CheckAvailability, domain.SourceManual, t0, t0)
	if err != nil || len(queued.New) != 1 {
		t.Fatalf("enqueue: %+v %v", queued, err)
	}
	job, ok, err := db.ClaimJob(ctx, domain.CheckAvailability, t0)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	result := domain.Availability{ProxyID: id, CheckedAt: t0.Add(time.Second), State: domain.StateDown, Failure: &domain.Failure{Code: "timeout"}}
	if _, err := db.CompleteAvailability(ctx, domain.AvailabilityCompletion{JobID: job.ID, Result: result, CompletedAt: t0.Add(time.Second), ScheduleRevision: job.ScheduleRevision}); err != nil {
		t.Fatalf("first completion: %v", err)
	}
	if _, err := db.CompleteAvailability(ctx, domain.AvailabilityCompletion{JobID: job.ID, Result: result, CompletedAt: t0.Add(2 * time.Second), ScheduleRevision: job.ScheduleRevision}); err != nil {
		t.Fatalf("retry completion: %v", err)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM availability_results WHERE job_id = ?`, job.ID); n != 1 {
		t.Fatalf("results for job = %d, want 1", n)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM proxy_state WHERE proxy_id = ? AND failure_streak = 1`, int64(id)); n != 1 {
		t.Fatalf("retry double-counted failure streak: %d", n)
	}
	newer, err := db.EnqueueJobs(ctx, []domain.ProxyID{id}, domain.CheckAvailability, domain.SourceManual, t0.Add(2*time.Second), t0.Add(2*time.Second))
	if err != nil || len(newer.New) != 1 {
		t.Fatalf("enqueue newer observation: %+v %v", newer, err)
	}
	newerJob, ok, err := db.ClaimJob(ctx, domain.CheckAvailability, t0.Add(2*time.Second))
	if err != nil || !ok {
		t.Fatalf("claim newer job: ok=%v err=%v", ok, err)
	}
	newerResult := domain.Availability{ProxyID: id, CheckedAt: t0.Add(3 * time.Second), State: domain.StateUp}
	if _, err := db.CompleteAvailability(ctx, domain.AvailabilityCompletion{JobID: newerJob.ID, Result: newerResult, CompletedAt: t0.Add(3 * time.Second), ScheduleRevision: newerJob.ScheduleRevision}); err != nil {
		t.Fatalf("complete newer job: %v", err)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM availability_results WHERE job_id = ?`, newerJob.ID); n != 1 {
		t.Fatalf("new retained result identity count = %d, want 1", n)
	}
	if _, err := db.Prune(ctx, t0.Add(24*time.Hour), 100); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM jobs WHERE id = ?`, newerJob.ID); n != 1 {
		t.Fatal("job referenced by retained result was pruned")
	}
	if _, err := db.CompleteAvailability(ctx, domain.AvailabilityCompletion{JobID: job.ID, Result: result, CompletedAt: t0.Add(25 * time.Hour), ScheduleRevision: job.ScheduleRevision}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("pruned completion replay error = %v, want not found", err)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM availability_results`); n != 1 {
		t.Fatalf("pruned completion replay recreated history: count=%d", n)
	}
}

func TestCommitSpeedJobOutcomeHonorsCancellationIntent(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	id := addProxies(t, db, 1)[0]
	queued, err := db.EnqueueJobs(ctx, []domain.ProxyID{id}, domain.CheckSpeed, domain.SourceManual, t0, t0)
	if err != nil || len(queued.New) != 1 {
		t.Fatalf("enqueue: %+v %v", queued, err)
	}
	job, ok, err := db.ClaimJob(ctx, domain.CheckSpeed, t0)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if _, err := db.RequestJobCancel(ctx, job.ID, t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	result := domain.Speed{ProxyID: id, CheckedAt: t0.Add(2 * time.Second), Provider: "fixture", Method: "fixture"}
	if err := db.CompleteSpeed(ctx, domain.SpeedCompletion{JobID: job.ID, Result: result, CompletedAt: t0.Add(2 * time.Second), ScheduleRevision: job.ScheduleRevision}); !errors.Is(err, ErrJobCanceled) {
		t.Fatalf("canceled speed outcome error=%v, want ErrJobCanceled", err)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM speed_results`); n != 0 {
		t.Fatalf("canceled speed result count = %d", n)
	}
	got, err := db.GetJob(ctx, job.ID)
	if err != nil || got.Status != domain.JobCanceled {
		t.Fatalf("job state: %+v %v", got, err)
	}
}

func TestCanceledManualRunningJobPreservesSchedule(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	id := addProxies(t, db, 1)[0]
	settings := domain.DefaultSettings()
	settings.Availability = domain.Schedule{Enabled: true, Interval: time.Hour}
	if err := db.PutSettings(ctx, settings, t0); err != nil {
		t.Fatal(err)
	}
	next := t0.Add(30 * time.Minute)
	if err := db.SetNextRun(ctx, id, domain.CheckAvailability, &next); err != nil {
		t.Fatal(err)
	}
	queued, err := db.EnqueueJobs(ctx, []domain.ProxyID{id}, domain.CheckAvailability, domain.SourceManual, t0, t0)
	if err != nil || len(queued.New) != 1 {
		t.Fatalf("enqueue: %+v %v", queued, err)
	}
	job, ok, err := db.ClaimJob(ctx, domain.CheckAvailability, t0)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if _, err := db.RequestJobCancel(ctx, job.ID, t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := db.FinishJob(ctx, job.ID, domain.JobCanceled, t0.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	states, err := db.ListSchedules(ctx)
	if err != nil || len(states) != 1 || states[0].NextAvailability == nil || !states[0].NextAvailability.Equal(next) {
		t.Fatalf("canceling manual running job changed next run: states=%+v err=%v", states, err)
	}
}

func TestCancelRunningScheduledJobAdvancesScheduleImmediately(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	id := addProxies(t, db, 1)[0]
	settings := domain.DefaultSettings()
	settings.Availability = domain.Schedule{Enabled: true, Interval: time.Hour}
	if err := db.PutSettings(ctx, settings, t0); err != nil {
		t.Fatal(err)
	}
	queued, err := db.EnqueueJobs(ctx, []domain.ProxyID{id}, domain.CheckAvailability, domain.SourceScheduled, t0, t0)
	if err != nil || len(queued.New) != 1 {
		t.Fatalf("enqueue: %+v %v", queued, err)
	}
	job, ok, err := db.ClaimJob(ctx, domain.CheckAvailability, t0)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if _, err := db.RequestJobCancel(ctx, job.ID, t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	states, err := db.ListSchedules(ctx)
	if err != nil || len(states) != 1 || states[0].NextAvailability == nil || !states[0].NextAvailability.Equal(t0.Add(time.Hour+time.Second)) {
		t.Fatalf("scheduled cancellation did not advance next run atomically: states=%+v err=%v", states, err)
	}
}

func TestCrossTableCancelCompletionRaceIsAtomic(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	id := addProxies(t, db, 1)[0]
	queued, err := db.EnqueueJobs(ctx, []domain.ProxyID{id}, domain.CheckAvailability, domain.SourceManual, t0, t0)
	if err != nil || len(queued.New) != 1 {
		t.Fatalf("enqueue: %+v %v", queued, err)
	}
	job, ok, err := db.ClaimJob(ctx, domain.CheckAvailability, t0)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	start := make(chan struct{})
	cancelDone := make(chan error, 1)
	completeDone := make(chan error, 1)
	go func() {
		<-start
		_, err := db.RequestJobCancel(ctx, job.ID, t0.Add(time.Second))
		cancelDone <- err
	}()
	go func() {
		<-start
		result := domain.Availability{ProxyID: id, CheckedAt: t0.Add(time.Second), State: domain.StateDown, Failure: &domain.Failure{Code: "timeout"}}
		_, err := db.CompleteAvailability(ctx, domain.AvailabilityCompletion{JobID: job.ID, Result: result, CompletedAt: t0.Add(time.Second), ScheduleRevision: job.ScheduleRevision})
		completeDone <- err
	}()
	close(start)
	cancelErr, completeErr := <-cancelDone, <-completeDone
	finished, err := db.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	resultCount := countRows(t, db, `SELECT COUNT(*) FROM availability_results WHERE job_id = ?`, job.ID)
	switch finished.Status {
	case domain.JobCanceled:
		if cancelErr != nil || !errors.Is(completeErr, ErrJobCanceled) || resultCount != 0 {
			t.Fatalf("cancel won with partial outcome: cancel=%v complete=%v results=%d job=%+v", cancelErr, completeErr, resultCount, finished)
		}
	case domain.JobDone:
		if !errors.Is(cancelErr, ErrConflict) || completeErr != nil || resultCount != 1 {
			t.Fatalf("completion won with inconsistent cancellation: cancel=%v complete=%v results=%d job=%+v", cancelErr, completeErr, resultCount, finished)
		}
	default:
		t.Fatalf("unexpected terminal race state: status=%s cancel=%v complete=%v", finished.Status, cancelErr, completeErr)
	}
}

func TestAvailabilityCompletionPreservesScheduleChangedWhileRunning(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	id := addProxies(t, db, 1)[0]
	settings := domain.DefaultSettings()
	settings.Availability = domain.Schedule{Enabled: true, Interval: 7 * 24 * time.Hour}
	if err := db.PutSettings(ctx, settings, t0); err != nil {
		t.Fatal(err)
	}
	queued, err := db.EnqueueJobs(ctx, []domain.ProxyID{id}, domain.CheckAvailability, domain.SourceScheduled, t0, t0)
	if err != nil || len(queued.New) != 1 {
		t.Fatalf("enqueue: %+v %v", queued, err)
	}
	job, ok, err := db.ClaimJob(ctx, domain.CheckAvailability, t0)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	changedAt := t0.Add(time.Second)
	settings.Availability.Interval = time.Minute
	if err := db.PutSettings(ctx, settings, changedAt); err != nil {
		t.Fatal(err)
	}
	wantNext := changedAt.Add(time.Minute)
	result := domain.Availability{ProxyID: id, CheckedAt: t0.Add(2 * time.Second), State: domain.StateUp}
	if _, err := db.CompleteAvailability(ctx, domain.AvailabilityCompletion{JobID: job.ID, Result: result, CompletedAt: t0.Add(2 * time.Second), ScheduleRevision: job.ScheduleRevision}); err != nil {
		t.Fatal(err)
	}
	states, err := db.ListSchedules(ctx)
	if err != nil || len(states) != 1 || states[0].NextAvailability == nil || !states[0].NextAvailability.Equal(wantNext) {
		t.Fatalf("completion overwrote changed schedule: states=%+v want next %v err=%v", states, wantNext, err)
	}
	finished, err := db.GetJob(ctx, job.ID)
	if err != nil || finished.Status != domain.JobDone {
		t.Fatalf("old job outcome did not commit: %+v %v", finished, err)
	}
}

func TestSpeedCompletionFailureNeverMarksJobDone(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	id := addProxies(t, db, 1)[0]
	queued, err := db.EnqueueJobs(ctx, []domain.ProxyID{id}, domain.CheckSpeed, domain.SourceManual, t0, t0)
	if err != nil || len(queued.New) != 1 {
		t.Fatalf("enqueue: %+v %v", queued, err)
	}
	job, ok, err := db.ClaimJob(ctx, domain.CheckSpeed, t0)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if _, err := db.db.ExecContext(ctx, `CREATE TRIGGER reject_speed_job_finish BEFORE UPDATE OF status ON jobs WHEN NEW.status = 'done' BEGIN SELECT RAISE(ABORT, 'test job finish failure'); END`); err != nil {
		t.Fatal(err)
	}
	result := domain.Speed{ProxyID: id, CheckedAt: t0.Add(time.Second), Provider: "fixture", Method: "fixture"}
	if err := db.CompleteSpeed(ctx, domain.SpeedCompletion{JobID: job.ID, Result: result, CompletedAt: t0.Add(time.Second), ScheduleRevision: job.ScheduleRevision}); err == nil {
		t.Fatal("expected job update failure")
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM speed_results`); n != 0 {
		t.Fatalf("failed completion left %d speed results", n)
	}
	got, err := db.GetJob(ctx, job.ID)
	if err != nil || got.Status != domain.JobRunning {
		t.Fatalf("failed completion marked job done: %+v %v", got, err)
	}
}

func TestSettingsDefaultsRoundTripAndValidation(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	got, err := db.GetSettings(ctx)
	if err != nil || got != domain.DefaultSettings() {
		t.Fatalf("defaults: %+v %v", got, err)
	}
	if got.Availability.Enabled || got.Speed.Enabled {
		t.Fatal("both schedules must start disabled")
	}
	if got.Availability.Interval != 5*time.Minute || got.Speed.Interval != 6*time.Hour || got.DownThreshold != 3 ||
		got.AvailabilityConcurrency != 5 || got.AvailabilityTimeout != 10*time.Second || got.RetentionDays != 30 {
		t.Fatalf("default values: %+v", got)
	}
	want := got
	want.Availability = domain.Schedule{Enabled: true, Interval: 90 * time.Minute}
	want.Speed = domain.Schedule{Enabled: true, Interval: 24 * time.Hour}
	want.AvailabilityConcurrency = 7
	want.DownThreshold = 2
	if err := db.PutSettings(ctx, want, t0); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.GetSettings(ctx); got != want {
		t.Fatalf("round trip: %+v", got)
	}
	bad := want
	bad.AvailabilityConcurrency = 21
	var se *domain.SettingsError
	if err := db.PutSettings(ctx, bad, t0); !errors.As(err, &se) || se.Field != "availability_concurrency" {
		t.Fatalf("invalid settings accepted: %v", err)
	}
	if got, _ := db.GetSettings(ctx); got != want {
		t.Fatal("a rejected update changed the stored settings")
	}
}

func TestSettingsBounds(t *testing.T) {
	base := domain.DefaultSettings()
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mod := range map[string]func(*domain.Settings){
		"availability interval short": func(s *domain.Settings) { s.Availability.Interval = 59 * time.Second },
		"availability interval long":  func(s *domain.Settings) { s.Availability.Interval = 10081 * time.Minute },
		"speed interval short":        func(s *domain.Settings) { s.Speed.Interval = 0 },
		"concurrency zero":            func(s *domain.Settings) { s.AvailabilityConcurrency = 0 },
		"timeout short":               func(s *domain.Settings) { s.AvailabilityTimeout = 500 * time.Millisecond },
		"timeout long":                func(s *domain.Settings) { s.AvailabilityTimeout = 61 * time.Second },
		"sample zero":                 func(s *domain.Settings) { s.SpeedSampleBytes = 0 },
		"sample huge":                 func(s *domain.Settings) { s.SpeedSampleBytes = 65 << 20 },
		"warmup negative":             func(s *domain.Settings) { s.SpeedWarmupBytes = -1 },
		"direction timeout short":     func(s *domain.Settings) { s.SpeedDirectionTimeout = time.Millisecond },
		"threshold zero":              func(s *domain.Settings) { s.DownThreshold = 0 },
		"threshold eleven":            func(s *domain.Settings) { s.DownThreshold = 11 },
		"retention zero":              func(s *domain.Settings) { s.RetentionDays = 0 },
		"retention 366":               func(s *domain.Settings) { s.RetentionDays = 366 },
	} {
		s := base
		mod(&s)
		if s.Validate() == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// Boundary values are valid.
	edge := base
	edge.Availability.Interval = time.Minute
	edge.Speed.Interval = 10080 * time.Minute
	edge.AvailabilityConcurrency = 20
	edge.AvailabilityTimeout = time.Minute
	edge.DownThreshold = 10
	edge.RetentionDays = 365
	if err := edge.Validate(); err != nil {
		t.Errorf("boundary values rejected: %v", err)
	}
}

func TestEnqueueDeduplicatesPerProxyAndKind(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	ids := addProxies(t, db, 3)

	r, err := db.EnqueueJobs(ctx, ids[:2], domain.CheckAvailability, domain.SourceManual, t0, t0)
	if err != nil || len(r.New) != 2 || len(r.Existing) != 0 {
		t.Fatalf("%+v %v", r, err)
	}
	r2, err := db.EnqueueJobs(ctx, ids, domain.CheckAvailability, domain.SourceManual, t0, t0)
	if err != nil || len(r2.New) != 1 || len(r2.Existing) != 2 {
		t.Fatalf("%+v %v", r2, err)
	}
	if r2.Existing[0].ID != r.New[0].ID && r2.Existing[1].ID != r.New[0].ID {
		t.Fatalf("existing jobs not reported: %+v", r2.Existing)
	}
	// Another kind for the same proxy is a different job.
	r3, _ := db.EnqueueJobs(ctx, ids[:1], domain.CheckSpeed, domain.SourceManual, t0, t0)
	if len(r3.New) != 1 {
		t.Fatalf("%+v", r3)
	}
	// Unknown proxies are reported, not failed.
	r4, err := db.EnqueueJobs(ctx, []domain.ProxyID{ids[0], 999}, domain.CheckAvailability, domain.SourceManual, t0, t0)
	if err != nil || len(r4.Missing) != 1 || r4.Missing[0] != 999 || len(r4.Existing) != 1 {
		t.Fatalf("%+v %v", r4, err)
	}
	// A finished job frees the slot.
	job, ok, _ := db.ClaimJob(ctx, domain.CheckAvailability, t0)
	if !ok {
		t.Fatal("nothing to claim")
	}
	if err := db.FinishJob(ctx, job.ID, domain.JobDone, t0); err != nil {
		t.Fatal(err)
	}
	r5, _ := db.EnqueueJobs(ctx, []domain.ProxyID{job.ProxyID}, domain.CheckAvailability, domain.SourceManual, t0, t0)
	if len(r5.New) != 1 {
		t.Fatalf("slot not freed: %+v", r5)
	}
}

func TestManualEnqueuePromotesAScheduledPendingJob(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	id := addProxies(t, db, 1)[0]
	enableSchedules(t, db, true, false)
	later := t0.Add(20 * time.Second)
	r, _ := db.EnqueueJobs(ctx, []domain.ProxyID{id}, domain.CheckAvailability, domain.SourceScheduled, later, t0)
	if len(r.New) != 1 || r.New[0].Source != domain.SourceScheduled {
		t.Fatalf("%+v", r)
	}
	m, err := db.EnqueueJobs(ctx, []domain.ProxyID{id}, domain.CheckAvailability, domain.SourceManual, t0, t0)
	if err != nil || len(m.Existing) != 1 || len(m.New) != 0 {
		t.Fatalf("%+v %v", m, err)
	}
	j := m.Existing[0]
	if j.ID != r.New[0].ID || j.Source != domain.SourceManual || !j.DueAt.Equal(t0) {
		t.Fatalf("not promoted: %+v", j)
	}
	// Promoted jobs survive removal of scheduled work.
	n, _ := db.RemoveScheduledPending(ctx, domain.CheckAvailability, nil)
	if n != 0 {
		t.Fatalf("removed %d manual jobs", n)
	}
	// A scheduled enqueue never demotes or reschedules an existing job.
	again, _ := db.EnqueueJobs(ctx, []domain.ProxyID{id}, domain.CheckAvailability, domain.SourceScheduled, t0.Add(time.Hour), t0)
	if len(again.Existing) != 1 || again.Existing[0].Source != domain.SourceManual || !again.Existing[0].DueAt.Equal(t0) {
		t.Fatalf("%+v", again)
	}
}

func TestClaimRespectsDueTimeOrderAndProxyExclusion(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	ids := addProxies(t, db, 3)
	enableSchedules(t, db, true, false)
	_, _ = db.EnqueueJobs(ctx, []domain.ProxyID{ids[0]}, domain.CheckAvailability, domain.SourceScheduled, t0.Add(10*time.Second), t0)
	_, _ = db.EnqueueJobs(ctx, []domain.ProxyID{ids[1]}, domain.CheckAvailability, domain.SourceScheduled, t0, t0)
	_, _ = db.EnqueueJobs(ctx, []domain.ProxyID{ids[2]}, domain.CheckAvailability, domain.SourceScheduled, t0, t0)

	j, ok, err := db.ClaimJob(ctx, domain.CheckAvailability, t0)
	if err != nil || !ok || j.ProxyID != ids[1] || j.Status != domain.JobRunning || j.StartedAt == nil {
		t.Fatalf("first claim: %+v %v %v", j, ok, err)
	}
	j2, ok, _ := db.ClaimJob(ctx, domain.CheckAvailability, t0)
	if !ok || j2.ProxyID != ids[2] {
		t.Fatalf("second claim: %+v %v", j2, ok)
	}
	if _, ok, _ := db.ClaimJob(ctx, domain.CheckAvailability, t0); ok {
		t.Fatal("a job that is not due was claimed")
	}
	if _, ok, _ := db.ClaimJob(ctx, domain.CheckSpeed, t0); ok {
		t.Fatal("claimed a job of another kind")
	}
	if j3, ok, _ := db.ClaimJob(ctx, domain.CheckAvailability, t0.Add(10*time.Second)); !ok || j3.ProxyID != ids[0] {
		t.Fatalf("due job not claimed: %+v %v", j3, ok)
	}

	// While a proxy is being checked, its other-kind job must wait.
	_, _ = db.EnqueueJobs(ctx, []domain.ProxyID{ids[1]}, domain.CheckSpeed, domain.SourceManual, t0, t0)
	if _, ok, _ := db.ClaimJob(ctx, domain.CheckSpeed, t0); ok {
		t.Fatal("speed job claimed while the same proxy's availability job runs")
	}
	if err := db.FinishJob(ctx, j.ID, domain.JobDone, t0); err != nil {
		t.Fatal(err)
	}
	if sj, ok, _ := db.ClaimJob(ctx, domain.CheckSpeed, t0); !ok || sj.ProxyID != ids[1] {
		t.Fatalf("speed job not released: %+v %v", sj, ok)
	}
}

func TestConcurrentClaimsNeverDoubleClaim(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	ids := addProxies(t, db, 8)
	_, _ = db.EnqueueJobs(ctx, ids, domain.CheckAvailability, domain.SourceManual, t0, t0)
	type res struct {
		id int64
		ok bool
	}
	out := make(chan res, 32)
	for i := 0; i < 16; i++ {
		go func() {
			j, ok, err := db.ClaimJob(ctx, domain.CheckAvailability, t0)
			if err != nil {
				t.Error(err)
			}
			out <- res{j.ID, ok}
		}()
	}
	seen := map[int64]bool{}
	claimed := 0
	for i := 0; i < 16; i++ {
		r := <-out
		if r.ok {
			claimed++
			if seen[r.id] {
				t.Fatalf("job %d claimed twice", r.id)
			}
			seen[r.id] = true
		}
	}
	if claimed != 8 {
		t.Fatalf("claimed %d of 8", claimed)
	}
}

func TestFinishCancelAndInterruptTransitions(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	ids := addProxies(t, db, 3)
	enableSchedules(t, db, true, false)
	_, _ = db.EnqueueJobs(ctx, ids, domain.CheckAvailability, domain.SourceManual, t0, t0)
	running, _, _ := db.ClaimJob(ctx, domain.CheckAvailability, t0)

	// Pending jobs cancel immediately; running jobs retain durable intent.
	list, _ := db.ListActiveJobs(ctx)
	var pending domain.Job
	for _, j := range list {
		if j.Status == domain.JobPending {
			pending = j
			break
		}
	}
	job, err := db.RequestJobCancel(ctx, pending.ID, t0)
	if err != nil || job.Status != domain.JobCanceled || job.FinishedAt == nil {
		t.Fatalf("%+v %v", job, err)
	}
	job, err = db.RequestJobCancel(ctx, running.ID, t0)
	if err != nil || job.Status != domain.JobRunning || job.CancelRequestedAt == nil {
		t.Fatalf("running job: %+v %v", job, err)
	}
	if _, err := db.RequestJobCancel(ctx, 9999, t0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing job: %v", err)
	}
	if err := db.FinishJob(ctx, running.ID, domain.JobDone, t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := db.FinishJob(ctx, running.ID, domain.JobCanceled, t0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("finishing a finished job: %v", err)
	}
	if err := db.FinishJob(ctx, running.ID, domain.JobPending, t0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("non-terminal status accepted: %v", err)
	}
	got, _ := db.GetJob(ctx, running.ID)
	if got.Status != domain.JobCanceled || got.FinishedAt == nil || !got.FinishedAt.Equal(t0.Add(time.Second)) {
		t.Fatalf("%+v", got)
	}
}

func TestRecoverInterruptedMarksOnlyRunningJobs(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	ids := addProxies(t, db, 3)
	_, _ = db.EnqueueJobs(ctx, ids, domain.CheckAvailability, domain.SourceManual, t0, t0)
	a, _, _ := db.ClaimJob(ctx, domain.CheckAvailability, t0)
	b, _, _ := db.ClaimJob(ctx, domain.CheckAvailability, t0)
	n, err := db.RecoverInterrupted(ctx, t0.Add(time.Minute))
	if err != nil || n != 2 {
		t.Fatalf("recovered %d: %v", n, err)
	}
	for _, id := range []int64{a.ID, b.ID} {
		j, _ := db.GetJob(ctx, id)
		if j.Status != domain.JobInterrupted || j.FinishedAt == nil {
			t.Fatalf("%+v", j)
		}
	}
	active, _ := db.ListActiveJobs(ctx)
	if len(active) != 1 || active[0].Status != domain.JobPending {
		t.Fatalf("pending job lost: %+v", active)
	}
	// Interrupted work released the proxies: they can be queued again.
	r, _ := db.EnqueueJobs(ctx, []domain.ProxyID{a.ProxyID}, domain.CheckAvailability, domain.SourceManual, t0, t0)
	if len(r.New) != 1 {
		t.Fatalf("%+v", r)
	}
	if n, _ := db.RecoverInterrupted(ctx, t0); n != 0 {
		t.Fatalf("second recovery changed %d jobs", n)
	}
}

func TestCancelProxyJobsAndRemoveScheduled(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	ids := addProxies(t, db, 3)
	enableSchedules(t, db, true, false)
	_, _ = db.EnqueueJobs(ctx, ids, domain.CheckAvailability, domain.SourceScheduled, t0, t0)
	_, _ = db.EnqueueJobs(ctx, ids[:1], domain.CheckSpeed, domain.SourceManual, t0, t0)
	run, _, _ := db.ClaimJob(ctx, domain.CheckAvailability, t0)

	running, err := db.CancelProxyJobs(ctx, run.ProxyID, t0)
	if err != nil || len(running) != 1 || running[0].ID != run.ID {
		t.Fatalf("%+v %v", running, err)
	}
	// Only the proxy's pending job was cancelled; the running one is left for the runner.
	left, _ := db.ListActiveJobs(ctx)
	for _, j := range left {
		if j.ProxyID == run.ProxyID && j.Status == domain.JobPending {
			t.Fatalf("pending job of the proxy survived: %+v", j)
		}
	}

	// Remove scheduled pending jobs except for proxies that stay enabled.
	n, err := db.RemoveScheduledPending(ctx, domain.CheckAvailability, []domain.ProxyID{ids[1]})
	if err != nil {
		t.Fatal(err)
	}
	active, _ := db.ListActiveJobs(ctx)
	kept := map[domain.ProxyID]bool{}
	for _, j := range active {
		if j.Kind == domain.CheckAvailability {
			kept[j.ProxyID] = true
		}
	}
	if !kept[ids[1]] || kept[ids[2]] || n < 1 {
		t.Fatalf("removed %d, remaining availability jobs for %v", n, kept)
	}
}

func TestProxyDeleteRemovesItsJobs(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	ids := addProxies(t, db, 2)
	_, _ = db.EnqueueJobs(ctx, ids, domain.CheckAvailability, domain.SourceManual, t0, t0)
	if err := db.DeleteProxy(ctx, ids[0]); err != nil {
		t.Fatal(err)
	}
	active, _ := db.ListActiveJobs(ctx)
	if len(active) != 1 || active[0].ProxyID != ids[1] {
		t.Fatalf("%+v", active)
	}
}

func TestNextRunsRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	ids := addProxies(t, db, 2)
	runs, err := db.ListSchedules(ctx)
	if err != nil || len(runs) != 2 || runs[0].NextAvailability != nil || runs[0].NextSpeed != nil {
		t.Fatalf("%+v %v", runs, err)
	}
	at := t0.Add(5 * time.Minute)
	if err := db.SetNextRun(ctx, ids[0], domain.CheckAvailability, &at); err != nil {
		t.Fatal(err)
	}
	if err := db.SetNextRun(ctx, ids[0], domain.CheckSpeed, &at); err != nil {
		t.Fatal(err)
	}
	runs, _ = db.ListSchedules(ctx)
	if runs[0].NextAvailability == nil || !runs[0].NextAvailability.Equal(at) || runs[0].NextSpeed == nil || runs[1].NextAvailability != nil {
		t.Fatalf("%+v", runs)
	}
	_ = db.SetNextRun(ctx, ids[0], domain.CheckAvailability, nil)
	runs, _ = db.ListSchedules(ctx)
	if runs[0].NextAvailability != nil || runs[0].NextSpeed == nil {
		t.Fatalf("clearing one kind touched the other: %+v", runs[0])
	}
	if err := db.SetNextRun(ctx, 999, domain.CheckSpeed, &at); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown proxy: %v", err)
	}
}

func TestPruneJobsKeepsActiveAndRecent(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	ids := addProxies(t, db, 3)
	_, _ = db.EnqueueJobs(ctx, ids, domain.CheckAvailability, domain.SourceManual, t0, t0)
	old, _, _ := db.ClaimJob(ctx, domain.CheckAvailability, t0)
	recent, _, _ := db.ClaimJob(ctx, domain.CheckAvailability, t0)
	_ = db.FinishJob(ctx, old.ID, domain.JobDone, t0)
	_ = db.FinishJob(ctx, recent.ID, domain.JobDone, t0.Add(48*time.Hour))
	n, err := db.PruneJobs(ctx, t0.Add(24*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("pruned %d: %v", n, err)
	}
	if _, err := db.GetJob(ctx, old.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old job still present: %v", err)
	}
	if _, err := db.GetJob(ctx, recent.ID); err != nil {
		t.Fatal(err)
	}
	if active, _ := db.ListActiveJobs(ctx); len(active) != 1 {
		t.Fatalf("active jobs were pruned: %+v", active)
	}
}

func TestScheduledEnqueueSkipsProxiesThatAreNotDue(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t)
	ids := addProxies(t, db, 2)
	enableSchedules(t, db, true, true)
	future := t0.Add(time.Hour)
	if err := db.SetNextRun(ctx, ids[0], domain.CheckAvailability, &future); err != nil {
		t.Fatal(err)
	}
	r, err := db.EnqueueJobs(ctx, ids, domain.CheckAvailability, domain.SourceScheduled, t0, t0)
	if err != nil || len(r.New) != 1 || r.New[0].ProxyID != ids[1] || len(r.Existing) != 0 {
		t.Fatalf("a proxy whose next run is in the future was queued: %+v %v", r, err)
	}
	// Due exactly now is due; the other kind has its own next run.
	if err := db.SetNextRun(ctx, ids[0], domain.CheckAvailability, &t0); err != nil {
		t.Fatal(err)
	}
	if r, _ := db.EnqueueJobs(ctx, ids[:1], domain.CheckAvailability, domain.SourceScheduled, t0, t0); len(r.New) != 1 {
		t.Fatalf("%+v", r)
	}
	if r, _ := db.EnqueueJobs(ctx, ids[:1], domain.CheckSpeed, domain.SourceScheduled, t0, t0); len(r.New) != 1 {
		t.Fatalf("the speed schedule must not depend on the availability next run: %+v", r)
	}
	// Manual requests ignore next-run times.
	if err := db.SetNextRun(ctx, ids[1], domain.CheckSpeed, &future); err != nil {
		t.Fatal(err)
	}
	if r, _ := db.EnqueueJobs(ctx, ids[1:], domain.CheckSpeed, domain.SourceManual, t0, t0); len(r.New) != 1 {
		t.Fatalf("%+v", r)
	}
}
