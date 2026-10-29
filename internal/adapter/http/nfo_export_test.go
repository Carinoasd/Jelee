package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
)

type httpNFOExportJobs struct {
	submit func(context.Context, domain.Actor, string, string, string, domain.JobPolicy) (domain.Job, bool, error)
	state  func(context.Context, domain.Actor, string) (domain.NFOExportJobState, error)
	// submitMode, when set, receives the submissions with their mode.
	submitMode func(context.Context, domain.Actor, string, string, string, string, domain.JobPolicy) (domain.Job, bool, error)
}

func (r httpNFOExportJobs) SubmitNFOExportJob(ctx context.Context, a domain.Actor, library, key, priority string, policy domain.JobPolicy) (domain.Job, bool, error) {
	return r.submit(ctx, a, library, key, priority, policy)
}

func (r httpNFOExportJobs) SubmitNFOExportModeJob(ctx context.Context, a domain.Actor, library, key, priority, mode string, policy domain.JobPolicy) (domain.Job, bool, error) {
	if r.submitMode != nil {
		return r.submitMode(ctx, a, library, key, priority, mode, policy)
	}
	return r.submit(ctx, a, library, key, priority, policy)
}

func (r httpNFOExportJobs) GetNFOExportState(ctx context.Context, a domain.Actor, job string) (domain.NFOExportJobState, error) {
	return r.state(ctx, a, job)
}

func newNFOExportHTTP(t *testing.T, repo app.NFOExportJobRepository) http.Handler {
	t.Helper()
	jobs, err := app.NewJobs(httpJobRepo{}, config.DefaultJobsConfig().Policy())
	if err != nil {
		t.Fatal(err)
	}
	if repo != nil {
		if jobs, err = app.NewJobsWithNFOExport(jobs, repo); err != nil {
			t.Fatal(err)
		}
	}
	return newJobsHTTPService(t, jobs, true)
}

func TestNFOExportSubmitRoute(t *testing.T) {
	calls := 0
	var priorities []string
	h := newNFOExportHTTP(t, httpNFOExportJobs{submit: func(ctx context.Context, a domain.Actor, library, key, priority string, policy domain.JobPolicy) (domain.Job, bool, error) {
		nfoHTTPBoundedActor(t, ctx, a)
		calls++
		priorities = append(priorities, priority)
		if library != libraryID || key != "one" || policy.MaxEntries != 100000 {
			t.Fatal("wrong submission")
		}
		return domain.Job{ID: itemID, LibraryID: library, Kind: domain.JobNFOExport, State: domain.JobQueued, Priority: priority}, calls == 2, nil
	}})
	path := "/api/v1/libraries/" + libraryID + "/nfo/export"
	for _, want := range []int{202, 200} {
		w := jobRequest(h, "POST", path, "{}", "a", "one")
		if w.Code != want || w.Header().Get("Location") != "/api/v1/jobs/"+itemID || (w.Header().Get("Idempotency-Replayed") == "true") != (want == 200) || string(nfoHTTPData(t, w.Body.Bytes())["kind"]) != `"nfo_export"` {
			t.Fatalf("submit %d: %d %s", want, w.Code, w.Body)
		}
	}
	if w := jobRequest(h, "POST", path, `{"priority":"background"}`, "a", "one"); w.Code != 202 && w.Code != 200 {
		t.Fatalf("background: %d", w.Code)
	}
	if strings.Join(priorities, ",") != "manual,manual,background" {
		t.Fatal("priorities", priorities)
	}
	for token, status := range map[string]int{"": 401, "u": 403} {
		if w := jobRequest(h, "POST", path, "{}", token, "one"); w.Code != status {
			t.Fatalf("auth %q: %d", token, w.Code)
		}
	}
	for _, keys := range [][]string{nil, {"a", "b"}, {"bad key"}} {
		if w := jobRequest(h, "POST", path, "{}", "a", keys...); w.Code != 400 {
			t.Fatalf("key %v: %d", keys, w.Code)
		}
	}
	for _, bad := range []string{`null`, `[]`, `{"priority":"urgent"}`, `{"priority":null}`, `{"root":"/private"}`, `{"nfo":true}`, `{"priority":"manual","priority":"background"}`, `{} {}`} {
		if w := jobRequest(h, "POST", path, bad, "a", "one"); w.Code != 400 || !strings.Contains(w.Body.String(), "invalid_request") {
			t.Fatalf("body %s: %d %s", bad, w.Code, w.Body)
		}
	}
	if w := jobRequest(h, "POST", path, strings.Repeat(" ", 65537), "a", "one"); w.Code != 413 {
		t.Fatalf("body not bounded: %d", w.Code)
	}
	if w := jobRequest(h, "POST", path+"?path=/etc", "{}", "a", "one"); w.Code != 400 {
		t.Fatalf("unknown query: %d", w.Code)
	}
	if w := jobRequest(h, "POST", "/api/v1/libraries/not-a-uuid/nfo/export", "{}", "a", "one"); w.Code != 400 {
		t.Fatalf("library id: %d", w.Code)
	}
	if calls != 3 {
		t.Fatalf("rejected input reached the repository: %d", calls)
	}
}

