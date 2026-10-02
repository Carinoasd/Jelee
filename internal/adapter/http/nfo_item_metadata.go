package httpapi

import (
	"net/http"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/go-chi/chi/v5"
)

func (s *Server) nfoItemMetadataRoutes(r chi.Router) {
	r.Post("/api/v1/items/{id}/metadata/nfo", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, actor domain.Actor) (any, int, error) {
		var input struct {
			ExpectedRevision int64 `json:"expectedRevision"`
			Confirmed        *bool `json:"confirmed"`
		}
		if err := DecodeJSON(w, r, &input, 8<<10); err != nil {
			return nil, 0, err
		}
		if input.Confirmed == nil || !*input.Confirmed {
			return nil, 0, domain.ErrInvalid
		}
		value, err := s.metadata.ApplyNFO(r.Context(), actor, chi.URLParam(r, "id"), input.ExpectedRevision, true)
		return value, 200, err
	}))
}

func nfoItemMetadataSpecification(paths map[string]any) {
	op := operation("Apply confirmed read-only selected NFO fields (administrator)", "200", "400", "401", "403", "404", "408", "409", "503")
	op["security"] = []any{map[string]any{"bearer": []string{}}}
	op["parameters"] = []any{idParameter()}
	op["description"] = "Resolve a unique item media source through trusted library/root ownership. Observe a bounded directory and match allowed filenames case-insensitively: video basename first, then movie.nfo or tvshow.nfo. Ambiguous case variants, symlinks, unavailable media and incomplete listings are rejected. Read and parse outside database locks, then recheck candidate names and full bytes. Missing or invalid NFO still returns 503; source or selection changes return 409. Final transaction rechecks live administrator, item revision, media/root identity, path and NFO generation. Manual fields (including explicit empty values), manual locks and existing NFO locks are preserved. NFO lock intent is recorded in nfoOrigin.locked separately from the manual locked flag. Source IDs, digest and observation time contain no paths. Missing values preserve fields. Files are not modified. Checkpoints do not provide an atomic filesystem snapshot."
	op["requestBody"] = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": objectSchema(map[string]any{"expectedRevision": map[string]any{"type": "integer", "minimum": 1, "maximum": domain.ItemMetadataRevisionMax - 1}, "confirmed": map[string]any{"type": "boolean", "const": true}}, "expectedRevision", "confirmed")}}}
	op["responses"].(map[string]any)["200"] = map[string]any{"description": "Applied/skipped fields, persisted NFO origins and revision", "content": map[string]any{"application/json": map[string]any{"schema": objectSchema(map[string]any{"data": map[string]any{"$ref": "#/components/schemas/MetadataApplyResult"}}, "data")}}}
	paths["/api/v1/items/{id}/metadata/nfo"] = map[string]any{"post": op}
}
