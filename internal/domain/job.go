package domain

import "time"

// JobStatus is the lifecycle state of a check job.
type JobStatus string

const (
	JobPending     JobStatus = "pending"
	JobRunning     JobStatus = "running"
	JobDone        JobStatus = "done"
	JobCanceled    JobStatus = "canceled"
	JobInterrupted JobStatus = "interrupted"
)

// Active reports whether the job still occupies its proxy/kind slot.
func (s JobStatus) Active() bool { return s == JobPending || s == JobRunning }

// Job sources.
const (
	SourceManual    = "manual"
	SourceScheduled = "scheduled"
)

// Job is one queued or finished check of a proxy.
type Job struct {
	ID         int64
	ProxyID    ProxyID
	Kind       CheckKind
	Status     JobStatus
	Source     string // SourceManual or SourceScheduled
	DueAt      time.Time
	CreatedAt  time.Time
	StartedAt  *time.Time
	FinishedAt *time.Time
}
