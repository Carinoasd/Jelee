package httpapi

import (
	"net/http"
	"strconv"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/go-chi/chi/v5"
)

func (s *Server) metadataImageRoutes(r chi.Router) {
	for _, route := range []struct{ path, resource string }{{"movies", "movie"}, {"series", "series"}} {
		r.Get("/api/v1/metadata/tmdb/"+route.path+"/{id}/images", s.accountEndpoint(true, true, func(_ http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
			query, err := strictQuery(r, "libraryId")
			if err != nil {
				return nil, 0, err
			}
			raw := chi.URLParam(r, "id")
			id, err := strconv.ParseInt(raw, 10, 32)
			if err != nil || id <= 0 || raw != strconv.FormatInt(id, 10) {
				return nil, 0, domain.ErrInvalid
			}
			languages := domain.DefaultMetadataImageLanguages(metadataRequestLanguage(r, nil))
			if library, exists := query["libraryId"]; exists {
				preferences, err := s.metadata.LibraryPreferences(r.Context(), a, library)
				if err != nil {
					return nil, 0, err
				}
				languages = preferences.ImageLanguages
			}
			value, err := s.metadata.Images(r.Context(), route.resource, int32(id), languages)
			return value, 200, err
		}))
	}
}

func metadataImageSpecification(paths, schemas map[string]any) {
	schemas["MetadataImageCandidate"] = objectSchema(map[string]any{
		"kind":     map[string]any{"type": "string", "enum": []string{"poster", "backdrop"}},
		"language": map[string]any{"type": "string", "enum": []string{"zh", "ja", "en", "null"}},
		"filePath": map[string]any{"type": "string", "maxLength": 256}, "url": map[string]any{"type": "string", "format": "uri"},
		"width": map[string]any{"type": "integer", "minimum": 1, "maximum": 32768}, "height": map[string]any{"type": "integer", "minimum": 1, "maximum": 32768},
		"voteAverage": map[string]any{"type": "number", "minimum": 0, "maximum": 10}, "voteCount": map[string]any{"type": "integer", "minimum": 0},
		"needsConfirmation": map[string]any{"type": "boolean", "const": true},
	}, "kind", "language", "filePath", "url", "width", "height", "voteAverage", "voteCount", "needsConfirmation")
	schemas["MetadataImages"] = objectSchema(map[string]any{
		"resource": map[string]any{"type": "string", "enum": []string{"movie", "series"}}, "providerId": map[string]any{"type": "integer", "format": "int32", "minimum": 1},
		"source": map[string]any{"type": "string", "const": "TMDB"}, "sourceUrl": map[string]any{"type": "string", "format": "uri"}, "fetchedAt": map[string]any{"type": "string", "format": "date-time"},
		"imageLanguages": metadataImageLanguageSchema(), "candidates": map[string]any{"type": "array", "maxItems": domain.MetadataImageLimit, "items": map[string]any{"$ref": "#/components/schemas/MetadataImageCandidate"}},
	}, "resource", "providerId", "source", "sourceUrl", "fetchedAt", "imageLanguages", "candidates")
	for _, resource := range []string{"movies", "series"} {
		op := adminOperation("List TMDB image candidates for administrator confirmation", "200", "400", "401", "403", "404", "408", "503")
		op["security"] = []any{map[string]any{"bearer": []string{}}}
		op["parameters"] = []any{map[string]any{"name": "id", "in": "path", "required": true, "schema": map[string]any{"type": "integer", "format": "int32", "minimum": 1, "maximum": 2147483647}}, map[string]any{"name": "libraryId", "in": "query", "schema": map[string]any{"type": "string", "format": "uuid"}, "description": "Use this library's ordered image languages; omitted uses profile language defaults."}}
		op["responses"].(map[string]any)["200"] = map[string]any{"description": "Ordered poster and backdrop candidates with original provider URLs; every image needs confirmation. No image download or artwork write.", "content": map[string]any{"application/json": map[string]any{"schema": objectSchema(map[string]any{"data": map[string]any{"$ref": "#/components/schemas/MetadataImages"}}, "data")}}}
		paths["/api/v1/metadata/tmdb/"+resource+"/{id}/images"] = map[string]any{"get": op}
	}
}
