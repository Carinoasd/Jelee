package httpapi

import (
	"net/http"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/go-chi/chi/v5"
)

// catalogNamingRoutes exposes the per-library naming options (G21.3).
func (s *Server) catalogNamingRoutes(r chi.Router) {
	naming := "/api/v1/libraries/{id}/naming"
	r.Get(naming, s.accountEndpoint(true, false, func(_ http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		v, err := s.jobs.CatalogNaming(r.Context(), a, chi.URLParam(r, "id"))
		return v, 200, err
	}))
	r.Put(naming, s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		var input struct {
			DateEpisodes     *bool `json:"dateEpisodes"`
			AbsoluteEpisodes *bool `json:"absoluteEpisodes"`
		}
		if err := DecodeJSON(w, r, &input, accountBodyLimit); err != nil {
			return nil, 0, err
		}
		// A full replacement: an omitted option would silently reset it.
		if input.DateEpisodes == nil || input.AbsoluteEpisodes == nil {
			return nil, 0, domain.ErrInvalid
		}
		v, err := s.jobs.PutCatalogNaming(r.Context(), a, chi.URLParam(r, "id"), domain.CatalogNamingOptions{DateEpisodes: *input.DateEpisodes, AbsoluteEpisodes: *input.AbsoluteEpisodes})
		return v, 200, err
	}))
}
