package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const jobsTestID = "11111111-1111-4111-8111-111111111111"

func TestJobsCLIUsesAuthenticatedAPIAndOnlyPublicFields(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/api/v1/libraries/"+jobsTestID+"/scan" || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("a", 43) || r.Header.Get("Idempotency-Key") != "scan-1" {
			t.Error("wrong request")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(202)
		job := validJobsCLIJob()
		job["owner"], job["rootPath"], job["ID"] = "private-owner", "/secret/path", "case-alias-secret"
		_ = json.NewEncoder(w).Encode(map[string]any{"data": job, "token": "secret-token"})
	}))
	defer server.Close()
	var out, errs bytes.Buffer
	status := runJobsCLI(context.Background(), []string{"scan", "--id", jobsTestID, "--key", "scan-1", "--url", server.URL, "--token-stdin"}, strings.NewReader(strings.Repeat("a", 43)+"\n"), &out, &errs)
	if status != 0 || calls != 1 || !strings.Contains(out.String(), jobsTestID) || strings.Contains(out.String(), "secret") || strings.Contains(out.String(), "private-owner") || errs.Len() != 0 {
		t.Fatalf("status=%d out=%s errors=%s", status, &out, &errs)
	}
}

func validJobsCLIJob() map[string]any {
	return map[string]any{
		"id": jobsTestID, "libraryId": jobsTestID, "kind": "inventory_scan", "state": "queued", "priority": "manual",
		"attempts": 0, "cancelRequested": false, "files": 0, "directories": 0, "skipped": 0, "bytes": 0,
		"missing": 0, "reviewRequired": false, "createdAt": "2026-10-01T01:02:03Z",
	}
}

func jobsCLIReply(t *testing.T, command string, payload []byte, status int) (int, string, string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	args := []string{command, "--url", server.URL, "--token-stdin"}
	if command == "get" || command == "scan" || command == "cancel" || command == "retry" || command == "entries" || command == "probe" || command == "probe-rebuild-library" || command == "probe-rebuild-item" {
		args = append(args, "--id", jobsTestID)
	}
	if command == "scan" || command == "retry" || command == "probe-rebuild-library" || command == "probe-rebuild-item" {
		args = append(args, "--key", "response-test")
	}
	if command == "probe-rebuild-library" {
		args = append(args, "--i-understand")
	}
	if command == "list" || command == "entries" || command == "libraries" {
		args = append(args, "--limit", "2")
	}
	var out, errs bytes.Buffer
	exit := runJobsCLI(context.Background(), args, strings.NewReader(strings.Repeat("a", 43)), &out, &errs)
	return exit, out.String(), errs.String()
}

func jobsCLIJSON(t *testing.T, data any) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"data": data})
	if err != nil {
		t.Fatal("cannot create response fixture")
	}
	return raw
}

func rejectJobsCLIReply(t *testing.T, command string, payload []byte) {
	t.Helper()
	exit, out, errs := jobsCLIReply(t, command, payload, http.StatusOK)
	if exit != 1 || out != "" || errs != "jobs_response_invalid\n" {
		t.Fatal("invalid successful response was accepted or leaked response data")
	}
}

func TestJobsCLIRejectsAbsentNullOrMalformedSuccessEnvelope(t *testing.T) {
	for _, payload := range [][]byte{
		[]byte(`null`), []byte(`{}`), []byte(`{"data":null}`), []byte(`[]`), []byte(`{"Data":{}}`),
		[]byte(`{"data":[]}`), []byte(`{"data":{}} {}`), []byte(`{"data":{},"data":{}}`),
		append([]byte(`{"data":{},"private":"`), 0xff, '"', '}'),
	} {
		for _, command := range []string{"get", "list", "entries", "libraries"} {
			rejectJobsCLIReply(t, command, payload)
		}
	}
	for _, command := range []string{"scan", "retry"} {
		exit, out, errs := jobsCLIReply(t, command, []byte(`{"data":null}`), http.StatusAccepted)
		if exit != 1 || out != "" || errs != "jobs_response_invalid\n" {
			t.Fatal("invalid 202 response accepted")
		}
	}
}

