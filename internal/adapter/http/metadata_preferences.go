package httpapi

import (
	"net/http"
	"strings"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/go-chi/chi/v5"
)

func (s *Server) metadataPreferenceRoutes(r chi.Router) {
	path := "/api/v1/libraries/{id}/metadata-preferences"
	r.Get(path, s.accountEndpoint(true, false, func(_ http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		value, err := s.metadata.LibraryPreferences(r.Context(), a, chi.URLParam(r, "id"))
		return value, 200, err
	}))
	r.Put(path, s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		var input struct {
			Language         string `json:"language"`
			ExpectedRevision int64  `json:"expectedRevision"`
		}
		if err := DecodeJSON(w, r, &input, accountBodyLimit); err != nil {
			return nil, 0, err
		}
		value, err := s.metadata.UpdateLibraryPreferences(r.Context(), a, chi.URLParam(r, "id"), input.Language, input.ExpectedRevision)
		return value, 200, err
	}))
}

func metadataPreferencesSpecification(paths, schemas map[string]any) {
	schemas["MetadataPreferences"] = objectSchema(map[string]any{"libraryId": map[string]any{"type": "string", "format": "uuid"}, "language": map[string]any{"type": "string", "enum": []string{"zh-CN", "zh-TW", "ja-JP", "en-US"}}, "revision": map[string]any{"type": "integer", "minimum": 1, "maximum": 2147483647}}, "libraryId", "language", "revision")
	get := operation("Read library metadata language (administrator)", "200", "400", "401", "403", "404", "408", "503")
	put := operation("Update library metadata language with revision check (administrator)", "200", "400", "401", "403", "404", "408", "409", "503")
	for _, op := range []map[string]any{get, put} {
		op["security"] = []any{map[string]any{"bearer": []string{}}}
		op["parameters"] = []any{idParameter()}
		op["responses"].(map[string]any)["200"] = map[string]any{"description": "Library preference and current revision", "content": map[string]any{"application/json": map[string]any{"schema": objectSchema(map[string]any{"data": map[string]any{"$ref": "#/components/schemas/MetadataPreferences"}}, "data")}}}
	}
	put["requestBody"] = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": objectSchema(map[string]any{"language": map[string]any{"type": "string", "enum": []string{"zh-CN", "zh-TW", "ja-JP", "en-US"}}, "expectedRevision": map[string]any{"type": "integer", "minimum": 1, "maximum": 2147483646}}, "language", "expectedRevision")}}}
	paths["/api/v1/libraries/{id}/metadata-preferences"] = map[string]any{"get": get, "put": put}
	for path, methods := range paths {
		if !strings.HasPrefix(path, "/api/v1/metadata/tmdb/") {
			continue
		}
		for _, entry := range methods.(map[string]any) {
			op := entry.(map[string]any)
			params, ok := op["parameters"].([]any)
			if !ok {
				continue
			}
			for _, item := range params {
				parameter := item.(map[string]any)
				if parameter["name"] == "language" {
					parameter["description"] = "Explicit administrator override; omitted uses the selected library language, then authenticated user locale, then zh-CN."
					op["parameters"] = append(params, map[string]any{"name": "libraryId", "in": "query", "description": "Optional existing library; administrator session is rechecked before any provider request.", "schema": map[string]any{"type": "string", "format": "uuid"}})
					break
				}
			}
		}
	}
}
