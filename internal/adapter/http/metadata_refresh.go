package httpapi

import (
	"net/http"
	"strings"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/go-chi/chi/v5"
)

// metadataRefreshRoutes mounts automatic metadata refresh (G13.6, G14.1,
// G14.4, G14.6): queueing a refresh job, its progress, the per-library
// settings and the review queue. Every route is administrator-only. Without
// a provider a submission or an accept answers 503 metadata_unavailable;
// settings, progress and reviews stay readable.
func (s *Server) metadataRefreshRoutes(r chi.Router) {
	r.Post("/api/v1/libraries/{id}/metadata/refresh", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		var input struct {
			Priority string `json:"priority"`
		}
		if err := DecodeJSON(w, r, &input, accountBodyLimit); err != nil {
			return nil, 0, err
		}
		key, err := jobKey(r)
		if err != nil {
			return nil, 0, err
		}
		if input.Priority == "" {
			input.Priority = domain.JobPriorityManual
		}
		job, replay, err := s.jobs.SubmitMetadataRefresh(r.Context(), a, chi.URLParam(r, "id"), key, input.Priority)
		return acceptedJob(w, job, replay, err)
	}))
	r.Get("/api/v1/jobs/{id}/metadata-refresh", s.accountEndpoint(true, false, func(_ http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		state, err := s.jobs.MetadataRefreshState(r.Context(), a, chi.URLParam(r, "id"))
		return state, http.StatusOK, err
	}))
	settings := "/api/v1/libraries/{id}/metadata/refresh/settings"
	r.Get(settings, s.accountEndpoint(true, false, func(_ http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		v, err := s.jobs.MetadataRefreshSettings(r.Context(), a, chi.URLParam(r, "id"))
		return v, http.StatusOK, err
	}))
	r.Put(settings, s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		var input struct {
			Auto      *bool    `json:"auto"`
			AutoApply *string  `json:"autoApply"`
			Sources   []string `json:"sources"`
		}
		if err := DecodeJSON(w, r, &input, accountBodyLimit); err != nil {
			return nil, 0, err
		}
		if input.Auto == nil || input.AutoApply == nil || input.Sources == nil {
			return nil, 0, domain.ErrInvalid
		}
		v, err := s.jobs.PutMetadataRefreshSettings(r.Context(), a, domain.MetadataRefreshSettings{LibraryID: chi.URLParam(r, "id"), Auto: *input.Auto, AutoApply: *input.AutoApply, Sources: input.Sources})
		return v, http.StatusOK, err
	}))
	r.Get("/api/v1/libraries/{id}/metadata/refresh/reviews", s.accountEndpoint(true, true, func(_ http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		q, err := listQuery(r, "/api/v1/libraries/{id}/metadata/refresh/reviews")
		if err != nil {
			return nil, 0, err
		}
		rows, next, err := keysetPage(q, func(cursor string, limit int) ([]domain.MetadataRefreshReview, string, error) {
			rows, err := s.jobs.MetadataRefreshReviews(r.Context(), a, chi.URLParam(r, "id"), q.Filters["state"], cursor, limit)
			return rows, nextKey(rows, limit, func(v domain.MetadataRefreshReview) string { return v.ItemID }), err
		})
		if err != nil {
			return nil, 0, err
		}
		return listData(q, pageResult("reviews", rows, next, q.Limit))
	}))
	r.Post("/api/v1/items/{id}/metadata/refresh/accept", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		var input struct {
			ProviderID       *int32 `json:"providerId"`
			ExpectedRevision *int64 `json:"expectedRevision"`
		}
		if err := DecodeJSON(w, r, &input, accountBodyLimit); err != nil {
			return nil, 0, err
		}
		if input.ProviderID == nil || input.ExpectedRevision == nil {
			return nil, 0, domain.ErrInvalid
		}
		v, err := s.jobs.AcceptMetadataRefreshReview(r.Context(), a, chi.URLParam(r, "id"), domain.MetadataRefreshAcceptance{ProviderID: *input.ProviderID, ExpectedRevision: *input.ExpectedRevision})
		return v, http.StatusOK, err
	}))
	r.Post("/api/v1/items/{id}/metadata/refresh/reject", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		if err := emptyAccountInput(w, r); err != nil {
			return nil, 0, err
		}
		v, err := s.jobs.RejectMetadataRefreshReview(r.Context(), a, chi.URLParam(r, "id"))
		return v, http.StatusOK, err
	}))
}

