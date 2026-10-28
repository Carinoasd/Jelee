package postgres

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	"github.com/MoYuanCN/Jelee/internal/adapter/nfo"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/password"
	"github.com/MoYuanCN/Jelee/internal/platform/resources"
)

// TestNFOWriteBackOverHTTPEndToEnd drives G39.6/G39.7 through the real router
// and store: policy, preparation, admission, the runner and the files.
func TestNFOWriteBackOverHTTPEndToEnd(t *testing.T) {
	f := newJobFixture(t)
	grant := accountLogin(t, f.ctx, f.s, "job-admin")
	lib := f.registration.Library.ID
	item := metadataItem(t, f)
	if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO media_sources(item_id,library_id,root_id,relative_path,content_type) VALUES($1::uuid,$2::uuid,$3::uuid,'film.mkv','video/x-matroska')`, item, lib, f.registration.RootID); err != nil {
		t.Fatal(err)
	}

	jobs, err := app.NewJobsWithScanStages(f.s, f.policy, f.s, app.ScanServices{NFOAdmin: f.s, NFOQueries: f.s, Images: f.s, NFOAvailable: func() bool { return false }})
	if err != nil {
		t.Fatal(err)
	}
	if jobs, err = app.NewJobsWithNFOWrite(jobs, f.s); err != nil {
		t.Fatal(err)
	}
	budget, err := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 1, Queue: 32})
	if err != nil {
		t.Fatal(err)
	}
	preparer, err := nfo.NewWritePreparer(budget)
	if err != nil {
		t.Fatal(err)
	}
	preparations, err := app.NewNFOWritePreparations(f.s, preparer)
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
	handler, err := httpapi.NewWithImages(cfg, f.s, app.NewCatalog(f.s), f.s, slog.New(slog.NewTextHandler(io.Discard, nil)), accounts, jobs, nil, nil, nil, httpapi.WithNFOWritePreparations(preparations))
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body, key string) (int, http.Header, []byte) {
		t.Helper()
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
	base := "/api/v1/libraries/" + lib + "/nfo"

	// Writing needs reading first.
	if status, _, raw := call("PUT", base+"/write-policy", `{"enabled":true}`, ""); status != 409 || !strings.Contains(string(raw), `"conflict"`) {
		t.Fatalf("write enabled while reading is off: %d %s", status, raw)
	}
	var policy domain.NFOLibraryPolicy
	status, _, raw := call("GET", base+"/policy", "", "")
	data(raw, &policy)
	if status != 200 || policy.Mode != domain.NFOModeOff || policy.WriteEnabled {
		t.Fatalf("initial policy: %d %s", status, raw)
	}
	status, _, raw = call("PUT", base+"/policy", `{"mode":"read-only","expectedGeneration":`+itoa(policy.Generation)+`}`, "read-on")
	if status != 200 {
		t.Fatalf("enable reading: %d %s", status, raw)
	}

	scope, err := f.s.ResolveItemNFO(f.ctx, f.a, item, 1)
	if err != nil {
		t.Fatal(err)
	}
	media := filepath.Join(scope.Source.RootPath, filepath.FromSlash(scope.MediaPath))
	target := filepath.Join(scope.Source.RootPath, filepath.FromSlash(scope.Source.RelativePath))
	original := []byte("<movie>\n  <title>old</title>\n  <!--keep-->\n</movie>\n")
	if err = os.WriteFile(media, []byte("owned synthetic media fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(target, original, 0o600); err != nil {
		t.Fatal(err)
	}

	status, header, raw := call("POST", "/api/v1/items/"+item+"/nfo/write-preparations", `{"revision":1,"edits":[{"field":"title","value":"新標題"}],"backups":1}`, "prep-1")
	var prepared struct {
		ID                string   `json:"id"`
		ItemID            string   `json:"itemId"`
		LibraryID         string   `json:"libraryId"`
		Fields            []string `json:"fields"`
		OriginalBytes     int      `json:"originalBytes"`
		ReplacementBytes  int      `json:"replacementBytes"`
		OriginalSHA256    string   `json:"originalSha256"`
		ReplacementSHA256 string   `json:"replacementSha256"`
	}
	data(raw, &prepared)
	originalDigest := sha256.Sum256(original)
	if status != 201 || header.Get("Idempotency-Replayed") != "" || !domain.ValidID(prepared.ID) || prepared.ItemID != item || prepared.LibraryID != lib || len(prepared.Fields) != 1 ||
		prepared.OriginalBytes != len(original) || prepared.OriginalSHA256 != hex.EncodeToString(originalDigest[:]) || prepared.ReplacementBytes < len(original) {
		t.Fatalf("preparation: %d %s", status, raw)
	}
	for _, leak := range []string{scope.Source.RootPath, "film.mkv", "keep", "<movie>"} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("preparation response leaks %q", leak)
		}
	}
	if status, header, _ = call("POST", "/api/v1/items/"+item+"/nfo/write-preparations", `{"revision":1,"edits":[{"field":"title","value":"新標題"}],"backups":1}`, "prep-1"); status != 200 || header.Get("Idempotency-Replayed") != "true" {
		t.Fatalf("preparation replay: %d", status)
	}
	if after, _ := os.ReadFile(target); !bytes.Equal(after, original) {
		t.Fatal("preparing changed the NFO")
	}

	// Reading is on but writing is not: admission is refused, at the store and over HTTP.
	ids := []string{prepared.ID}
	if _, _, err = f.s.SubmitNFOWriteJob(f.ctx, f.a, lib, "direct-refused", domain.JobPriorityManual, ids, f.policy); !errors.Is(err, domain.ErrNFOWriteDisabled) {
		t.Fatal("store admitted a write job without the write permission", err)
	}
	submit := `{"preparations":["` + prepared.ID + `"]}`
	if status, _, raw = call("POST", base+"/writes", submit, "write-1"); status != 409 || !strings.Contains(string(raw), `"nfo_write_disabled"`) {
		t.Fatalf("submit with writes disabled: %d %s", status, raw)
	}

	status, _, raw = call("PUT", base+"/write-policy", `{"enabled":true}`, "")
	var enabled domain.NFOLibraryPolicy
	data(raw, &enabled)
	if status != 200 || !enabled.WriteEnabled || enabled.Mode != domain.NFOModeReadOnly {
		t.Fatalf("enable writing: %d %s", status, raw)
	}
	status, header, raw = call("POST", base+"/writes", submit, "write-1")
	var job domain.Job
	data(raw, &job)
	if status != 202 || header.Get("Location") != "/api/v1/jobs/"+job.ID || job.Kind != domain.JobNFOWrite || job.State != domain.JobQueued || job.LibraryID != lib {
		t.Fatalf("submit: %d %s", status, raw)
	}
	var again domain.Job
	status, header, raw = call("POST", base+"/writes", submit, "write-1")
	data(raw, &again)
	if status != 200 || header.Get("Idempotency-Replayed") != "true" || again.ID != job.ID {
		t.Fatalf("submit replay: %d %s", status, raw)
	}

	startNFOWriteRunner(t, f.s, nfoWriteTestWriter(t))
	deadline := time.Now().Add(45 * time.Second)
	for {
		_, _, raw = call("GET", "/api/v1/jobs/"+job.ID, "", "")
		data(raw, &job)
		if job.State == domain.JobSucceeded {
			break
		}
		if job.State == domain.JobFailed || job.State == domain.JobCancelled || time.Now().After(deadline) {
			t.Fatalf("job did not succeed: %s", raw)
		}
		time.Sleep(100 * time.Millisecond)
	}
	written, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	replacementDigest := sha256.Sum256(written)
	backup, err := os.ReadFile(target + ".jelee.bak")
	if err != nil {
		t.Fatal("backup missing", err)
	}
	if hex.EncodeToString(replacementDigest[:]) != prepared.ReplacementSHA256 || !bytes.Contains(written, []byte("新標題")) || !bytes.Contains(written, []byte("<!--keep-->")) || !bytes.Equal(backup, original) {
		t.Fatalf("files differ: %q backup %q", written, backup)
	}
}

func itoa(v int64) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}
