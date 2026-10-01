package httpapi

func ignoreReportSpecification(paths, schemas map[string]any) {
	uuid := map[string]any{"type": "string", "format": "uuid"}
	boolean := map[string]any{"type": "boolean"}
	count := map[string]any{"type": "integer", "format": "int64", "minimum": 0}
	cursor := stringSchema(4096)
	schemas["IgnoreReportEntry"] = objectSchema(map[string]any{
		"source": map[string]any{"type": "string", "enum": []string{"baseline", "scan"}},
		"rootId": uuid, "path": stringSchema(1024),
		"kind":          map[string]any{"type": "string", "enum": []string{"directory", "video", "nfo", "image", "other"}},
		"outcome":       map[string]any{"type": "string", "enum": []string{"excluded", "included_missing", "unknown"}},
		"family":        map[string]any{"type": "string", "enum": []string{"jeleeignore", "legacy-ignore-021"}},
		"ruleDirectory": stringSchema(1024),
		"ruleLine":      map[string]any{"type": "integer", "minimum": 1, "maximum": 4096},
		"matchedPath":   stringSchema(1024),
		"reason":        map[string]any{"type": "string", "enum": []string{"source_unavailable", "source_changed", "coverage_unknown", "rule", "blank-source", "invalid-source"}},
	}, "source", "rootId", "path", "outcome")
	schemas["IgnoreReportEntry"].(map[string]any)["description"] = "Paths are relative to rootId. Excluded entries include ruleDirectory and matchedPath. Family mode adds family and reason; blank-source and invalid-source omit ruleLine because no individual line matched. Rule matches use one-based ruleLine. Unknown entries include reason and omit family. Scan entries have kind and outcome excluded. Baseline entries classify retained old paths and omit kind."
	schemas["IgnoreReport"] = objectSchema(map[string]any{
		"jobId":   uuid,
		"state":   map[string]any{"type": "string", "enum": []string{"succeeded", "failed", "cancelled"}},
		"enabled": boolean, "reviewRequired": boolean, "invalidated": boolean,
		"excludedFiles": count, "excludedDirectories": count, "unknown": count,
		"entries":    map[string]any{"type": "array", "maxItems": 100, "items": schemaRef("IgnoreReportEntry")},
		"nextCursor": cursor,
	}, "jobId", "state", "enabled", "reviewRequired", "invalidated", "excludedFiles", "excludedDirectories", "unknown", "entries")
	op := operation("Read retained ignore decisions for a terminal job", "200", "400", "401", "403", "404", "408", "409", "429", "503")
	op["security"] = []any{map[string]any{"bearer": []string{}}}
	op["x-jelee-role"] = "administrator"
	op["description"] = "Requires live administrator authorization on every page. Queued or running jobs return 409. Failed or cancelled jobs may contain partial observations. Counts describe current scan exclusions and unknown baseline decisions; entries include all retained baseline decisions and scan exclusions, so the same relative path may appear once per source. No filesystem read is performed. Cursor is opaque and bound to this job; history cleanup may remove the report."
	op["parameters"] = []any{idParameter(),
		map[string]any{"name": "cursor", "in": "query", "schema": cursor},
		map[string]any{"name": "limit", "in": "query", "schema": map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "default": 50}},
	}
	op["responses"].(map[string]any)["200"] = map[string]any{"description": "Retained report page", "content": map[string]any{"application/json": map[string]any{"schema": objectSchema(map[string]any{"data": schemaRef("IgnoreReport")}, "data")}}}
	paths["/api/v1/jobs/{id}/ignore"] = map[string]any{"get": op}
}