func TestJobsCLIRequiresEveryJobFieldWithCorrectType(t *testing.T) {
	for name := range validJobsCLIJob() {
		for _, remove := range []bool{true, false} {
			t.Run(name+map[bool]string{true: " missing", false: " null"}[remove], func(t *testing.T) {
				job := validJobsCLIJob()
				if remove {
					delete(job, name)
				} else {
					job[name] = nil
				}
				rejectJobsCLIReply(t, "get", jobsCLIJSON(t, job))
			})
		}
	}
	for _, tc := range []struct {
		name  string
		value any
	}{
		{"id", "not-a-uuid"}, {"libraryId", "not-a-uuid"}, {"kind", "probe"}, {"state", "complete"}, {"priority", "urgent"},
		{"attempts", -1}, {"files", -1}, {"directories", -1}, {"skipped", -1}, {"bytes", -1}, {"missing", -1},
		{"files", "1"}, {"attempts", 0.5}, {"cancelRequested", "false"}, {"reviewRequired", 0},
		{"createdAt", "not-a-time"}, {"createdAt", "0001-01-01T00:00:00Z"}, {"startedAt", "not-a-time"}, {"finishedAt", "0001-01-01T00:00:00Z"},
		{"startedAt", nil}, {"finishedAt", nil}, {"errorCode", nil}, {"errorCode", "private-server-error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			job := validJobsCLIJob()
			job[tc.name] = tc.value
			rejectJobsCLIReply(t, "get", jobsCLIJSON(t, job))
		})
	}
	job := validJobsCLIJob()
	job["state"] = "failed"
	rejectJobsCLIReply(t, "get", jobsCLIJSON(t, job))
	job["errorCode"] = "private-server-error"
	rejectJobsCLIReply(t, "get", jobsCLIJSON(t, job))
}

func TestJobsCLIValidStatesTimesAndSafeFailureCodes(t *testing.T) {
	for _, state := range []string{"queued", "running", "succeeded", "cancelled"} {
		job := validJobsCLIJob()
		job["state"] = state
		job["startedAt"], job["finishedAt"] = "2026-10-01T01:03:03+08:00", "2026-10-01T01:04:03Z"
		exit, _, errs := jobsCLIReply(t, "get", jobsCLIJSON(t, job), 200)
		if exit != 0 || errs != "" {
			t.Fatal("valid job state or RFC3339 time rejected")
		}
	}
	for _, code := range []string{"scan_unavailable", "scan_io", "scan_limit", "scan_failed", "job_timeout", "job_attempts_exhausted"} {
		job := validJobsCLIJob()
		job["state"], job["errorCode"] = "failed", code
		exit, out, errs := jobsCLIReply(t, "get", jobsCLIJSON(t, job), 200)
		if exit != 0 || errs != "" || !strings.Contains(out, code) {
			t.Fatal("safe job failure rejected")
		}
	}
}

func jobsCLIPageFixture(field string, items any) map[string]any {
	return map[string]any{field: items, "pagination": map[string]any{"nextCursor": "", "limit": 2}}
}

