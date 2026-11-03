package jobs

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/thejohnd0e/ProxyWard/internal/check"
	"github.com/thejohnd0e/ProxyWard/internal/domain"
	"github.com/thejohnd0e/ProxyWard/internal/store"
)

// AvailabilityChecker runs one availability check.
type AvailabilityChecker interface {
	Check(ctx context.Context, p domain.Proxy, client *http.Client) domain.Availability
}

// SpeedMeasurer runs one throughput measurement and reports provider pauses.
type SpeedMeasurer interface {
	Measure(ctx context.Context, p domain.Proxy, client *http.Client, cfg check.SpeedConfig) domain.Speed
	Gate(ctx context.Context) (check.Gate, error)
}

// ClientFactory builds the HTTP client that routes through a proxy.
type ClientFactory func(p domain.Proxy, timeout time.Duration) (*http.Client, error)

var errUserCanceled = errors.New("canceled by the user")

// Runner executes queued jobs: as many availability workers as configured and
// exactly one speed worker. A proxy never has two jobs running at once (the
// store refuses to claim a second one).
type Runner struct {
	Store        *store.Store
	Clock        domain.Clock
	Availability AvailabilityChecker
	Speed        SpeedMeasurer
	NewClient    ClientFactory
	// OnAvailability is called after an availability result was stored; may be nil.
	OnAvailability func(domain.Proxy, domain.Availability)
	Logger         *slog.Logger
	// PollInterval is how often idle workers look for due jobs.
	PollInterval time.Duration

	mu               sync.Mutex
	signal           chan struct{}
	cancels          map[int64]context.CancelCauseFunc
	completionFailed atomic.Bool
}

// Ready reports whether workers may safely continue claiming jobs.
func (r *Runner) Ready() error {
	if r.completionFailed.Load() {
		return errors.New("job outcome persistence has failed; workers are paused")
	}
	return nil
}

func (r *Runner) init() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.signal == nil {
		r.signal = make(chan struct{})
	}
	if r.cancels == nil {
		r.cancels = map[int64]context.CancelCauseFunc{}
	}
}

// Wake tells idle workers to look for work now.
func (r *Runner) Wake() {
	r.init()
	r.mu.Lock()
	close(r.signal)
	r.signal = make(chan struct{})
	r.mu.Unlock()
}

func (r *Runner) waiter() <-chan struct{} {
	r.init()
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.signal
}

// CancelRunning asks the worker executing the job to stop. It reports whether
// the job was running here.
func (r *Runner) CancelRunning(jobID int64) bool {
	r.init()
	r.mu.Lock()
	cancel, ok := r.cancels[jobID]
	r.mu.Unlock()
	if ok {
		cancel(errUserCanceled)
	}
	return ok
}

// Run starts the workers and blocks until ctx is done and they have stopped.
// The number of availability workers is read from the settings at start.
func (r *Runner) Run(ctx context.Context) error {
	r.init()
	settings, err := r.Store.GetSettings(ctx)
	if err != nil {
		return err
	}
	var wg sync.WaitGroup
	for i := 0; i < settings.AvailabilityConcurrency; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); r.worker(ctx, domain.CheckAvailability) }()
	}
	wg.Add(1)
	go func() { defer wg.Done(); r.worker(ctx, domain.CheckSpeed) }()
	wg.Wait()
	return nil
}

func (r *Runner) poll() time.Duration {
	if r.PollInterval > 0 {
		return r.PollInterval
	}
	return time.Second
}

func (r *Runner) worker(ctx context.Context, kind domain.CheckKind) {
	for ctx.Err() == nil {
		sig := r.waiter() // taken before looking, so a wake-up cannot be missed
		if r.runnable(ctx, kind) {
			job, ok, err := r.Store.ClaimJob(ctx, kind, r.Clock.Now())
			if err != nil {
				if ctx.Err() == nil {
					r.logger().Error("claim job failed", "kind", kind, "error", err)
				}
			} else if ok {
				r.execute(ctx, job)
				continue
			}
		}
		select {
		case <-sig:
		case <-r.Clock.After(r.poll()):
		case <-ctx.Done():
		}
	}
}

// runnable is false while the speed provider is paused: speed jobs then wait
// in the queue instead of being claimed and failing.
func (r *Runner) runnable(ctx context.Context, kind domain.CheckKind) bool {
	if r.completionFailed.Load() {
		return false
	}
	if kind != domain.CheckSpeed {
		return true
	}
	g, err := r.Speed.Gate(ctx)
	return err == nil && !g.Paused
}

func (r *Runner) logger() *slog.Logger {
	if r.Logger != nil {
		return r.Logger
	}
	return slog.Default()
}

