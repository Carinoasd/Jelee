package httpapi

import (
	"net/http"
	"strings"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/go-chi/chi/v5"
)

// versionBodyLimit bounds version decision and track preference bodies;
// they carry a few identifiers and short tokens.
const versionBodyLimit = 4 << 10

// versionRoutes registers the administrator version decisions (G20.3,
// G20.5) and every user's track preferences (G16.5, G20.4). An item the
// caller may not see is answered like a missing one; decisions refuse a
// non-administrator before any lookup.
func (s *Server) versionRoutes(r chi.Router) {
	r.Get("/api/v1/items/{id}/versions", s.accountEndpoint(true, false, func(_ http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		view, err := s.catalog.VersionOverview(r.Context(), a, chi.URLParam(r, "id"))
		return view, 200, s.hiddenContentError(err)
	}))
	r.Post("/api/v1/items/{id}/versions/split", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		var input domain.SplitVersionInput
		if err := DecodeJSON(w, r, &input, versionBodyLimit); err != nil {
			return nil, 0, err
		}
		op, err := s.catalog.SplitVersion(r.Context(), a, chi.URLParam(r, "id"), input)
		return op, 201, s.hiddenContentError(err)
	}))
	r.Post("/api/v1/items/{id}/versions/merge", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		var input struct {
			SourceItemID string `json:"sourceItemId"`
		}
		if err := DecodeJSON(w, r, &input, versionBodyLimit); err != nil {
			return nil, 0, err
		}
		op, err := s.catalog.MergeItems(r.Context(), a, chi.URLParam(r, "id"), input.SourceItemID)
		return op, 201, s.hiddenContentError(err)
	}))
	r.Put("/api/v1/items/{id}/versions/primary", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		var input struct {
			SourceID *string `json:"sourceId"`
		}
		if err := decodeJSONNullable(w, r, &input, versionBodyLimit, func(path string) bool { return path == "/SOURCEID" }); err != nil {
			return nil, 0, err
		}
		source := ""
		if input.SourceID != nil {
			if *input.SourceID == "" {
				return nil, 0, domain.ErrInvalid
			}
			source = *input.SourceID
		}
		op, err := s.catalog.SetPrimaryVersion(r.Context(), a, chi.URLParam(r, "id"), source)
		return op, 200, s.hiddenContentError(err)
	}))
	r.Delete("/api/v1/items/{id}/versions/exclusions/{exclusionId}", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		if err := emptyAccountInput(w, r); err != nil {
			return nil, 0, err
		}
		op, err := s.catalog.RemoveVersionExclusion(r.Context(), a, chi.URLParam(r, "id"), chi.URLParam(r, "exclusionId"))
		return op, 200, s.hiddenContentError(err)
	}))
	r.Post("/api/v1/version-operations/{id}/undo", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		if err := emptyAccountInput(w, r); err != nil {
			return nil, 0, err
		}
		op, err := s.catalog.UndoVersionOperation(r.Context(), a, chi.URLParam(r, "id"))
		return op, 200, s.hiddenContentError(err)
	}))

	r.Get("/api/v1/items/{id}/track-preferences", s.accountEndpoint(false, false, func(_ http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		view, err := s.catalog.TrackPreferences(r.Context(), a, chi.URLParam(r, "id"))
		return view, 200, s.hiddenContentError(err)
	}))
	r.Put("/api/v1/items/{id}/track-preferences", s.accountEndpoint(false, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		var input struct {
			SourceID *string `json:"sourceId"`
			domain.TrackPreference
		}
		if err := decodeJSONNullable(w, r, &input, versionBodyLimit, topLevelNullable); err != nil {
			return nil, 0, err
		}
		source := ""
		if input.SourceID != nil {
			if *input.SourceID == "" {
				return nil, 0, domain.ErrInvalid
			}
			source = *input.SourceID
		}
		view, err := s.catalog.SetTrackPreference(r.Context(), a, chi.URLParam(r, "id"), source, input.TrackPreference)
		return view, 200, s.hiddenContentError(err)
	}))
	r.Get("/api/v1/users/me/track-preferences", s.accountEndpoint(false, false, func(_ http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		p, err := s.catalog.UserTrackPreference(r.Context(), a)
		return map[string]any{"preference": p}, 200, err
	}))
	r.Put("/api/v1/users/me/track-preferences", s.accountEndpoint(false, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		var input domain.TrackPreference
		if err := decodeJSONNullable(w, r, &input, versionBodyLimit, topLevelNullable); err != nil {
			return nil, 0, err
		}
		p, err := s.catalog.SetUserTrackPreference(r.Context(), a, input)
		return map[string]any{"preference": p}, 200, err
	}))
}

// topLevelNullable accepts null for every member of the body object: a
// null member inherits like an absent one.
func topLevelNullable(path string) bool { return strings.Count(path, "/") == 1 }
