package httpapi

import (
	"net/http"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/go-chi/chi/v5"
)

// contentAccessRoutes registers the administrator content access API
// (G48.1, G48.4): per-user rating ceilings, blocked tags and keywords,
// restricted time windows and item rules, the rating code table and the
// server-wide policy. Every change is audited; the unified storage
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
			BlockedKeywords   []string `json:"blockedKeywords"`
		}
		if err := DecodeJSON(w, r, &input, accountBodyLimit); err != nil {
			return nil, 0, err
		}
		if input.BlockedTags == nil {
			return nil, 0, domain.ErrInvalid
		}
		view, err := s.accounts.SetContentAccess(r.Context(), a, chi.URLParam(r, "id"),
			domain.ContentAccess{ParentalRatingMax: input.ParentalRatingMax, BlockUnrated: input.BlockUnrated, BlockedTags: input.BlockedTags, BlockedKeywords: input.BlockedKeywords})
		return view, 200, err
	}))
	// Restricted time windows (G48.4) are replaced as a whole; an empty
	// array removes them.
	r.Put("/api/v1/users/{id}/content-access/windows", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		var input struct {
			Windows []domain.AccessWindow `json:"windows"`
		}
		if err := DecodeJSON(w, r, &input, accountBodyLimit); err != nil {
			return nil, 0, err
		}
		if input.Windows == nil {
			return nil, 0, domain.ErrInvalid
		}
		view, err := s.accounts.SetAccessWindows(r.Context(), a, chi.URLParam(r, "id"), input.Windows)
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
	r.Put("/api/v1/access/parental-ratings", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		var input struct {
			Ratings []domain.ParentalRating `json:"ratings"`
		}
		if err := DecodeJSON(w, r, &input, accountBodyLimit); err != nil {
			return nil, 0, err
		}
		if input.Ratings == nil {
			return nil, 0, domain.ErrInvalid
		}
		ratings, err := s.accounts.ReplaceParentalRatings(r.Context(), a, input.Ratings)
		return ratings, 200, err
	}))
	s.accessAdminRoutes(r)
}

// accessBulkBodyLimit bounds a bulk grant change or template application:
// up to 200 operations naming 100 users and 1000 libraries each.
const accessBulkBodyLimit = 1 << 20

// WithAccessClock replaces the clock of the request time the unified
// filter decides restricted time windows at (G48.4); tests inject one.
func WithAccessClock(now func() time.Time) Option { return func(s *Server) { s.accessNow = now } }

// accessAdminRoutes registers the G48.7 administration: the grant matrix,
// bulk grant changes, templates and their previews. A request with
// "preview": true writes nothing and returns the same counts an applied
// change audits.
func (s *Server) accessAdminRoutes(r chi.Router) {
	r.Get("/api/v1/access/library-grants", s.accountEndpoint(true, false, func(_ http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		m, err := s.accounts.AccessGrantMatrix(r.Context(), a)
		return m, 200, err
	}))
	r.Post("/api/v1/access/library-grants/bulk", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		var input struct {
			Operations []domain.AccessGrantOperation `json:"operations"`
			Preview    *bool                         `json:"preview"`
		}
		if err := DecodeJSON(w, r, &input, accessBulkBodyLimit); err != nil {
			return nil, 0, err
		}
		if input.Preview == nil {
			return nil, 0, domain.ErrInvalid
		}
		result, err := s.accounts.ApplyAccessGrants(r.Context(), a, input.Operations, *input.Preview)
		return result, 200, err
	}))
	r.Get("/api/v1/access/templates", s.accountEndpoint(true, true, func(_ http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		return memoryList(r, "/api/v1/access/templates", func() (any, error) { return s.accounts.AccessTemplates(r.Context(), a) })
	}))
	r.Post("/api/v1/access/templates", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		in, err := decodeAccessTemplate(w, r)
		if err != nil {
			return nil, 0, err
		}
		t, err := s.accounts.CreateAccessTemplate(r.Context(), a, in)
		return t, 201, err
	}))
	r.Put("/api/v1/access/templates/{id}", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		in, err := decodeAccessTemplate(w, r)
		if err != nil {
			return nil, 0, err
		}
		t, err := s.accounts.UpdateAccessTemplate(r.Context(), a, chi.URLParam(r, "id"), in)
		return t, 200, err
	}))
	r.Delete("/api/v1/access/templates/{id}", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		if err := emptyAccountInput(w, r); err != nil {
			return nil, 0, err
		}
		return nil, 204, s.accounts.DeleteAccessTemplate(r.Context(), a, chi.URLParam(r, "id"))
	}))
	r.Post("/api/v1/access/templates/{id}/apply", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		var input struct {
			UserIDs []string `json:"userIds"`
			Preview *bool    `json:"preview"`
		}
		if err := DecodeJSON(w, r, &input, accountBodyLimit); err != nil {
			return nil, 0, err
		}
		if input.Preview == nil {
			return nil, 0, domain.ErrInvalid
		}
		result, err := s.accounts.ApplyAccessTemplate(r.Context(), a, chi.URLParam(r, "id"), input.UserIDs, *input.Preview)
		return result, 200, err
	}))
}

// decodeAccessTemplate reads a template body: name, libraryIds and
// blockedTags are required, like the restrictions of a user.
func decodeAccessTemplate(w http.ResponseWriter, r *http.Request) (domain.AccessTemplateInput, error) {
	var input struct {
		Name              string   `json:"name"`
		LibraryIDs        []string `json:"libraryIds"`
		ParentalRatingMax *int     `json:"parentalRatingMax"`
		BlockUnrated      *bool    `json:"blockUnrated"`
		BlockedTags       []string `json:"blockedTags"`
		BlockedKeywords   []string `json:"blockedKeywords"`
	}
	if err := DecodeJSON(w, r, &input, accountBodyLimit); err != nil {
		return domain.AccessTemplateInput{}, err
	}
	if input.LibraryIDs == nil || input.BlockedTags == nil {
		return domain.AccessTemplateInput{}, domain.ErrInvalid
	}
	return domain.AccessTemplateInput{Name: input.Name, LibraryIDs: input.LibraryIDs, ContentAccess: domain.ContentAccess{ParentalRatingMax: input.ParentalRatingMax,
		BlockUnrated: input.BlockUnrated, BlockedTags: input.BlockedTags, BlockedKeywords: input.BlockedKeywords}}, nil
}
