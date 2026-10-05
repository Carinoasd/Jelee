package httpapi

import (
	"net/http"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/go-chi/chi/v5"
)

// contentAccessRoutes registers the administrator content access API
// (G48.1, G48.4): per-user rating ceilings, blocked tags and item rules,
// and the server-wide policy. Every change is audited; the unified storage
// filter applies it to the next request of the affected user.
func (s *Server) contentAccessRoutes(r chi.Router) {
	r.Get("/api/v1/users/{id}/content-access", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		view, err := s.accounts.ContentAccess(r.Context(), a, chi.URLParam(r, "id"))
		return view, 200, err
	}))
	// The whole restriction set is replaced: an omitted ceiling or unrated
	// override is cleared. Item rules are kept.
	r.Put("/api/v1/users/{id}/content-access", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		var input struct {
			ParentalRatingMax *int     `json:"parentalRatingMax"`
			BlockUnrated      *bool    `json:"blockUnrated"`
			BlockedTags       []string `json:"blockedTags"`
		}
		if err := DecodeJSON(w, r, &input, accountBodyLimit); err != nil {
			return nil, 0, err
		}
		if input.BlockedTags == nil {
			return nil, 0, domain.ErrInvalid
		}
		view, err := s.accounts.SetContentAccess(r.Context(), a, chi.URLParam(r, "id"),
			domain.ContentAccess{ParentalRatingMax: input.ParentalRatingMax, BlockUnrated: input.BlockUnrated, BlockedTags: input.BlockedTags})
		return view, 200, err
	}))
	r.Put("/api/v1/users/{id}/content-access/items/{itemId}", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		var input struct {
			Effect domain.ItemAccessEffect `json:"effect"`
		}
		if err := DecodeJSON(w, r, &input, accountBodyLimit); err != nil {
			return nil, 0, err
		}
		rule, err := s.accounts.SetItemAccessRule(r.Context(), a, chi.URLParam(r, "id"), chi.URLParam(r, "itemId"), input.Effect)
		return rule, 200, err
	}))
	r.Delete("/api/v1/users/{id}/content-access/items/{itemId}", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		if err := emptyAccountInput(w, r); err != nil {
			return nil, 0, err
		}
		return nil, 204, s.accounts.DeleteItemAccessRule(r.Context(), a, chi.URLParam(r, "id"), chi.URLParam(r, "itemId"))
	}))
	r.Get("/api/v1/access/policy", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		policy, err := s.accounts.AccessPolicy(r.Context(), a)
		return policy, 200, err
	}))
	r.Put("/api/v1/access/policy", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		var input struct {
			RestrictAdmins *bool `json:"restrictAdmins"`
			BlockUnrated   *bool `json:"blockUnrated"`
		}
		if err := DecodeJSON(w, r, &input, accountBodyLimit); err != nil {
			return nil, 0, err
		}
		if input.RestrictAdmins == nil || input.BlockUnrated == nil {
			return nil, 0, domain.ErrInvalid
		}
		policy, err := s.accounts.SetAccessPolicy(r.Context(), a, domain.AccessPolicy{RestrictAdmins: *input.RestrictAdmins, BlockUnrated: *input.BlockUnrated})
		return policy, 200, err
	}))
	r.Get("/api/v1/access/parental-ratings", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		ratings, err := s.accounts.ParentalRatings(r.Context(), a)
		return ratings, 200, err
	}))
}
