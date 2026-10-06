package httpapi

import "github.com/MoYuanCN/Jelee/internal/domain"

// repairSpecification documents the self-healing repair routes (G50.4).
// They are mounted with the job routes. See docs/repair.md.
func repairSpecification(paths, schemas map[string]any) {
	uuid := map[string]any{"type": "string", "format": "uuid"}
	count := map[string]any{"type": "integer", "format": "int64", "minimum": 0}
	instant := map[string]any{"type": "string", "format": "date-time"}
	targets := []string{domain.RepairTargetCatalogVideo, domain.RepairTargetImageVariant, domain.RepairTargetProbeStale, domain.RepairTargetProbeOrphan, domain.RepairTargetVariantOrphan,
		domain.RepairTargetDailyCounters, domain.RepairTargetNFOObservation, domain.RepairTargetUserData, domain.RepairTargetSession}
	action := map[string]any{"type": "string", "enum": domain.RepairActions()}
	schemas["RepairRequest"] = objectSchema(map[string]any{
		"action":      action,
		"libraryId":   map[string]any{"type": "string", "format": "uuid", "description": "Limit the action to one library; omit for every library. image-variants is store-wide and refuses a library."},
		"dryRun":      map[string]any{"type": "boolean", "default": false, "description": "List the affected objects and their number without writing anything."},
		"iUnderstand": map[string]any{"type": "boolean", "description": "Required true unless dryRun; otherwise 400 confirmation_required."},
		"statBudget":  map[string]any{"type": "integer", "minimum": 0, "maximum": domain.ConsistencyMaxStatBudget, "description": "File and variant probes of the orphans action; 0 uses the configured budget."},
	}, "action")
	schemas["RepairTargetCount"] = objectSchema(map[string]any{"kind": map[string]any{"type": "string", "enum": targets}, "planned": count, "applied": count, "skipped": count}, "kind", "planned", "applied", "skipped")
	schemas["RepairSample"] = objectSchema(map[string]any{"kind": map[string]any{"type": "string", "enum": targets}, "libraryId": uuid, "itemId": uuid, "sourceId": uuid, "userId": uuid,
		"day": map[string]any{"type": "string", "format": "date"}, "object": stringSchema(160), "path": map[string]any{"type": "string", "description": "Root-relative per logging.pathMode, or [redacted]; never absolute."}}, "kind")
	schemas["RepairJob"] = objectSchema(map[string]any{"libraryId": uuid, "jobId": uuid, "replayed": map[string]any{"type": "boolean", "description": "The library already had the job active; nothing new was queued."}}, "libraryId", "jobId", "replayed")
	schemas["RepairResult"] = objectSchema(map[string]any{
		"schema": map[string]any{"const": domain.RepairResultSchema}, "action": action, "runId": uuid, "origin": map[string]any{"type": "string", "enum": []string{domain.RepairOriginCLI, domain.RepairOriginAPI}},
		"libraryId": uuid, "dryRun": map[string]any{"type": "boolean"},
		"state":      map[string]any{"type": "string", "enum": []string{domain.RepairStatePlanned, domain.RepairStateCompleted, domain.RepairStatePartial, domain.RepairStateFailed}},
		"reason":     map[string]any{"type": "string", "enum": []string{domain.RepairReasonNoBaseline, domain.RepairReasonUnconfirmed, domain.RepairReasonStoreDisabled, domain.RepairReasonFailed}},
		"revertible": map[string]any{"type": "boolean"}, "planned": count, "applied": count, "skipped": count,
		"targets": map[string]any{"type": "array", "items": schemaRef("RepairTargetCount")}, "jobs": map[string]any{"type": "array", "items": schemaRef("RepairJob")},
		"info":    map[string]any{"type": "object", "additionalProperties": count},
		"samples": map[string]any{"type": "array", "maxItems": domain.RepairSamples, "items": schemaRef("RepairSample")}, "samplesTruncated": map[string]any{"type": "boolean"},
		"startedAt": instant, "finishedAt": instant,
	}, "schema", "action", "origin", "dryRun", "state", "revertible", "planned", "applied", "skipped", "targets", "samples", "samplesTruncated", "startedAt", "finishedAt")
	schemas["RepairRevertRequest"] = objectSchema(map[string]any{"iUnderstand": map[string]any{"type": "boolean", "description": "Required true; otherwise 400 confirmation_required."}}, "iUnderstand")
	schemas["RepairRevertResult"] = objectSchema(map[string]any{"runId": uuid, "reverted": count, "skipped": count}, "runId", "reverted", "skipped")
	data := func(schema map[string]any) map[string]any {
		return map[string]any{"application/json": map[string]any{"schema": objectSchema(map[string]any{"data": schema}, "data")}}
	}
	admin := func(op map[string]any, body string, result string) map[string]any {
		op["security"] = []any{map[string]any{"bearer": []string{}}}
		op["x-jelee-role"] = "administrator"
		op["requestBody"] = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": schemaRef(body)}}, "description": "Maximum 64 KiB; one strict JSON object."}
		op["responses"].(map[string]any)["200"].(map[string]any)["content"] = data(schemaRef(result))
		return op
	}
	run := admin(operation("Plan or run a self-healing repair action", "200", "400", "401", "403", "404", "408", "409", "413", "415", "429", "503"), "RepairRequest", "RepairResult")
	run["description"] = "Actions: items (queue catalog synchronisation for baseline videos without an item), image-variants (clear the live image variant generation; variants render again on request), caches (drop probe cache rows of changed files), stats (recount daily statistics from sessions on stored days), orphans (remove probe cache rows of deleted files and variant index rows without a file), nfo (queue an NFO validating scan for stale NFO observations), counts (clear version references to another item). A dry run writes nothing and lists the affected objects and their number; an execution applies the same plan, records a run, audits every batch as repair.applied and the run as repair.finished, and is idempotent: a second execution affects nothing. stats and counts are journaled and revertible. A library with another active job answers 409 job_busy; nfo without the scan pipeline 503 nfo_reader_unavailable; image-variants without an image store 404 image_unavailable. The request deadline applies; use jelee-cli repair on the host for large libraries."
	paths["/api/v1/admin/repairs"] = map[string]any{"post": run}
	revert := admin(operation("Revert a stats or counts repair run", "200", "400", "401", "403", "404", "408", "409", "413", "415", "503"), "RepairRevertRequest", "RepairRevertResult")
	revert["description"] = "Restores the values before every journaled repair of the run, newest first, while the row still holds the repaired value; others are skipped and counted. Reverted entries are never replayed. Other actions are not revertible (409 conflict). Audited as repair.reverted."
	revert["parameters"] = []any{idParameter()}
	paths["/api/v1/admin/repairs/{id}/revert"] = map[string]any{"post": revert}
}
