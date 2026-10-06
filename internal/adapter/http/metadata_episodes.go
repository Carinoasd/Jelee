package httpapi

import (
	"net/http"
	"strconv"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/go-chi/chi/v5"
)

func (s *Server) episodeRoutes(r chi.Router) {
	r.Get("/api/v1/metadata/tmdb/series/{id}/seasons/{season}", s.accountEndpoint(true, true, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		series, season, _, language, err := s.metadataEpisodeInput(r, a, false)
		if err != nil {
			return nil, 0, err
		}
		value, err := s.metadata.Season(r.Context(), series, season, language)
		return value, http.StatusOK, err
	}))
	r.Get("/api/v1/metadata/tmdb/series/{id}/seasons/{season}/episodes/{episode}", s.accountEndpoint(true, true, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		series, season, episode, language, err := s.metadataEpisodeInput(r, a, true)
		if err != nil {
			return nil, 0, err
		}
		value, err := s.metadata.Episode(r.Context(), series, season, episode, language)
		return value, http.StatusOK, err
	}))
}

func (s *Server) metadataEpisodeInput(r *http.Request, a domain.Actor, episode bool) (int32, int32, int32, string, error) {
	query, err := strictQuery(r, "language", "libraryId")
	if err != nil {
		return 0, 0, 0, "", err
	}
	language, err := s.metadataLibraryLanguage(r, query, a)
	if err != nil {
		return 0, 0, 0, "", err
	}
	var numbers [3]int32
	for i, name := range []string{"id", "season", "episode"} {
		if i == 2 && !episode {
			break
		}
		raw := chi.URLParam(r, name)
		parsed, err := strconv.ParseInt(raw, 10, 32)
		if err != nil || parsed < 0 || (i != 1 && parsed == 0) || raw != strconv.FormatInt(parsed, 10) {
			return 0, 0, 0, "", domain.ErrInvalid
		}
		numbers[i] = int32(parsed)
	}
	if !domain.ValidMetadataLanguage(language) {
		return 0, 0, 0, "", domain.ErrInvalid
	}
	return numbers[0], numbers[1], numbers[2], language, nil
}

func episodeSpecification(paths, schemas map[string]any) {
	for _, item := range []struct {
		name, path string
		episode    bool
	}{
		{"SeasonCandidate", "/api/v1/metadata/tmdb/series/{id}/seasons/{season}", false},
		{"EpisodeCandidate", "/api/v1/metadata/tmdb/series/{id}/seasons/{season}/episodes/{episode}", true},
	} {
		schema := copyMetadataSchema(schemas["MovieCandidate"].(map[string]any))
		properties := copyMetadataSchema(schema["properties"].(map[string]any))
		properties["airDate"] = properties["releaseDate"]
		delete(properties, "releaseDate")
		delete(properties, "originalTitle")
		properties["seriesId"] = map[string]any{"type": "integer", "format": "int32", "minimum": 1}
		properties["seasonNumber"] = map[string]any{"type": "integer", "format": "int32", "minimum": 0}
		required := []string{"providerId", "seriesId", "seasonNumber", "source", "sourceUrl", "language", "fetchedAt", "title", "overview", "overviewSource", "airDate"}
		if item.episode {
			properties["episodeNumber"] = map[string]any{"type": "integer", "format": "int32", "minimum": 1}
			required = append(required, "episodeNumber")
		} else {
			properties["episodes"] = map[string]any{"type": "array", "maxItems": domain.MaxMetadataSeasonEpisodes, "items": map[string]any{"$ref": "#/components/schemas/EpisodeCandidate"}}
			required = append(required, "episodes")
		}
		schema["properties"] = properties
		schema["required"] = required
		schemas[item.name] = schema
		op := adminOperation("Preview TMDB season or episode data (administrator)", "200", "400", "401", "403", "404", "408", "503")
		op["security"] = []any{map[string]any{"bearer": []string{}}}
		params := []any{map[string]any{"name": "id", "in": "path", "required": true, "schema": map[string]any{"type": "integer", "format": "int32", "minimum": 1, "maximum": 2147483647}}, map[string]any{"name": "season", "in": "path", "required": true, "schema": map[string]any{"type": "integer", "format": "int32", "minimum": 0, "maximum": 2147483647}}}
		if item.episode {
			params = append(params, map[string]any{"name": "episode", "in": "path", "required": true, "schema": map[string]any{"type": "integer", "format": "int32", "minimum": 1, "maximum": 2147483647}})
		}
		params = append(params, map[string]any{"name": "language", "in": "query", "description": "Explicit administrator override; omitted uses authenticated user locale, then zh-CN.", "schema": map[string]any{"type": "string", "enum": []string{"zh-CN", "zh-TW", "ja-JP", "en-US"}}})
		op["parameters"] = params
		op["responses"].(map[string]any)["200"] = map[string]any{"description": "Selected candidate data; no automatic library write.", "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"type": "object", "required": []string{"data"}, "properties": map[string]any{"data": map[string]any{"$ref": "#/components/schemas/" + item.name}}}}}}
		paths[item.path] = map[string]any{"get": op}
	}
}
