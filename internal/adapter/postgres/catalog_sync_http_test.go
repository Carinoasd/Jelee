package postgres

import (
	"encoding/json"
	"fmt"
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

func TestCatalogSyncHTTPAcceptMissingSettingsAndPending(t *testing.T) {
	f := newJobFixture(t)
	grant := accountLogin(t, f.ctx, f.s, "job-admin")
	names := make([]string, 20)
	for i := range names {
		names[i] = fmt.Sprintf("Show %02d.mkv", i)
	}
	f.complete(t, "baseline", names, 0)
	reviewed := f.complete(t, "shrunk", names[:5], 0)
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
	request := func(method, path, body, token, key string) (int, []byte) {
		t.Helper()
		r, err := http.NewRequestWithContext(f.ctx, method, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Host = "localhost"
		r.Header.Set("Content-Type", "application/json")
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		value, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		return response.StatusCode, value
	}
	if status, body := request("GET", "/api/v1/openapi.json", "", "", ""); status != 200 || !strings.Contains(string(body), "/api/v1/jobs/{id}/accept-missing") || !strings.Contains(string(body), "CatalogPendingPage") {
		t.Fatal("catalog sync contract absent", status)
	}
	accept := "/api/v1/jobs/" + reviewed.ID + "/accept-missing"
	if status, _ := request("POST", accept, `{"expectedMissing":15}`, "", ""); status != 401 {
		t.Fatal("anonymous acceptance", status)
	}
	if status, _ := request("POST", accept, `{}`, grant.Token, ""); status != 400 {
		t.Fatal("acceptance without count", status)
	}
	if status, _ := request("POST", accept, `{"expectedMissing":3}`, grant.Token, ""); status != 409 {
		t.Fatal("wrong count accepted", status)
	}
	status, body := request("POST", accept, `{"expectedMissing":15}`, grant.Token, "")
	var job struct {
		Data domain.Job `json:"data"`
	}
	if status != 202 || json.Unmarshal(body, &job) != nil || job.Data.Kind != domain.JobCatalogSync {
		t.Fatal("acceptance", status, string(body))
	}
	if status, _ := request("POST", accept, `{"expectedMissing":15}`, grant.Token, ""); status != 409 {
		t.Fatal("replayed acceptance", status)
	}
	if status, body := request("GET", "/api/v1/jobs/"+job.Data.ID+"/catalog-sync", "", grant.Token, ""); status != 200 || !strings.Contains(string(body), `"mode":"accept"`) || !strings.Contains(string(body), `"phase":"publish"`) {
		t.Fatal("report", status, string(body))
	}
	settings := "/api/v1/libraries/" + f.registration.Library.ID + "/catalog-sync"
	if status, body := request("GET", settings, "", grant.Token, ""); status != 200 || !strings.Contains(string(body), `"auto":false`) {
		t.Fatal("settings", status, string(body))
	}
	if status, body := request("PUT", settings, `{"auto":true}`, grant.Token, ""); status != 200 || !strings.Contains(string(body), `"auto":true`) {
		t.Fatal("settings update", status, string(body))
	}
	if status, _ := request("POST", settings, `{}`, grant.Token, "sync-key"); status != 409 {
		t.Fatal("busy library accepted another job", status)
	}
	if status, body := request("GET", settings+"/pending?limit=10", "", grant.Token, ""); status != 200 || !strings.Contains(string(body), `"entries":[]`) {
		t.Fatal("pending", status, string(body))
	}
}
