package httpapi

import "strings"

func nfoSpecification(paths, schemas map[string]any) {
	uuid := map[string]any{"type": "string", "format": "uuid"}
	count := map[string]any{"type": "integer", "format": "int64", "minimum": 0, "maximum": 500000}
	issueCount := map[string]any{"type": "integer", "format": "int64", "minimum": 0, "maximum": 1000000}
	boolean := map[string]any{"type": "boolean"}
	mode := map[string]any{"type": "string", "enum": []string{"off", "read-only"}}
	generation := map[string]any{"type": "integer", "format": "int64", "minimum": 1}
	entries := map[string]any{"type": "integer", "minimum": 0, "maximum": 128}
	parseFailure := map[string]any{"type": "string", "enum": []string{"", "nfo_invalid_xml", "nfo_unsafe_xml", "nfo_invalid_encoding", "nfo_unsupported_encoding", "nfo_too_complex"}}
	properties := schemas["ScanRequest"].(map[string]any)["properties"].(map[string]any)
	properties["nfo"] = map[string]any{"type": "boolean", "default": false, "description": "Validate NFO after inventory. A new opted-in job requires library mode read-only and an available NFO reader, independently of ffprobe. Omitted or false stays off for this job. Retained replay preserves original intent."}
	schemas["NFOLibraryPolicy"] = objectSchema(map[string]any{"libraryId": uuid, "mode": mode, "generation": generation}, "libraryId", "mode", "generation")
	schemas["NFOPolicyUpdate"] = objectSchema(map[string]any{"mode": mode, "expectedGeneration": generation}, "mode", "expectedGeneration")
	schemas["NFOValidateRequest"] = objectSchema(map[string]any{"priority": map[string]any{"type": "string", "enum": []string{"manual", "background"}, "default": "manual"}})
	jobProperties := map[string]any{"jobId": uuid, "libraryId": uuid, "mode": mode, "phase": map[string]any{"type": "string", "enum": []string{"disabled", "waiting_scan", "running", "done", "aborted", "cancelled"}}, "errorCode": map[string]any{"type": "string", "enum": []string{"nfo_disabled", "nfo_invalidated", "nfo_cache_capacity", "nfo_identity_mismatch", "nfo_unavailable", "nfo_cancelled", "scan_unavailable", "scan_io", "scan_limit", "scan_failed", "job_timeout", "job_attempts_exhausted"}}}
	jobRequired := []string{"jobId", "libraryId", "mode", "phase"}
	for _, name := range []string{"processed", "hits", "negativeHits", "parsed", "valid", "invalid", "warningFiles", "changed", "unavailable", "rejected"} {
		jobProperties[name] = count
		jobRequired = append(jobRequired, name)
	}
	schemas["NFOJobSummary"] = objectSchema(jobProperties, jobRequired...)
	schemas["NFOJobSummary"].(map[string]any)["description"] = "Committed historical counters. Per-file issues are current cache observations and are not reconstructed as a historical job report."
	imageProperties := map[string]any{"jobId": uuid, "libraryId": uuid, "comparisonComplete": boolean}
	imageRequired := []string{"jobId", "libraryId", "comparisonComplete"}
	for _, name := range []string{"added", "changed", "unchanged", "missing", "uncompared"} {
		imageProperties[name] = count
		imageRequired = append(imageRequired, name)
	}
	schemas["ImageJobSummary"] = objectSchema(imageProperties, imageRequired...)
	schemas["ImageJobSummary"].(map[string]any)["description"] = "Compares inventory kind, size and mtime. Does not decode images or compare content hashes. Incomplete scans report missing=0 and comparisonComplete=false; zero then does not confirm all files exist. Jobs without a committed comparison report their observed images as uncompared, without consulting a later baseline."
	observationFields := map[string]any{"id": uuid, "rootId": uuid, "path": stringSchema(1024), "status": map[string]any{"type": "string", "enum": []string{"valid", "invalid"}}, "entries": entries, "failureCode": parseFailure, "warningCount": issueCount, "errorCount": issueCount, "issueCount": issueCount, "issuesTruncated": boolean, "observedAt": map[string]any{"type": "string", "format": "date-time"}, "expiresAt": map[string]any{"type": "string", "format": "date-time"}}
	schemas["NFOObservation"] = objectSchema(observationFields, "id", "rootId", "path", "status", "entries", "failureCode", "warningCount", "errorCount", "issueCount", "issuesTruncated", "observedAt", "expiresAt")
	schemas["NFOObservationPage"] = objectSchema(map[string]any{"items": map[string]any{"type": "array", "maxItems": 50, "items": schemaRef("NFOObservation")}, "nextCursor": uuid}, "items")
	schemas["NFOObservationPage"].(map[string]any)["description"] = "Current means an unexpired observation matching current policy, roots and reader identity at query time, not a fresh filesystem read. New observations replace IDs; eviction, expiry or policy changes may remove results between pages."
	schemas["NFOIssue"] = objectSchema(map[string]any{"severity": map[string]any{"type": "string", "enum": []string{"warning", "error"}}, "code": stringSchema(64), "field": stringSchema(32), "entry": map[string]any{"type": "integer", "minimum": -1, "maximum": 127}}, "severity", "code", "field", "entry")
	schemas["NFOIssue"].(map[string]any)["description"] = "Only fixed parser code/severity/field combinations are returned. No arbitrary XML values or error text. Entry -1 denotes the document."
	schemas["NFOIssuesPage"] = objectSchema(map[string]any{"observationId": uuid, "entries": entries, "failureCode": parseFailure, "issueCount": issueCount, "issuesTruncated": boolean, "offset": map[string]any{"type": "integer", "minimum": 0, "maximum": 64}, "nextOffset": map[string]any{"type": "integer", "minimum": 1, "maximum": 63}, "issues": map[string]any{"type": "array", "maxItems": 32, "items": schemaRef("NFOIssue")}}, "observationId", "entries", "failureCode", "issueCount", "issuesTruncated", "offset", "issues")
	schemas["NFOIssuesPage"].(map[string]any)["description"] = "Pages only the retained first 64 issues. issueCount includes omitted issues; issuesTruncated does not imply the remaining issues can be retrieved. failureCode reports parse failures that have no issue rows."
	for _, route := range []struct {
		path, method, summary, body, result, page string
		key, enqueue                              bool
	}{
		{"/libraries/{id}/nfo/policy", "get", "Read the library NFO policy", "", "NFOLibraryPolicy", "", false, false},
		{"/libraries/{id}/nfo/policy", "put", "Set NFO mode with expected-generation CAS and retained idempotency", "NFOPolicyUpdate", "NFOLibraryPolicy", "", true, false},
		{"/libraries/{id}/nfo/validate", "post", "Queue a new NFO-only validation scan without changing the library policy", "NFOValidateRequest", "Job", "", true, true},
		{"/jobs/{id}/nfo", "get", "Read committed historical NFO counters", "", "NFOJobSummary", "", false, false},
		{"/jobs/{id}/images", "get", "Read image inventory attribute comparisons", "", "ImageJobSummary", "", false, false},
		{"/libraries/{id}/nfo/current-validations", "get", "List current NFO cache observations by UUID cursor", "", "NFOObservationPage", "observations", false, false},
		{"/libraries/{id}/nfo/current-validations/{observationId}/issues", "get", "Read retained issues for one current observation", "", "NFOIssuesPage", "issues", false, false},
	} {
		op := operation(route.summary, "200", "400", "401", "403", "404", "408", "409", "413", "415", "429", "503")
		op["security"] = []any{map[string]any{"bearer": []string{}}}
		op["x-jelee-role"] = "administrator"
		op["description"] = "Every request, including retained replay, checks live administrator authorization. Internal reader identities, absolute roots, hashes and original XML are never accepted as request parameters."
		parameters := []any{idParameter()}
		if strings.Contains(route.path, "{observationId}") {
			parameters = append(parameters, map[string]any{"name": "observationId", "in": "path", "required": true, "schema": uuid})
		}
		switch route.page {
		case "observations":
			parameters = append(parameters, map[string]any{"name": "cursor", "in": "query", "schema": uuid}, map[string]any{"name": "limit", "in": "query", "schema": map[string]any{"type": "integer", "minimum": 1, "maximum": 50, "default": 20}})
		case "issues":
			parameters = append(parameters, map[string]any{"name": "offset", "in": "query", "schema": map[string]any{"type": "integer", "minimum": 0, "maximum": 64, "default": 0}}, map[string]any{"name": "limit", "in": "query", "schema": map[string]any{"type": "integer", "minimum": 1, "maximum": 32, "default": 32}})
		}
		if route.key {
			parameters = append(parameters, map[string]any{"name": "Idempotency-Key", "in": "header", "required": true, "schema": map[string]any{"type": "string", "pattern": "^[!-~]{1,128}$"}})
		}
		op["parameters"] = parameters
		if route.body != "" {
			op["requestBody"] = map[string]any{"required": true, "description": "One strict JSON object, at most 64 KiB.", "content": map[string]any{"application/json": map[string]any{"schema": schemaRef(route.body)}}}
		}
		response := map[string]any{"description": "Successful result. Retained mutations may include Idempotency-Replayed: true.", "content": map[string]any{"application/json": map[string]any{"schema": objectSchema(map[string]any{"data": schemaRef(route.result)}, "data")}}}
		op["responses"].(map[string]any)["200"] = response
		if route.enqueue {
			op["responses"].(map[string]any)["202"] = response
		}
		path := "/api/v1" + route.path
		if paths[path] == nil {
			paths[path] = map[string]any{}
		}
		paths[path].(map[string]any)[route.method] = op
	}
}
