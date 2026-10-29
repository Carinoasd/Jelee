package httpapi

import "strings"

func nfoSpecification(paths, schemas map[string]any) {
	ignoreReportSpecification(paths, schemas)
	uuid := map[string]any{"type": "string", "format": "uuid"}
	count := map[string]any{"type": "integer", "format": "int64", "minimum": 0, "maximum": 500000}
	issueCount := map[string]any{"type": "integer", "format": "int64", "minimum": 0, "maximum": 1000000}
	boolean := map[string]any{"type": "boolean"}
	mode := map[string]any{"type": "string", "enum": []string{"off", "read-only"}}
	generation := map[string]any{"type": "integer", "format": "int64", "minimum": 1}
	entries := map[string]any{"type": "integer", "minimum": 0, "maximum": 128}
	parseFailure := map[string]any{"type": "string", "enum": []string{"", "nfo_invalid_xml", "nfo_unsafe_xml", "nfo_invalid_encoding", "nfo_unsupported_encoding", "nfo_too_complex"}}
	properties := schemas["ScanRequest"].(map[string]any)["properties"].(map[string]any)
	schemas["IgnoreIntent"] = objectSchema(map[string]any{"mode": map[string]any{"type": "string", "enum": []string{"jeleeignore", "jeleeignore-legacy-v1"}}, "caseMode": map[string]any{"type": "string", "enum": []string{"sensitive", "ascii-insensitive"}}}, "mode", "caseMode")
	properties["ignore"] = schemaRef("IgnoreIntent")
	properties["nfo"] = map[string]any{"type": "boolean", "default": false, "description": "Validate NFO after inventory. A new opted-in job requires library mode read-only and an available NFO reader, independently of ffprobe. Omitted or false stays off for this job. Retained replay preserves original intent."}
	schemas["NFOLibraryPolicy"] = objectSchema(map[string]any{"libraryId": uuid, "mode": mode, "generation": generation, "writeEnabled": boolean}, "libraryId", "mode", "generation", "writeEnabled")
	schemas["NFOPolicyUpdate"] = objectSchema(map[string]any{"mode": mode, "expectedGeneration": generation}, "mode", "expectedGeneration")
	schemas["NFOValidateRequest"] = objectSchema(map[string]any{"priority": map[string]any{"type": "string", "enum": []string{"manual", "background"}, "default": "manual"}})
	priority := map[string]any{"type": "string", "enum": []string{"manual", "background"}, "default": "manual"}
	schemas["NFOWritePolicyUpdate"] = objectSchema(map[string]any{"enabled": map[string]any{"type": "boolean", "description": "Enabling needs library mode read-only, otherwise 409 conflict. Disabling stops queued and running writes at their next step."}}, "enabled")
	schemas["NFOWriteTextEdit"] = objectSchema(map[string]any{"field": map[string]any{"type": "string", "enum": []string{"title", "originaltitle", "sorttitle", "plot", "outline", "tagline", "showtitle", "status", "mpaa", "certification"}}, "value": map[string]any{"type": "string"}}, "field", "value")
	schemas["NFOWritePrepareRequest"] = objectSchema(map[string]any{
		"revision": map[string]any{"type": "integer", "format": "int64", "minimum": 1, "description": "Expected item metadata revision."},
		"edits":    map[string]any{"type": "array", "maxItems": 10, "items": schemaRef("NFOWriteTextEdit"), "description": "Ordered text edits; a field may appear once."},
		"bom":      map[string]any{"type": "string", "enum": []string{"preserve", "include", "omit"}, "default": "preserve"},
		"indent":   map[string]any{"type": "string", "maxLength": 8, "description": "Spaces and tabs only."},
		"maxBytes": map[string]any{"type": "integer", "format": "int64", "minimum": 1, "maximum": 33554432, "default": 8388608},
		"backups":  map[string]any{"type": "integer", "minimum": 0, "maximum": 16, "default": 3, "description": "Backups to keep when the file is replaced. With create it must be 0 (the default then): a created file has nothing to back up."},
		"create":   map[string]any{"type": "boolean", "default": false, "description": "Create a missing NFO instead of rewriting an existing one. The document is built from an empty root of the item's kind (movie, tvshow, season or episodedetails) with the edits; the file is named like the NFO reader looks it up (media basename plus .nfo, or tvshow.nfo or season.nfo in the series or season directory). 409 conflict when that name already exists in any form. At commit the file is only ever created by a hardlink and an object that appears at the name is never replaced."},
	}, "revision", "edits")
	sha := map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"}
	size := map[string]any{"type": "integer", "minimum": 1, "maximum": 33554432}
	originalSize := map[string]any{"type": "integer", "minimum": 0, "maximum": 33554432, "description": "0 for a create preparation: there is no original."}
	schemas["NFOWritePreparation"] = objectSchema(map[string]any{"id": uuid, "itemId": uuid, "libraryId": uuid, "revision": map[string]any{"type": "integer", "format": "int64", "minimum": 1},
		"fields": map[string]any{"type": "array", "maxItems": 10, "items": map[string]any{"type": "string"}}, "originalBytes": originalSize, "replacementBytes": size, "originalSha256": sha, "replacementSha256": sha,
		"createdAt": map[string]any{"type": "string", "format": "date-time"}, "expiresAt": map[string]any{"type": "string", "format": "date-time"}},
		"id", "itemId", "libraryId", "revision", "fields", "originalBytes", "replacementBytes", "originalSha256", "replacementSha256", "createdAt", "expiresAt")
	schemas["NFOWritePreparation"].(map[string]any)["description"] = "Summary of a stored preparation: byte counts and digests only. File content, paths and roots are never returned. A preparation grants no write authority; it expires after 24 hours."
	schemas["NFOWriteSubmitRequest"] = objectSchema(map[string]any{"preparations": map[string]any{"type": "array", "minItems": 1, "maxItems": 100, "uniqueItems": true, "items": uuid}, "priority": priority}, "preparations")
	schemas["NFOApplyRequest"] = objectSchema(map[string]any{"priority": priority})
	applyCount := map[string]any{"type": "integer", "minimum": 0}
	schemas["NFOApplyJobState"] = objectSchema(map[string]any{"jobId": uuid, "libraryId": uuid, "cursorItemId": map[string]any{"oneOf": []any{uuid, map[string]any{"type": "null"}}, "description": "The last item the job finished with; null before the first checkpoint. Items are visited in ascending ID order and a resumed job continues after it."},
		"applied": applyCount, "unchanged": applyCount, "skipped": applyCount, "failed": applyCount, "updatedAt": map[string]any{"type": "string", "format": "date-time"}},
		"jobId", "libraryId", "cursorItemId", "applied", "unchanged", "skipped", "failed", "updatedAt")
	schemas["NFOApplyJobState"].(map[string]any)["description"] = "Committed counters of a batch NFO apply, at most one checkpoint (50 items) behind. applied changed fields, unchanged had nothing to change, skipped had no applicable NFO (missing or invalid NFO, no media source, unresolved scope), failed hit an item-local error such as a repeated revision conflict."
	schemas["NFOExportRequest"] = objectSchema(map[string]any{"priority": priority, "mode": map[string]any{"type": "string", "enum": []string{"all", "missing", "fix"}, "default": "all", "description": "all writes the stored values into existing NFOs; missing creates the NFO of every item that has none and never touches an existing NFO; fix repairs only the validation issues that can be repaired safely (a missing title the database stores; the rewrite also adds a missing Jelee identifier) with a .jelee.bak backup and counts the rest as unfixable. Part of the idempotency request: replaying a key with another mode is 409 conflict."}})
	schemas["NFOExportJobState"] = objectSchema(map[string]any{"jobId": uuid, "libraryId": uuid, "mode": map[string]any{"type": "string", "enum": []string{"all", "missing", "fix"}}, "cursorItemId": map[string]any{"oneOf": []any{uuid, map[string]any{"type": "null"}}, "description": "The last item the job prepared or skipped; null before the first checkpoint. Items are visited in ascending ID order and a resumed job continues after it."},
		"childJobId": map[string]any{"oneOf": []any{uuid, map[string]any{"type": "null"}}, "description": "The nfo_write job whose outcome is not yet counted; null while no write job is outstanding."},
		"chunks":     applyCount, "written": applyCount, "fixed": applyCount, "unfixable": applyCount, "unchanged": applyCount, "skippedExisting": applyCount, "skippedMissing": applyCount, "skippedInvalid": applyCount, "skippedOther": applyCount, "failed": applyCount, "updatedAt": map[string]any{"type": "string", "format": "date-time"}},
		"jobId", "libraryId", "mode", "cursorItemId", "childJobId", "chunks", "written", "fixed", "unfixable", "unchanged", "skippedExisting", "skippedMissing", "skippedInvalid", "skippedOther", "failed", "updatedAt")
	schemas["NFOExportJobState"].(map[string]any)["description"] = "Counters of a batch NFO export. written entries were replaced by a child nfo_write job (counted when that job ends), unchanged had no differing stored value, skippedExisting (missing mode) already had an NFO file, or got one while the job ran, and was left alone, skippedMissing (all and fix mode) had no NFO file, skippedInvalid had an invalid NFO, skippedOther could not be prepared (no media source, locked or unsupported item), failed hit an item-local error or were prepared but not replaced. In fix mode the entries the child job replaced are counted as fixed (written stays 0) and unfixable counts the items that have a validation issue the fix does not repair (parse failures, error issues, an illegal path, a lock, nothing safe to write). chunks is the number of child write jobs submitted."
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
	schemas["NFOIssue"].(map[string]any)["description"] = "Only fixed parser code/severity/field combinations are returned. No arbitrary XML values or error text. Entry -1 denotes the document. An illegal location of the NFO is one error issue with field path and a code nfo_path_symlink, nfo_path_element_symlink, nfo_path_not_regular, nfo_path_case_collision, nfo_path_outside_root, nfo_path_too_long or nfo_path_invalid_name; the file was not read (entries 0, encoding and root unknown) and the issue is never repaired automatically."
	schemas["NFOIssuesPage"] = objectSchema(map[string]any{"observationId": uuid, "entries": entries, "failureCode": parseFailure, "issueCount": issueCount, "issuesTruncated": boolean, "offset": map[string]any{"type": "integer", "minimum": 0, "maximum": 64}, "nextOffset": map[string]any{"type": "integer", "minimum": 1, "maximum": 63}, "issues": map[string]any{"type": "array", "maxItems": 32, "items": schemaRef("NFOIssue")}}, "observationId", "entries", "failureCode", "issueCount", "issuesTruncated", "offset", "issues")
	schemas["NFOIssuesPage"].(map[string]any)["description"] = "Pages only the retained first 64 issues. issueCount includes omitted issues; issuesTruncated does not imply the remaining issues can be retrieved. failureCode reports parse failures that have no issue rows."
	for _, route := range []struct {
		path, method, summary, body, result, page string
		key, enqueue                              bool
		created                                   bool
	}{
		{"/libraries/{id}/nfo/policy", "get", "Read the library NFO policy", "", "NFOLibraryPolicy", "", false, false, false},
		{"/libraries/{id}/nfo/policy", "put", "Set NFO mode with expected-generation CAS and retained idempotency", "NFOPolicyUpdate", "NFOLibraryPolicy", "", true, false, false},
		{"/libraries/{id}/nfo/validate", "post", "Queue a new NFO-only validation scan without changing the library policy", "NFOValidateRequest", "Job", "", true, true, false},
		{"/libraries/{id}/nfo/write-policy", "put", "Allow or forbid NFO write-back for a library", "NFOWritePolicyUpdate", "NFOLibraryPolicy", "", false, false, false},
		{"/items/{id}/nfo/write-preparations", "post", "Prepare controlled NFO text edits for an item; answers 201 first and 200 with Idempotency-Replayed on replay, 503 nfo_reader_unavailable when the service is off", "NFOWritePrepareRequest", "NFOWritePreparation", "", true, false, true},
		{"/libraries/{id}/nfo/writes", "post", "Queue prepared NFO edits as one write job; 409 nfo_write_disabled unless the library is read-only with writes enabled", "NFOWriteSubmitRequest", "Job", "", true, true, false},
		{"/libraries/{id}/nfo/apply", "post", "Queue a batch apply of every item's NFO fields through the per-item apply path; 409 conflict unless the library is read-only, 409 job_busy while the library has an active job", "NFOApplyRequest", "Job", "", true, true, false},
		{"/jobs/{id}/nfo-apply", "get", "Read the cursor and counters of a batch NFO apply; 404 for other job kinds", "", "NFOApplyJobState", "", false, false, false},
		{"/libraries/{id}/nfo/export", "post", "Queue a batch export of every item's stored metadata into its existing NFO (mode all), into newly created NFOs for the items that have none (mode missing), or repairs the safely repairable validation issues of existing NFOs (mode fix), through preparations and child nfo_write jobs; 400 for an unknown mode; 409 nfo_write_disabled unless the library is read-only with writes enabled, 409 job_busy while the library has an active job", "NFOExportRequest", "Job", "", true, true, false},
		{"/jobs/{id}/nfo-export", "get", "Read the cursor and counters of a batch NFO export; 404 for other job kinds", "", "NFOExportJobState", "", false, false, false},
		{"/jobs/{id}/nfo", "get", "Read committed historical NFO counters", "", "NFOJobSummary", "", false, false, false},
		{"/jobs/{id}/images", "get", "Read image inventory attribute comparisons", "", "ImageJobSummary", "", false, false, false},
		{"/libraries/{id}/nfo/current-validations", "get", "List current NFO cache observations by UUID cursor", "", "NFOObservationPage", "observations", false, false, false},
		{"/libraries/{id}/nfo/current-validations/{observationId}/issues", "get", "Read retained issues for one current observation", "", "NFOIssuesPage", "issues", false, false, false},
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
		if route.created {
			op["responses"].(map[string]any)["201"] = response
		}
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
