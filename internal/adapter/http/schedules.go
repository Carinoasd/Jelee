package httpapi

import (
	"net/http"
	"strings"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/go-chi/chi/v5"
)

// scheduleRoutes mounts the generic job schedules (G13.1): one definition
// per kind and scope queues jobs as the system actor. Every route is
// administrator-only; running a schedule now submits the job as the caller.
func (s *Server) scheduleRoutes(r chi.Router) {
	r.Get("/api/v1/schedules", s.accountEndpoint(true, true, func(_ http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		query, err := strictQuery(r, "libraryId")
		if err != nil {
			return nil, 0, err
		}
		list, err := s.jobs.ListJobSchedules(r.Context(), a, query["libraryId"])
		return struct {
			Schedules []domain.JobSchedule `json:"schedules"`
		}{list}, http.StatusOK, err
	}))
	r.Post("/api/v1/schedules", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		var input domain.JobScheduleInput
		if err := DecodeJSON(w, r, &input, accountBodyLimit); err != nil {
			return nil, 0, err
		}
		v, err := s.jobs.PutJobSchedule(r.Context(), a, "", input)
		if err == nil {
			w.Header().Set("Location", "/api/v1/schedules/"+v.ID)
		}
		return v, http.StatusCreated, err
	}))
	r.Get("/api/v1/schedules/{id}", s.accountEndpoint(true, false, func(_ http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		v, err := s.jobs.GetJobSchedule(r.Context(), a, chi.URLParam(r, "id"))
		return v, http.StatusOK, err
	}))
	r.Put("/api/v1/schedules/{id}", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		var input domain.JobScheduleInput
		if err := DecodeJSON(w, r, &input, accountBodyLimit); err != nil {
			return nil, 0, err
		}
		v, err := s.jobs.PutJobSchedule(r.Context(), a, chi.URLParam(r, "id"), input)
		return v, http.StatusOK, err
	}))
	r.Delete("/api/v1/schedules/{id}", s.accountEndpoint(true, true, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		query, err := strictQuery(r, "expectedRevision")
		if err != nil {
			return nil, 0, err
		}
		if err := emptyAccountInput(w, r); err != nil {
			return nil, 0, err
		}
		expected, ok := parseExpectedRevision(query["expectedRevision"])
		if !ok {
			return nil, 0, domain.ErrInvalid
		}
		return nil, http.StatusNoContent, s.jobs.DeleteJobSchedule(r.Context(), a, chi.URLParam(r, "id"), expected)
	}))
	r.Post("/api/v1/schedules/{id}/run", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		var input struct{}
		if err := DecodeJSON(w, r, &input, accountBodyLimit); err != nil {
			return nil, 0, err
		}
		key, err := jobKey(r)
		if err != nil {
			return nil, 0, err
		}
		job, replay, err := s.jobs.RunJobSchedule(r.Context(), a, chi.URLParam(r, "id"), key)
		return acceptedJob(w, job, replay, err)
	}))
}

