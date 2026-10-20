package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"proxy-checker/internal/apierr"
	"proxy-checker/internal/domain"
	"proxy-checker/internal/store"
)

// JobQueue is the part of the job service the API needs (*jobs.Queue).
type JobQueue interface {
	Enqueue(ctx context.Context, ids []domain.ProxyID, kind domain.CheckKind, source string) (store.EnqueueResult, error)
	EnqueueAll(ctx context.Context, kind domain.CheckKind, source string) (store.EnqueueResult, error)
	Cancel(ctx context.Context, id int64) (domain.Job, error)
	CancelProxy(ctx context.Context, id domain.ProxyID) error
}

// WithJobs enables the job routes and cancels jobs when proxies are deleted.
func WithJobs(q JobQueue) Option { return func(s *Server) { s.jobs = q } }

func (s *Server) registerJobRoutes(mux *http.ServeMux) {
	protected := func(h http.HandlerFunc) http.Handler { return s.auth.Require(h) }
	mux.Handle("POST /api/jobs", protected(s.enqueueJobs))
	mux.Handle("GET /api/jobs", protected(s.listJobs))
	mux.Handle("POST /api/jobs/{id}/cancel", protected(s.cancelJob))
}

type jobDTO struct {
	ID         int64   `json:"id"`
	ProxyID    int64   `json:"proxy_id"`
	Kind       string  `json:"kind"`
	Status     string  `json:"status"`
	Source     string  `json:"source"`
	DueAt      string  `json:"due_at"`
	CreatedAt  string  `json:"created_at"`
	StartedAt  *string `json:"started_at"`
	FinishedAt *string `json:"finished_at"`
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func tsPtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	v := ts(*t)
	return &v
}

func toJobDTO(j domain.Job) jobDTO {
	return jobDTO{
		ID: j.ID, ProxyID: int64(j.ProxyID), Kind: string(j.Kind), Status: string(j.Status), Source: j.Source,
		DueAt: ts(j.DueAt), CreatedAt: ts(j.CreatedAt), StartedAt: tsPtr(j.StartedAt), FinishedAt: tsPtr(j.FinishedAt),
	}
}

func toJobDTOs(in []domain.Job) []jobDTO {
	out := make([]jobDTO, len(in))
	for i, j := range in {
		out[i] = toJobDTO(j)
	}
	return out
}

func (s *Server) jobsUnavailable(w http.ResponseWriter) bool {
	if s.jobs == nil {
		apierr.Write(w, http.StatusServiceUnavailable, "unavailable", "Checks are not available.", nil)
		return true
	}
	return false
}

type enqueueRequest struct {
	Kind     string  `json:"kind"`
	ProxyIDs []int64 `json:"proxy_ids"`
	All      bool    `json:"all"`
}

func (s *Server) enqueueJobs(w http.ResponseWriter, r *http.Request) {
	if s.jobsUnavailable(w) {
		return
	}
	var req enqueueRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	fields := map[string]string{}
	kind := domain.CheckKind(req.Kind)
	if kind != domain.CheckAvailability && kind != domain.CheckSpeed {
		fields["kind"] = "Must be availability or speed."
	}
	switch {
	case req.All && len(req.ProxyIDs) > 0:
		fields["proxy_ids"] = "Send either proxy_ids or all, not both."
	case !req.All && len(req.ProxyIDs) == 0:
		fields["proxy_ids"] = "Select at least one proxy, or set all to true."
	case len(req.ProxyIDs) > domain.MaxProxies:
		fields["proxy_ids"] = "At most 100 proxies can be selected."
	}
	ids := make([]domain.ProxyID, 0, len(req.ProxyIDs))
	for _, id := range req.ProxyIDs {
		if id <= 0 {
			fields["proxy_ids"] = "Proxy IDs must be positive."
			break
		}
		ids = append(ids, domain.ProxyID(id))
	}
	if len(fields) > 0 {
		writeValidation(w, fields)
		return
	}

	var (
		res store.EnqueueResult
		err error
	)
	if req.All {
		res, err = s.jobs.EnqueueAll(r.Context(), kind, domain.SourceManual)
	} else {
		res, err = s.jobs.Enqueue(r.Context(), ids, kind, domain.SourceManual)
	}
	if err != nil {
		s.internalError(w, "enqueue jobs", err)
		return
	}
	if !req.All && len(res.New) == 0 && len(res.Existing) == 0 && len(res.Missing) > 0 {
		apierr.Write(w, http.StatusNotFound, "not_found", "Not found.", nil)
		return
	}
	missing := make([]int64, len(res.Missing))
	for i, id := range res.Missing {
		missing[i] = int64(id)
	}
	apierr.JSON(w, http.StatusAccepted, map[string]any{
		"new": toJobDTOs(res.New), "existing": toJobDTOs(res.Existing), "missing_proxy_ids": missing,
	})
}

func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) {
	active, err := s.db.ListActiveJobs(r.Context())
	if err != nil {
		s.internalError(w, "list jobs", err)
		return
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"jobs": toJobDTOs(active)})
}

func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request) {
	if s.jobsUnavailable(w) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		apierr.Write(w, http.StatusNotFound, "not_found", "Not found.", nil)
		return
	}
	job, err := s.jobs.Cancel(r.Context(), id)
	switch {
	case err != nil:
		s.storeError(w, "cancel job", err)
	case job.Status == domain.JobCanceled:
		apierr.JSON(w, http.StatusOK, toJobDTO(job))
	case job.Status.Active():
		// The runner stops it and finishes it as canceled shortly.
		apierr.JSON(w, http.StatusAccepted, toJobDTO(job))
	default:
		apierr.Write(w, http.StatusConflict, "job_finished", "The job has already finished.", nil)
	}
}
