package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
)

type httpNFOJobs struct {
	app.ScanJobRepository
	app.NFOAdminRepository
	app.NFOQueryRepository
	app.ImageQueryRepository
	submit       func(context.Context, domain.Actor, string, string, string, domain.ScanIntent, domain.JobPolicy, *domain.ProbeIdentity, *domain.NFOIdentity) (domain.Job, bool, error)
	policy       func(context.Context, domain.Actor, string) (domain.NFOLibraryPolicy, error)
	setPolicy    func(context.Context, domain.Actor, string, string, int64, string) (domain.NFOLibraryPolicy, bool, error)
	summary      func(context.Context, domain.Actor, string) (domain.NFOJobSummary, error)
	observations func(context.Context, domain.Actor, string, string, int, domain.NFOIdentity) (domain.NFOObservationPage, error)
	issues       func(context.Context, domain.Actor, string, string, int, int, domain.NFOIdentity) (domain.NFOIssuesPage, error)
	images       func(context.Context, domain.Actor, string) (domain.ImageJobSummary, error)
}

func (p httpNFOJobs) SubmitScanWithStages(ctx context.Context, a domain.Actor, id, key, priority string, intent domain.ScanIntent, policy domain.JobPolicy, probe *domain.ProbeIdentity, nfo *domain.NFOIdentity) (domain.Job, bool, error) {
	return p.submit(ctx, a, id, key, priority, intent, policy, probe, nfo)
}
func (p httpNFOJobs) GetNFOLibraryPolicy(ctx context.Context, a domain.Actor, id string) (domain.NFOLibraryPolicy, error) {
	return p.policy(ctx, a, id)
}
func (p httpNFOJobs) SetNFOLibraryPolicy(ctx context.Context, a domain.Actor, id, key string, expected int64, mode string) (domain.NFOLibraryPolicy, bool, error) {
	return p.setPolicy(ctx, a, id, key, expected, mode)
}
func (p httpNFOJobs) GetNFOJobSummary(ctx context.Context, a domain.Actor, id string) (domain.NFOJobSummary, error) {
	return p.summary(ctx, a, id)
}
func (p httpNFOJobs) ListNFOObservations(ctx context.Context, a domain.Actor, id, cursor string, limit int, identity domain.NFOIdentity) (domain.NFOObservationPage, error) {
	return p.observations(ctx, a, id, cursor, limit, identity)
}
func (p httpNFOJobs) GetNFOObservationIssues(ctx context.Context, a domain.Actor, id, observation string, offset, limit int, identity domain.NFOIdentity) (domain.NFOIssuesPage, error) {
	return p.issues(ctx, a, id, observation, offset, limit, identity)
}
func (p httpNFOJobs) GetImageJobSummary(ctx context.Context, a domain.Actor, id string) (domain.ImageJobSummary, error) {
	return p.images(ctx, a, id)
}
func newNFOHTTP(t *testing.T, repo httpNFOJobs, available func() bool, enabled bool) http.Handler {
	t.Helper()
	identity := domain.DefaultNFOIdentity()
	probeIdentity := httpProbeIdentity()
	jobs, err := app.NewJobsWithScanStages(httpJobRepo{}, config.DefaultJobsConfig().Policy(), repo, app.ScanServices{Probes: httpProbeJobs{}, ProbeIdentity: &probeIdentity, ProbeCapability: httpAvailableProbe, NFOAdmin: repo, NFOQueries: repo, Images: repo, NFOIdentity: &identity, NFOAvailable: available})
	if err != nil {
		t.Fatal(err)
	}
	return newJobsHTTPService(t, jobs, enabled)
}
func nfoHTTPBoundedActor(t *testing.T, ctx context.Context, a domain.Actor) {
	t.Helper()
	if a.UserID != userID || a.SessionID != sessionID {
		t.Fatal("wrong actor")
	}
	if _, ok := ctx.Deadline(); !ok {
		t.Fatal("missing request deadline")
	}
}
func nfoHTTPData(t *testing.T, body []byte) map[string]json.RawMessage {
	t.Helper()
	var envelope struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Data
}
func TestNFORoutesRequireAdminAndRejectUntrustedInputs(t *testing.T) {
	h := newNFOHTTP(t, httpNFOJobs{}, func() bool { return true }, true)
	policyPath := "/api/v1/libraries/" + libraryID + "/nfo/policy"
	validatePath := "/api/v1/libraries/" + libraryID + "/nfo/validate"
	currentPath := "/api/v1/libraries/" + libraryID + "/nfo/current-validations"
	for _, r := range []struct{ method, path, body string }{
		{"GET", policyPath, ""}, {"PUT", policyPath, `{"mode":"read-only","expectedGeneration":1}`}, {"POST", validatePath, "{}"},
		{"GET", "/api/v1/jobs/" + sourceID + "/nfo", ""}, {"GET", "/api/v1/jobs/" + sourceID + "/images", ""},
		{"GET", currentPath, ""}, {"GET", currentPath + "/" + sourceID + "/issues", ""},
	} {
		for token, status := range map[string]int{"": 401, "u": 403} {
			if w := jobRequest(h, r.method, r.path, r.body, token, "one"); w.Code != status {
				t.Fatalf("%s %s: %d", r.method, r.path, w.Code)
			}
		}
	}
	for _, r := range []struct{ method, path, good string }{
		{"POST", validatePath, "{}"}, {"PUT", policyPath, `{"mode":"read-only","expectedGeneration":1}`},
	} {
		for _, keys := range [][]string{nil, {"one", "two"}, {"bad key"}} {
			if w := jobRequest(h, r.method, r.path, r.good, "a", keys...); w.Code != 400 {
				t.Fatalf("key accepted: %d", w.Code)
			}
		}
		for _, body := range []string{"null", "{} {}", `{"root":"/private"}`, `{"identity":{}}`, `{"priority":null}`, `{"mode":"off","mode":"read-only"}`} {
			if w := jobRequest(h, r.method, r.path, body, "a", "one"); w.Code != 400 {
				t.Fatalf("body %s accepted: %d", body, w.Code)
			}
		}
		if w := jobRequest(h, r.method, r.path, strings.Repeat(" ", 65537), "a", "one"); w.Code != 413 {
			t.Fatal("body not bounded")
		}
		if w := jobRequest(h, r.method, r.path+"?identity=secret", r.good, "a", "one"); w.Code != 400 {
			t.Fatal("unknown query accepted")
		}
	}
	for _, body := range []string{`{}`, `{"mode":"off"}`, `{"mode":"write","expectedGeneration":1}`, `{"mode":"off","expectedGeneration":0}`, `{"mode":"off","expectedGeneration":1.5}`, `{"mode":"off","expectedGeneration":1,"Mode":"read-only"}`} {
		if w := jobRequest(h, "PUT", policyPath, body, "a", "one"); w.Code != 400 {
			t.Fatalf("policy body accepted: %s %d", body, w.Code)
		}
	}
	for _, body := range []string{`{"nfo":null}`, `{"nfo":1}`, `{"nfo":"true"}`, `{"nfo":true,"nfo":false}`, `{"nfo":true,"NFO":false}`} {
		if w := jobRequest(h, "POST", "/api/v1/libraries/"+libraryID+"/scan", body, "a", "one"); w.Code != 400 {
			t.Fatalf("scan body accepted: %s", body)
		}
	}
	for _, body := range []string{`{"probe":true}`, `{"nfo":true}`, `{"priority":"urgent"}`} {
		if w := jobRequest(h, "POST", validatePath, body, "a", "one"); w.Code != 400 {
			t.Fatal("validate input accepted")
		}
	}
	for _, path := range []string{currentPath + "?limit=0", currentPath + "?limit=51", currentPath + "?limit=1&limit=2", currentPath + "?cursor=secret", currentPath + "?identity=secret",
		currentPath + "/" + sourceID + "/issues?offset=-1", currentPath + "/" + sourceID + "/issues?offset=65", currentPath + "/" + sourceID + "/issues?limit=33", currentPath + "/" + sourceID + "/issues?offset=x", currentPath + "/" + sourceID + "/issues?limit=0", currentPath + "/" + sourceID + "/issues?cursor=1",
		"/api/v1/jobs/not-uuid/nfo", "/api/v1/jobs/not-uuid/images", policyPath + "?mode=off",
	} {
		if w := jobRequest(h, "GET", path, "", "a"); w.Code != 400 {
			t.Fatalf("query accepted: %s %d", path, w.Code)
		}
	}
}
func TestNFOScanIntentAndReplayArePreserved(t *testing.T) {
	for _, tc := range []struct {
		path, body string
		probe, nfo bool
	}{
		{"/scan", "{}", false, false}, {"/scan", `{"nfo":false}`, false, false}, {"/scan", `{"nfo":true}`, false, true},
		{"/scan", `{"probe":true,"nfo":true}`, true, true}, {"/nfo/validate", "{}", false, true},
	} {
		t.Run(tc.path+tc.body, func(t *testing.T) {
			calls := 0
			available := true
			h := newNFOHTTP(t, httpNFOJobs{submit: func(ctx context.Context, a domain.Actor, id, key, priority string, intent domain.ScanIntent, policy domain.JobPolicy, probe *domain.ProbeIdentity, nfo *domain.NFOIdentity) (domain.Job, bool, error) {
				nfoHTTPBoundedActor(t, ctx, a)
				calls++
				if id != libraryID || key != "one" || priority != domain.JobPriorityManual || intent.NFO != tc.nfo || (intent.Probe.Scope != "") != tc.probe || policy.MaxEntries != 100000 {
					t.Fatal("wrong intent")
				}
				if (probe != nil) != tc.probe || (nfo != nil) != (tc.nfo && available) {
					t.Fatal("wrong reader authority")
				}
				if nfo != nil && *nfo != domain.DefaultNFOIdentity() {
					t.Fatal("untrusted reader")
				}
				return domain.Job{ID: sourceID, LibraryID: libraryID, State: domain.JobQueued}, calls == 2, nil
			}}, func() bool { return available }, true)
			for _, want := range []int{202, 200} {
				w := jobRequest(h, "POST", "/api/v1/libraries/"+libraryID+tc.path, tc.body, "a", "one")
				if w.Code != want || w.Header().Get("Location") != "/api/v1/jobs/"+sourceID || (w.Header().Get("Idempotency-Replayed") == "true") != (want == 200) {
					t.Fatalf("replay: %d %s", w.Code, w.Body)
				}
				available = false
			}
		})
	}
}
func TestNFOPolicyCASAndHistoricalQueriesWorkWithoutReader(t *testing.T) {
	calls := 0
	repo := httpNFOJobs{
		policy: func(ctx context.Context, a domain.Actor, id string) (domain.NFOLibraryPolicy, error) {
			nfoHTTPBoundedActor(t, ctx, a)
			return domain.NFOLibraryPolicy{LibraryID: id, Mode: domain.NFOModeOff, Generation: 2}, nil
		},
		setPolicy: func(ctx context.Context, a domain.Actor, id, key string, expected int64, mode string) (domain.NFOLibraryPolicy, bool, error) {
			nfoHTTPBoundedActor(t, ctx, a)
			calls++
			if id != libraryID || key != "one" || expected != 2 || mode != domain.NFOModeReadOnly {
				t.Fatal("CAS projection")
			}
			return domain.NFOLibraryPolicy{LibraryID: id, Mode: mode, Generation: 3}, calls == 2, nil
		},
		summary: func(ctx context.Context, a domain.Actor, id string) (domain.NFOJobSummary, error) {
			nfoHTTPBoundedActor(t, ctx, a)
			return domain.NFOJobSummary{JobID: id, LibraryID: libraryID, Mode: domain.NFOModeReadOnly, Phase: domain.NFOSummaryDone, Processed: 2, Hits: 1, Parsed: 1, Valid: 2}, nil
		},
		images: func(ctx context.Context, a domain.Actor, id string) (domain.ImageJobSummary, error) {
			nfoHTTPBoundedActor(t, ctx, a)
			return domain.ImageJobSummary{JobID: id, LibraryID: libraryID, ImageProgress: domain.ImageProgress{Uncompared: 2}}, nil
		},
	}
	h := newNFOHTTP(t, repo, func() bool { return false }, true)
	base := "/api/v1/libraries/" + libraryID + "/nfo"
	if w := jobRequest(h, "GET", base+"/policy", "", "a"); w.Code != 200 || len(nfoHTTPData(t, w.Body.Bytes())) != 3 {
		t.Fatal("policy unavailable")
	}
	for _, replay := range []bool{false, true} {
		w := jobRequest(h, "PUT", base+"/policy", `{"mode":"read-only","expectedGeneration":2}`, "a", "one")
		if w.Code != 200 || (w.Header().Get("Idempotency-Replayed") == "true") != replay {
			t.Fatal("policy replay")
		}
	}
	for suffix, fields := range map[string]int{"/nfo": 14, "/images": 8} {
		w := jobRequest(h, "GET", "/api/v1/jobs/"+sourceID+suffix, "", "a")
		if w.Code != 200 || len(nfoHTTPData(t, w.Body.Bytes())) != fields {
			t.Fatalf("history projection %s: %d %s", suffix, w.Code, w.Body)
		}
	}
	for _, path := range []string{base + "/current-validations", base + "/current-validations/" + sourceID + "/issues"} {
		if w := jobRequest(h, "GET", path, "", "a"); w.Code != 503 || !strings.Contains(w.Body.String(), "nfo_reader_unavailable") {
			t.Fatal("current query bypassed unavailable reader")
		}
	}
}
func TestNFOCurrentPagesProjectBoundedPublicData(t *testing.T) {
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	h := newNFOHTTP(t, httpNFOJobs{
		observations: func(ctx context.Context, a domain.Actor, id, cursor string, limit int, identity domain.NFOIdentity) (domain.NFOObservationPage, error) {
			nfoHTTPBoundedActor(t, ctx, a)
			if id != libraryID || cursor != itemID || limit != 7 || identity != domain.DefaultNFOIdentity() {
				t.Fatal("observation query")
			}
			return domain.NFOObservationPage{Items: []domain.NFOObservation{{ID: sourceID, RootID: itemID, Path: "series/movie.nfo", Status: domain.NFOStatusInvalid, FailureCode: domain.NFOFailureInvalidXML, ObservedAt: now, ExpiresAt: now.Add(time.Minute)}}, NextCursor: sourceID}, nil
		},
		issues: func(ctx context.Context, a domain.Actor, id, observation string, offset, limit int, identity domain.NFOIdentity) (domain.NFOIssuesPage, error) {
			nfoHTTPBoundedActor(t, ctx, a)
			if id != libraryID || observation != sourceID || offset != 32 || limit != 32 || identity != domain.DefaultNFOIdentity() {
				t.Fatal("issue query")
			}
			issues := make([]domain.NFOIssue, 32)
			for i := range issues {
				issues[i] = domain.NFOIssue{Severity: "warning", Code: "nfo_title_missing", Field: "title", Entry: 0}
			}
			return domain.NFOIssuesPage{ObservationID: observation, Entries: 1, IssueCount: 80, IssuesTruncated: true, Offset: offset, Issues: issues}, nil
		},
	}, func() bool { return true }, true)
	base := "/api/v1/libraries/" + libraryID + "/nfo/current-validations"
	w := jobRequest(h, "GET", base+"?cursor="+itemID+"&limit=7", "", "a")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var page struct {
		Data domain.NFOObservationPage `json:"data"`
	}
	if json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Data.Items) != 1 || page.Data.Items[0].FailureCode != domain.NFOFailureInvalidXML || page.Data.NextCursor != sourceID {
		t.Fatal("observation lost failure/cursor")
	}
	var envelope struct {
		Data struct {
			Items []map[string]any `json:"items"`
		} `json:"data"`
	}
	if json.Unmarshal(w.Body.Bytes(), &envelope) != nil || len(envelope.Data.Items[0]) != 12 {
		t.Fatal("private observation fields")
	}
	w = jobRequest(h, "GET", base+"/"+sourceID+"/issues?offset=32&limit=32", "", "a")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	data := nfoHTTPData(t, w.Body.Bytes())
	if len(data) != 7 || data["nextOffset"] != nil || string(data["issueCount"]) != "80" || string(data["issuesTruncated"]) != "true" {
		t.Fatal("retained prefix implies nonexistent continuation")
	}
	var issues []map[string]any
	if json.Unmarshal(data["issues"], &issues) != nil || len(issues) != 32 || len(issues[0]) != 4 {
		t.Fatal("unsafe issue projection")
	}
}
func TestNFOErrorsStayFixedAndPrivate(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{domain.ErrNFODisabled, 409, "nfo_disabled"}, {domain.ErrNFOReaderUnavailable, 503, "nfo_reader_unavailable"},
		{domain.ErrNFOCacheCapacity, 409, "nfo_cache_capacity"}, {domain.ErrNFOIdentityMismatch, 409, "nfo_identity_mismatch"}, {domain.ErrNFOInvalidated, 409, "nfo_invalidated"},
		{domain.ErrNotFound, 404, "not_found"}, {domain.ErrConflict, 409, "conflict"}, {domain.ErrForbidden, 403, "forbidden"}, {domain.ErrUnauthenticated, 401, "authentication_required"},
		{errors.New("/private/token=secret"), 500, "internal_error"},
	} {
		h := newNFOHTTP(t, httpNFOJobs{policy: func(context.Context, domain.Actor, string) (domain.NFOLibraryPolicy, error) {
			return domain.NFOLibraryPolicy{}, fmt.Errorf("secret root /private: %w", tc.err)
		}}, func() bool { return true }, true)
		w := jobRequest(h, "GET", "/api/v1/libraries/"+libraryID+"/nfo/policy", "", "a")
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.code) || strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "secret") || w.Header().Get("Content-Language") != "zh-TW" {
			t.Fatalf("unsafe error %d %s", w.Code, w.Body)
		}
	}
}
func TestNFOFeatureFlagAndOpenAPIContract(t *testing.T) {
	h := newNFOHTTP(t, httpNFOJobs{}, func() bool { return true }, false)
	for _, path := range []string{"/api/v1/libraries/" + libraryID + "/nfo/policy", "/api/v1/jobs/" + sourceID + "/nfo", "/api/v1/jobs/" + sourceID + "/images"} {
		if w := jobRequest(h, "GET", path, "", "a"); w.Code != 404 {
			t.Fatal("disabled route exposed")
		}
	}
	cfg := validConfig()
	cfg.EnableAccounts, cfg.EnableJobs = true, true
	spec := Specification(cfg)
	paths := spec["paths"].(map[string]any)
	for _, r := range []struct {
		path, method string
		key          bool
	}{
		{"/libraries/{id}/nfo/policy", "get", false}, {"/libraries/{id}/nfo/policy", "put", true}, {"/libraries/{id}/nfo/validate", "post", true},
		{"/libraries/{id}/nfo/current-validations", "get", false}, {"/libraries/{id}/nfo/current-validations/{observationId}/issues", "get", false}, {"/jobs/{id}/nfo", "get", false}, {"/jobs/{id}/images", "get", false},
	} {
		op := paths["/api/v1"+r.path].(map[string]any)[r.method].(map[string]any)
		if op["x-jelee-role"] != "administrator" || len(op["security"].([]any)) != 1 {
			t.Fatal("missing auth")
		}
		found := false
		for _, p := range op["parameters"].([]any) {
			v := p.(map[string]any)
			if v["name"] == "Idempotency-Key" && v["required"] == true {
				found = true
			}
		}
		if found != r.key {
			t.Fatal("key declaration")
		}
	}
	schemas := spec["components"].(map[string]any)["schemas"].(map[string]any)
	for _, name := range []string{"ScanRequest", "NFOPolicyUpdate", "NFOValidateRequest", "NFOJobSummary", "ImageJobSummary", "NFOObservation", "NFOObservationPage", "NFOIssue", "NFOIssuesPage"} {
		if schemas[name].(map[string]any)["additionalProperties"] != false {
			t.Fatal("non-strict schema", name)
		}
	}
	if schemas["ScanRequest"].(map[string]any)["properties"].(map[string]any)["nfo"].(map[string]any)["default"] != false {
		t.Fatal("NFO default opt-in")
	}
	cfg.EnableJobs = false
	for path := range Specification(cfg)["paths"].(map[string]any) {
		if strings.Contains(path, "/nfo") || strings.HasSuffix(path, "/images") {
			t.Fatal("disabled docs exposed")
		}
	}
}
