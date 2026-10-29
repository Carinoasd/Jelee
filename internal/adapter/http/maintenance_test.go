package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
)

// httpMaintenanceRepo is a MaintenanceJobRepository recording what reached
// it.
type httpMaintenanceRepo struct {
	calls []string
}

func (r *httpMaintenanceRepo) job(kind, priority, key string) (domain.Job, bool, error) {
	return domain.Job{ID: itemID, LibraryID: libraryID, Kind: kind, State: domain.JobQueued, Priority: priority}, key == "again", nil
}

func (r *httpMaintenanceRepo) SubmitSubtitleCacheCleanupJob(_ context.Context, _ domain.Actor, key, priority string, days int, _ domain.JobPolicy) (domain.Job, bool, error) {
	r.calls = append(r.calls, "cleanup:"+priority+":"+strconv.Itoa(days))
	return r.job(domain.JobSubtitleCacheCleanup, priority, key)
}

func (r *httpMaintenanceRepo) GetSubtitleCacheCleanupState(_ context.Context, _ domain.Actor, job string) (domain.SubtitleCacheCleanupJobState, error) {
	r.calls = append(r.calls, "cleanup-state")
	if job != sourceID {
		return domain.SubtitleCacheCleanupJobState{}, domain.ErrNotFound
	}
	return domain.SubtitleCacheCleanupJobState{JobID: job, MaxIdleDays: 30, Phase: domain.SubtitleCachePhaseDone, Orphaned: 2, FreedBytes: 4096, UpdatedAt: time.Date(2026, 10, 30, 1, 2, 3, 0, time.UTC)}, nil
}

func (r *httpMaintenanceRepo) SubmitBackupReminderJob(_ context.Context, _ domain.Actor, key, priority string, days int, _ domain.JobPolicy) (domain.Job, bool, error) {
	r.calls = append(r.calls, "reminder:"+priority+":"+strconv.Itoa(days))
	return r.job(domain.JobBackupReminder, priority, key)
}

func (r *httpMaintenanceRepo) GetBackupReminderState(_ context.Context, _ domain.Actor, job string) (domain.BackupReminderJobState, error) {
	r.calls = append(r.calls, "reminder-state")
	result := domain.EvaluateBackupStatus(time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC), nil, 7)
	return domain.BackupReminderJobState{JobID: job, Trigger: domain.BackupReminderSchedule, ThresholdDays: 7, Result: &result, UpdatedAt: result.CheckedAt}, nil
}

func (r *httpMaintenanceRepo) BackupStatus(_ context.Context, _ domain.Actor, days int) (domain.BackupStatus, error) {
	r.calls = append(r.calls, "status:"+strconv.Itoa(days))
	last := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	return domain.EvaluateBackupStatus(time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC), &last, days), nil
}

func (r *httpMaintenanceRepo) SubmitExternalMetadataRemovalJob(_ context.Context, _ domain.Actor, _, key string, input domain.ExternalMetadataRemovalInput, _ domain.JobPolicy) (domain.Job, bool, error) {
	drop := "keep"
	if input.DropRejectedReviews {
		drop = "drop"
	}
	r.calls = append(r.calls, "remove:"+input.Priority+":"+drop)
	return r.job(domain.JobExternalMetadataRemoval, input.Priority, key)
}

func (r *httpMaintenanceRepo) GetExternalMetadataRemovalState(_ context.Context, _ domain.Actor, job string) (domain.ExternalMetadataRemovalJobState, error) {
	r.calls = append(r.calls, "remove-state")
	return domain.ExternalMetadataRemovalJobState{JobID: job, LibraryID: libraryID, Removed: 3, Fields: 7, UpdatedAt: time.Date(2026, 10, 30, 1, 2, 3, 0, time.UTC)}, nil
}

func newMaintenanceHTTP(t *testing.T, repo *httpMaintenanceRepo, cache bool) http.Handler {
	t.Helper()
	jobs, err := app.NewJobs(httpJobRepo{}, config.DefaultJobsConfig().Policy())
	if err != nil {
		t.Fatal(err)
	}
	if repo != nil {
		if jobs, err = app.NewJobsWithMaintenance(jobs, repo, cache, 0); err != nil {
			t.Fatal(err)
		}
	}
	return newJobsHTTPService(t, jobs, true)
}

