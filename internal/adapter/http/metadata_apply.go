package httpapi

import (
	"encoding/json"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/go-chi/chi/v5"
	"net/http"
)

func (s *Server) metadataApplyRoutes(r chi.Router) {
	r.Post("/api/v1/items/{id}/metadata/tmdb", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		var raw struct {
			Resource             string          `json:"resource"`
			ProviderID           int32           `json:"providerId"`
			ExpectedRevision     int64           `json:"expectedRevision"`
			Confirmed            *bool           `json:"confirmed"`
			ReplaceExistingTitle json.RawMessage `json:"replaceExistingTitle"`
			Language             json.RawMessage `json:"language"`
		}
		if err := DecodeJSON(w, r, &raw, 8<<10); err != nil {
			return nil, 0, err
		}
		if raw.Confirmed == nil || !*raw.Confirmed {
			return nil, 0, domain.ErrInvalid
		}
		input := domain.TMDBMetadataApplyInput{Resource: raw.Resource, ProviderID: raw.ProviderID, ExpectedRevision: raw.ExpectedRevision, Confirmed: true}
		if raw.ReplaceExistingTitle != nil {
			var flag *bool
			if json.Unmarshal(raw.ReplaceExistingTitle, &flag) != nil || flag == nil {
				return nil, 0, domain.ErrInvalid
			}
			input.ReplaceExistingTitle = *flag
		}
		if raw.Language != nil {
			var language *string
			if json.Unmarshal(raw.Language, &language) != nil || language == nil || !domain.ValidMetadataLanguage(*language) {
				return nil, 0, domain.ErrInvalid
			}
			input.Language = *language
		}
		value, err := s.metadata.ApplyTMDB(r.Context(), a, chi.URLParam(r, "id"), input)
		return value, 200, err
	}))
}

func metadataOriginSpecification(schemas map[string]any) {
	schemas["MetadataProviderOrigin"] = objectSchema(map[string]any{"resource": map[string]any{"type": "string", "enum": []string{"movie", "series"}}, "providerId": map[string]any{"type": "integer", "format": "int32", "minimum": 1}, "sourceUrl": map[string]any{"type": "string", "format": "uri"}, "requestedLanguage": map[string]any{"type": "string", "enum": []string{"zh-CN", "zh-TW", "ja-JP", "en-US"}}, "fetchedAt": map[string]any{"type": "string", "format": "date-time"}}, "resource", "providerId", "sourceUrl", "requestedLanguage", "fetchedAt")
	field := schemas["ItemMetadataField"].(map[string]any)
	field["properties"].(map[string]any)["source"] = map[string]any{"type": "string", "enum": []string{"existing", "manual", "tmdb", "nfo"}}
	field["properties"].(map[string]any)["providerOrigin"] = map[string]any{"oneOf": []any{map[string]any{"$ref": "#/components/schemas/MetadataProviderOrigin"}, map[string]any{"type": "null"}}}
	field["required"] = append(field["required"].([]string), "providerOrigin")
	schemas["NFOItemOrigin"] = objectSchema(map[string]any{"sourceId": map[string]any{"type": "string", "format": "uuid"}, "rootId": map[string]any{"type": "string", "format": "uuid"}, "generation": map[string]any{"type": "integer", "minimum": 1}, "sha256": map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"}, "identityDigest": map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"}, "projection": map[string]any{"type": "string", "const": domain.NFOItemFieldsVersion}, "readAt": map[string]any{"type": "string", "format": "date-time"}, "locked": map[string]any{"type": "boolean"}}, "sourceId", "rootId", "generation", "sha256", "identityDigest", "projection", "readAt", "locked")
	field["properties"].(map[string]any)["nfoOrigin"] = map[string]any{"oneOf": []any{map[string]any{"$ref": "#/components/schemas/NFOItemOrigin"}, map[string]any{"type": "null"}}}
	field["required"] = append(field["required"].([]string), "nfoOrigin")
	item := schemas["ItemMetadata"].(map[string]any)
	item["properties"].(map[string]any)["kind"] = map[string]any{"type": "string", "enum": []string{"Movie", "Series", "Season", "Episode", "HomeVideo"}}
	item["required"] = append(item["required"].([]string), "kind")
}

