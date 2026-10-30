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

	"github.com/MoYuanCN/Jelee/internal/adapter/calendar"
	httpapi "github.com/MoYuanCN/Jelee/internal/adapter/http"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/password"
)

func TestScheduleActualTLS(t *testing.T) {
	f := newJobFixture(t)
	grant := accountLogin(t, f.ctx, f.s, "job-admin")
	service, err := app.NewJobs(f.s, f.policy)
	if err != nil {
		t.Fatal(err)
	}
	service, err = app.NewJobsWithSchedules(service, f.s, calendar.Calendar{})
	if err != nil {
		t.Fatal(err)
	}
	service, err = app.NewJobsWithWatch(service, f.s)
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
	request := func(method, path, body, token string) (int, []byte) {
		t.Helper()
		r, err := http.NewRequestWithContext(f.ctx, method, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Host = "localhost"
		r.Header.Set("Content-Type", "application/json")
		if method == "POST" {
			r.Header.Set("Idempotency-Key", "tls-catalog-batch")
		}
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

	route := "/api/v1/libraries/" + f.registration.Library.ID + "/schedule"
	payload := `{"expectedRevision":0,"enabled":true,"timing":{"mode":"interval","intervalSeconds":3600,"cron":"","timezone":"Asia/Taipei"},"probe":false,"nfo":false,"ignore":{"mode":"","caseMode":""}}`
	viewerInput := accountInput("schedule-viewer")
	viewerInput.Admin = false
	if _, _, err := f.s.CreateUser(f.ctx, f.a, viewerInput, "viewer"); err != nil {
		t.Fatal(err)
	}
	viewer := accountLogin(t, f.ctx, f.s, "schedule-viewer")
	for _, tc := range []struct {
		token string
		want  int
	}{{"", 401}, {viewer.Token, 403}} {
		if status, _ := request("PUT", route, payload, tc.token); status != tc.want {
			t.Fatal("schedule authority", status)
		}
	}
	if status, _ := request("GET", route, "", grant.Token); status != 404 {
		t.Fatal("missing schedule", status)
	}
	status, body := request("PUT", route, payload, grant.Token)
	var saved struct{ Data domain.ScanSchedule }
	if status != 200 || json.Unmarshal(body, &saved) != nil || saved.Data.Revision != 1 || saved.Data.NextDue == nil {
		t.Fatal("save", status, string(body))
	}
	if status, _ = request("PUT", route, payload, grant.Token); status != 409 {
		t.Fatal("stale revision", status)
	}
	if status, _ = request("PUT", route, strings.TrimSuffix(payload, "}")+`,"ownerId":"injected"}`, grant.Token); status != 400 {
		t.Fatal("authority injection", status)
	}
	if status, body = request("GET", route, "", grant.Token); status != 200 || strings.Contains(string(body), "ownerId") {
		t.Fatal("schedule report", status)
	}
	dueBefore := *saved.Data.NextDue
	status, body = request("POST", route+"/run", "{}", grant.Token)
	var job struct{ Data domain.Job }
	if status != 202 || json.Unmarshal(body, &job) != nil || job.Data.Priority != domain.JobPriorityManual {
		t.Fatal("manual schedule", status, string(body))
	}
	if status, _ = request("POST", route+"/run", "{}", grant.Token); status != 200 {
		t.Fatal("manual replay", status)
	}
	current, err := f.s.GetScanSchedule(f.ctx, f.a, f.registration.Library.ID)
	if err != nil || !current.NextDue.Equal(dueBefore) {
		t.Fatal("manual advanced due", err)
	}
	if status, body = request("GET", "/api/v1/openapi.json", "", ""); status != 200 || !strings.Contains(string(body), "/api/v1/libraries/{id}/schedule/run") || !strings.Contains(string(body), "ScanScheduleInput") {
		t.Fatal("schedule OpenAPI absent", status)
	}
	watchRoute := "/api/v1/libraries/" + f.registration.Library.ID + "/watch"
	for _, tc := range []struct {
		token string
		want  int
	}{{"", 401}, {viewer.Token, 403}} {
		if status, _ := request("GET", watchRoute, "", tc.token); status != tc.want {
			t.Fatal("watch report authority", status)
		}
	}
	input := watchInput()
	input.ExpectedRevision = 1
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if status, body = request("PUT", route, string(encoded), grant.Token); status != 200 {
		t.Fatal("watch enable via TLS", status, string(body))
	}
	if status, body = request("GET", watchRoute, "", grant.Token); status != 200 || !strings.Contains(string(body), `"enabled":true`) || !strings.Contains(string(body), `"observing":false`) || strings.Contains(string(body), "lease_owner") || strings.Contains(string(body), "RootPath") {
		t.Fatal("watch report", status, string(body))
	}

	// Generic schedules (G13.1).
	generic := `{"kind":"image_refresh","libraryId":"` + f.registration.Library.ID + `","expectedRevision":0,"enabled":true,"timing":{"mode":"cron","intervalSeconds":0,"cron":"0 3 * * *","timezone":"Asia/Taipei"},"window":{"start":"02:00","end":"05:00"},"params":{}}`
	if status, _ = request("POST", "/api/v1/schedules", generic, viewer.Token); status != 403 {
		t.Fatal("viewer created a schedule", status)
	}
	destructive := strings.Replace(generic, "image_refresh", "external_metadata_removal", 1)
	if status, body = request("POST", "/api/v1/schedules", destructive, grant.Token); status != 400 || !strings.Contains(string(body), "schedule_kind_not_allowed") {
		t.Fatal("destructive schedule", status, string(body))
	}
	var created struct{ Data domain.JobSchedule }
	if status, body = request("POST", "/api/v1/schedules", generic, grant.Token); status != 201 || json.Unmarshal(body, &created) != nil || created.Data.Revision != 1 || created.Data.NextDue == nil {
		t.Fatal("create schedule", status, string(body))
	}
	if status, _ = request("POST", "/api/v1/schedules", generic, grant.Token); status != 409 {
		t.Fatal("duplicate schedule", status)
	}
	var list struct {
		Data struct{ Schedules []domain.JobSchedule }
	}
	if status, body = request("GET", "/api/v1/schedules?libraryId="+f.registration.Library.ID, "", grant.Token); status != 200 || json.Unmarshal(body, &list) != nil || len(list.Data.Schedules) != 2 {
		t.Fatal("list schedules", status, string(body))
	}
	if status, _ = request("DELETE", "/api/v1/schedules/"+created.Data.ID+"?expectedRevision=2", "", grant.Token); status != 409 {
		t.Fatal("stale delete", status)
	}
	if status, _ = request("DELETE", "/api/v1/schedules/"+created.Data.ID+"?expectedRevision=1", "", grant.Token); status != 204 {
		t.Fatal("delete", status)
	}
	if status, _ = request("GET", "/api/v1/schedules/"+created.Data.ID, "", grant.Token); status != 404 {
		t.Fatal("deleted schedule readable", status)
	}
}