func (r *Runner) execute(ctx context.Context, job domain.Job) {
	jobCtx, cancel := context.WithCancelCause(ctx)
	r.mu.Lock()
	r.cancels[job.ID] = cancel
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.cancels, job.ID)
		r.mu.Unlock()
		cancel(nil)
	}()
	current, err := r.Store.GetJob(ctx, job.ID)
	if err != nil {
		r.logger().Error("load claimed job state failed", "job", job.ID, "error", err)
		cancel(errUserCanceled)
	} else if !current.Status.Active() || current.CancelRequestedAt != nil {
		cancel(errUserCanceled)
	}

	status := r.run(ctx, jobCtx, job)

	// Bookkeeping must survive shutdown, so it does not use the job context.
	fin, done := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer done()
	if err := r.Store.FinishJob(fin, job.ID, status, r.Clock.Now()); err != nil && !errors.Is(err, store.ErrNotFound) {
		r.logger().Error("finish job failed", "job", job.ID, "error", err)
	}
}

// abortStatus tells shutdown (interrupted) from a user cancel (canceled).
func abortStatus(parent context.Context) domain.JobStatus {
	if parent.Err() != nil {
		return domain.JobInterrupted
	}
	return domain.JobCanceled
}

func (r *Runner) run(parent, jobCtx context.Context, job domain.Job) domain.JobStatus {
	p, err := r.Store.GetProxy(jobCtx, job.ProxyID)
	if err != nil {
		if jobCtx.Err() != nil {
			return abortStatus(parent)
		}
		if !errors.Is(err, store.ErrNotFound) {
			r.logger().Error("load proxy failed", "job", job.ID, "error", err)
		}
		return domain.JobCanceled // deleted meanwhile, or unreadable
	}
	settings, err := r.Store.GetSettings(jobCtx)
	if err != nil {
		r.logger().Error("load settings failed", "job", job.ID, "error", err)
		return domain.JobCanceled
	}
	if job.Kind == domain.CheckSpeed {
		return r.runSpeed(parent, jobCtx, job, p, settings)
	}
	return r.runAvailability(parent, jobCtx, job, p, settings)
}

func (r *Runner) writeCtx(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), 10*time.Second)
}

func (r *Runner) runAvailability(parent, jobCtx context.Context, job domain.Job, p domain.Proxy, settings domain.Settings) domain.JobStatus {
	var res domain.Availability
	client, err := r.NewClient(p, settings.AvailabilityTimeout)
	if err != nil {
		res = domain.Availability{ProxyID: p.ID, CheckedAt: r.Clock.Now(), State: domain.StateIndeterminate, Failure: check.NewFailure(check.CodeInvalidConfig)}
	} else {
		res = r.Availability.Check(jobCtx, p, client)
	}
	if jobCtx.Err() != nil {
		return abortStatus(parent) // nothing is recorded for work that did not finish
	}
	wctx, done := r.writeCtx(parent)
	defer done()
	completion := domain.AvailabilityCompletion{JobID: job.ID, Result: res, CompletedAt: r.Clock.Now(), ScheduleRevision: job.ScheduleRevision}
	err = r.retryOutcomeCommit(wctx, func() error {
		_, err := r.Store.CompleteAvailability(wctx, completion)
		return err
	})
	if err != nil {
		if errors.Is(err, store.ErrJobCanceled) {
			return domain.JobCanceled
		}
		if !errors.Is(err, store.ErrNotFound) {
			r.completionFailed.Store(true)
			r.logger().Error("commit availability outcome failed", "job", job.ID, "error", err)
			return domain.JobInterrupted
		}
		return domain.JobDone
	}
	if r.OnAvailability != nil {
		r.OnAvailability(p, res)
	}
	return domain.JobDone
}

func (r *Runner) runSpeed(parent, jobCtx context.Context, job domain.Job, p domain.Proxy, settings domain.Settings) domain.JobStatus {
	cfg := check.SpeedConfig{
		SampleBytes: settings.SpeedSampleBytes, WarmupBytes: settings.SpeedWarmupBytes,
		DirectionTimeout: settings.SpeedDirectionTimeout,
	}
	client, err := r.NewClient(p, cfg.DirectionTimeout)
	if err != nil {
		r.logger().Error("build client failed", "job", job.ID, "error", err)
		return domain.JobDone
	}
	res := r.Speed.Measure(jobCtx, p, client, cfg)
	if jobCtx.Err() != nil {
		return abortStatus(parent)
	}
	wctx, done := r.writeCtx(parent)
	defer done()
	completion := domain.SpeedCompletion{JobID: job.ID, Result: res, CompletedAt: r.Clock.Now(), ScheduleRevision: job.ScheduleRevision}
	if err := r.retryOutcomeCommit(wctx, func() error { return r.Store.CompleteSpeed(wctx, completion) }); err != nil {
		if errors.Is(err, store.ErrJobCanceled) {
			return domain.JobCanceled
		}
		if !errors.Is(err, store.ErrNotFound) {
			r.completionFailed.Store(true)
			r.logger().Error("commit speed outcome failed", "job", job.ID, "error", err)
			return domain.JobInterrupted
		}
	}
	return domain.JobDone
}

func (r *Runner) retryOutcomeCommit(ctx context.Context, commit func() error) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		err = commit()
		if err == nil || errors.Is(err, store.ErrJobCanceled) || errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrConflict) {
			return err
		}
		if attempt == 2 {
			break
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 25 * time.Millisecond)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
	}
	return err
}
