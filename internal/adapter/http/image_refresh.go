package httpapi

import (
	"net/http"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/go-chi/chi/v5"
)

// imageRefreshRoutes mounts image refresh jobs (G40.9, G40.10). Without
// remote image fetching or an image store a submission answers 404
// image_unavailable; earlier jobs stay readable.
func (s *Server) imageRefreshRoutes(r chi.Router) {
	r.Post("/api/v1/libraries/{id}/images/refresh", s.accountEndpoint(true, false, s.refreshLibraryImages))
	r.Get("/api/v1/jobs/{id}/image-refresh", s.accountEndpoint(true, false, func(_ http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		state, err := s.jobs.ImageRefreshState(r.Context(), a, chi.URLParam(r, "id"))
		return state, http.StatusOK, err
	}))
}

func (s *Server) refreshLibraryImages(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
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
	job, replay, err := s.jobs.SubmitImageRefresh(r.Context(), a, chi.URLParam(r, "id"), key, input.Priority)
	return acceptedJob(w, job, replay, err)
}
