package postgres

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
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
	"github.com/MoYuanCN/Jelee/internal/adapter/nfo"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/password"
)

// TestNFOCreateOverHTTPEndToEnd drives a create through the real router and
// store: preparation of a missing NFO, replay, the refusals, admission, the
// runner and the file, and then reads the created NFO back into the item.
func TestNFOCreateOverHTTPEndToEnd(t *testing.T) {
	f := newJobFixture(t)
	grant := accountLogin(t, f.ctx, f.s, "job-admin")
	lib := f.registration.Library.ID
	service, scope, _ := nfoCreateService(t, f)
	jobs, err := app.NewJobsWithScanStages(f.s, f.policy, f.s, app.ScanServices{NFOAdmin: f.s, NFOQueries: f.s, Images: f.s, NFOAvailable: func() bool { return false }})
	if err != nil {
		t.Fatal(err)
	}
	if jobs, err = app.NewJobsWithNFOWrite(jobs, f.s); err != nil {
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
	handler, err := httpapi.NewWithImages(cfg, f.s, app.NewCatalog(f.s), f.s, slog.New(slog.NewTextHandler(io.Discard, nil)), accounts, jobs, nil, nil, nil, httpapi.WithNFOWritePreparations(service))
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
	item := scope.ItemID
	prepare := "/api/v1/items/" + item + "/nfo/write-preparations"
	target := scope.Source.RootPath + "/" + scope.Source.RelativePath
	body := `{"revision":1,"edits":[{"field":"title","value":"新建標題"},{"field":"plot","value":"created plot"}],"create":true}`

	// The preparation neither creates the file nor needs the write permission.
	status, header, raw := call("POST", prepare, body, "create-1")
	var prepared struct {
		ID                string   `json:"id"`
		Fields            []string `json:"fields"`
		OriginalBytes     int      `json:"originalBytes"`
		ReplacementBytes  int      `json:"replacementBytes"`
		OriginalSHA256    string   `json:"originalSha256"`
		ReplacementSHA256 string   `json:"replacementSha256"`
	}
	data(raw, &prepared)
	empty := sha256.Sum256(nil)
	if status != 201 || header.Get("Idempotency-Replayed") != "" || !domain.ValidID(prepared.ID) || prepared.OriginalBytes != 0 || prepared.OriginalSHA256 != hex.EncodeToString(empty[:]) || prepared.ReplacementBytes < 1 || len(prepared.Fields) != 2 {
		t.Fatalf("create preparation: %d %s", status, raw)
	}
	for _, leak := range []string{scope.Source.RootPath, "film.mkv", "<movie>", "created plot"} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("preparation response leaks %q", leak)
		}
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatal("preparing created the file")
	}
	if status, header, _ = call("POST", prepare, body, "create-1"); status != 200 || header.Get("Idempotency-Replayed") != "true" {
		t.Fatalf("create preparation replay: %d", status)
	}
	// Rules of the request: a create has no backups; a rewrite of a missing NFO
	// is still the old error, not a create.
	if status, _, raw = call("POST", prepare, `{"revision":1,"edits":[{"field":"title","value":"x"}],"create":true,"backups":3}`, "create-backups"); status != 400 {
		t.Fatalf("create with backups: %d %s", status, raw)
	}
	if status, _, raw = call("POST", prepare, `{"revision":1,"edits":[{"field":"title","value":"x"}],"create":true,"backups":0}`, "create-zero-backups"); status != 201 {
		t.Fatalf("create with zero backups: %d %s", status, raw)
	}
	if status, _, raw = call("POST", prepare, `{"revision":1,"edits":[{"field":"title","value":"x"}],"create":"yes"}`, "create-type"); status != 400 {
		t.Fatalf("create of the wrong type: %d %s", status, raw)
	}
	if status, _, _ = call("POST", prepare, `{"revision":1,"edits":[{"field":"title","value":"x"}]}`, "no-create"); status == 201 {
		t.Fatal("a rewrite of a missing NFO was prepared")
	}

	// Admission is the rewrite path: the library is read-only with writes on.
	base := "/api/v1/libraries/" + lib + "/nfo"
	if status, _, raw = call("POST", base+"/writes", `{"preparations":["`+prepared.ID+`"]}`, "write-1"); status != 202 {
		t.Fatalf("submit: %d %s", status, raw)
	}
	var job domain.Job
	data(raw, &job)
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
	digest := sha256.Sum256(written)
	if hex.EncodeToString(digest[:]) != prepared.ReplacementSHA256 || !bytes.Contains(written, []byte("新建標題")) || !bytes.HasPrefix(written, []byte("<?xml")) {
		t.Fatalf("created file differs: %q", written)
	}
	if _, err := os.Lstat(target + ".jelee.bak"); !os.IsNotExist(err) {
		t.Fatal("a create made a backup")
	}
	// The name now exists: the same request is a conflict, not another create.
	if status, _, raw = call("POST", prepare, body, "create-2"); status != 409 {
		t.Fatalf("create over an existing NFO: %d %s", status, raw)
	}

	// Reading the item again applies the created NFO.
	baseReader, err := nfo.NewSummaryReader(domain.NFODefaultSourceBytes)
	if err != nil {
		t.Fatal(err)
	}
	fields, err := baseReader.ReadItemFields(f.ctx, scope.Source, scope.Kind)
	if err != nil {
		t.Fatal("the created NFO cannot be read", err)
	}
	live, err := f.s.ResolveItemNFO(f.ctx, f.a, item, 1)
	if err != nil {
		t.Fatal(err)
	}
	applied, err := f.s.ApplyItemNFO(f.ctx, f.a, live, fields)
	if err != nil || len(applied.Applied) == 0 {
		t.Fatal("the created NFO was not applied", err)
	}
	var title string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT title FROM items WHERE id=$1::uuid`, item).Scan(&title); err != nil || title != "新建標題" {
		t.Fatal("applied title differs", title, err)
	}
}
