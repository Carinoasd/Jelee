package httpapi

import (
	"net/http"
	"strings"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/go-chi/chi/v5"
)

// maintenanceRoutes mounts the maintenance jobs (G13.6, G14.7): the
// extraction cache cleanup, the backup reminder and the removal of a
// library's external metadata, with their progress. Every route is
// administrator-only.
func (s *Server) maintenanceRoutes(r chi.Router) {
	r.Post("/api/v1/maintenance/subtitle-cache/cleanup", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		var input struct {
			Priority    string `json:"priority"`
			MaxIdleDays int    `json:"maxIdleDays"`
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
		job, replay, err := s.jobs.SubmitSubtitleCacheCleanup(r.Context(), a, key, input.Priority, input.MaxIdleDays)
		return acceptedJob(w, job, replay, err)
	}))
	r.Get("/api/v1/jobs/{id}/subtitle-cache-cleanup", s.accountEndpoint(true, false, func(_ http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		v, err := s.jobs.SubtitleCacheCleanupState(r.Context(), a, chi.URLParam(r, "id"))
		return v, http.StatusOK, err
	}))
	r.Post("/api/v1/maintenance/backup-reminder", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		var input struct {
			Priority      string `json:"priority"`
			ThresholdDays int    `json:"thresholdDays"`
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
		job, replay, err := s.jobs.SubmitBackupReminder(r.Context(), a, key, input.Priority, input.ThresholdDays)
		return acceptedJob(w, job, replay, err)
	}))
	r.Get("/api/v1/jobs/{id}/backup-reminder", s.accountEndpoint(true, false, func(_ http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		v, err := s.jobs.BackupReminderState(r.Context(), a, chi.URLParam(r, "id"))
		return v, http.StatusOK, err
	}))
	r.Get("/api/v1/maintenance/backup-status", s.accountEndpoint(true, false, func(_ http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		v, err := s.jobs.BackupStatus(r.Context(), a)
		return v, http.StatusOK, err
	}))
	r.Post("/api/v1/libraries/{id}/metadata/external/remove", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		var input struct {
			Priority            string `json:"priority"`
			Confirm             bool   `json:"confirm"`
			DropRejectedReviews bool   `json:"dropRejectedReviews"`
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
		job, replay, err := s.jobs.SubmitExternalMetadataRemoval(r.Context(), a, chi.URLParam(r, "id"), key, domain.ExternalMetadataRemovalInput{Priority: input.Priority, Confirm: input.Confirm, DropRejectedReviews: input.DropRejectedReviews})
		return acceptedJob(w, job, replay, err)
	}))
	r.Get("/api/v1/jobs/{id}/external-metadata-removal", s.accountEndpoint(true, false, func(_ http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		v, err := s.jobs.ExternalMetadataRemovalState(r.Context(), a, chi.URLParam(r, "id"))
		return v, http.StatusOK, err
	}))
}

