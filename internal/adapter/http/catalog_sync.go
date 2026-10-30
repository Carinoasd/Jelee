package httpapi

import (
	"bytes"
	"encoding/json"
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
		q, err := listQuery(r, "/api/v1/libraries/{id}/catalog-sync/pending")
		if err != nil {
			return nil, 0, err
		}
		rows, next, err := keysetPage(q, func(cursor string, limit int) ([]domain.CatalogPendingEntry, string, error) {
			rows, err := s.jobs.CatalogPending(r.Context(), a, chi.URLParam(r, "id"), cursor, limit)
			return rows, nextKey(rows, limit, func(e domain.CatalogPendingEntry) string { return e.ID }), err
		})
		if err != nil {
			return nil, 0, err
		}
		return listData(q, pageResult("entries", rows, next, q.Limit))
	}))
	pending := "/api/v1/libraries/{id}/catalog-sync/pending/{entry}"
	r.Post(pending+"/accept", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		var input domain.CatalogPendingDecision
		if err := DecodeJSON(w, r, &input, accountBodyLimit); err != nil {
			return nil, 0, err
		}
		v, err := s.jobs.AcceptCatalogPending(r.Context(), a, chi.URLParam(r, "id"), chi.URLParam(r, "entry"), input)
		return v, 200, err
	}))
	r.Post(pending+"/reject", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		if err := emptyAccountInput(w, r); err != nil {
			return nil, 0, err
		}
		v, err := s.jobs.RejectCatalogPending(r.Context(), a, chi.URLParam(r, "id"), chi.URLParam(r, "entry"))
		return v, 200, err
	}))
	identity := "/api/v1/items/{id}/identity"
	r.Get(identity, s.accountEndpoint(true, false, func(_ http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		v, err := s.jobs.ItemIdentity(r.Context(), a, chi.URLParam(r, "id"))
		return v, 200, err
	}))
	r.Put(identity, s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		var input struct {
			ExpectedRevision int64 `json:"expectedRevision"`
			Fields           []struct {
				Field    string          `json:"field"`
				Value    json.RawMessage `json:"value"`
				ValueEnd *int            `json:"valueEnd"`
				Locked   *bool           `json:"locked"`
			} `json:"fields"`
		}
		// Only a parent value may be null: it removes the parent.
		if err := decodeJSONNullable(w, r, &input, accountBodyLimit, func(path string) bool { return path == "/FIELDS/*/VALUE" }); err != nil {
			return nil, 0, err
		}
		patches := make([]domain.ItemIdentityPatch, 0, len(input.Fields))
		for _, raw := range input.Fields {
			patch, err := identityPatch(raw.Field, raw.Value, raw.ValueEnd, raw.Locked)
			if err != nil {
				return nil, 0, err
			}
			patches = append(patches, patch)
		}
		v, err := s.jobs.UpdateItemIdentity(r.Context(), a, chi.URLParam(r, "id"), input.ExpectedRevision, patches)
		return v, 200, err
	}))
}

// identityPatch decodes one typed field value: a kind name, a parent ID or
// null, or a season or episode number with an optional episode range end.
func identityPatch(field string, value json.RawMessage, end *int, locked *bool) (domain.ItemIdentityPatch, error) {
	patch := domain.ItemIdentityPatch{Field: field, Locked: locked, HasValue: value != nil}
	if end != nil && (field != "episode" || value == nil) {
		return patch, domain.ErrInvalid
	}
	if value == nil {
		return patch, nil
	}
	null := bytes.Equal(bytes.TrimSpace(value), []byte("null"))
	switch field {
	case "kind":
		if null || json.Unmarshal(value, &patch.Kind) != nil {
			return patch, domain.ErrInvalid
		}
	case "parent":
		if !null && (json.Unmarshal(value, &patch.Parent) != nil || patch.Parent == "") {
			return patch, domain.ErrInvalid
		}
	case "season", "episode":
		if null || json.Unmarshal(value, &patch.Number) != nil {
			return patch, domain.ErrInvalid
		}
		if field == "episode" {
			patch.NumberEnd = patch.Number
			if end != nil {
				patch.NumberEnd = *end
			}
		}
	default:
		return patch, domain.ErrInvalid
	}
	return patch, nil
}
