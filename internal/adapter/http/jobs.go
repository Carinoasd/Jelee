package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/go-chi/chi/v5"
)

func (s *Server) jobBudget(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case s.jobSlots <- struct{}{}:
			defer func() { <-s.jobSlots }()
		case <-r.Context().Done():
			WriteError(w, r, r.Context().Err())
			return
		default:
			w.Header().Set("Retry-After", "1")
			writeProblem(w, r, 503, "jobs_busy", "Job service is busy. Try again later.")
			return
		}
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(s.cfg.RequestTimeout()))
		next.ServeHTTP(w, r)
	})
}

func (s *Server) jobRoutes(router chi.Router) {
	router.Group(func(r chi.Router) {
		r.Use(s.jobBudget, s.authenticate)
		s.nfoRoutes(r)
		r.Get("/api/v1/jobs/{id}/ignore", s.accountEndpoint(true, true, s.ignoreReport))
		r.Get("/api/v1/libraries", s.accountEndpoint(true, true, s.listJobLibraries))
		r.Post("/api/v1/libraries/{id}/scan", s.accountEndpoint(true, false, s.submitScan))
		r.Post("/api/v1/libraries/{id}/probe/rebuild", s.accountEndpoint(true, false, s.rebuildLibraryProbe))
		r.Post("/api/v1/items/{id}/probe/rebuild", s.accountEndpoint(true, false, s.rebuildItemProbe))
		r.Get("/api/v1/jobs/{id}/probe", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
			summary, err := s.jobs.ProbeSummary(r.Context(), a, chi.URLParam(r, "id"))
			return summary, 200, err
		}))
		r.Get("/api/v1/jobs", s.accountEndpoint(true, true, s.listJobs))
		r.Get("/api/v1/jobs/{id}", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
			j, err := s.jobs.Get(r.Context(), a, chi.URLParam(r, "id"))
			return j, 200, err
		}))
		r.Get("/api/v1/jobs/{id}/entries", s.accountEndpoint(true, true, s.listJobEntries))
		r.Post("/api/v1/jobs/{id}/cancel", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
			if err := emptyAccountInput(w, r); err != nil {
				return nil, 0, err
			}
			j, err := s.jobs.Cancel(r.Context(), a, chi.URLParam(r, "id"))
			return j, 200, err
		}))
		r.Post("/api/v1/jobs/{id}/retry", s.accountEndpoint(true, false, s.retryJob))
	})
}

func jobKey(r *http.Request) (string, error) {
	keys := r.Header.Values("Idempotency-Key")
	if len(keys) != 1 {
		return "", domain.ErrInvalid
	}
	return keys[0], nil
}
func acceptedJob(w http.ResponseWriter, j domain.Job, replayed bool, err error) (any, int, error) {
	status := 202
	if replayed {
		status = 200
		w.Header().Set("Idempotency-Replayed", "true")
	}
	if err == nil {
		w.Header().Set("Location", "/api/v1/jobs/"+j.ID)
	}
	return j, status, err
}
func (s *Server) submitScan(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
	var input ScanRequestBody
	if err := DecodeJSON(w, r, &input, accountBodyLimit); err != nil {
		return nil, 0, err
	}
	key, err := jobKey(r)
	if err != nil {
		return nil, 0, err
	}
	if input.Priority == "" {
		input.Priority = domain.JobPriorityManual
	}
	ignore := domain.IgnoreIntent{}
	if input.Ignore != nil {
		ignore = domain.IgnoreIntent{Mode: input.Ignore.Mode, CaseMode: input.Ignore.CaseMode}
		if ignore.Mode == "" || domain.ValidateIgnoreIntent(ignore) != nil {
			return nil, 0, domain.ErrInvalid
		}
	}
	j, replayed, err := s.jobs.SubmitScanOptions(r.Context(), a, chi.URLParam(r, "id"), key, input.Priority, input.Probe, input.NFO, ignore)
	return acceptedJob(w, j, replayed, err)
}

func (s *Server) rebuildLibraryProbe(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
	return s.rebuildProbe(w, r, a, false)
}
func (s *Server) rebuildItemProbe(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
	return s.rebuildProbe(w, r, a, true)
}
func (s *Server) rebuildProbe(w http.ResponseWriter, r *http.Request, a domain.Actor, item bool) (any, int, error) {
	var input struct {
		Priority string `json:"priority"`
	}
	if err := DecodeJSON(w, r, &input, accountBodyLimit); err != nil {
		return nil, 0, err
	}
	key, err := jobKey(r)
	if err != nil {
		return nil, 0, err
	}
	if input.Priority == "" {
		input.Priority = domain.JobPriorityManual
	}
	job, replayed, err := s.jobs.RebuildProbe(r.Context(), a, chi.URLParam(r, "id"), key, input.Priority, item)
	return acceptedJob(w, job, replayed, err)
}
func (s *Server) retryJob(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
	if err := emptyAccountInput(w, r); err != nil {
		return nil, 0, err
	}
	key, err := jobKey(r)
	if err != nil {
		return nil, 0, err
	}
	j, replayed, err := s.jobs.Retry(r.Context(), a, chi.URLParam(r, "id"), key)
	return acceptedJob(w, j, replayed, err)
}
func jobPageQuery(r *http.Request, extra ...string) (map[string]string, int, error) {
	keys := append([]string{"cursor", "limit"}, extra...)
	q, err := strictQuery(r, keys...)
	if err != nil {
		return nil, 0, err
	}
	limit := 50
	if v, ok := q["limit"]; ok {
		limit, err = strconv.Atoi(v)
		if err != nil {
			return nil, 0, domain.ErrInvalid
		}
	}
	return q, limit, nil
}
func pageResult(name string, data any, lastID string, length, limit int) map[string]any {
	next := ""
	if length == limit {
		next = lastID
	}
	return map[string]any{name: data, "pagination": map[string]any{"nextCursor": next, "limit": limit}}
}
func (s *Server) listJobs(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
	q, limit, err := jobPageQuery(r, "state")
	if err != nil {
		return nil, 0, err
	}
	rows, err := s.jobs.List(r.Context(), a, q["cursor"], limit, q["state"])
	if err != nil {
		return nil, 0, err
	}
	last := ""
	if len(rows) > 0 {
		last = rows[len(rows)-1].ID
	}
	return pageResult("jobs", rows, last, len(rows), limit), 200, nil
}
func (s *Server) listJobEntries(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
	q, limit, err := jobPageQuery(r)
	if err != nil {
		return nil, 0, err
	}
	rows, err := s.jobs.Entries(r.Context(), a, chi.URLParam(r, "id"), q["cursor"], limit)
	if err != nil {
		return nil, 0, err
	}
	last := ""
	if len(rows) > 0 {
		last = rows[len(rows)-1].ID
	}
	return pageResult("entries", rows, last, len(rows), limit), 200, nil
}
func (s *Server) listJobLibraries(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
	q, limit, err := jobPageQuery(r)
	if err != nil {
		return nil, 0, err
	}
	rows, err := s.jobs.Libraries(r.Context(), a, q["cursor"], limit)
	if err != nil {
		return nil, 0, err
	}
	last := ""
	if len(rows) > 0 {
		last = rows[len(rows)-1].ID
	}
	return pageResult("libraries", rows, last, len(rows), limit), 200, nil
}

func (s *Server) ignoreReport(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
	q, limit, err := jobPageQuery(r)
	if err != nil {
		return nil, 0, err
	}
	report, err := s.jobs.IgnoreReport(r.Context(), a, chi.URLParam(r, "id"), limit, q["cursor"])
	return report, http.StatusOK, err
}