func TestNFOExportSubmitErrorsAreStable(t *testing.T) {
	path := "/api/v1/libraries/" + libraryID + "/nfo/export"
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{domain.ErrNFOWriteDisabled, 409, "nfo_write_disabled"}, {domain.ErrConflict, 409, "conflict"}, {domain.ErrJobBusy, 409, "job_busy"},
		{domain.ErrJobQueueFull, 429, "job_queue_full"}, {domain.ErrNotFound, 404, "not_found"}, {errors.New("/private/root failed"), 500, ""},
	} {
		h := newNFOExportHTTP(t, httpNFOExportJobs{submit: func(context.Context, domain.Actor, string, string, string, domain.JobPolicy) (domain.Job, bool, error) {
			return domain.Job{}, false, tc.err
		}})
		w := jobRequest(h, "POST", path, "{}", "a", "one")
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.code) || strings.Contains(w.Body.String(), "private") {
			t.Fatalf("%v: %d %s", tc.err, w.Code, w.Body)
		}
	}
	// A runner without the NFO writer cannot admit the job: no library is writable.
	if w := jobRequest(newNFOExportHTTP(t, nil), "POST", path, "{}", "a", "one"); w.Code != 409 || !strings.Contains(w.Body.String(), "nfo_write_disabled") {
		t.Fatalf("no repository: %d %s", w.Code, w.Body)
	}
}

