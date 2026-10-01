package httpapi

import (
	"net/http"
	"strconv"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/go-chi/chi/v5"
)

func (s *Server) metadataRoutes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(s.accountBudget, s.authenticate)
		r.Get("/api/v1/metadata/tmdb/movies/{id}", s.accountEndpoint(true, true, func(w http.ResponseWriter, r *http.Request, _ domain.Actor) (any, int, error) {
			query, err := strictQuery(r, "language")
			if err != nil {
				return nil, 0, err
			}
			language, specified := query["language"]
			if !specified {
				language = "zh-CN"
			}
			raw := chi.URLParam(r, "id")
			id, err := strconv.ParseInt(raw, 10, 32)
			if err != nil || id <= 0 || raw != strconv.FormatInt(id, 10) || !domain.ValidMetadataLanguage(language) {
				return nil, 0, domain.ErrInvalid
			}
			movie, err := s.metadata.Movie(r.Context(), int32(id), language)
			return movie, http.StatusOK, err
		}))
	})
}

func metadataSpecification(paths, schemas map[string]any) {
	op := operation("Preview a TMDB movie candidate (administrator)", "200", "400", "401", "403", "404", "408", "503")
	op["security"] = []any{map[string]any{"bearer": []string{}}}
	op["parameters"] = []any{
		map[string]any{"name": "id", "in": "path", "required": true, "schema": map[string]any{"type": "integer", "format": "int32", "minimum": 1, "maximum": 2147483647}},
		map[string]any{"name": "language", "in": "query", "schema": map[string]any{"type": "string", "enum": []string{"zh-CN", "zh-TW", "ja-JP", "en-US"}, "default": "zh-CN"}},
	}
	responses := op["responses"].(map[string]any)
	responses["200"] = map[string]any{"description": "Provider data for review; no library or original asset changes.", "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"type": "object", "required": []string{"data"}, "properties": map[string]any{"data": map[string]any{"$ref": "#/components/schemas/MovieCandidate"}}}}}}
	paths["/api/v1/metadata/tmdb/movies/{id}"] = map[string]any{"get": op}
	schemas["MovieCandidate"] = map[string]any{"type": "object", "additionalProperties": false, "required": []string{"providerId", "source", "sourceUrl", "language", "fetchedAt", "title", "originalTitle", "overview", "releaseDate"}, "properties": map[string]any{
		"providerId":    map[string]any{"type": "integer", "format": "int32", "minimum": 1},
		"source":        map[string]any{"type": "string", "const": "TMDB"},
		"sourceUrl":     map[string]any{"type": "string", "format": "uri"},
		"language":      map[string]any{"type": "string", "enum": []string{"zh-CN", "zh-TW", "ja-JP", "en-US"}},
		"fetchedAt":     map[string]any{"type": "string", "format": "date-time"},
		"title":         map[string]any{"type": "string", "maxLength": 1024},
		"originalTitle": map[string]any{"type": "string", "maxLength": 1024},
		"overview":      map[string]any{"type": "string", "maxLength": 16384},
		"releaseDate":   map[string]any{"type": "string", "pattern": "^([0-9]{4}-[0-9]{2}-[0-9]{2})?$"},
	}}
}