func TestJobsCLIPagesRequireArraysBoundedByRequestedLimitAndPagination(t *testing.T) {
	for command, field := range map[string]string{"list": "jobs", "entries": "entries", "libraries": "libraries"} {
		for _, change := range []func(map[string]any){
			func(p map[string]any) { delete(p, field) }, func(p map[string]any) { p[field] = nil },
			func(p map[string]any) { p[field] = map[string]any{} }, func(p map[string]any) { p[field] = []any{nil} },
			func(p map[string]any) { p[field] = []any{map[string]any{}, map[string]any{}, map[string]any{}} },
			func(p map[string]any) { delete(p, "pagination") }, func(p map[string]any) { p["pagination"] = nil },
			func(p map[string]any) { p["pagination"] = map[string]any{} },
			func(p map[string]any) { p["pagination"] = map[string]any{"limit": 2} },
			func(p map[string]any) { p["pagination"] = map[string]any{"nextCursor": ""} },
			func(p map[string]any) { p["pagination"] = map[string]any{"nextCursor": nil, "limit": 2} },
			func(p map[string]any) { p["pagination"] = map[string]any{"nextCursor": "", "limit": nil} },
			func(p map[string]any) { p["pagination"] = map[string]any{"nextCursor": "", "limit": 1} },
			func(p map[string]any) { p["pagination"] = map[string]any{"nextCursor": "private-cursor", "limit": 2} },
		} {
			page := jobsCLIPageFixture(field, []any{})
			change(page)
			rejectJobsCLIReply(t, command, jobsCLIJSON(t, page))
		}
		exit, out, errs := jobsCLIReply(t, command, jobsCLIJSON(t, jobsCLIPageFixture(field, []any{})), 200)
		if exit != 0 || errs != "" || !strings.Contains(out, `"`+field+`":[]`) {
			t.Fatal("valid empty array page rejected or encoded as null")
		}
	}
	page := jobsCLIPageFixture("jobs", []any{validJobsCLIJob(), validJobsCLIJob()})
	page["pagination"] = map[string]any{"nextCursor": jobsTestID, "limit": 2, "token": "private-token"}
	exit, out, errs := jobsCLIReply(t, "list", jobsCLIJSON(t, page), 200)
	if exit != 0 || errs != "" || strings.Contains(out, "private-token") {
		t.Fatal("valid full page rejected or private pagination field leaked")
	}
	page["jobs"] = []any{validJobsCLIJob(), validJobsCLIJob(), validJobsCLIJob()}
	rejectJobsCLIReply(t, "list", jobsCLIJSON(t, page))
}

func validJobsCLIInventory() map[string]any {
	return map[string]any{"id": jobsTestID, "rootId": jobsTestID, "path": "影片/a movie.mkv", "kind": "video", "size": 12, "modifiedUnixNano": -1}
}

func TestJobsCLIInventoryValidatesMetadataAndPortableRelativePaths(t *testing.T) {
	for name := range validJobsCLIInventory() {
		entry := validJobsCLIInventory()
		delete(entry, name)
		rejectJobsCLIReply(t, "entries", jobsCLIJSON(t, jobsCLIPageFixture("entries", []any{entry})))
		entry[name] = nil
		rejectJobsCLIReply(t, "entries", jobsCLIJSON(t, jobsCLIPageFixture("entries", []any{entry})))
	}
	for _, tc := range []struct {
		name  string
		value any
	}{
		{"id", "bad"}, {"rootId", "bad"}, {"kind", "directory"}, {"size", -1}, {"modifiedUnixNano", "0"},
		{"path", ""}, {"path", "."}, {"path", "../private"}, {"path", "/private"}, {"path", "a/../private"}, {"path", "a/./file"},
		{"path", "a//file"}, {"path", "a/"}, {"path", `a\file`}, {"path", `C:/private`}, {"path", "a\nfile"}, {"path", strings.Repeat("a", 1025)},
		{"path", strings.Repeat("界", 342)},
	} {
		entry := validJobsCLIInventory()
		entry[tc.name] = tc.value
		rejectJobsCLIReply(t, "entries", jobsCLIJSON(t, jobsCLIPageFixture("entries", []any{entry})))
	}
	for _, kind := range []string{"video", "nfo", "image", "other"} {
		entry := validJobsCLIInventory()
		entry["kind"], entry["rootPath"] = kind, "/private-root"
		exit, out, errs := jobsCLIReply(t, "entries", jobsCLIJSON(t, jobsCLIPageFixture("entries", []any{entry})), 200)
		if exit != 0 || errs != "" || !strings.Contains(out, "影片/a movie.mkv") || strings.Contains(out, "private-root") {
			t.Fatal("valid public inventory rejected or private root leaked")
		}
	}
	entry := validJobsCLIInventory()
	entry["path"] = strings.Repeat("a", 1024)
	exit, _, errs := jobsCLIReply(t, "entries", jobsCLIJSON(t, jobsCLIPageFixture("entries", []any{entry})), 200)
	if exit != 0 || errs != "" {
		t.Fatal("maximum valid relative path rejected")
	}
}

