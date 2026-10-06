package httpapi

import (
	"net/http"
	"strconv"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/go-chi/chi/v5"
)

// metadataRemoveRoutes does not depend on a configured TMDB key: removal of
// stored provider values must stay possible after the key is withdrawn.
func (s *Server) metadataRemoveRoutes(r chi.Router) {
	r.Delete("/api/v1/items/{id}/metadata/external", s.accountEndpoint(true, true, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		query, err := strictQuery(r, "expectedRevision")
		if err != nil {
			return nil, 0, err
		}
		if err := emptyAccountInput(w, r); err != nil {
			return nil, 0, err
		}
		expected, ok := parseExpectedRevision(query["expectedRevision"])
		if !ok {
			return nil, 0, domain.ErrInvalid
		}
		value, err := s.metadata.RemoveExternal(r.Context(), a, chi.URLParam(r, "id"), expected)
		return value, 200, err
	}))
}

// parseExpectedRevision accepts only canonical positive decimals.
func parseExpectedRevision(raw string) (int64, bool) {
	if raw == "" || raw[0] < '1' || raw[0] > '9' || len(raw) > 19 {
		return 0, false
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || strconv.FormatInt(value, 10) != raw || value >= domain.ItemMetadataRevisionMax {
		return 0, false
	}
	return value, true
}

func metadataRemoveSpecification(paths, schemas map[string]any) {
	applied := schemas["MetadataApplyResult"].(map[string]any)["properties"].(map[string]any)["applied"]
	schemas["MetadataRemoveResult"] = objectSchema(map[string]any{
		"metadata": map[string]any{"$ref": "#/components/schemas/ItemMetadata"},
		"removed":  applied,
		"skipped":  map[string]any{"type": "array", "maxItems": len(domain.ItemMetadataFieldNames()), "items": objectSchema(map[string]any{"field": map[string]any{"type": "string"}, "reason": map[string]any{"type": "string", "enum": []string{"locked", "required"}}}, "field", "reason")},
	}, "metadata", "removed", "skipped")
	op := adminOperation("Remove stored external (TMDB) metadata from one item (administrator)", "200", "400", "401", "403", "404", "408", "409", "503")
	op["security"] = []any{map[string]any{"bearer": []string{}}}
	op["parameters"] = []any{idParameter(), map[string]any{"name": "expectedRevision", "in": "query", "required": true, "description": "Current item metadata revision; a stale value returns 409 without changes.", "schema": map[string]any{"type": "integer", "minimum": 1, "maximum": domain.ItemMetadataRevisionMax - 1}}}
	op["description"] = "G14.7 removal. Deletes every unlocked field value whose source is tmdb, including provider origin and fetch time, in one transaction with one revision increment and one item.external_metadata_removed audit that records removed provider origins but not the removed text. Manual, NFO and existing values are untouched. Locked tmdb fields (Jelee lock or NFO lock intent) are kept and reported as skipped with reason locked; unlock first to remove them. Providers never replace manual, NFO or nonempty local values, so removed fields fall back to absent; a removed title falls back to the local media filename without extension, or the directory name, and is reported as skipped with reason required when no local source exists. NFO values reappear through the next NFO apply. Facts never store provider values. Does not need a TMDB key, call the provider, change item kind, or modify media, NFO or image files. Request body must be empty. A review with nothing to remove still advances the revision."
	op["responses"].(map[string]any)["200"] = map[string]any{"description": "Persisted metadata and removed/skipped report", "content": map[string]any{"application/json": map[string]any{"schema": objectSchema(map[string]any{"data": map[string]any{"$ref": "#/components/schemas/MetadataRemoveResult"}}, "data")}}}
	paths["/api/v1/items/{id}/metadata/external"] = map[string]any{"delete": op}
}
