package httpapi

import (
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"net/url"
)

func parseQuery(raw string) (url.Values, error) { return url.ParseQuery(raw) }

// Specification is generated from the same rollout configuration as the router.
func Specification(cfg config.Config) map[string]any {
	paths := map[string]any{}
	for _, path := range []string{"/healthz", "/readyz", "/api/v1/system", "/api/v1/openapi.json", "/api-docs"} {
		paths[path] = map[string]any{"get": operation("Inspect service", "200")}
	}
	if cfg.EnableCatalog {
		op := operation("List visible video items", "200")
		op["security"] = []any{map[string]any{"bearer": []string{}}}
		op["parameters"] = []any{map[string]any{"name": "cursor", "in": "query", "schema": map[string]any{"type": "string", "format": "uuid"}}, map[string]any{"name": "limit", "in": "query", "schema": map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "default": 50}}}
		paths["/api/v1/items"] = map[string]any{"get": op}
		op = operation("Read a visible video item", "200")
		op["security"] = []any{map[string]any{"bearer": []string{}}}
		op["parameters"] = []any{idParameter()}
		paths["/api/v1/items/{id}"] = map[string]any{"get": op}
	}
	if cfg.EnableCatalog && cfg.EnableDirect {
		op := operation("Read the unmodified original resource", "200", "206", "409", "416")
		op["security"] = []any{map[string]any{"bearer": []string{}}}
		op["parameters"] = []any{idParameter(), map[string]any{"name": "Range", "in": "header", "schema": map[string]any{"type": "string"}}, map[string]any{"name": "If-Range", "in": "header", "schema": map[string]any{"type": "string"}}}
		paths["/api/v1/sources/{id}/stream"] = map[string]any{"get": op, "head": op}
	}
	if cfg.EnableAccounts {
		accountSpecification(paths)
	}
	schemas := accountSchemas()
	if cfg.EnableJobs {
		jobSpecification(paths, schemas)
		nfoSpecification(paths, schemas)
	}
	return map[string]any{"openapi": "3.1.0", "info": map[string]any{"title": "Jelee API", "version": "0.1.0-dev", "description": "Experimental foundation. Full feature parity is not yet available."}, "paths": paths, "components": map[string]any{"schemas": schemas, "securitySchemes": map[string]any{"bearer": map[string]any{"type": "http", "scheme": "bearer"}}}}
}
func idParameter() map[string]any {
	return map[string]any{"name": "id", "in": "path", "required": true, "schema": map[string]any{"type": "string", "format": "uuid"}}
}
func operation(summary string, statuses ...string) map[string]any {
	responses := map[string]any{"default": map[string]any{"description": "Jelee structured error envelope with code, message, details and traceId."}}
	for _, status := range statuses {
		responses[status] = map[string]any{"description": "HTTP " + status}
	}
	return map[string]any{"summary": summary, "responses": responses}
}
