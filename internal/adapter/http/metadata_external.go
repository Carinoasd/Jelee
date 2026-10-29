package httpapi

import (
	"net/http"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/go-chi/chi/v5"
)

// Called inside the same admission/authentication group as movie previews.
func (s *Server) externalIDRoutes(r chi.Router) {
	r.Get("/api/v1/metadata/tmdb/find", s.accountEndpoint(true, true, func(_ http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		query, err := strictQuery(r, "source", "id", "language", "libraryId")
		if err != nil {
			return nil, 0, err
		}
		// Reject a malformed identifier before reading library preferences.
		if !domain.ValidExternalID(query["source"], query["id"]) {
			return nil, 0, domain.ErrInvalid
		}
		language, err := s.metadataLibraryLanguage(r, query, a)
		if err != nil {
			return nil, 0, err
		}
		value, err := s.metadata.FindByExternalID(r.Context(), query["source"], query["id"], language)
		return value, http.StatusOK, err
	}))
}

func externalIDSpecification(paths, schemas map[string]any) {
	schemas["ExternalIDMatches"] = objectSchema(map[string]any{
		"externalSource":    map[string]any{"type": "string", "enum": []string{domain.ExternalSourceIMDb, domain.ExternalSourceTVDB, domain.ExternalSourceWikidata}},
		"externalId":        map[string]any{"type": "string", "maxLength": 12},
		"language":          map[string]any{"type": "string", "enum": []string{"zh-CN", "zh-TW", "ja-JP", "en-US"}},
		"fetchedAt":         map[string]any{"type": "string", "format": "date-time"},
		"movies":            map[string]any{"type": "array", "maxItems": domain.MaxExternalIDResults, "items": map[string]any{"$ref": "#/components/schemas/MovieCandidate"}},
		"series":            map[string]any{"type": "array", "maxItems": domain.MaxExternalIDResults, "items": map[string]any{"$ref": "#/components/schemas/SeriesCandidate"}},
		"needsConfirmation": map[string]any{"type": "boolean", "const": true},
	}, "externalSource", "externalId", "language", "fetchedAt", "movies", "series", "needsConfirmation")
	op := adminOperation("Find TMDB movie and series candidates by an external identifier (administrator)", "200", "400", "401", "403", "404", "408", "503")
	op["security"] = []any{map[string]any{"bearer": []string{}}}
	op["description"] = "Resolves an IMDb title (tt + 7–10 digits), TVDB (positive int32) or Wikidata (Q + digits) identifier through the governed TMDB adapter. An unknown identifier answers 200 with empty lists. No candidate is selected and nothing is written."
	op["parameters"] = []any{
		map[string]any{"name": "source", "in": "query", "required": true, "schema": map[string]any{"type": "string", "enum": []string{domain.ExternalSourceIMDb, domain.ExternalSourceTVDB, domain.ExternalSourceWikidata}}},
		map[string]any{"name": "id", "in": "query", "required": true, "schema": map[string]any{"type": "string", "minLength": 1, "maxLength": 12, "pattern": "^(tt[0-9]{7,10}|[1-9][0-9]{0,9}|Q[1-9][0-9]{0,9})$"}, "description": "Canonical identifier of the named source."},
		map[string]any{"name": "language", "in": "query", "description": "Explicit administrator override; omitted uses the library language, then authenticated user locale, then zh-CN.", "schema": map[string]any{"type": "string", "enum": []string{"zh-CN", "zh-TW", "ja-JP", "en-US"}}},
		map[string]any{"name": "libraryId", "in": "query", "schema": map[string]any{"type": "string", "format": "uuid"}, "description": "Use this library's metadata language unless language is given."},
	}
	op["responses"].(map[string]any)["200"] = map[string]any{"description": "Movie and series candidates for review; every candidate requires confirmation.", "content": map[string]any{"application/json": map[string]any{"schema": objectSchema(map[string]any{"data": map[string]any{"$ref": "#/components/schemas/ExternalIDMatches"}}, "data")}}}
	paths["/api/v1/metadata/tmdb/find"] = map[string]any{"get": op}
}
