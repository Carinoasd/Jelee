package httpapi

import (
	"net/http"
	"strconv"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/go-chi/chi/v5"
)

type ScanRequestBody struct {
	Priority string `json:"priority"`
	Probe    bool   `json:"probe"`
	NFO      bool   `json:"nfo"`
}

func (s *Server) nfoRoutes(r chi.Router) {
	r.Get("/api/v1/libraries/{id}/nfo/policy", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		policy, err := s.jobs.NFOPolicy(r.Context(), a, chi.URLParam(r, "id"))
		return policy, http.StatusOK, err
	}))
	r.Put("/api/v1/libraries/{id}/nfo/policy", s.accountEndpoint(true, false, s.setNFOPolicy))
	r.Post("/api/v1/libraries/{id}/nfo/validate", s.accountEndpoint(true, false, s.validateLibraryNFO))
	r.Get("/api/v1/jobs/{id}/nfo", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		summary, err := s.jobs.NFOSummary(r.Context(), a, chi.URLParam(r, "id"))
		return summary, http.StatusOK, err
	}))
	r.Get("/api/v1/jobs/{id}/images", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		summary, err := s.jobs.Images(r.Context(), a, chi.URLParam(r, "id"))
		return summary, http.StatusOK, err
	}))
	r.Get("/api/v1/libraries/{id}/nfo/current-validations", s.accountEndpoint(true, true, s.listNFOObservations))
	r.Get("/api/v1/libraries/{id}/nfo/current-validations/{observationId}/issues", s.accountEndpoint(true, true, s.listNFOIssues))
}

func (s *Server) setNFOPolicy(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
	var input struct {
		Mode               string `json:"mode"`
		ExpectedGeneration int64  `json:"expectedGeneration"`
	}
	if err := DecodeJSON(w, r, &input, accountBodyLimit); err != nil {
		return nil, 0, err
	}
	key, err := jobKey(r)
	if err != nil {
		return nil, 0, err
	}
	policy, replay, err := s.jobs.SetNFOPolicy(r.Context(), a, chi.URLParam(r, "id"), key, input.ExpectedGeneration, input.Mode)
	if err == nil && replay {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	return policy, http.StatusOK, err
}

func (s *Server) validateLibraryNFO(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
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
	job, replay, err := s.jobs.SubmitScanStages(r.Context(), a, chi.URLParam(r, "id"), key, input.Priority, false, true)
	return acceptedJob(w, job, replay, err)
}

func (s *Server) listNFOObservations(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
	query, err := strictQuery(r, "cursor", "limit")
	if err != nil {
		return nil, 0, err
	}
	limit := domain.NFOObservationPageDefault
	if value, exists := query["limit"]; exists {
		limit, err = strconv.Atoi(value)
		if err != nil {
			return nil, 0, domain.ErrInvalid
		}
	}
	page, err := s.jobs.NFOObservations(r.Context(), a, chi.URLParam(r, "id"), query["cursor"], limit)
	return page, http.StatusOK, err
}

func (s *Server) listNFOIssues(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
	query, err := strictQuery(r, "offset", "limit")
	if err != nil {
		return nil, 0, err
	}
	offset, limit := 0, domain.NFOIssuesPageMax
	if value, exists := query["offset"]; exists {
		offset, err = strconv.Atoi(value)
		if err != nil {
			return nil, 0, domain.ErrInvalid
		}
	}
	if value, exists := query["limit"]; exists {
		limit, err = strconv.Atoi(value)
		if err != nil {
			return nil, 0, domain.ErrInvalid
		}
	}
	page, err := s.jobs.NFOIssues(r.Context(), a, chi.URLParam(r, "id"), chi.URLParam(r, "observationId"), offset, limit)
	return page, http.StatusOK, err
}