func scheduleSpecification(paths, schemas map[string]any) {
	uuid := map[string]any{"type": "string", "format": "uuid"}
	instant := map[string]any{"type": "string", "format": "date-time"}
	clock := map[string]any{"type": "string", "pattern": "^$|^([01][0-9]|2[0-3]):[0-5][0-9]$"}
	schemas["ScheduleWindow"] = objectSchema(map[string]any{"start": clock, "end": clock}, "start", "end")
	schemas["ScheduleWindow"].(map[string]any)["description"] = "Occurrences start only between start (inclusive) and end (exclusive), HH:MM local time in the schedule's timezone; an end before the start crosses midnight. Both empty: no window. An occurrence due outside the window waits for its next opening; skipped DST minutes never match and repeated ones follow the wall clock."
	schemas["JobScheduleParams"] = objectSchema(map[string]any{
		"probe":         map[string]any{"type": "boolean", "description": "inventory_scan: probe new and changed files."},
		"nfo":           map[string]any{"type": "boolean", "description": "inventory_scan: read NFO files."},
		"ignore":        schemaRef("ScheduleIgnore"),
		"maxIdleDays":   map[string]any{"type": "integer", "minimum": 0, "maximum": domain.SubtitleCacheIdleDaysMax, "description": "subtitle_cache_cleanup: 0 selects 30 days."},
		"thresholdDays": map[string]any{"type": "integer", "minimum": 0, "maximum": domain.BackupReminderDaysMax, "description": "backup_reminder: 0 selects jobs.backupReminderDays (7 when unset)."}})
	schemas["JobScheduleParams"].(map[string]any)["description"] = "Options of the kind; options of other kinds must be absent or zero (400)."
	kind := map[string]any{"type": "string", "enum": domain.ScheduleKinds(), "description": "subtitle_cache_cleanup and backup_reminder are server-wide (no libraryId); the others need libraryId. Kinds that need a confirmation or a prepared request, such as external_metadata_removal, are refused with 400 schedule_kind_not_allowed."}
	schemas["JobScheduleInput"] = objectSchema(map[string]any{"kind": map[string]any{"type": "string", "description": kind["description"]}, "libraryId": uuid,
		"expectedRevision": map[string]any{"type": "integer", "format": "int64", "minimum": 0, "description": "0 when creating; the current revision when replacing (409 when stale)."},
		"enabled":          map[string]any{"type": "boolean"}, "timing": schemaRef("ScheduleTiming"), "window": schemaRef("ScheduleWindow"), "params": schemaRef("JobScheduleParams")},
		"kind", "expectedRevision", "enabled", "timing")
	schemas["JobSchedule"] = objectSchema(map[string]any{"id": uuid, "kind": kind, "libraryId": uuid, "revision": map[string]any{"type": "integer", "format": "int64", "minimum": 1},
		"enabled": map[string]any{"type": "boolean", "description": "A once schedule turns itself off after its run."}, "timing": schemaRef("ScheduleTiming"), "window": schemaRef("ScheduleWindow"), "params": schemaRef("JobScheduleParams"),
		"nextDue": instant, "retryAfter": instant, "lastJobId": uuid,
		"lastError": map[string]any{"type": "string", "enum": []string{domain.ScheduleErrorAdmission, domain.ScheduleErrorKindUnavailable, domain.ScheduleErrorOwnerUnavailable},
			"description": "admission_unavailable: the library was busy or the queue full, retried every 30 seconds; kind_unavailable: this server cannot run the kind (no provider key, no image store or extraction cache), the occurrence was skipped; owner_unavailable: kept from scan schedules disabled before schedules ran as the system."},
		"lastRunAt": instant, "createdAt": instant, "updatedAt": instant},
		"id", "kind", "revision", "enabled", "timing", "window", "params", "createdAt", "updatedAt")
	schemas["JobSchedule"].(map[string]any)["description"] = "A persisted schedule (G13.1). Due occurrences queue a background job as the system actor (audited without an actor and with request ID system:schedule:<id>), so they need no administrator session; missed occurrences are coalesced into one."
	schemas["JobScheduleList"] = objectSchema(map[string]any{"schedules": map[string]any{"type": "array", "maxItems": 1000, "items": schemaRef("JobSchedule")}}, "schedules")
	for _, route := range []struct {
		path, method, summary, body, result, status string
		id, key                                     bool
	}{
		{"/schedules", "get", "List job schedules, optionally of one library (libraryId query)", "", "JobScheduleList", "200", false, false},
		{"/schedules", "post", "Create a schedule for a schedulable kind; one per kind and library (or server); 409 when one exists; 400 schedule_kind_not_allowed for kinds that cannot run unattended", "JobScheduleInput", "JobSchedule", "201", false, false},
		{"/schedules/{id}", "get", "Read a job schedule", "", "JobSchedule", "200", true, false},
		{"/schedules/{id}", "put", "Replace a schedule under its expected revision; kind and libraryId cannot change", "JobScheduleInput", "JobSchedule", "200", true, false},
		{"/schedules/{id}", "delete", "Delete a schedule under its expected revision (expectedRevision query); a library's scan options and directory watch stay", "", "", "204", true, false},
		{"/schedules/{id}/run", "post", "Submit the schedule's job now as the calling administrator with manual priority, without advancing the schedule; consistency_check cannot be run this way (400 schedule_kind_not_allowed)", "Empty", "Job", "200", true, true},
	} {
		op := adminOperation(route.summary, route.status, "400", "401", "403", "404", "408", "409", "413", "415", "429", "503")
		op["security"] = []any{map[string]any{"bearer": []string{}}}
		params := []any{}
		if route.id {
			params = append(params, idParameter())
		}
		if route.path == "/schedules" && route.method == "get" {
			params = append(params, map[string]any{"name": "libraryId", "in": "query", "required": false, "schema": uuid})
		}
		if route.method == "delete" {
			params = append(params, map[string]any{"name": "expectedRevision", "in": "query", "required": true, "schema": map[string]any{"type": "integer", "minimum": 1}})
		}
		if route.key {
			params = append(params, map[string]any{"name": "Idempotency-Key", "in": "header", "required": true, "schema": map[string]any{"type": "string", "pattern": "^[!-~]{1,128}$"}})
		}
		op["parameters"] = params
		if route.body != "" {
			op["requestBody"] = map[string]any{"required": true, "description": "One strict JSON object, at most 64 KiB.", "content": map[string]any{"application/json": map[string]any{"schema": schemaRef(route.body)}}}
		}
		if route.result != "" {
			response := map[string]any{"description": "Successful result.", "content": map[string]any{"application/json": map[string]any{"schema": objectSchema(map[string]any{"data": schemaRef(route.result)}, "data")}}}
			op["responses"].(map[string]any)[route.status] = response
			if route.key {
				op["responses"].(map[string]any)["202"] = response
			}
		}
		path := "/api/v1" + route.path
		if paths[path] == nil {
			paths[path] = map[string]any{}
		}
		paths[path].(map[string]any)[strings.ToLower(route.method)] = op
	}
}
