package postgres

import (
	"context"
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
	"github.com/MoYuanCN/Jelee/internal/adapter/nfo"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/password"
)

type observedNFOItemReader struct {
	reader app.NFOItemFieldsReader
	calls  int
	after  func(int)
}

func (r *observedNFOItemReader) ReadItemFields(ctx context.Context, path domain.NFOSource, kind string) (domain.NFOItemFields, error) {
	value, err := r.reader.ReadItemFields(ctx, path, kind)
	r.calls++
	if r.after != nil {
		r.after(r.calls)
	}
	return value, err
}

func TestNFOItemActualFilesHTTPAndPostgres(t *testing.T) {
	f, scope, _ := nfoItemApplyFixture(t)
	data := []byte(`<movie><title>NFO title</title><originaltitle>Original NFO</originaltitle><plot>NFO overview</plot><premiered>2024-02-29</premiered><lockedfields>Name</lockedfields></movie>`)
	file := filepath.Join(scope.Source.RootPath, scope.Source.RelativePath)
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	media := filepath.Join(scope.Source.RootPath, scope.MediaPath)
	if err := os.WriteFile(media, []byte("original media fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	grant := accountLogin(t, f.ctx, f.s, "job-admin")
	cfg, err := config.LoadWith(func(key string) (string, bool) {
		if key == "JELEE_DATABASE_URL" {
			return f.s.Pool.Config().ConnString(), true
		}
		return "", false
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg.EnableAccounts = true
	cfg.EnableCatalog = true
	cfg.EnableJobs = false
	hasher, err := password.New(password.Config{MemoryKiB: uint32(cfg.Accounts.PasswordMemoryKiB), Iterations: uint32(cfg.Accounts.PasswordIterations), Parallelism: uint8(cfg.Accounts.PasswordParallelism), MaxConcurrent: cfg.Accounts.PasswordConcurrency})
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := app.NewAccounts(f.s, hasher, app.AccountOptions{SessionTTL: time.Hour, MaxSessions: 8, LockAfter: 5, LockFor: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	baseReader, _ := nfo.NewSummaryReader(domain.NFODefaultSourceBytes)
	reader := &observedNFOItemReader{reader: baseReader}
	local, _ := app.NewLocalMetadata(f.s)
	metadata, _ := local.WithNFOItemFields(reader)
	handler, err := httpapi.NewWithJobs(cfg, f.s, app.NewCatalog(f.s), f.s, slog.New(slog.NewTextHandler(io.Discard, nil)), accounts, nil, metadata)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	request := func(method, path, body string, authenticated bool) (int, []byte) {
		t.Helper()
		r, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Content-Type", "application/json")
		if authenticated {
			r.Header.Set("Authorization", "Bearer "+grant.Token)
		}
		response, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		raw, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, raw
	}
	path := "/api/v1/items/" + scope.ItemID + "/metadata"
	if code, _ := request("POST", path+"/nfo", `{"expectedRevision":1,"confirmed":true}`, false); code != 401 {
		t.Fatal("anonymous NFO", code)
	}
	for _, body := range []string{`{"expectedRevision":1}`, `{"expectedRevision":1,"confirmed":false}`, `{"expectedRevision":1,"confirmed":null}`, `{"expectedRevision":1,"confirmed":true,"path":"forged.nfo"}`} {
		if code, _ := request("POST", path+"/nfo", body, true); code != 400 {
			t.Fatal("invalid NFO intent", code)
		}
	}
	if reader.calls != 0 {
		t.Fatal("NFO read before authorized confirmation")
	}
	code, raw := request("POST", path+"/nfo", `{"expectedRevision":1,"confirmed":true}`, true)
	var result struct {
		Data domain.MetadataApplyResult `json:"data"`
	}
	if code != 200 || json.Unmarshal(raw, &result) != nil || result.Data.Metadata.Revision != 2 || len(result.Data.Applied) != 4 || result.Data.Metadata.Fields[0].NFOOrigin == nil || !result.Data.Metadata.Fields[0].NFOOrigin.Locked || result.Data.Metadata.Fields[0].Value != "NFO title" {
		t.Fatal("actual NFO not persisted", code, string(raw))
	}
	if strings.Contains(string(raw), scope.Source.RootPath) || strings.Contains(string(raw), "film.nfo") {
		t.Fatal("NFO path exposed")
	}
	if original, err := os.ReadFile(file); err != nil || string(original) != string(data) {
		t.Fatal("NFO original modified")
	}
	if original, err := os.ReadFile(media); err != nil || string(original) != "original media fixture" {
		t.Fatal("original media modified")
	}
	if code, _ := request("PUT", path, `{"expectedRevision":2,"fields":[{"field":"title","value":"Owner title"},{"field":"overview","value":""},{"field":"date","locked":true}]}`, true); code != 200 {
		t.Fatal("manual takeover", code)
	}
	code, raw = request("POST", path+"/nfo", `{"expectedRevision":3,"confirmed":true}`, true)
	if code != 200 || json.Unmarshal(raw, &result) != nil || len(result.Data.Applied) != 1 || len(result.Data.Skipped) != 3 || result.Data.Metadata.Fields[0].Value != "Owner title" || result.Data.Metadata.Fields[0].NFOOrigin != nil {
		t.Fatal("NFO overwrote manual/locked fields", code, string(raw))
	}
	if code, _ := request("POST", path+"/nfo", `{"expectedRevision":3,"confirmed":true}`, true); code != 409 {
		t.Fatal("stale NFO accepted", code)
	}
	if err := os.WriteFile(file, []byte(`<movie><title>broken`), 0600); err != nil {
		t.Fatal(err)
	}
	if code, _ := request("POST", path+"/nfo", `{"expectedRevision":4,"confirmed":true}`, true); code != 503 {
		t.Fatal("invalid NFO applied", code)
	}
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	reader.calls = 0
	reader.after = func(call int) {
		if call == 1 {
			if err := os.WriteFile(file, []byte(strings.Replace(string(data), "Original NFO", "Changed NFO", 1)), 0600); err != nil {
				t.Error(err)
			}
		}
	}
	if code, _ := request("POST", path+"/nfo", `{"expectedRevision":4,"confirmed":true}`, true); code != 409 {
		t.Fatal("changed file applied", code)
	}
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	reader.calls = 0
	reader.after = func(call int) {
		if call == 2 {
			if _, err := f.s.Pool.Exec(f.ctx, `UPDATE libraries SET nfo_generation=nfo_generation+1 WHERE id=$1::uuid`, scope.LibraryID); err != nil {
				t.Error(err)
			}
		}
	}
	if code, _ := request("POST", path+"/nfo", `{"expectedRevision":4,"confirmed":true}`, true); code != 409 {
		t.Fatal("changed generation applied", code)
	}
	reader.after = nil
	value, err := f.s.ItemMetadata(f.ctx, f.a, scope.ItemID)
	if err != nil || value.Revision != 4 || value.Fields[0].Value != "Owner title" {
		t.Fatal("failed NFO requests changed item", err)
	}
	if catalog, err := f.s.GetItem(f.ctx, f.a.UserID, scope.ItemID); err != nil || catalog.Title != "Owner title" {
		t.Fatal("catalog title not synchronized", err)
	}
	var audits int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM audit_logs WHERE event='item.nfo_metadata_applied' AND target_id=$1::uuid`, scope.ItemID).Scan(&audits); err != nil || audits != 2 {
		t.Fatal("failed writes produced audit", audits, err)
	}
}