func maintenanceSpecification(paths, schemas map[string]any) {
	uuid := map[string]any{"type": "string", "format": "uuid"}
	count := map[string]any{"type": "integer", "minimum": 0}
	instant := map[string]any{"type": "string", "format": "date-time"}
	priority := map[string]any{"type": "string", "enum": []string{"manual", "background"}, "default": "manual"}
	nullable := func(v map[string]any) map[string]any {
		return map[string]any{"oneOf": []any{v, map[string]any{"type": "null"}}}
	}
	schemas["SubtitleCacheCleanupRequest"] = objectSchema(map[string]any{"priority": priority,
		"maxIdleDays": map[string]any{"type": "integer", "minimum": 0, "maximum": domain.SubtitleCacheIdleDaysMax, "default": domain.SubtitleCacheIdleDaysDefault, "description": "Revisions of live sources not used for longer are removed; 0 selects the default of 30 days."}})
	schemas["SubtitleCacheCleanupJobState"] = objectSchema(map[string]any{"jobId": uuid, "maxIdleDays": map[string]any{"type": "integer", "minimum": 1, "maximum": domain.SubtitleCacheIdleDaysMax},
		"phase":        map[string]any{"type": "string", "enum": []string{domain.SubtitleCachePhaseSources, domain.SubtitleCachePhaseCapacity, domain.SubtitleCachePhaseDone}, "description": "sources walks the source directories in name order, capacity trims the cache to its size bound, done has nothing left."},
		"cursorSource": nullable(uuid), "sources": count, "orphaned": count, "revisions": count, "capacity": count, "failed": count, "freedBytes": map[string]any{"type": "integer", "format": "int64", "minimum": 0}, "updatedAt": instant},
		"jobId", "maxIdleDays", "phase", "cursorSource", "sources", "orphaned", "revisions", "capacity", "failed", "freedBytes", "updatedAt")
	schemas["SubtitleCacheCleanupJobState"].(map[string]any)["description"] = "Committed counters of an extraction cache cleanup. sources counts source directories finished without a failed removal; orphaned whole directories of deleted sources; revisions idle, unreadable or crashed staging revisions of live sources; capacity revisions removed to fit the size bound; failed directories or revisions that could not be removed and stay for a later run."
	schemas["BackupReminderRequest"] = objectSchema(map[string]any{"priority": priority,
		"thresholdDays": map[string]any{"type": "integer", "minimum": 0, "maximum": domain.BackupReminderDaysMax, "description": "A metadata export older than this many days is overdue; 0 selects the configured threshold (jobs.backupReminderDays, 7 when unset)."}})
	schemas["BackupStatus"] = objectSchema(map[string]any{"status": map[string]any{"type": "string", "enum": []string{domain.BackupReminderOK, domain.BackupReminderOverdue, domain.BackupReminderNever}, "description": "never: no metadata export is recorded (none made, or older than the security audit retention)."},
		"thresholdDays": map[string]any{"type": "integer", "minimum": 1, "maximum": domain.BackupReminderDaysMax}, "lastExportAt": nullable(instant), "ageSeconds": nullable(map[string]any{"type": "integer", "format": "int64", "minimum": 0}), "checkedAt": instant},
		"status", "thresholdDays", "lastExportAt", "ageSeconds", "checkedAt")
	schemas["BackupStatus"].(map[string]any)["description"] = "The last jelee-cli metadata export compared with the threshold. Full PostgreSQL dumps are made outside Jelee and are not visible here."
	schemas["BackupReminderJobState"] = objectSchema(map[string]any{"jobId": uuid, "trigger": map[string]any{"type": "string", "enum": []string{domain.BackupReminderManual, domain.BackupReminderSchedule}},
		"thresholdDays": map[string]any{"type": "integer", "minimum": 1, "maximum": domain.BackupReminderDaysMax}, "result": map[string]any{"oneOf": []any{schemaRef("BackupStatus"), map[string]any{"type": "null"}}, "description": "Null until the check ran."}, "updatedAt": instant},
		"jobId", "trigger", "thresholdDays", "result", "updatedAt")
	schemas["ExternalMetadataRemovalRequest"] = objectSchema(map[string]any{"priority": priority,
		"confirm":             map[string]any{"type": "boolean", "description": "Must be true; without it the request is refused with 400 confirmation_required."},
		"dropRejectedReviews": map[string]any{"type": "boolean", "default": false, "description": "Also delete the administrator's rejections of automatic matches. They hold provider candidate titles; without them later automatic refreshes may match the items again."}}, "confirm")
	schemas["ExternalMetadataRemovalJobState"] = objectSchema(map[string]any{"jobId": uuid, "libraryId": uuid, "dropRejectedReviews": map[string]any{"type": "boolean"},
		"cursorItemId": map[string]any{"oneOf": []any{uuid, map[string]any{"type": "null"}}, "description": "The last item the job finished with; items are visited in ascending ID order and a resumed job continues after it."},
		"removed":      count, "fields": count, "kept": count, "skipped": count, "failed": count, "reviews": count, "updatedAt": instant},
		"jobId", "libraryId", "dropRejectedReviews", "cursorItemId", "removed", "fields", "kept", "skipped", "failed", "reviews", "updatedAt")
	schemas["ExternalMetadataRemovalJobState"].(map[string]any)["description"] = "Committed counters of a library-wide external metadata removal; every item is checkpointed. removed items lost at least one provider field (fields counts them); kept items have provider fields left only because they are locked or required; skipped items had no provider field left or were deleted meanwhile; failed items were refused on their own and left unchanged; reviews counts deleted automatic match reviews."
	for _, route := range []struct {
		path, method, summary, body, result string
		id, key                             bool
	}{
		{"/maintenance/subtitle-cache/cleanup", "post", "Queue a cleanup of Jelee's embedded subtitle and font extraction cache: remove the directories of deleted sources, revisions idle longer than maxIdleDays and crashed staging, then trim the cache to its size bound; the cache is instance-wide and the job is queued under the first library without an active job; 503 subtitle_cache_unavailable without a configured cache, 409 job_busy while a cleanup is active or every library is busy", "SubtitleCacheCleanupRequest", "Job", false, true},
		{"/jobs/{id}/subtitle-cache-cleanup", "get", "Read the phase, cursor and counters of an extraction cache cleanup; 404 for other job kinds", "", "SubtitleCacheCleanupJobState", true, false},
		{"/maintenance/backup-reminder", "post", "Queue a backup check: a metadata export older than thresholdDays, or none, records a backup.reminder audit entry; nothing is backed up; queued under the first library without an active job; 409 job_busy while a check is active or every library is busy", "BackupReminderRequest", "Job", false, true},
		{"/jobs/{id}/backup-reminder", "get", "Read the result of a backup check; 404 for other job kinds", "", "BackupReminderJobState", true, false},
		{"/maintenance/backup-status", "get", "Compare the last metadata export with the configured threshold now, without queueing a job", "", "BackupStatus", false, false},
		{"/libraries/{id}/metadata/external/remove", "post", "Queue the removal of the external (TMDB) metadata of every item of a library with the rules of DELETE /items/{id}/metadata/external: locked and required fields are kept, manual and NFO values are untouched, pending automatic match reviews are deleted; needs confirm=true (400 confirmation_required), no provider key; 409 job_busy while the library has an active job", "ExternalMetadataRemovalRequest", "Job", true, true},
		{"/jobs/{id}/external-metadata-removal", "get", "Read the cursor and counters of an external metadata removal; 404 for other job kinds", "", "ExternalMetadataRemovalJobState", true, false},
	} {
		op := adminOperation(route.summary, "200", "400", "401", "403", "404", "408", "409", "413", "415", "429", "503")
		op["security"] = []any{map[string]any{"bearer": []string{}}}
		params := []any{}
		if route.id {
			params = append(params, idParameter())
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