func metadataRefreshSpecification(paths, schemas map[string]any) {
	uuid := map[string]any{"type": "string", "format": "uuid"}
	count := map[string]any{"type": "integer", "minimum": 0}
	instant := map[string]any{"type": "string", "format": "date-time"}
	confidence := map[string]any{"type": "string", "enum": []string{domain.MetadataConfidenceHigh, domain.MetadataConfidenceMedium, domain.MetadataConfidenceLow}}
	threshold := map[string]any{"type": "string", "enum": []string{domain.MetadataAutoApplyHigh, domain.MetadataAutoApplyMedium, domain.MetadataAutoApplyNever},
		"description": "Lowest confidence applied without review: high applies identifier matches only (default), medium also unique title and year matches, never sends every match to review."}
	sources := map[string]any{"type": "array", "minItems": 1, "maxItems": 2, "uniqueItems": true, "items": map[string]any{"type": "string", "enum": append([]string{domain.MetadataSourceNFO}, domain.MetadataRefreshProviders()...)},
		"description": "Ordered sources: nfo uses the local uniqueid identifiers (TMDB, IMDb, TVDB), every other entry is a remote provider and at least one is required. TMDB is the only provider; fallback providers register here (G14.1)."}
	reasons := []string{domain.MetadataMatchNFOTMDB, domain.MetadataMatchNFOIMDb, domain.MetadataMatchNFOTVDB, domain.MetadataMatchExistingProvider, domain.MetadataMatchTitleYear,
		domain.MetadataMatchTitleYearAmbiguous, domain.MetadataMatchTitleOnly, domain.MetadataMatchYearMismatch, domain.MetadataMatchLowSimilarity, domain.MetadataMatchExternalAmbiguous, domain.MetadataMatchExternalConflict}
	schemas["MetadataRefreshRequest"] = objectSchema(map[string]any{"priority": map[string]any{"type": "string", "enum": []string{"manual", "background"}, "default": "manual"}})
	schemas["MetadataRefreshJobState"] = objectSchema(map[string]any{"jobId": uuid, "libraryId": uuid, "trigger": map[string]any{"type": "string", "enum": []string{domain.MetadataRefreshTriggerManual, domain.MetadataRefreshTriggerCatalogSync}},
		"cursorItemId": map[string]any{"oneOf": []any{uuid, map[string]any{"type": "null"}}, "description": "The last item the job finished with; null before the first checkpoint. Items are visited in ascending ID order and a resumed job continues after it."},
		"applied":      count, "unchanged": count, "review": count, "unmatched": count, "skipped": count, "failed": count, "updatedAt": instant},
		"jobId", "libraryId", "trigger", "cursorItemId", "applied", "unchanged", "review", "unmatched", "skipped", "failed", "updatedAt")
	schemas["MetadataRefreshJobState"].(map[string]any)["description"] = "Committed counters of a metadata refresh; every item is checkpointed. applied changed at least one field through the confirmed TMDB apply, unchanged were applied without a change (every field locked, manual, NFO or equal), review were recorded for administrator review, unmatched had no provider candidate, skipped were not looked up (every field protected, a rejected review, a locked HomeVideo kind, deleted meanwhile), failed hit a provider or item-local error."
	schemas["MetadataRefreshSettings"] = objectSchema(map[string]any{"libraryId": uuid, "auto": map[string]any{"type": "boolean", "description": "Queue a refresh after every successful catalog synchronisation; off by default."}, "autoApply": threshold, "sources": sources}, "libraryId", "auto", "autoApply", "sources")
	schemas["MetadataRefreshSettingsInput"] = objectSchema(map[string]any{"auto": map[string]any{"type": "boolean"}, "autoApply": threshold, "sources": sources}, "auto", "autoApply", "sources")
	candidate := objectSchema(map[string]any{"providerId": map[string]any{"type": "integer", "format": "int32", "minimum": 1}, "title": stringSchema(1024), "originalTitle": stringSchema(1024),
		"date": map[string]any{"type": "string", "description": "YYYY-MM-DD or empty"}, "similarity": map[string]any{"type": "integer", "minimum": 0, "maximum": 100}}, "providerId", "title", "originalTitle", "date", "similarity")
	schemas["MetadataRefreshReview"] = objectSchema(map[string]any{"itemId": uuid, "libraryId": uuid, "kind": map[string]any{"type": "string", "enum": []string{"Movie", "HomeVideo", "Series"}}, "title": stringSchema(1024),
		"revision": map[string]any{"type": "integer", "minimum": 1, "maximum": domain.ItemMetadataRevisionMax - 1, "description": "The item's metadata revision an accept names as expectedRevision."},
		"state":    map[string]any{"type": "string", "enum": []string{domain.MetadataReviewPending, domain.MetadataReviewRejected}}, "resource": map[string]any{"type": "string", "enum": []string{"movie", "series"}},
		"confidence": confidence, "reason": map[string]any{"type": "string", "enum": reasons}, "queryTitle": stringSchema(1024), "queryYear": map[string]any{"type": []string{"integer", "null"}, "minimum": 1000, "maximum": 9999},
		"candidates": map[string]any{"type": "array", "minItems": 1, "maxItems": domain.MetadataRefreshMaxCandidates, "items": candidate}, "jobId": map[string]any{"oneOf": []any{uuid, map[string]any{"type": "null"}}}, "updatedAt": instant},
		"itemId", "libraryId", "kind", "title", "revision", "state", "resource", "confidence", "reason", "queryTitle", "queryYear", "candidates", "jobId", "updatedAt")
	pagination := objectSchema(map[string]any{"nextCursor": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100}}, "nextCursor", "limit")
	schemas["MetadataRefreshReviewPage"] = objectSchema(map[string]any{"reviews": map[string]any{"type": "array", "maxItems": 100, "items": schemaRef("MetadataRefreshReview")}, "pagination": pagination}, "reviews", "pagination")
	schemas["MetadataRefreshAcceptance"] = objectSchema(map[string]any{"providerId": map[string]any{"type": "integer", "format": "int32", "minimum": 1, "maximum": 2147483647, "description": "One of the review's candidates."},
		"expectedRevision": map[string]any{"type": "integer", "minimum": 1, "maximum": domain.ItemMetadataRevisionMax - 1}}, "providerId", "expectedRevision")
	if _, ok := schemas["MetadataApplyResult"]; !ok {
		metadataApplyResultSpecification(schemas)
	}
	for _, route := range []struct {
		path, method, summary, body, result string
		key, page                           bool
	}{
		{"/libraries/{id}/metadata/refresh", "post", "Queue a metadata refresh: match the library's movies, home videos and series against TMDB (NFO identifiers first, then title and year), apply matches at or above the library's autoApply threshold through the confirmed TMDB apply and record the others for review; 503 metadata_unavailable without a provider, 409 job_busy while the library has an active job", "MetadataRefreshRequest", "Job", true, false},
		{"/jobs/{id}/metadata-refresh", "get", "Read the cursor and counters of a metadata refresh; 404 for other job kinds", "", "MetadataRefreshJobState", false, false},
		{"/libraries/{id}/metadata/refresh/settings", "get", "Read whether successful catalog synchronisations queue a metadata refresh, the automatic apply threshold and the source order; a library without settings reports the defaults", "", "MetadataRefreshSettings", false, false},
		{"/libraries/{id}/metadata/refresh/settings", "put", "Replace the metadata refresh settings of a library", "MetadataRefreshSettingsInput", "MetadataRefreshSettings", false, false},
		{"/libraries/{id}/metadata/refresh/reviews", "get", "List matches awaiting review (and rejected ones) by item ID cursor; state filters pending or rejected", "", "MetadataRefreshReviewPage", false, true},
		{"/items/{id}/metadata/refresh/accept", "post", "Apply one candidate of a pending review through the confirmed TMDB apply (locks, manual values and trusted NFO keep priority; revision checked) and remove the review; 400 for a candidate the review does not list, 409 for a rejected review or a stale revision", "MetadataRefreshAcceptance", "MetadataApplyResult", false, false},
		{"/items/{id}/metadata/refresh/reject", "post", "Reject every candidate of a pending review; later refreshes leave the item alone until a match is applied by hand; rejecting again returns 409", "Empty", "MetadataRefreshReview", false, false},
	} {
		op := adminOperation(route.summary, "200", "400", "401", "403", "404", "408", "409", "413", "415", "429", "503")
		op["security"] = []any{map[string]any{"bearer": []string{}}}
		params := []any{idParameter()}
		if route.page {
			params = append(params, map[string]any{"name": "state", "in": "query", "schema": map[string]any{"type": "string", "enum": []string{domain.MetadataReviewPending, domain.MetadataReviewRejected}}},
				map[string]any{"name": "cursor", "in": "query", "schema": uuid}, map[string]any{"name": "limit", "in": "query", "schema": map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "default": 50}})
		}
		if route.key {
			params = append(params, map[string]any{"name": "Idempotency-Key", "in": "header", "required": true, "schema": map[string]any{"type": "string", "pattern": "^[!-~]{1,128}$"}})
		}
		op["parameters"] = params
		if route.body != "" {
			op["requestBody"] = map[string]any{"required": true, "description": "One strict JSON object, at most 64 KiB.", "content": map[string]any{"application/json": map[string]any{"schema": schemaRef(route.body)}}}
		}
		response := map[string]any{"description": "Successful result. A replayed submission adds Idempotency-Replayed: true; an accepted job adds Location.", "content": map[string]any{"application/json": map[string]any{"schema": objectSchema(map[string]any{"data": schemaRef(route.result)}, "data")}}}
		op["responses"].(map[string]any)["200"] = response
		if route.key {
			op["responses"].(map[string]any)["202"] = response
		}
		path := "/api/v1" + route.path
		if paths[path] == nil {
			paths[path] = map[string]any{}
		}
		paths[path].(map[string]any)[strings.ToLower(route.method)] = op
	}
}
