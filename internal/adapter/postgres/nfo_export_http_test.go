package postgres

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	httpapi "github.com/MoYuanCN/Jelee/internal/adapter/http"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/password"
)

// TestNFOExportOverHTTPEndToEnd drives the export through the real router,
// store, runner and files: admission, replay, observation and the result.
func TestNFOExportOverHTTPEndToEnd(t *testing.T) {
	f := newJobFixture(t)
	grant := accountLogin(t, f.ctx, f.s, "job-admin")
	lib := f.registration.Library.ID
	metadata := nfoApplyTestMetadata(t, f)
	items := nfoExportLibrary(t, f, metadata, lib, 24)
	// nfoExportLibrary enabled writing; start from a library that is read-only.
	if _, err := f.s.SetNFOWritePolicy(f.ctx, f.a, lib, false); err != nil {
		t.Fatal(err)
	}
	jobs, err := app.NewJobsWithScanStages(f.s, f.policy, f.s, app.ScanServices{NFOAdmin: f.s, NFOQueries: f.s, Images: f.s, NFOAvailable: func() bool { return false }})
	if err != nil {
		t.Fatal(err)
	}
	if jobs, err = app.NewJobsWithNFOWrite(jobs, f.s); err != nil {
		t.Fatal(err)
	}
	if jobs, err = app.NewJobsWithNFOExport(jobs, f.s); err != nil {
		t.Fatal(err)
	}
	hasher, err := password.New(password.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := app.NewAccounts(f.s, hasher, app.AccountOptions{SessionTTL: time.Hour, MaxSessions: 8, LockAfter: 5, LockFor: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Resources: config.DefaultResourcesConfig(), Listen: "127.0.0.1:8097", AllowedHosts: []string{"localhost"}, DatabaseURL: "postgres://localhost/jelee_test", MaxConnections: 16, MaxStreams: 2, RequestTimeoutSeconds: 5, EnableAccounts: true, Accounts: config.DefaultAccountsConfig(), EnableJobs: true, Jobs: config.DefaultJobsConfig()}
	handler, err := httpapi.NewWithImages(cfg, f.s, app.NewCatalog(f.s), f.s, slog.New(slog.NewTextHandler(io.Discard, nil)), accounts, jobs, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body, key, token string) (int, http.Header, []byte) {
		t.Helper()
		r := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(body))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code, w.Header(), w.Body.Bytes()
	}
	data := func(raw []byte, target any) {
		t.Helper()
		var envelope struct {
			Data json.RawMessage `json:"data"`
		}
		if json.Unmarshal(raw, &envelope) != nil || json.Unmarshal(envelope.Data, target) != nil {
			t.Fatalf("response is not an envelope: %s", raw)
		}
	}
	path := "/api/v1/libraries/" + lib + "/nfo/export"
	if status, _, raw := call("POST", path, `{}`, "export-1", grant.Token); status != 409 || !strings.Contains(string(raw), `"nfo_write_disabled"`) {
		t.Fatalf("export with writes disabled: %d %s", status, raw)
	}
	if status, _, _ := call("POST", path, `{}`, "export-1", ""); status != 401 {
		t.Fatalf("export without a session: %d", status)
	}
	if _, err = f.s.SetNFOWritePolicy(f.ctx, f.a, lib, true); err != nil {
		t.Fatal(err)
	}
	status, header, raw := call("POST", path, `{}`, "export-1", grant.Token)
	var job domain.Job
	data(raw, &job)
	if status != 202 || header.Get("Location") != "/api/v1/jobs/"+job.ID || job.Kind != domain.JobNFOExport || job.State != domain.JobQueued || job.LibraryID != lib {
		t.Fatalf("submit: %d %s", status, raw)
	}
	var again domain.Job
	status, header, raw = call("POST", path, `{}`, "export-1", grant.Token)
	data(raw, &again)
	if status != 200 || header.Get("Idempotency-Replayed") != "true" || again.ID != job.ID {
		t.Fatalf("replay: %d %s", status, raw)
	}
	if status, _, raw = call("POST", path, `{"priority":"background"}`, "export-1", grant.Token); status != 409 || !strings.Contains(string(raw), `"conflict"`) {
		t.Fatalf("key reused with another priority: %d %s", status, raw)
	}
	if status, _, raw = call("POST", path, `{}`, "export-2", grant.Token); status != 409 || !strings.Contains(string(raw), `"job_busy"`) {
		t.Fatalf("second export while one is active: %d %s", status, raw)
	}
	stop := startNFOExportRunner(t, f, metadata, f.s)
	defer stop()
	deadline := time.Now().Add(120 * time.Second)
	for {
		_, _, raw = call("GET", "/api/v1/jobs/"+job.ID, "", "", grant.Token)
		data(raw, &job)
		if job.State == domain.JobSucceeded {
			break
		}
		if job.State == domain.JobFailed || job.State == domain.JobCancelled || time.Now().After(deadline) {
			t.Fatalf("job did not succeed: %s", raw)
		}
		time.Sleep(100 * time.Millisecond)
	}
	var state domain.NFOExportJobState
	status, _, raw = call("GET", "/api/v1/jobs/"+job.ID+"/nfo-export", "", "", grant.Token)
	data(raw, &state)
	want := nfoExportWant(24)
	if status != 200 || nfoExportCounts(state) != want || state.ChildJobID != nil || state.CursorItemID == nil || state.Chunks < 1 {
		t.Fatalf("state: %d %s", status, raw)
	}
	if strings.Contains(string(raw), items[0].path) || strings.Contains(string(raw), "<movie>") {
		t.Fatalf("state leaks a path or content: %s", raw)
	}
	requireNFOExportFiles(t, items, "")
	// The scan job has no export state.
	scan := f.submit(t, "http-scan")
	if status, _, _ = call("GET", "/api/v1/jobs/"+scan.ID+"/nfo-export", "", "", grant.Token); status != 404 {
		t.Fatalf("state of another job kind: %d", status)
	}
	if _, err := os.Stat(items[0].path + ".jelee.bak"); err != nil {
		t.Fatal("no backup")
	}
}
