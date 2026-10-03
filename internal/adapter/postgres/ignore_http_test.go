//go:build linux || windows

package postgres

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	httpapi "github.com/MoYuanCN/Jelee/internal/adapter/http"
	"github.com/MoYuanCN/Jelee/internal/adapter/scan"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/jobs"
	"github.com/MoYuanCN/Jelee/internal/platform/password"
)

func TestIgnoreHTTPPostgresNativeReport(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Setenv("TMPDIR", "/tmp")
	}
	f := newJobFixture(t)
	grant := accountLogin(t, f.ctx, f.s, "job-admin")
	available := true
	service, err := app.NewJobsWithScanStages(f.s, f.policy, f.s, app.ScanServices{NFOAdmin: f.s, NFOQueries: f.s, Images: f.s, NFOAvailable: func() bool { return false }, IgnoreAvailable: func() bool { return available }})
	if err != nil {
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
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler, err := httpapi.NewWithJobs(cfg, f.s, app.NewCatalog(f.s), f.s, logger, accounts, service)
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body, key string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+grant.Token)
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	var root string
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT path FROM library_roots WHERE id=$1::uuid`, f.registration.RootID).Scan(&root); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{".jeleeignore": "*.tmp\n", "keep.mkv": "kept", "a.tmp": "excluded", "b.tmp": "excluded"} {
		if err = os.WriteFile(filepath.Join(root, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	path := "/api/v1/libraries/" + f.registration.Library.ID + "/scan"
	body := `{"ignore":{"mode":"jeleeignore","caseMode":"sensitive"}}`
	w := request("POST", path, body, "http-ignore")
	if w.Code != 202 {
		t.Fatal("HTTP admission", w.Code, w.Body)
	}
	var job struct {
		Data domain.Job `json:"data"`
	}
	if json.Unmarshal(w.Body.Bytes(), &job) != nil || !domain.ValidID(job.Data.ID) {
		t.Fatal("invalid job response")
	}
	available = false
	if w = request("POST", path, body, "http-ignore"); w.Code != 200 {
		t.Fatal("retained HTTP replay", w.Code)
	}
	if w = request("POST", path, body, "new-unavailable"); w.Code != 503 {
		t.Fatal("unavailable HTTP admission", w.Code)
	}
	reportPath := "/api/v1/jobs/" + job.Data.ID + "/ignore?limit=1"
	if w = request("GET", reportPath, "", ""); w.Code != 409 {
		t.Fatal("mutable HTTP report exposed", w.Code)
	}
	scanner := scan.NewIgnoreScanner()
	opts := jobs.DefaultOptions()
	opts.Workers = 1
	opts.Ignore = &jobs.IgnoreOptions{Repository: f.s, Scanner: scanner, Observer: scanner}
	runner, err := jobs.New(f.s, scan.New(), opts, logger)
	if err != nil {
		t.Fatal(err)
	}
	if err = runner.Start(f.ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := runner.Stop(ctx); err != nil {
			t.Error(err)
		}
	}()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for f.get(t, job.Data.ID).State != domain.JobSucceeded {
		select {
		case <-deadline.C:
			t.Fatal("HTTP-submitted worker did not succeed")
		case <-ticker.C:
		}
	}
	var first struct {
		Data domain.IgnoreReport `json:"data"`
	}
	w = request("GET", reportPath, "", "")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &first) != nil || first.Data.ExcludedFiles != 2 || len(first.Data.Entries) != 1 || first.Data.NextCursor == "" || first.Data.Entries[0].RuleLine != 1 {
		t.Fatal("HTTP report missing scan evidence", w.Code)
	}
	var second struct {
		Data domain.IgnoreReport `json:"data"`
	}
	w = request("GET", reportPath+"&cursor="+first.Data.NextCursor, "", "")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &second) != nil || len(second.Data.Entries) != 1 || second.Data.NextCursor != "" || second.Data.Entries[0].Path == first.Data.Entries[0].Path || strings.Contains(w.Body.String(), root) {
		t.Fatal("HTTP report pagination failed", w.Code)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE sessions SET revoked_at=clock_timestamp() WHERE id=$1::uuid`, grant.Session.ID); err != nil {
		t.Fatal(err)
	}
	if w = request("GET", reportPath, "", ""); w.Code != 401 {
		t.Fatal("revoked token retained HTTP report access", w.Code)
	}
}
