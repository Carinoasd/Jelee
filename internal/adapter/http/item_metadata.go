package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/go-chi/chi/v5"
)

func (s *Server) itemMetadataRoutes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(s.accountBudget, s.authenticate)
		if s.cfg.TMDBAPIKey != "" {
			s.metadataApplyRoutes(r)
		}
		path := "/api/v1/items/{id}/metadata"
		r.Get(path, s.accountEndpoint(true, false, func(_ http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
			value, err := s.metadata.ItemFields(r.Context(), a, chi.URLParam(r, "id"))
			return value, 200, err
		}))
		r.Put(path, s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
			var input struct {
				ExpectedRevision int64 `json:"expectedRevision"`
				Fields           []struct {
					Field  string          `json:"field"`
					Value  json.RawMessage `json:"value"`
					Locked json.RawMessage `json:"locked"`
				} `json:"fields"`
			}
			if err := DecodeJSON(w, r, &input, 32<<10); err != nil {
				return nil, 0, err
			}
			patches := make([]domain.ItemMetadataPatch, 0, len(input.Fields))
			for _, raw := range input.Fields {
				patch := domain.ItemMetadataPatch{Field: raw.Field}
				if raw.Value != nil {
					var value *string
					if json.Unmarshal(raw.Value, &value) != nil || value == nil {
						return nil, 0, domain.ErrInvalid
					}
					patch.Value = value
				}
				if raw.Locked != nil {
					var locked *bool
					if json.Unmarshal(raw.Locked, &locked) != nil || locked == nil {
						return nil, 0, domain.ErrInvalid
					}
					patch.Locked = locked
				}
				patches = append(patches, patch)
			}
			value, err := s.metadata.UpdateItemFields(r.Context(), a, chi.URLParam(r, "id"), input.ExpectedRevision, patches)
			return value, 200, err
		}))
	})
}

func itemMetadataSpecification(paths, schemas map[string]any) {
	field := map[string]any{"type": "string", "enum": []string{"title", "originalTitle", "overview", "date"}}
	schemas["ItemMetadataField"] = objectSchema(map[string]any{"field": field, "value": map[string]any{"type": "string", "maxLength": 16384}, "source": map[string]any{"type": "string", "enum": []string{"existing", "manual"}}, "locked": map[string]any{"type": "boolean"}, "updatedAt": map[string]any{"type": []string{"string", "null"}, "format": "date-time"}}, "field", "value", "source", "locked", "updatedAt")
	schemas["ItemMetadata"] = objectSchema(map[string]any{"itemId": map[string]any{"type": "string", "format": "uuid"}, "libraryId": map[string]any{"type": "string", "format": "uuid"}, "revision": map[string]any{"type": "integer", "minimum": 1, "maximum": domain.ItemMetadataRevisionMax}, "fields": map[string]any{"type": "array", "minItems": 1, "maxItems": 4, "items": map[string]any{"$ref": "#/components/schemas/ItemMetadataField"}}}, "itemId", "libraryId", "revision", "fields")
	get := operation("Read item metadata and field locks (administrator)", "200", "400", "401", "403", "404", "408", "503")
	put := operation("Manually update item metadata and locks with revision check (administrator)", "200", "400", "401", "403", "404", "408", "409", "503")
	for _, op := range []map[string]any{get, put} {
		op["security"] = []any{map[string]any{"bearer": []string{}}}
		op["parameters"] = []any{idParameter()}
		op["responses"].(map[string]any)["200"] = map[string]any{"description": "Persisted fields, source, locks and current revision. Original files are unchanged.", "content": map[string]any{"application/json": map[string]any{"schema": objectSchema(map[string]any{"data": map[string]any{"$ref": "#/components/schemas/ItemMetadata"}}, "data")}}}
	}
	patch := objectSchema(map[string]any{"field": field, "value": map[string]any{"type": "string", "maxLength": 16384}, "locked": map[string]any{"type": "boolean"}}, "field")
	patch["anyOf"] = []any{map[string]any{"required": []string{"value"}}, map[string]any{"required": []string{"locked"}}}
	put["requestBody"] = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": objectSchema(map[string]any{"expectedRevision": map[string]any{"type": "integer", "minimum": 1, "maximum": domain.ItemMetadataRevisionMax - 1}, "fields": map[string]any{"type": "array", "minItems": 1, "maxItems": 4, "items": patch}}, "expectedRevision", "fields")}}}
	put["description"] = "Explicit manual edits may change locked fields and lock flags. Omitted value/locked preserves it; an empty optional value is a recorded manual clear. JSON null, repeated fields, unknown properties and invalid dates are rejected. Title must be nonblank; title/originalTitle 1024 bytes, overview 16384 bytes, date empty or a real YYYY-MM-DD. No provider calls or original asset writes."
	paths["/api/v1/items/{id}/metadata"] = map[string]any{"get": get, "put": put}
}
