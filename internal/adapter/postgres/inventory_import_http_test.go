package postgres

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	httpapi "github.com/MoYuanCN/Jelee/internal/adapter/http"
	"github.com/MoYuanCN/Jelee/internal/adapter/scan"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/password"
)

func TestInventoryImportActualTLS(t *testing.T) {
	f := newJobFixture(t)
	grant := accountLogin(t, f.ctx, f.s, "job-admin")
	var root string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT path FROM library_roots WHERE id=$1`, f.registration.RootID).Scan(&root); err != nil {
		t.Fatal(err)
	}
	original := []byte("unchanged test video")
	if err := os.WriteFile(filepath.Join(root, "film.mkv"), original, 0600); err != nil {
		t.Fatal(err)
	}
	job := f.submit(t, "http-import-scan")
	lease := f.claim(t, "http-import-worker")
	directory := f.directory(t, lease)
	if err := scan.New().ScanDirectory(f.ctx, directory, func(batch domain.ScanBatch) error { return f.s.SaveScanBatch(f.ctx, lease, directory, batch) }); err != nil {
		t.Fatal(err)
	}
	if err := f.s.FinishJob(f.ctx, lease, domain.JobSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	entries, err := f.s.ListInventory(f.ctx, f.a, job.ID, "", 10)
	if err != nil || len(entries) != 1 {
		t.Fatal(err)
	}
	service, err := app.NewJobs(f.s, f.policy)
	if err != nil {
		t.Fatal(err)
	}
	service, err = app.NewJobsWithInventoryImport(service, f.s, scan.New())
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
	cfg := config.Config{Listen: "127.0.0.1:8097", AllowedHosts: []string{"localhost"}, DatabaseURL: "postgres://localhost/jelee_test", MaxConnections: 16, MaxStreams: 2, RequestTimeoutSeconds: 5, EnableAccounts: true, Accounts: config.DefaultAccountsConfig(), EnableJobs: true, Jobs: config.DefaultJobsConfig(), EnableCatalog: true}
	handler, err := httpapi.NewWithJobs(cfg, f.s, app.NewCatalog(f.s), f.s, slog.New(slog.NewTextHandler(io.Discard, nil)), accounts, service)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	route := "/api/v1/jobs/" + job.ID + "/entries/" + entries[0].ID + "/item"
	request := func(method, path, body, token string) (int, []byte) {
		t.Helper()
		r, err := http.NewRequestWithContext(f.ctx, method, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Host = "localhost"
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		value, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, value
	}
	payload := `{"title":"Scanned film","kind":"Movie"}`
	if status, body := request("GET", "/api/v1/openapi.json", "", ""); status != 200 || !strings.Contains(string(body), "/api/v1/jobs/{id}/entries/{entry}/item") || !strings.Contains(string(body), "InventoryImportResult") {
		t.Fatal("inventory import contract absent from actual OpenAPI", status)
	}
	viewerInput := accountInput("import-viewer")
	viewerInput.Admin = false
	if _, _, err := f.s.CreateUser(f.ctx, f.a, viewerInput, "import-viewer-create"); err != nil {
		t.Fatal(err)
	}
	viewer := accountLogin(t, f.ctx, f.s, "import-viewer")
	if status, _ := request("PUT", route, payload, viewer.Token); status != 403 {
		t.Fatal("non-admin imported", status)
	}
	if status, _ := request("PUT", route, payload, ""); status != 401 {
		t.Fatal("anonymous import status", status)
	}
	status, body := request("PUT", route, payload, grant.Token)
	var result struct {
		Data struct {
			ItemID   string `json:"itemId"`
			SourceID string `json:"sourceId"`
		} `json:"data"`
	}
	if status != 200 || json.Unmarshal(body, &result) != nil || !domain.ValidID(result.Data.ItemID) || !domain.ValidID(result.Data.SourceID) {
		t.Fatal("TLS inventory import", status, string(body))
	}
	first := result.Data
	status, body = request("PUT", route, payload, grant.Token)
	if status != 200 || json.Unmarshal(body, &result) != nil || result.Data != first {
		t.Fatal("PUT repeat changed item", status, string(body))
	}
	if status, _ := request("PUT", route, `{"title":"Overwrite","kind":"Movie"}`, grant.Token); status != 409 {
		t.Fatal("PUT changed existing metadata", status)
	}
	if status, _ := request("PUT", route, `{"title":"Scanned film","kind":"Movie","root":"/private"}`, grant.Token); status != 400 {
		t.Fatal("request supplied source path", status)
	}
	if status, body := request("GET", "/api/v1/items/"+first.ItemID, "", grant.Token); status != 200 || !strings.Contains(string(body), "Scanned film") {
		t.Fatal("catalog import not visible", status)
	}
	var count int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM audit_logs WHERE event='inventory.imported'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("PUT repeat added audit", count, err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE sessions SET revoked_at=clock_timestamp() WHERE id=$1`, grant.Session.ID); err != nil {
		t.Fatal(err)
	}
	if status, _ := request("PUT", route, payload, grant.Token); status != 401 {
		t.Fatal("revoked session imported", status)
	}
	got, err := os.ReadFile(filepath.Join(root, "film.mkv"))
	if err != nil || string(got) != string(original) {
		t.Fatal("HTTP import modified media")
	}
}
