package httpapi

import (
	"net/http"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/go-chi/chi/v5"
)

func (s *Server) catalogSyncRoutes(r chi.Router) {
	r.Post("/api/v1/jobs/{id}/accept-missing", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		var input struct {
			ExpectedMissing *int64 `json:"expectedMissing"`
		}
		if err := DecodeJSON(w, r, &input, accountBodyLimit); err != nil {
			return nil, 0, err
		}
		if input.ExpectedMissing == nil {
			return nil, 0, domain.ErrInvalid
		}
		job, err := s.jobs.AcceptMissing(r.Context(), a, chi.URLParam(r, "id"), *input.ExpectedMissing)
		return acceptedJob(w, job, false, err)
	}))
	r.Get("/api/v1/jobs/{id}/catalog-sync", s.accountEndpoint(true, false, func(_ http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		v, err := s.jobs.CatalogSyncReport(r.Context(), a, chi.URLParam(r, "id"))
		return v, 200, err
	}))
	r.Post("/api/v1/libraries/{id}/catalog-sync", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		var input struct {
			Priority string `json:"priority"`
		}
		if err := DecodeJSON(w, r, &input, accountBodyLimit); err != nil {
			return nil, 0, err
		}
		if input.Priority == "" {
			input.Priority = domain.JobPriorityManual
		}
		key, err := jobKey(r)
		if err != nil {
			return nil, 0, err
		}
		job, replay, err := s.jobs.SubmitCatalogSync(r.Context(), a, chi.URLParam(r, "id"), key, input.Priority)
		return acceptedJob(w, job, replay, err)
	}))
	r.Get("/api/v1/libraries/{id}/catalog-sync", s.accountEndpoint(true, false, func(_ http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		v, err := s.jobs.CatalogSyncSettings(r.Context(), a, chi.URLParam(r, "id"))
		return v, 200, err
	}))
	r.Put("/api/v1/libraries/{id}/catalog-sync", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		var input struct {
			Auto *bool `json:"auto"`
		}
		if err := DecodeJSON(w, r, &input, accountBodyLimit); err != nil {
			return nil, 0, err
		}
		if input.Auto == nil {
			return nil, 0, domain.ErrInvalid
		}
		v, err := s.jobs.PutCatalogSyncSettings(r.Context(), a, chi.URLParam(r, "id"), *input.Auto)
		return v, 200, err
	}))
	r.Get("/api/v1/libraries/{id}/catalog-sync/pending", s.accountEndpoint(true, true, func(_ http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		q, limit, err := jobPageQuery(r)
		if err != nil {
			return nil, 0, err
		}
		rows, err := s.jobs.CatalogPending(r.Context(), a, chi.URLParam(r, "id"), q["cursor"], limit)
		if err != nil {
			return nil, 0, err
		}
		last := ""
		if len(rows) > 0 {
			last = rows[len(rows)-1].ID
		}
		return pageResult("entries", rows, last, len(rows), limit), 200, nil
	}))
}
