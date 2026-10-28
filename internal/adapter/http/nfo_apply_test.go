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

type httpNFOApplyJobs struct {
	submit func(context.Context, domain.Actor, string, string, string, domain.JobPolicy) (domain.Job, bool, error)
	state  func(context.Context, domain.Actor, string) (domain.NFOApplyJobState, error)
}

func (r httpNFOApplyJobs) SubmitNFOApplyJob(ctx context.Context, a domain.Actor, library, key, priority string, policy domain.JobPolicy) (domain.Job, bool, error) {
	return r.submit(ctx, a, library, key, priority, policy)
}

func (r httpNFOApplyJobs) GetNFOApplyState(ctx context.Context, a domain.Actor, job string) (domain.NFOApplyJobState, error) {
	return r.state(ctx, a, job)
}

func newNFOApplyHTTP(t *testing.T, repo app.NFOApplyJobRepository) http.Handler {
	t.Helper()
	jobs, err := app.NewJobs(httpJobRepo{}, config.DefaultJobsConfig().Policy())
	if err != nil {
		t.Fatal(err)
	}
	if repo != nil {
		if jobs, err = app.NewJobsWithNFOApply(jobs, repo); err != nil {
			t.Fatal(err)
		}
	}
	return newJobsHTTPService(t, jobs, true)
}

func TestNFOApplySubmitRoute(t *testing.T) {
	calls := 0
	var priorities []string
	h := newNFOApplyHTTP(t, httpNFOApplyJobs{submit: func(ctx context.Context, a domain.Actor, library, key, priority string, policy domain.JobPolicy) (domain.Job, bool, error) {
		nfoHTTPBoundedActor(t, ctx, a)
		calls++
		priorities = append(priorities, priority)
		if library != libraryID || key != "one" || policy.MaxEntries != 100000 {
			t.Fatal("wrong submission")
		}
		return domain.Job{ID: itemID, LibraryID: library, Kind: domain.JobNFOApply, State: domain.JobQueued, Priority: priority}, calls == 2, nil
	}})
	path := "/api/v1/libraries/" + libraryID + "/nfo/apply"
	for _, want := range []int{202, 200} {
		w := jobRequest(h, "POST", path, "{}", "a", "one")
		if w.Code != want || w.Header().Get("Location") != "/api/v1/jobs/"+itemID || (w.Header().Get("Idempotency-Replayed") == "true") != (want == 200) || string(nfoHTTPData(t, w.Body.Bytes())["kind"]) != `"nfo_apply"` {
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
	if w := jobRequest(h, "POST", path+"?identity=secret", "{}", "a", "one"); w.Code != 400 {
		t.Fatalf("unknown query: %d", w.Code)
	}
	if w := jobRequest(h, "POST", "/api/v1/libraries/not-a-uuid/nfo/apply", "{}", "a", "one"); w.Code != 400 {
		t.Fatalf("library id: %d", w.Code)
	}
	if calls != 3 {
		t.Fatalf("rejected input reached the repository: %d", calls)
	}
}

func TestNFOApplySubmitErrorsAreStable(t *testing.T) {
	path := "/api/v1/libraries/" + libraryID + "/nfo/apply"
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{domain.ErrConflict, 409, "conflict"}, {domain.ErrJobBusy, 409, "job_busy"}, {domain.ErrJobQueueFull, 429, "job_queue_full"},
		{domain.ErrNotFound, 404, "not_found"}, {errors.New("/private/root failed"), 500, ""},
	} {
		h := newNFOApplyHTTP(t, httpNFOApplyJobs{submit: func(context.Context, domain.Actor, string, string, string, domain.JobPolicy) (domain.Job, bool, error) {
			return domain.Job{}, false, tc.err
		}})
		w := jobRequest(h, "POST", path, "{}", "a", "one")
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.code) || strings.Contains(w.Body.String(), "private") {
			t.Fatalf("%v: %d %s", tc.err, w.Code, w.Body)
		}
	}
	// A runner without the applier cannot admit the job.
	if w := jobRequest(newNFOApplyHTTP(t, nil), "POST", path, "{}", "a", "one"); w.Code != 503 || !strings.Contains(w.Body.String(), "nfo_reader_unavailable") {
		t.Fatalf("no repository: %d %s", w.Code, w.Body)
	}
}

func TestNFOApplyStateRoute(t *testing.T) {
	cursor := itemID
	updated := time.Date(2026, 10, 1, 1, 2, 3, 0, time.UTC)
	h := newNFOApplyHTTP(t, httpNFOApplyJobs{state: func(ctx context.Context, a domain.Actor, job string) (domain.NFOApplyJobState, error) {
		nfoHTTPBoundedActor(t, ctx, a)
		switch job {
		case sourceID:
			return domain.NFOApplyJobState{JobID: job, LibraryID: libraryID, CursorItemID: &cursor, Applied: 5, Unchanged: 4, Skipped: 3, Failed: 2, UpdatedAt: updated}, nil
		case itemID:
			return domain.NFOApplyJobState{JobID: job, LibraryID: libraryID, UpdatedAt: updated}, nil
		}
		return domain.NFOApplyJobState{}, domain.ErrNotFound
	}})
	w := jobRequest(h, "GET", "/api/v1/jobs/"+sourceID+"/nfo-apply", "", "a")
	var got map[string]any
	data := nfoHTTPData(t, w.Body.Bytes())
	raw, _ := json.Marshal(data)
	_ = json.Unmarshal(raw, &got)
	if w.Code != 200 || len(got) != 8 || got["jobId"] != sourceID || got["libraryId"] != libraryID || got["cursorItemId"] != itemID || got["applied"] != float64(5) || got["unchanged"] != float64(4) || got["skipped"] != float64(3) || got["failed"] != float64(2) || got["updatedAt"] != "2026-10-01T01:02:03Z" {
		t.Fatalf("state: %d %s", w.Code, w.Body)
	}
	if w = jobRequest(h, "GET", "/api/v1/jobs/"+itemID+"/nfo-apply", "", "a"); w.Code != 200 || string(nfoHTTPData(t, w.Body.Bytes())["cursorItemId"]) != "null" {
		t.Fatalf("fresh job: %d %s", w.Code, w.Body)
	}
	if w = jobRequest(h, "GET", "/api/v1/jobs/"+userID+"/nfo-apply", "", "a"); w.Code != 404 {
		t.Fatalf("other job kind: %d", w.Code)
	}
	for token, status := range map[string]int{"": 401, "u": 403} {
		if w = jobRequest(h, "GET", "/api/v1/jobs/"+sourceID+"/nfo-apply", "", token); w.Code != status {
			t.Fatalf("auth %q: %d", token, w.Code)
		}
	}
	for _, path := range []string{"/api/v1/jobs/not-uuid/nfo-apply", "/api/v1/jobs/" + sourceID + "/nfo-apply?x=1"} {
		if w = jobRequest(h, "GET", path, "", "a"); w.Code != 400 {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
	if w = jobRequest(newNFOApplyHTTP(t, nil), "GET", "/api/v1/jobs/"+sourceID+"/nfo-apply", "", "a"); w.Code != 503 {
		t.Fatalf("no repository: %d", w.Code)
	}
}