func TestJobsCLILibraryValidatesPublicSummary(t *testing.T) {
	valid := func() map[string]any { return map[string]any{"id": jobsTestID, "name": "測試影庫", "roots": 0} }
	for name := range valid() {
		item := valid()
		delete(item, name)
		rejectJobsCLIReply(t, "libraries", jobsCLIJSON(t, jobsCLIPageFixture("libraries", []any{item})))
		item[name] = nil
		rejectJobsCLIReply(t, "libraries", jobsCLIJSON(t, jobsCLIPageFixture("libraries", []any{item})))
	}
	for _, tc := range []struct {
		name  string
		value any
	}{
		{"id", "bad"}, {"name", ""}, {"name", " "}, {"name", " name"}, {"name", "name\n"}, {"name", strings.Repeat("a", 129)},
		{"name", strings.Repeat("界", 43)}, {"roots", -1}, {"roots", "1"},
	} {
		item := valid()
		item[tc.name] = tc.value
		rejectJobsCLIReply(t, "libraries", jobsCLIJSON(t, jobsCLIPageFixture("libraries", []any{item})))
	}
	item := valid()
	item["rootPath"] = "/private-root"
	exit, out, errs := jobsCLIReply(t, "libraries", jobsCLIJSON(t, jobsCLIPageFixture("libraries", []any{item})), 200)
	if exit != 0 || errs != "" || strings.Contains(out, "private-root") {
		t.Fatal("valid library rejected or private root leaked")
	}
}
func TestJobsCLIDoesNotForwardTokenOnRedirect(t *testing.T) {
	targetCalls := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls++ }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	var out, errs bytes.Buffer
	status := runJobsCLI(context.Background(), []string{"list", "--url", server.URL, "--token-stdin"}, strings.NewReader(strings.Repeat("a", 43)), &out, &errs)
	if status != 1 || targetCalls != 0 || strings.Contains(errs.String(), server.URL) {
		t.Fatal("redirect leaked bearer or URL")
	}
}
func TestJobsCLIRejectsUnsafeOriginsAndInvalidInput(t *testing.T) {
	for _, origin := range []string{"http://example.com", "http://localhost:8097", "http://127.0.0.1@evil.test", "http://127.0.0.1/secret", "https://user:password@example.com", "http://127.0.0.1?token=secret", "ftp://127.0.0.1", "http://127.0.0.1:0"} {
		if _, err := jobsBaseURL(origin); err == nil {
			t.Fatalf("unsafe origin accepted %s", origin)
		}
	}
	for _, origin := range []string{"http://127.0.0.1:8097", "http://[::1]:8097", "https://example.com"} {
		if _, err := jobsBaseURL(origin); err != nil {
			t.Fatal("safe origin rejected")
		}
	}
	for _, argv := range [][]string{{"list"}, {"scan", "--token-stdin", "--id", jobsTestID}, {"list", "--token-stdin", "--limit", "101"}, {"cancel", "--token-stdin", "--id", "../secret"}, {"list", "--token-stdin", "--root", "/secret"}} {
		var out, errs bytes.Buffer
		if status := runJobsCLI(context.Background(), argv, strings.NewReader("secret"), &out, &errs); status != 2 || strings.Contains(errs.String(), "/secret") {
			t.Fatal("unsafe argv accepted or leaked")
		}
	}
	for _, token := range []string{strings.Repeat("a", 44), strings.Repeat("a", 42), strings.Repeat("a", 43) + "\nextra", strings.Repeat("a", 42) + "!"} {
		if _, err := readJobsToken(context.Background(), strings.NewReader(token)); err == nil {
			t.Fatal("invalid token accepted")
		}
	}
}
