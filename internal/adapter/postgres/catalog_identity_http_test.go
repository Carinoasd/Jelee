package postgres

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
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

// G21.5 over real HTTP: correction, lock, unlock, pending decisions and the
// permission boundary (viewer 403 before any lookup, unknown or foreign IDs
// answer like missing ones).
func TestItemIdentityAndPendingHTTP(t *testing.T) {
	f := newJobFixtureWithin(t, 180*time.Second)
	root := syncRootPath(t, f)
	writeSyncFiles(t, root, syncLibraryFiles...)
	enableAutoSync(t, f)
	stop := startSyncWorker(t, f, 2)
	defer stop()
	f.submit(t, "scan-1")
	waitJobsIdle(t, f)
	items := syncedItems(t, f)
	episode, season2 := items["Episode:Breaking Bad S01E02"].id, items["Season:Season 2"].id
	pending := pendingByPath(t, f)
	other, err := f.s.RegisterLibrary(f.ctx, "second", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	createAccount(t, f.ctx, f.s, f.a, "viewer")
	admin := accountLogin(t, f.ctx, f.s, "job-admin")
	viewer := accountLogin(t, f.ctx, f.s, "viewer")

	service, err := app.NewJobs(f.s, f.policy)
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
	cfg := config.Config{Resources: config.DefaultResourcesConfig(), Listen: "127.0.0.1:8097", AllowedHosts: []string{"localhost"}, DatabaseURL: "postgres://localhost/jelee_test", MaxConnections: 16, MaxStreams: 2, RequestTimeoutSeconds: 5, EnableAccounts: true, Accounts: config.DefaultAccountsConfig(), EnableJobs: true, Jobs: config.DefaultJobsConfig(), EnableCatalog: true}
	handler, err := httpapi.NewWithJobs(cfg, f.s, app.NewCatalog(f.s), f.s, slog.New(slog.NewTextHandler(io.Discard, nil)), accounts, service)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	request := func(method, path, body, token string) (int, string) {
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
		defer func() { _ = response.Body.Close() }()
		value, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		return response.StatusCode, string(value)
	}
	identity := "/api/v1/items/" + episode + "/identity"
	missingItem := "/api/v1/items/00000000-0000-4000-8000-000000000000/identity"
	pendingBase := "/api/v1/libraries/" + f.registration.Library.ID + "/catalog-sync/pending/"
	clip, n12 := pending["random clip.mkv"].ID, pending["Naruto/Naruto - 012.mkv"].ID

	// Viewers are refused before any lookup: existing and missing IDs alike.
	for _, c := range []struct{ method, path, body string }{
		{"GET", identity, ""}, {"GET", missingItem, ""},
		{"PUT", identity, `{"expectedRevision":1,"fields":[{"field":"season","value":3}]}`},
		{"POST", pendingBase + clip + "/accept", `{}`}, {"POST", pendingBase + clip + "/reject", `{}`},
		{"POST", pendingBase + "00000000-0000-4000-8000-000000000000/reject", `{}`},
	} {
		if status, _ := request(c.method, c.path, c.body, viewer.Token); status != 403 {
			t.Fatal("viewer", c.method, c.path, status)
		}
	}
	if status, _ := request("GET", identity, "", ""); status != 401 {
		t.Fatal("anonymous identity read", status)
	}
	// Unknown item, and a pending entry addressed through another library,
	// answer exactly like a missing one.
	if status, _ := request("GET", missingItem, "", admin.Token); status != 404 {
		t.Fatal("missing item", status)
	}
	foreign := "/api/v1/libraries/" + other.Library.ID + "/catalog-sync/pending/" + clip + "/reject"
	_, missingBody := request("POST", pendingBase+"00000000-0000-4000-8000-000000000000/reject", `{}`, admin.Token)
	status, foreignBody := request("POST", foreign, `{}`, admin.Token)
	if status != 404 || stripRequestID(foreignBody) != stripRequestID(missingBody) {
		t.Fatal("foreign pending entry differs from a missing one", status, foreignBody, missingBody)
	}

	status, body := request("GET", identity, "", admin.Token)
	if status != 200 || !strings.Contains(body, `"seasonSource":"scan"`) || !strings.Contains(body, `"revision":1`) {
		t.Fatal("read", status, body)
	}
	for _, bad := range []string{
		`{"expectedRevision":1,"fields":[{"field":"season","value":3,"locked":false}]}`,
		`{"expectedRevision":1,"fields":[{"field":"season","value":null}]}`,
		`{"expectedRevision":1,"fields":[{"field":"season","value":2.5}]}`,
		`{"expectedRevision":1,"fields":[{"field":"season","valueEnd":3,"value":1}]}`,
		`{"expectedRevision":1,"fields":[{"field":"kind","value":"Series"}]}`,
		`{"expectedRevision":1,"fields":[{"field":"title","value":"x"}]}`,
		`{"expectedRevision":1,"fields":[]}`,
	} {
		if status, _ := request("PUT", identity, bad, admin.Token); status != 400 {
			t.Fatal("invalid correction accepted", bad, status)
		}
	}
	status, body = request("PUT", identity, `{"expectedRevision":1,"fields":[{"field":"season","value":2},{"field":"episode","value":3,"valueEnd":4},{"field":"parent","value":"`+season2+`"}]}`, admin.Token)
	var got struct {
		Data domain.ItemIdentity `json:"data"`
	}
	if status != 200 || json.Unmarshal([]byte(body), &got) != nil || got.Data.Revision != 2 || intValue(got.Data.Season) != 2 || intValue(got.Data.EpisodeEnd) != 4 || !got.Data.ParentLocked {
		t.Fatal("correction", status, body)
	}
	if status, _ := request("PUT", identity, `{"expectedRevision":1,"fields":[{"field":"season","locked":false}]}`, admin.Token); status != 409 {
		t.Fatal("stale revision", status)
	}
	if status, body = request("PUT", identity, `{"expectedRevision":2,"fields":[{"field":"parent","value":null}]}`, admin.Token); status != 200 || strings.Contains(body, `"parentId"`) {
		t.Fatal("parent removal", status, body)
	}
	if status, body = request("PUT", identity, `{"expectedRevision":3,"fields":[{"field":"season","locked":false},{"field":"episode","locked":false}]}`, admin.Token); status != 200 || !strings.Contains(body, `"episodeSource":"scan"`) || !strings.Contains(body, `"episode":2`) {
		t.Fatal("unlock", status, body)
	}

	// Pending decisions.
	if status, body = request("POST", pendingBase+n12+"/reject", `{}`, admin.Token); status != 200 || !strings.Contains(body, `"decision":"rejected"`) {
		t.Fatal("reject", status, body)
	}
	if status, _ = request("POST", pendingBase+n12+"/reject", `{}`, admin.Token); status != 409 {
		t.Fatal("repeated rejection", status)
	}
	if status, body = request("GET", pendingBase[:len(pendingBase)-1]+"?limit=10", "", admin.Token); status != 200 || !strings.Contains(body, `"decision":"rejected"`) || !strings.Contains(body, `"decision":"pending"`) {
		t.Fatal("list decisions", status, body)
	}
	if status, _ = request("POST", pendingBase+clip+"/accept", `{"kind":"series"}`, admin.Token); status != 400 {
		t.Fatal("invalid decision", status)
	}
	if status, body = request("POST", pendingBase+clip+"/accept", `{"kind":"movie","title":"Holiday","year":2020}`, admin.Token); status != 200 || !strings.Contains(body, `"kind":"Movie"`) || !strings.Contains(body, `"created":true`) {
		t.Fatal("accept", status, body)
	}
	if status, _ = request("POST", pendingBase+clip+"/accept", `{}`, admin.Token); status != 404 {
		t.Fatal("accepted entry still decidable", status)
	}
	if syncCount(t, f, `SELECT count(*) FROM audit_logs WHERE event IN ('item.identity_changed','catalog_sync.pending_accepted','catalog_sync.pending_rejected')`) != 5 {
		t.Fatal("audit rows")
	}
}

// stripRequestID drops the per-request identifier from a problem body.
func stripRequestID(body string) string {
	var v map[string]any
	if json.Unmarshal([]byte(body), &v) != nil {
		return body
	}
	if e, ok := v["error"].(map[string]any); ok {
		delete(e, "traceId")
	}
	out, _ := json.Marshal(v)
	return string(out)
}
