package httpapi

import (
	"github.com/MoYuanCN/Jelee/internal/platform/buildinfo"
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
	paths["/api/v1/system"].(map[string]any)["get"].(map[string]any)["description"] = "Public service information: name, version (the server build, equal to info.version of this document; plugins compare minJeleeVersion against it), developer mode state and capabilities."
	if cfg.EnableCatalog && cfg.EnableDirect {
		op := operation("Read the unmodified original resource", "200", "206", "409", "416")
		op["security"] = []any{map[string]any{"bearer": []string{}}}
		op["parameters"] = []any{idParameter(), map[string]any{"name": "Range", "in": "header", "schema": map[string]any{"type": "string"}}, map[string]any{"name": "If-Range", "in": "header", "schema": map[string]any{"type": "string"}}}
		paths["/api/v1/sources/{id}/stream"] = map[string]any{"get": op, "head": op}
		for route, kind := range map[string]string{subtitleTrackRoute: "subtitle", audioTrackRoute: "audio"} {
			op := operation("Read an unmodified external "+kind+" file of a source", "200", "206", "403", "404", "409", "416")
			op["description"] = "Native sessions only; web sessions get 403 web_playback_disabled. Delivers the external " + kind + " file listed under externalTracks of GET /api/v1/items/{id}/playback byte for byte (G10.9): no burn-in, re-encoding, remuxing or charset conversion. The track must belong to the source and the source must be visible to the caller; a missing, invisible or foreign track is answered like a missing source. Range, HEAD, conditional requests, playback and bandwidth limits and revocation behave as for /api/v1/sources/{id}/stream, and a track counts as part of its source's playback. Content-Type comes from a fixed table keyed by the file extension (application/octet-stream when unknown); a detected subtitle charset is only reported as its charset parameter. Responses carry X-Content-Type-Options: nosniff and a sandbox Content-Security-Policy."
			op["security"] = []any{map[string]any{"bearer": []string{}}}
			op["x-jelee-session"] = "native"
			op["parameters"] = []any{idParameter(), map[string]any{"name": "trackId", "in": "path", "required": true, "schema": map[string]any{"type": "string", "format": "uuid"}}, map[string]any{"name": "Range", "in": "header", "schema": map[string]any{"type": "string"}}, map[string]any{"name": "If-Range", "in": "header", "schema": map[string]any{"type": "string"}}}
			paths[route] = map[string]any{"get": op, "head": op}
		}
	}
	schemas := accountSchemas()
	siteSettingsSchemas(schemas)
	twoFactorSchemas(schemas)
	if cfg.EnableCatalog && cfg.EnableDirect {
		playbackSpecification(paths, schemas)
	}
	if cfg.EnableCatalog {
		catalogSpecification(paths, schemas)
		progressSpecification(paths, schemas)
		watchStatsSpecification(paths, schemas)
		versionsSpecification(paths, schemas)
	}
	if cfg.EnableAccounts {
		itemMetadataSpecification(paths, schemas)
		metadataOriginSpecification(schemas)
		metadataApplyResultSpecification(schemas)
		metadataRemoveSpecification(paths, schemas)
		nfoItemMetadataSpecification(paths)
		if cfg.TMDBAPIKey != "" {
			metadataApplySpecification(paths, schemas)
		}
		accountSpecification(paths)
		siteSettingsSpecification(paths)
		twoFactorSpecification(paths)
		clientControlSpecification(paths, schemas)
		shareSpecification(paths, schemas)
		setupSpecification(paths, schemas)
		if cfg.Dev.Capable() {
			devSpecification(paths, schemas)
		}
	}
	if cfg.EnableAccounts && cfg.TMDBAPIKey != "" {
		metadataSpecification(paths, schemas)
	}
	if cfg.EnableAccounts && cfg.EnableMetrics {
		op := operation("Read local runtime and database pool metrics", "200", "400", "401", "403", "503")
		op["security"] = []any{map[string]any{"bearer": []string{}}}
		op["x-jelee-role"] = "administrator"
		op["description"] = "Prometheus exposition from this process. No query parameters. At most two concurrent requests including authentication; request and write deadline are three seconds."
		op["responses"].(map[string]any)["200"].(map[string]any)["content"] = map[string]any{"text/plain": map[string]any{"schema": map[string]any{"type": "string"}}}
		paths["/metrics"] = map[string]any{"get": op}
	}
	if cfg.EnableAccounts && cfg.EnableWebhooks {
		webhookSpecification(paths, schemas)
	}
	if cfg.EnableJobs {
		jobSpecification(paths, schemas)
		nfoSpecification(paths, schemas)
	}
	if cfg.EnableImages && cfg.EnableAccounts && cfg.EnableCatalog {
		imageSpecification(paths, cfg)
	}
	errorSpecification(paths, schemas)
	webSessionSpecification(paths)
	return map[string]any{"openapi": "3.1.0", "info": map[string]any{"title": "Jelee API", "version": buildinfo.Version(), "description": "Experimental foundation. Full feature parity is not yet available."}, "paths": paths, "x-jelee-removed-features": map[string]any{"pathRoots": []string{"/LiveTv", "/Channels", "/Dlna"}, "status": 501, "code": "feature_removed", "description": "All methods and descendant paths return a localized unsupported-feature error; transformation routes retain their 409 guard."}, "components": map[string]any{"schemas": schemas, "securitySchemes": map[string]any{"bearer": map[string]any{"type": "http", "scheme": "bearer"}, "webSession": webSessionScheme()}}}
}
func idParameter() map[string]any {
	return map[string]any{"name": "id", "in": "path", "required": true, "schema": map[string]any{"type": "string", "format": "uuid"}}
}
func operation(summary string, statuses ...string) map[string]any {
	responses := map[string]any{"default": map[string]any{"description": "Jelee error envelope with code, message, details and traceId; see the Error schema."}}
	for _, status := range statuses {
		responses[status] = map[string]any{"description": "HTTP " + status}
	}
	return map[string]any{"summary": summary, "responses": responses}
}

// webSessionScheme documents the browser cookie (G35.1). It is an alternative
// to bearer on every authenticated operation, accepted for web sessions only.
func webSessionScheme() map[string]any {
	return map[string]any{"type": "apiKey", "in": "cookie", "name": sessionCookieName, "description": "Set by login and rotate for web sessions only: HttpOnly, Secure, SameSite=Strict, Path=/, no Domain, Max-Age equal to the session lifetime. Ignored when an Authorization header is present. Native sessions are refused through the cookie. POST, PUT, PATCH and DELETE authenticated by it must send the X-Jelee-CSRF header (from login, rotate or GET /api/v1/auth/csrf); otherwise 403 csrf_failed."}
}

func webSessionSpecification(paths map[string]any) {
	for _, item := range paths {
		for _, raw := range item.(map[string]any) {
			op := raw.(map[string]any)
			if _, ok := op["security"]; ok {
				op["security"] = []any{map[string]any{"bearer": []string{}}, map[string]any{"webSession": []string{}}}
			}
		}
	}
}
