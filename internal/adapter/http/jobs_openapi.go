package httpapi

import "strings"

func jobSpecification(paths, schemas map[string]any) {
	uuid := map[string]any{"type": "string", "format": "uuid"}
	count := map[string]any{"type": "integer", "format": "int64", "minimum": 0}
	instant := map[string]any{"type": "string", "format": "date-time"}
	state := map[string]any{"type": "string", "enum": []string{"queued", "running", "succeeded", "failed", "cancelled"}}
	priority := map[string]any{"type": "string", "enum": []string{"manual", "background"}, "default": "manual"}
	schemas["ScanRequest"] = objectSchema(map[string]any{"priority": priority})
	schemas["Job"] = objectSchema(map[string]any{"id": uuid, "libraryId": uuid, "kind": map[string]any{"type": "string", "const": "inventory_scan"}, "state": state, "priority": priority, "attempts": count, "cancelRequested": map[string]any{"type": "boolean"}, "files": count, "directories": count, "skipped": count, "bytes": count, "missing": count, "reviewRequired": map[string]any{"type": "boolean"}, "errorCode": stringSchema(64), "createdAt": instant, "startedAt": instant, "finishedAt": instant}, "id", "libraryId", "kind", "state", "priority", "attempts", "cancelRequested", "files", "directories", "skipped", "bytes", "missing", "reviewRequired", "createdAt")
	schemas["InventoryEntry"] = objectSchema(map[string]any{"id": uuid, "rootId": uuid, "path": stringSchema(1024), "kind": map[string]any{"type": "string", "enum": []string{"video", "nfo", "image", "other"}}, "size": count, "modifiedUnixNano": map[string]any{"type": "integer", "format": "int64"}}, "id", "rootId", "path", "kind", "size", "modifiedUnixNano")
	schemas["LibrarySummary"] = objectSchema(map[string]any{"id": uuid, "name": stringSchema(128), "roots": count}, "id", "name", "roots")
	pagination := objectSchema(map[string]any{"nextCursor": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100}}, "nextCursor", "limit")
	for _, pair := range []struct{ name, item, field string }{{"JobPage", "Job", "jobs"}, {"InventoryPage", "InventoryEntry", "entries"}, {"LibraryPage", "LibrarySummary", "libraries"}} {
		schemas[pair.name] = objectSchema(map[string]any{pair.field: map[string]any{"type": "array", "maxItems": 100, "items": schemaRef(pair.item)}, "pagination": pagination}, pair.field, "pagination")
	}
	for _, route := range []struct {
		path, method, summary, body, result string
		page, key                           bool
	}{
		{"/libraries", "get", "List registered libraries without root paths", "", "LibraryPage", true, false},
		{"/libraries/{id}/scan", "post", "Queue a readonly inventory scan; replay is bounded by retained job history", "ScanRequest", "Job", false, true},
		{"/jobs", "get", "List retained jobs by UUID cursor", "", "JobPage", true, false},
		{"/jobs/{id}", "get", "Read job progress; total work and ETA remain unknown", "", "Job", false, false},
		{"/jobs/{id}/entries", "get", "List observed inventory; partial runs never authorize deletion", "", "InventoryPage", true, false},
		{"/jobs/{id}/cancel", "post", "Persist cancellation; running work stops at its next checkpoint or heartbeat", "Empty", "Job", false, false},
		{"/jobs/{id}/retry", "post", "Create a fresh scan for a failed or cancelled run", "Empty", "Job", false, true},
	} {
		op := operation(route.summary, "200", "400", "401", "403", "404", "408", "409", "413", "415", "429", "503")
		op["security"] = []any{map[string]any{"bearer": []string{}}}
		op["x-jelee-role"] = "administrator"
		params := []any{}
		if strings.Contains(route.path, "{id}") {
			params = append(params, idParameter())
		}
		if route.page {
			params = append(params, map[string]any{"name": "cursor", "in": "query", "schema": uuid}, map[string]any{"name": "limit", "in": "query", "schema": map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "default": 50}})
		}
		if route.path == "/jobs" {
			params = append(params, map[string]any{"name": "state", "in": "query", "schema": state})
		}
		if route.key {
			params = append(params, map[string]any{"name": "Idempotency-Key", "in": "header", "required": true, "schema": map[string]any{"type": "string", "pattern": "^[!-~]{1,128}$"}})
		}
		if len(params) > 0 {
			op["parameters"] = params
		}
		if route.body != "" {
			op["requestBody"] = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": schemaRef(route.body)}}, "description": "Maximum 64 KiB; one strict JSON object. Cancel and retry require an empty JSON object."}
		}
		response := map[string]any{"description": "Successful result; replay adds Idempotency-Replayed: true; accepted jobs add Location.", "content": map[string]any{"application/json": map[string]any{"schema": objectSchema(map[string]any{"data": schemaRef(route.result)}, "data")}}}
		op["responses"].(map[string]any)["200"] = response
		if route.key {
			op["responses"].(map[string]any)["202"] = response
		}
		paths["/api/v1"+route.path] = map[string]any{route.method: op}
	}
}
