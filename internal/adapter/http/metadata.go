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
		s.seriesRoutes(r)
		s.episodeRoutes(r)
		r.Get("/api/v1/metadata/tmdb/movies", s.accountEndpoint(true, true, func(w http.ResponseWriter, r *http.Request, _ domain.Actor) (any, int, error) {
			query, err := strictQuery(r, "query", "year", "language")
			if err != nil {
				return nil, 0, err
			}
			language := metadataRequestLanguage(r, query)
			year := 0
			if raw, exists := query["year"]; exists {
				parsed, err := strconv.ParseInt(raw, 10, 32)
				if err != nil || len(raw) != 4 || raw != strconv.FormatInt(parsed, 10) {
					return nil, 0, domain.ErrInvalid
				}
				year = int(parsed)
			}
			matches, err := s.metadata.SearchMovies(r.Context(), domain.MovieSearchInput{Query: query["query"], Year: year, Language: language})
			return matches, http.StatusOK, err
		}))
		r.Get("/api/v1/metadata/tmdb/movies/{id}", s.accountEndpoint(true, true, func(w http.ResponseWriter, r *http.Request, _ domain.Actor) (any, int, error) {
			query, err := strictQuery(r, "language")
			if err != nil {
				return nil, 0, err
			}
			language := metadataRequestLanguage(r, query)
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
	search := operation("Search TMDB movie candidates for administrator confirmation", "200", "400", "401", "403", "408", "503")
	search["security"] = []any{map[string]any{"bearer": []string{}}}
	search["parameters"] = []any{
		map[string]any{"name": "query", "in": "query", "required": true, "schema": map[string]any{"type": "string", "minLength": 1, "maxLength": 256}},
		map[string]any{"name": "year", "in": "query", "schema": map[string]any{"type": "integer", "minimum": 1000, "maximum": 9999}},
		map[string]any{"name": "language", "in": "query", "description": "Explicit administrator override; omitted uses authenticated user locale, then zh-CN.", "schema": map[string]any{"type": "string", "enum": []string{"zh-CN", "zh-TW", "ja-JP", "en-US"}}},
	}
	search["responses"].(map[string]any)["200"] = map[string]any{"description": "Candidates and title/year comparisons; every candidate requires confirmation.", "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"type": "object", "required": []string{"data"}, "properties": map[string]any{"data": map[string]any{"$ref": "#/components/schemas/MovieMatches"}}}}}}
	paths["/api/v1/metadata/tmdb/movies"] = map[string]any{"get": search}
	schemas["MovieMatches"] = map[string]any{"type": "object", "additionalProperties": false, "required": []string{"query", "year", "language", "candidates"}, "properties": map[string]any{
		"query": map[string]any{"type": "string", "maxLength": 256}, "year": map[string]any{"type": "integer"}, "language": map[string]any{"type": "string"}, "candidates": map[string]any{"type": "array", "maxItems": 20, "items": map[string]any{"$ref": "#/components/schemas/MovieMatch"}},
	}}
	schemas["MovieMatch"] = map[string]any{"type": "object", "additionalProperties": false, "required": []string{"movie", "exactTitle", "exactYear", "needsConfirmation"}, "properties": map[string]any{
		"movie": map[string]any{"$ref": "#/components/schemas/MovieCandidate"}, "exactTitle": map[string]any{"type": "boolean"}, "exactYear": map[string]any{"type": "boolean"}, "needsConfirmation": map[string]any{"type": "boolean", "const": true},
	}}
	op := operation("Preview a TMDB movie candidate (administrator)", "200", "400", "401", "403", "404", "408", "503")
	op["security"] = []any{map[string]any{"bearer": []string{}}}
	op["parameters"] = []any{
		map[string]any{"name": "id", "in": "path", "required": true, "schema": map[string]any{"type": "integer", "format": "int32", "minimum": 1, "maximum": 2147483647}},
		map[string]any{"name": "language", "in": "query", "description": "Explicit administrator override; omitted uses authenticated user locale, then zh-CN.", "schema": map[string]any{"type": "string", "enum": []string{"zh-CN", "zh-TW", "ja-JP", "en-US"}}},
	}
	responses := op["responses"].(map[string]any)
	responses["200"] = map[string]any{"description": "Provider data for review; no library or original asset changes.", "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"type": "object", "required": []string{"data"}, "properties": map[string]any{"data": map[string]any{"$ref": "#/components/schemas/MovieCandidate"}}}}}}
	paths["/api/v1/metadata/tmdb/movies/{id}"] = map[string]any{"get": op}
	schemas["MovieCandidate"] = map[string]any{"type": "object", "additionalProperties": false, "required": []string{"providerId", "source", "sourceUrl", "language", "fetchedAt", "title", "originalTitle", "overview", "releaseDate"}, "properties": map[string]any{
		"providerId":     map[string]any{"type": "integer", "format": "int32", "minimum": 1},
		"source":         map[string]any{"type": "string", "const": "TMDB"},
		"sourceUrl":      map[string]any{"type": "string", "format": "uri"},
		"language":       map[string]any{"type": "string", "enum": []string{"zh-CN", "zh-TW", "ja-JP", "en-US"}},
		"fetchedAt":      map[string]any{"type": "string", "format": "date-time"},
		"title":          map[string]any{"type": "string", "maxLength": 1024},
		"originalTitle":  map[string]any{"type": "string", "maxLength": 1024},
		"overview":       map[string]any{"type": "string", "maxLength": 16384},
		"overviewSource": map[string]any{"$ref": "#/components/schemas/MetadataFieldSource"},
		"releaseDate":    map[string]any{"type": "string", "pattern": "^([0-9]{4}-[0-9]{2}-[0-9]{2})?$"},
	}}
	schemas["MetadataFieldSource"] = map[string]any{"type": "object", "additionalProperties": false, "required": []string{"requestedLanguage", "fetchedAt"}, "properties": map[string]any{"requestedLanguage": map[string]any{"type": "string", "enum": []string{"", "zh-CN", "zh-TW", "ja-JP", "en-US"}}, "fetchedAt": map[string]any{"type": "string", "format": "date-time"}}, "description": "Provider request language and retrieval time for the overview. Empty language and zero timestamp indicate missing text; this is not a detected content language."}
	schemas["MovieCandidate"].(map[string]any)["required"] = append(schemas["MovieCandidate"].(map[string]any)["required"].([]string), "overviewSource")
	seriesSpecification(paths, schemas)
	episodeSpecification(paths, schemas)
}
