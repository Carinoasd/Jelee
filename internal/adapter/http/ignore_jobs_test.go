package httpapi

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
)

type httpIgnoreReportJobs struct {
	httpJobRepo
	report func(context.Context, domain.Actor, string, int, string) (domain.IgnoreReport, error)
}

func (r httpIgnoreReportJobs) GetIgnoreReport(ctx context.Context, a domain.Actor, id string, limit int, cursor string) (domain.IgnoreReport, error) {
	return r.report(ctx, a, id, limit, cursor)
}

func TestIgnoreHTTPReportAuthorizationAndPaging(t *testing.T) {
	calls, expectedLimit, expectedCursor := 0, 50, ""
	var repositoryError error
	repo := httpIgnoreReportJobs{report: func(ctx context.Context, a domain.Actor, id string, limit int, cursor string) (domain.IgnoreReport, error) {
		calls++
		if a.UserID != userID || a.SessionID != sessionID || id != sourceID || limit != expectedLimit || cursor != expectedCursor {
			t.Fatal("report request lost authenticated actor or page")
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("missing deadline")
		}
		return domain.IgnoreReport{JobID: id, State: domain.JobSucceeded, Enabled: true, Entries: []domain.IgnoreReportEntry{{Source: "scan", RootID: libraryID, Path: "hidden.mkv", Kind: "video", Outcome: domain.IgnoreBaselineExcluded, RuleDirectory: ".", RuleLine: 3, MatchedPath: "hidden.mkv"}}}, repositoryError
	}}
	jobs, err := app.NewJobs(repo, config.DefaultJobsConfig().Policy())
	if err != nil {
		t.Fatal(err)
	}
	h := newJobsHTTPService(t, jobs, true)
	path := "/api/v1/jobs/" + sourceID + "/ignore"
	for _, tc := range []struct {
		token  string
		status int
	}{{"", 401}, {"u", 403}} {
		w := jobRequest(h, "GET", path, "", tc.token)
		if w.Code != tc.status || calls != 0 {
			t.Fatal("unauthorized report reached repository", w.Code, calls)
		}
	}
	for _, query := range []string{"?limit=0", "?limit=101", "?limit=x", "?limit=1&limit=2", "?cursor=x&cursor=y", "?root=private"} {
		w := jobRequest(h, "GET", path+query, "", "a")
		if w.Code != 400 || calls != 0 {
			t.Fatal("invalid report query reached repository", query, w.Code, calls)
		}
	}
	for _, query := range []string{"", "?limit=1&cursor=opaque"} {
		if query != "" {
			expectedLimit, expectedCursor = 1, "opaque"
		}
		w := jobRequest(h, "GET", path+query, "", "a")
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body)
		}
		var envelope struct {
			Data domain.IgnoreReport `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil || len(envelope.Data.Entries) != 1 || envelope.Data.Entries[0].RuleLine != 3 {
			t.Fatal("report provenance lost", err, w.Body)
		}
	}
	expectedLimit, expectedCursor = 50, ""
	for _, tc := range []struct {
		err    error
		status int
	}{{domain.ErrConflict, 409}, {domain.ErrUnauthenticated, 401}, {domain.ErrForbidden, 403}, {domain.ErrIgnoreUnavailable, 503}} {
		repositoryError = tc.err
		w := jobRequest(h, "GET", path, "", "a")
		if w.Code != tc.status {
			t.Fatal("report error mapping", w.Code, w.Body)
		}
	}
	before := calls
	w := jobRequest(newJobsHTTPService(t, jobs, false), "GET", path, "", "a")
	if w.Code != 404 || calls != before {
		t.Fatal("disabled jobs exposed report", w.Code)
	}
}

type httpIgnoreJobs struct {
	httpNFOJobs
	app.IgnoreAdmissionRepository
	submitIgnore func(domain.ScanIntent, bool) (domain.Job, bool, error)
}

func TestIgnoreReportOpenAPIRolloutAndContract(t *testing.T) {
	cfg := validConfig()
	cfg.EnableJobs = true
	spec := Specification(cfg)
	path := "/api/v1/jobs/{id}/ignore"
	op := spec["paths"].(map[string]any)[path].(map[string]any)["get"].(map[string]any)
	if op["x-jelee-role"] != "administrator" || op["security"] == nil || op["responses"].(map[string]any)["409"] == nil {
		t.Fatal("missing authorization or terminal-state contract")
	}
	params := op["parameters"].([]any)
	if len(params) != 3 || params[1].(map[string]any)["schema"].(map[string]any)["maxLength"] != 4096 || params[2].(map[string]any)["schema"].(map[string]any)["maximum"] != 100 {
		t.Fatal("incorrect bounded report pagination")
	}
	schemas := spec["components"].(map[string]any)["schemas"].(map[string]any)
	if schemas["IgnoreReport"] == nil || schemas["IgnoreReportEntry"] == nil {
		t.Fatal("missing report schemas")
	}
	cfg.EnableJobs = false
	if Specification(cfg)["paths"].(map[string]any)[path] != nil {
		t.Fatal("disabled report remains advertised")
	}
}

func (f httpIgnoreJobs) SubmitScanWithIgnoreCapability(_ context.Context, _ domain.Actor, _, _, _ string, i domain.ScanIntent, _ domain.JobPolicy, _ *domain.ProbeIdentity, _ *domain.NFOIdentity, available bool) (domain.Job, bool, error) {
	return f.submitIgnore(i, available)
}

func TestIgnoreHTTPIntentAndUnavailableReplay(t *testing.T) {
	available, calls := true, 0
	reject := false
	expected := domain.IgnoreIntent{Mode: domain.IgnoreModeJeleeignore, CaseMode: domain.IgnoreCaseSensitive}
	repo := httpIgnoreJobs{submitIgnore: func(i domain.ScanIntent, capable bool) (domain.Job, bool, error) {
		calls++
		if i.Ignore != expected || capable != available {
			t.Fatal("lost ignore intent or capability")
		}
		if reject {
			return domain.Job{}, false, domain.ErrIgnoreUnavailable
		}
		return domain.Job{ID: sourceID, LibraryID: libraryID, State: domain.JobQueued}, calls > 1, nil
	}}
	identity := domain.DefaultNFOIdentity()
	service, err := app.NewJobsWithScanStages(httpJobRepo{}, config.DefaultJobsConfig().Policy(), repo, app.ScanServices{NFOAdmin: repo, NFOQueries: repo, Images: repo, NFOIdentity: &identity, NFOAvailable: func() bool { return true }, IgnoreAvailable: func() bool { return available }})
	if err != nil {
		t.Fatal(err)
	}
	h := newJobsHTTPService(t, service, true)
	for _, tc := range []struct {
		token  string
		status int
	}{{"", 401}, {"u", 403}} {
		w := jobRequest(h, "POST", "/api/v1/libraries/"+libraryID+"/scan", `{"ignore":{"mode":"jeleeignore","caseMode":"sensitive"}}`, tc.token, "one")
		if w.Code != tc.status || calls != 0 {
			t.Fatal("unauthorized ignore request reached repository", w.Code, calls)
		}
	}
	for _, want := range []int{202, 200} {
		w := jobRequest(h, "POST", "/api/v1/libraries/"+libraryID+"/scan", `{"ignore":{"mode":"jeleeignore","caseMode":"sensitive"}}`, "a", "one")
		if w.Code != want {
			t.Fatal(w.Code, w.Body)
		}
		available = false
	}
	before := calls
	for _, body := range []string{`{"ignore":null}`, `{"ignore":{}}`, `{"ignore":{"mode":"jeleeignore"}}`, `{"ignore":{"mode":"off","caseMode":"sensitive"}}`, `{"ignore":{"mode":"jeleeignore","caseMode":"sensitive","root":"private"}}`, `{"ignore":true}`} {
		w := jobRequest(h, "POST", "/api/v1/libraries/"+libraryID+"/scan", body, "a", "one")
		if w.Code != 400 {
			t.Fatal("invalid intent accepted", w.Code, w.Body)
		}
	}
	if calls != before {
		t.Fatal("invalid input reached repository")
	}
	expected = domain.IgnoreIntent{}
	for _, body := range []string{`{}`} {
		w := jobRequest(h, "POST", "/api/v1/libraries/"+libraryID+"/scan", body, "a", "off")
		if w.Code != 200 {
			t.Fatal("off request failed", w.Code)
		}
	}
	expected = domain.IgnoreIntent{Mode: domain.IgnoreModeJeleeignore, CaseMode: domain.IgnoreCaseASCIIInsensitive}
	reject = true
	w := jobRequest(h, "POST", "/api/v1/libraries/"+libraryID+"/scan", `{"ignore":{"mode":"jeleeignore","caseMode":"ascii-insensitive"}}`, "a", "new")
	if w.Code != 503 {
		t.Fatal("unavailable mode not mapped", w.Code, w.Body)
	}
}
