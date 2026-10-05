package postgres

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	httpapi "github.com/MoYuanCN/Jelee/internal/adapter/http"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/password"
)

func TestItemMetadataActualHTTPAndPostgres(t *testing.T) {
	f := newJobFixture(t)
	item := metadataItem(t, f)
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
	hasher, err := password.New(password.Config{MemoryKiB: uint32(cfg.Accounts.PasswordMemoryKiB), Iterations: uint32(cfg.Accounts.PasswordIterations), Parallelism: uint8(cfg.Accounts.PasswordParallelism), MaxConcurrent: cfg.Accounts.PasswordConcurrency})
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := app.NewAccounts(f.s, hasher, app.AccountOptions{SessionTTL: time.Hour, MaxSessions: 8, LockAfter: 5, LockFor: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := app.NewLocalMetadata(f.s)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := httpapi.NewWithJobs(cfg, f.s, app.NewCatalog(f.s), f.s, slog.New(slog.NewTextHandler(io.Discard, nil)), accounts, nil, metadata)
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body string, authenticated bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if authenticated {
			r.Header.Set("Authorization", "Bearer "+grant.Token)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	path := "/api/v1/items/" + item + "/metadata"
	if w := request("GET", path, "", false); w.Code != 401 {
		t.Fatal("anonymous metadata read", w.Code)
	}
	w := request("GET", path, "", true)
	var body struct {
		Data domain.ItemMetadata `json:"data"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Data.Revision != 1 || body.Data.Fields[0].Source != "existing" {
		t.Fatal("metadata read", w.Code, w.Body.String())
	}
	w = request("PUT", path, `{"expectedRevision":1,"fields":[{"field":"title","value":"人工標題","locked":true},{"field":"overview","value":""}]}`, true)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Data.Revision != 2 || body.Data.Fields[0].Value != "人工標題" || !body.Data.Fields[0].Locked || body.Data.Fields[0].Source != "manual" || body.Data.Fields[1].Source != "manual" {
		t.Fatal("manual metadata not wired", w.Code, w.Body.String())
	}
	w = request("GET", "/api/v1/items/"+item, "", true)
	var catalog struct {
		Data domain.Item `json:"data"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &catalog) != nil || catalog.Data.Title != "人工標題" {
		t.Fatal("actual catalog not synchronized", w.Code, w.Body.String())
	}
	w = request("PUT", path, `{"expectedRevision":1,"fields":[{"field":"title","value":"stale"}]}`, true)
	if w.Code != 409 {
		t.Fatal("HTTP stale item revision overwrote metadata", w.Code, w.Body.String())
	}
	for _, payload := range []string{`{"expectedRevision":2,"fields":[]}`, `{"expectedRevision":2,"fields":[{"field":"title","value":null}]}`, `{"expectedRevision":2,"fields":[{"field":"title","locked":null}]}`, `{"expectedRevision":2,"fields":[{"field":"title","value":""}]}`, `{"expectedRevision":2,"fields":[{"field":"date","value":"2023-02-29"}]}`, `{"expectedRevision":2,"fields":[{"field":"overview","value":"x","source":"TMDB"}]}`, `{"expectedRevision":2,"fields":[{"field":"overview","value":"x"},{"field":"overview","value":"y"}]}`, `{"expectedRevision":2,"fields":[{"field":"unknown","locked":true}]}`} {
		if w := request("PUT", path, payload, true); w.Code != 400 {
			t.Fatal("invalid metadata payload", w.Code, payload)
		}
	}
	w = request("PUT", path, `{"expectedRevision":2,"fields":[{"field":"title","locked":false}]}`, true)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Data.Revision != 3 || body.Data.Fields[0].Locked || body.Data.Fields[0].Value != "人工標題" {
		t.Fatal("explicit unlock did not preserve value", w.Code, w.Body.String())
	}
	if w := request("GET", path+"?language=ja-JP", "", true); w.Code != 400 {
		t.Fatal("unknown query accepted", w.Code)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE sessions SET revoked_at=now() WHERE id=$1::uuid`, grant.Session.ID); err != nil {
		t.Fatal(err)
	}
	if w := request("PUT", path, `{"expectedRevision":3,"fields":[{"field":"title","value":"revoked"}]}`, true); w.Code != 401 {
		t.Fatal("revoked metadata write", w.Code)
	}
	value, err := f.s.ItemMetadata(f.ctx, f.a, item)
	if err != nil || value.Revision != 3 || value.Fields[0].Value != "人工標題" {
		t.Fatal("invalid request altered metadata", err, value)
	}
}
