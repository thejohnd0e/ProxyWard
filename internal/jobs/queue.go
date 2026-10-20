package jobs

import (
	"context"
	"errors"

	"proxy-checker/internal/domain"
	"proxy-checker/internal/store"
)

// EnqueueResult lists the new and already-active jobs of an enqueue.
type EnqueueResult = store.EnqueueResult

// Canceler stops jobs that are executing.
type Canceler interface {
	CancelRunning(jobID int64) bool
}

// Queue creates and cancels jobs. The queue is bounded by construction: a
// proxy has at most one active job per kind.
type Queue struct {
	Store    *store.Store
	Clock    domain.Clock
	Wake     func() // called after jobs were queued; may be nil
	Canceler Canceler
}

// Enqueue queues one job per proxy for kind, due now. Proxies that already
// have an active job of that kind are reported in Existing.
func (q *Queue) Enqueue(ctx context.Context, ids []domain.ProxyID, kind domain.CheckKind, source string) (EnqueueResult, error) {
	now := q.Clock.Now()
	res, err := q.Store.EnqueueJobs(ctx, ids, kind, source, now, now)
	if err != nil {
		return res, err
	}
	if q.Wake != nil && (len(res.New) > 0 || len(res.Existing) > 0) {
		q.Wake()
	}
	return res, nil
}

// EnqueueAll queues a job of kind for every stored proxy.
func (q *Queue) EnqueueAll(ctx context.Context, kind domain.CheckKind, source string) (EnqueueResult, error) {
	proxies, err := q.Store.ListSchedules(ctx)
	if err != nil {
		return EnqueueResult{}, err
	}
	ids := make([]domain.ProxyID, len(proxies))
	for i, p := range proxies {
		ids[i] = p.ProxyID
	}
	return q.Enqueue(ctx, ids, kind, source)
}

// Cancel cancels a pending job immediately or asks the runner to stop a
// running one, which then finishes as canceled without recording a result.
func (q *Queue) Cancel(ctx context.Context, id int64) (domain.Job, error) {
	job, canceled, err := q.Store.CancelPendingJob(ctx, id, q.Clock.Now())
	if err != nil {
		return domain.Job{}, err
	}
	if !canceled && job.Status == domain.JobRunning && q.Canceler != nil {
		q.Canceler.CancelRunning(id)
	}
	return job, nil
}

// CancelProxy cancels every job of a proxy; call it before deleting the proxy.
func (q *Queue) CancelProxy(ctx context.Context, id domain.ProxyID) error {
	running, err := q.Store.CancelProxyJobs(ctx, id, q.Clock.Now())
	if err != nil {
		return err
	}
	if q.Canceler != nil {
		for _, j := range running {
			q.Canceler.CancelRunning(j.ID)
		}
	}
	return nil
}

// ErrUnknownKind is returned for a check kind that does not exist.
var ErrUnknownKind = errors.New("unknown check kind")
