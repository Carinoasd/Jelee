package httpapi

import (
	"net/http"
	"strconv"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/go-chi/chi/v5"
)

// Called inside the same admission/authentication group as movie previews.
func (s *Server) seriesRoutes(r chi.Router) {
	r.Get("/api/v1/metadata/tmdb/series", s.accountEndpoint(true, true, func(w http.ResponseWriter, r *http.Request, _ domain.Actor) (any, int, error) {
		query, err := strictQuery(r, "query", "year", "language")
		if err != nil {
			return nil, 0, err
		}
		language, specified := query["language"]
		if !specified {
			language = "zh-CN"
		}
		year := 0
		if raw, exists := query["year"]; exists {
			parsed, err := strconv.ParseInt(raw, 10, 32)
			if err != nil || len(raw) != 4 || raw != strconv.FormatInt(parsed, 10) {
				return nil, 0, domain.ErrInvalid
			}
			year = int(parsed)
		}
		result, err := s.metadata.SearchSeries(r.Context(), domain.SeriesSearchInput{Query: query["query"], Year: year, Language: language})
		return result, http.StatusOK, err
	}))
	r.Get("/api/v1/metadata/tmdb/series/{id}", s.accountEndpoint(true, true, func(w http.ResponseWriter, r *http.Request, _ domain.Actor) (any, int, error) {
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
		result, err := s.metadata.Series(r.Context(), int32(id), language)
		return result, http.StatusOK, err
	}))
}

func copyMetadataSchema(original map[string]any) map[string]any {
	clone := make(map[string]any, len(original))
	for key, value := range original {
		clone[key] = value
	}
	return clone
}

func seriesSpecification(paths, schemas map[string]any) {
	candidate := copyMetadataSchema(schemas["MovieCandidate"].(map[string]any))
	properties := copyMetadataSchema(candidate["properties"].(map[string]any))
	properties["firstAirDate"] = properties["releaseDate"]
	delete(properties, "releaseDate")
	candidate["properties"] = properties
	required := append([]string(nil), candidate["required"].([]string)...)
	for i, key := range required {
		if key == "releaseDate" {
			required[i] = "firstAirDate"
		}
	}
	candidate["required"] = required
	schemas["SeriesCandidate"] = candidate
	match := copyMetadataSchema(schemas["MovieMatch"].(map[string]any))
	properties = copyMetadataSchema(match["properties"].(map[string]any))
	delete(properties, "movie")
	properties["series"] = map[string]any{"$ref": "#/components/schemas/SeriesCandidate"}
	match["properties"] = properties
	match["required"] = []string{"series", "exactTitle", "exactYear", "needsConfirmation"}
	schemas["SeriesMatch"] = match
	matches := copyMetadataSchema(schemas["MovieMatches"].(map[string]any))
	properties = copyMetadataSchema(matches["properties"].(map[string]any))
	properties["candidates"] = map[string]any{"type": "array", "maxItems": 20, "items": map[string]any{"$ref": "#/components/schemas/SeriesMatch"}}
	matches["properties"] = properties
	schemas["SeriesMatches"] = matches
	for _, route := range []struct{ path, base, summary, schema string }{
		{"/api/v1/metadata/tmdb/series", "/api/v1/metadata/tmdb/movies", "Search TMDB series by title and first-air-date year for administrator confirmation", "SeriesMatches"},
		{"/api/v1/metadata/tmdb/series/{id}", "/api/v1/metadata/tmdb/movies/{id}", "Preview TMDB series data (administrator)", "SeriesCandidate"},
	} {
		op := copyMetadataSchema(paths[route.base].(map[string]any)["get"].(map[string]any))
		op["summary"] = route.summary
		responses := copyMetadataSchema(op["responses"].(map[string]any))
		responses["200"] = map[string]any{"description": "Series candidate data for review; no automatic metadata write.", "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"type": "object", "required": []string{"data"}, "properties": map[string]any{"data": map[string]any{"$ref": "#/components/schemas/" + route.schema}}}}}}
		op["responses"] = responses
		paths[route.path] = map[string]any{"get": op}
	}
}