func TestMaintenanceJobRoutes(t *testing.T) {
	repo := &httpMaintenanceRepo{}
	h := newMaintenanceHTTP(t, repo, true)
	cleanup := "/api/v1/maintenance/subtitle-cache/cleanup"
	if w := jobRequest(h, "POST", cleanup, "{}", "a", "one"); w.Code != 202 || w.Header().Get("Location") != "/api/v1/jobs/"+itemID || string(nfoHTTPData(t, w.Body.Bytes())["kind"]) != `"subtitle_cache_cleanup"` {
		t.Fatalf("cleanup submit: %d %s", w.Code, w.Body)
	}
	if w := jobRequest(h, "POST", cleanup, `{"priority":"background","maxIdleDays":5}`, "a", "again"); w.Code != 200 || w.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("cleanup replay: %d", w.Code)
	}
	for _, bad := range []string{`{"maxIdleDays":-1}`, `{"maxIdleDays":3651}`, `{"priority":"urgent"}`, `{"root":"/"}`} {
		if w := jobRequest(h, "POST", cleanup, bad, "a", "x"); w.Code != 400 {
			t.Fatalf("cleanup body %s: %d", bad, w.Code)
		}
	}
	w := jobRequest(h, "GET", "/api/v1/jobs/"+sourceID+"/subtitle-cache-cleanup", "", "a")
	if data := nfoHTTPData(t, w.Body.Bytes()); w.Code != 200 || string(data["phase"]) != `"done"` || string(data["freedBytes"]) != "4096" || string(data["cursorSource"]) != "null" {
		t.Fatalf("cleanup state: %d %s", w.Code, w.Body)
	}
	if w := jobRequest(h, "GET", "/api/v1/jobs/"+itemID+"/subtitle-cache-cleanup", "", "a"); w.Code != 404 {
		t.Fatalf("other job: %d", w.Code)
	}

	reminder := "/api/v1/maintenance/backup-reminder"
	if w := jobRequest(h, "POST", reminder, `{"thresholdDays":3}`, "a", "one"); w.Code != 202 || string(nfoHTTPData(t, w.Body.Bytes())["kind"]) != `"backup_reminder"` {
		t.Fatalf("reminder submit: %d %s", w.Code, w.Body)
	}
	if w := jobRequest(h, "POST", reminder, `{"thresholdDays":366}`, "a", "two"); w.Code != 400 {
		t.Fatalf("reminder threshold: %d", w.Code)
	}
	w = jobRequest(h, "GET", "/api/v1/jobs/"+sourceID+"/backup-reminder", "", "a")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"never"`) || !strings.Contains(w.Body.String(), `"lastExportAt":null`) {
		t.Fatalf("reminder state: %d %s", w.Code, w.Body)
	}
	w = jobRequest(h, "GET", "/api/v1/maintenance/backup-status", "", "a")
	if data := nfoHTTPData(t, w.Body.Bytes()); w.Code != 200 || string(data["status"]) != `"overdue"` || string(data["thresholdDays"]) != "7" || string(data["ageSeconds"]) != "2505600" {
		t.Fatalf("backup status: %d %s", w.Code, w.Body)
	}

	remove := "/api/v1/libraries/" + libraryID + "/metadata/external/remove"
	for body, code := range map[string]string{`{}`: "confirmation_required", `{"confirm":false,"dropRejectedReviews":true}`: "confirmation_required", `{"confirm":"yes"}`: "invalid_request", `{"confirm":true,"extra":1}`: "invalid_request"} {
		if w := jobRequest(h, "POST", remove, body, "a", "x"); w.Code != 400 || !strings.Contains(w.Body.String(), code) {
			t.Fatalf("remove body %s: %d %s", body, w.Code, w.Body)
		}
	}
	if w := jobRequest(h, "POST", remove, `{"confirm":true,"dropRejectedReviews":true}`, "a", "one"); w.Code != 202 || string(nfoHTTPData(t, w.Body.Bytes())["kind"]) != `"external_metadata_removal"` {
		t.Fatalf("remove submit: %d %s", w.Code, w.Body)
	}
	if w := jobRequest(h, "GET", "/api/v1/jobs/"+sourceID+"/external-metadata-removal", "", "a"); w.Code != 200 || string(nfoHTTPData(t, w.Body.Bytes())["fields"]) != "7" {
		t.Fatalf("remove state: %d %s", w.Code, w.Body)
	}
	for _, route := range [][2]string{{"POST", cleanup}, {"POST", reminder}, {"GET", "/api/v1/maintenance/backup-status"}, {"POST", remove}} {
		for token, status := range map[string]int{"": 401, "u": 403} {
			if w := jobRequest(h, route[0], route[1], `{"confirm":true}`, token, "one"); w.Code != status {
				t.Fatalf("auth %s %q: %d", route[1], token, w.Code)
			}
		}
	}
	want := "cleanup:manual:30,cleanup:background:5,cleanup-state,cleanup-state,reminder:manual:3,reminder-state,status:7,remove:manual:drop,remove-state"
	if got := strings.Join(repo.calls, ","); got != want {
		t.Fatalf("calls %s, want %s", got, want)
	}

	// Without a configured cache a cleanup is refused; the other jobs stay.
	offline := newMaintenanceHTTP(t, &httpMaintenanceRepo{}, false)
	if w := jobRequest(offline, "POST", cleanup, "{}", "a", "one"); w.Code != 503 || !strings.Contains(w.Body.String(), "subtitle_cache_unavailable") {
		t.Fatalf("offline cleanup: %d %s", w.Code, w.Body)
	}
	if w := jobRequest(offline, "POST", reminder, "{}", "a", "one"); w.Code != 202 {
		t.Fatalf("offline reminder: %d", w.Code)
	}
}