func metadataApplyResultSpecification(schemas map[string]any) {
	schemas["MetadataApplyResult"] = objectSchema(map[string]any{"metadata": map[string]any{"$ref": "#/components/schemas/ItemMetadata"}, "applied": map[string]any{"type": "array", "maxItems": 4, "items": map[string]any{"type": "string"}}, "skipped": map[string]any{"type": "array", "maxItems": 4, "items": objectSchema(map[string]any{"field": map[string]any{"type": "string"}, "reason": map[string]any{"type": "string", "enum": []string{"locked", "manual", "nfo", "existing"}}}, "field", "reason")}}, "metadata", "applied", "skipped")
	properties := schemas["MetadataApplyResult"].(map[string]any)["properties"].(map[string]any)
	schemas["MetadataFieldApplyReport"] = objectSchema(map[string]any{"applied": properties["applied"], "skipped": properties["skipped"]}, "applied", "skipped")
	properties["nfo"] = map[string]any{"$ref": "#/components/schemas/MetadataFieldApplyReport"}
	properties["tmdb"] = map[string]any{"$ref": "#/components/schemas/MetadataFieldApplyReport"}
}

func metadataApplySpecification(paths, schemas map[string]any) {
	metadataApplyResultSpecification(schemas)
	op := operation("Apply explicitly confirmed TMDB details while preserving local fields and locks (administrator)", "200", "400", "401", "403", "404", "408", "409", "503")
	op["security"] = []any{map[string]any{"bearer": []string{}}}
	op["parameters"] = []any{idParameter()}
	op["description"] = "Fetch selected movie/series ID outside database locks, then recheck live session, revision and kind. Read-only NFO libraries resolve a unique trusted adjacent NFO, observe its full bytes before provider lookup and reread after lookup. The final transaction rechecks source/root identity and NFO generation, applies NFO before TMDB, and advances one revision with one audit. Manual and locked fields are preserved; NFO has priority over TMDB. Missing, ambiguous, invalid or unbound NFO returns 503; changed observations or scope return 409. Optional nfo/tmdb reports explain each source; top-level applied/skipped lists describe the combined result without duplicate fields. Existing nonempty title requires replaceExistingTitle=true for TMDB replacement. Missing values preserve fields. All-protected reviews still advance revision. Movie fields change HomeVideo to Movie only if at least one field is applied. Filesystem observations are not an atomic snapshot."
	op["requestBody"] = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": objectSchema(map[string]any{"resource": map[string]any{"type": "string", "enum": []string{"movie", "series"}}, "providerId": map[string]any{"type": "integer", "format": "int32", "minimum": 1, "maximum": 2147483647}, "expectedRevision": map[string]any{"type": "integer", "minimum": 1, "maximum": domain.ItemMetadataRevisionMax - 1}, "confirmed": map[string]any{"type": "boolean", "const": true}, "replaceExistingTitle": map[string]any{"type": "boolean", "default": false}, "language": map[string]any{"type": "string", "enum": []string{"zh-CN", "zh-TW", "ja-JP", "en-US"}}}, "resource", "providerId", "expectedRevision", "confirmed")}}}
	op["responses"].(map[string]any)["200"] = map[string]any{"description": "Persisted metadata and applied/skipped report", "content": map[string]any{"application/json": map[string]any{"schema": objectSchema(map[string]any{"data": map[string]any{"$ref": "#/components/schemas/MetadataApplyResult"}}, "data")}}}
	paths["/api/v1/items/{id}/metadata/tmdb"] = map[string]any{"post": op}
}