func TestNFOExportStateRoute(t *testing.T) {
	cursor, child := itemID, userID
	updated := time.Date(2026, 10, 1, 1, 2, 3, 0, time.UTC)
	h := newNFOExportHTTP(t, httpNFOExportJobs{state: func(ctx context.Context, a domain.Actor, job string) (domain.NFOExportJobState, error) {
		nfoHTTPBoundedActor(t, ctx, a)
		switch job {
		case sourceID:
			return domain.NFOExportJobState{JobID: job, LibraryID: libraryID, Mode: "fix", CursorItemID: &cursor, ChildJobID: &child, Chunks: 6, Written: 5, Fixed: 9, Unfixable: 10, Unchanged: 4, SkippedExisting: 8, SkippedMissing: 3, SkippedInvalid: 2, SkippedOther: 1, Failed: 7, UpdatedAt: updated}, nil
		case itemID:
			return domain.NFOExportJobState{JobID: job, LibraryID: libraryID, UpdatedAt: updated}, nil
		}
		return domain.NFOExportJobState{}, domain.ErrNotFound
	}})
	w := jobRequest(h, "GET", "/api/v1/jobs/"+sourceID+"/nfo-export", "", "a")
	var got map[string]any
	raw, _ := json.Marshal(nfoHTTPData(t, w.Body.Bytes()))
	_ = json.Unmarshal(raw, &got)
	want := map[string]any{"jobId": sourceID, "libraryId": libraryID, "mode": "fix", "fixed": float64(9), "unfixable": float64(10), "skippedExisting": float64(8), "cursorItemId": itemID, "childJobId": userID, "chunks": float64(6), "written": float64(5), "unchanged": float64(4),
		"skippedMissing": float64(3), "skippedInvalid": float64(2), "skippedOther": float64(1), "failed": float64(7), "updatedAt": "2026-10-01T01:02:03Z"}
	if w.Code != 200 || len(got) != len(want) {
		t.Fatalf("state: %d %s", w.Code, w.Body)
	}
	for name, value := range want {
		if got[name] != value {
			t.Fatalf("%s is %v, want %v", name, got[name], value)
		}
	}
	if w = jobRequest(h, "GET", "/api/v1/jobs/"+itemID+"/nfo-export", "", "a"); w.Code != 200 || string(nfoHTTPData(t, w.Body.Bytes())["cursorItemId"]) != "null" || string(nfoHTTPData(t, w.Body.Bytes())["childJobId"]) != "null" {
		t.Fatalf("fresh job: %d %s", w.Code, w.Body)
	}
	if w = jobRequest(h, "GET", "/api/v1/jobs/"+userID+"/nfo-export", "", "a"); w.Code != 404 {
		t.Fatalf("other job kind: %d", w.Code)
	}
	for token, status := range map[string]int{"": 401, "u": 403} {
		if w = jobRequest(h, "GET", "/api/v1/jobs/"+sourceID+"/nfo-export", "", token); w.Code != status {
			t.Fatalf("auth %q: %d", token, w.Code)
		}
	}
	for _, path := range []string{"/api/v1/jobs/not-uuid/nfo-export", "/api/v1/jobs/" + sourceID + "/nfo-export?x=1"} {
		if w = jobRequest(h, "GET", path, "", "a"); w.Code != 400 {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
	if w = jobRequest(newNFOExportHTTP(t, nil), "GET", "/api/v1/jobs/"+sourceID+"/nfo-export", "", "a"); w.Code != 503 {
		t.Fatalf("no repository: %d", w.Code)
	}
}

func TestNFOExportSubmitRouteCarriesTheMode(t *testing.T) {
	var modes []string
	h := newNFOExportHTTP(t, httpNFOExportJobs{submitMode: func(_ context.Context, _ domain.Actor, library, _, priority, mode string, _ domain.JobPolicy) (domain.Job, bool, error) {
		modes = append(modes, mode)
		return domain.Job{ID: itemID, LibraryID: library, Kind: domain.JobNFOExport, State: domain.JobQueued, Priority: priority}, false, nil
	}})
	path := "/api/v1/libraries/" + libraryID + "/nfo/export"
	for _, body := range []string{`{}`, `{"mode":"all"}`, `{"mode":"missing"}`, `{"mode":"missing","priority":"background"}`} {
		if w := jobRequest(h, "POST", path, body, "a", "one"); w.Code != 202 {
			t.Fatalf("%s: %d %s", body, w.Code, w.Body)
		}
	}
	if strings.Join(modes, ",") != "all,all,missing,missing" {
		t.Fatal("modes", modes)
	}
	for _, bad := range []string{`{"mode":"Missing"}`, `{"mode":"none"}`, `{"mode":null}`, `{"mode":1}`, `{"mode":["missing"]}`, `{"mode":"missing","mode":"all"}`} {
		if w := jobRequest(h, "POST", path, bad, "a", "one"); w.Code != 400 || !strings.Contains(w.Body.String(), "invalid_request") {
			t.Fatalf("body %s: %d %s", bad, w.Code, w.Body)
		}
	}
	if len(modes) != 4 {
		t.Fatal("a rejected mode reached the repository", modes)
	}
}
