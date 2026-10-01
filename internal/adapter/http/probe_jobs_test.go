package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
)

type httpProbeJobs struct {
	app.ProbeJobRepository
	submit  func(context.Context, domain.Actor, string, string, string, domain.ProbeIntent, domain.JobPolicy, *domain.ProbeIdentity) (domain.Job, bool, error)
	summary func(context.Context, domain.Actor, string) (domain.ProbeJobSummary, error)
}

func (p httpProbeJobs) SubmitScanJob(ctx context.Context, a domain.Actor, id, key, priority string, intent domain.ProbeIntent, policy domain.JobPolicy, identity *domain.ProbeIdentity) (domain.Job, bool, error) {
	return p.submit(ctx, a, id, key, priority, intent, policy, identity)
}
func (p httpProbeJobs) GetProbeJobSummary(ctx context.Context, a domain.Actor, id string) (domain.ProbeJobSummary, error) {
	return p.summary(ctx, a, id)
}
func httpProbeIdentity() domain.ProbeIdentity {
	return domain.ProbeIdentity{Platform: "linux-amd64", VendorVersion: "vendor", UpstreamVersion: "9.0.2", SourceRevision: strings.Repeat("a", 40), ExecutableSHA256: strings.Repeat("b", 64), RuntimeSHA256: strings.Repeat("c", 64), ArgumentsSHA256: strings.Repeat("d", 64), ParserVersion: domain.ProbeParserVersion, MetadataSchemaVersion: domain.ProbeMetadataSchemaVersion, SandboxVersion: "v1", FingerprintVersion: domain.ProbeFingerprintVersion}
}
func newProbeHTTP(t *testing.T, repo httpProbeJobs, capability func() domain.ProbeCapability) http.Handler {
	t.Helper()
	identity := httpProbeIdentity()
	jobs, err := app.NewJobsWithProbe(httpJobRepo{}, config.DefaultJobsConfig().Policy(), repo, &identity, capability)
	if err != nil {
		t.Fatal(err)
	}
	return newJobsHTTPService(t, jobs, true)
}
func httpAvailableProbe() domain.ProbeCapability {
	return domain.ProbeCapability{Enabled: true, Available: true, State: "available", Reason: "verified"}
}

func TestProbeRoutesRequireAdminStrictBodyAndOneKey(t *testing.T) {
	calls := 0
	h := newProbeHTTP(t, httpProbeJobs{submit: func(context.Context, domain.Actor, string, string, string, domain.ProbeIntent, domain.JobPolicy, *domain.ProbeIdentity) (domain.Job, bool, error) {
		calls++
		return domain.Job{}, false, nil
	}}, httpAvailableProbe)
	for _, path := range []string{"/api/v1/libraries/" + libraryID + "/scan", "/api/v1/libraries/" + libraryID + "/probe/rebuild", "/api/v1/items/" + itemID + "/probe/rebuild"} {
		for _, tc := range []struct {
			body, token string
			keys        []string
			status      int
		}{
			{"{}", "", []string{"probe-1"}, 401}, {"{}", "u", []string{"probe-1"}, 403},
			{"{}", "a", nil, 400}, {"{}", "a", []string{"one", "two"}, 400}, {"{}", "a", []string{"bad key"}, 400},
			{"null", "a", []string{"one"}, 400}, {"{} {}", "a", []string{"one"}, 400},
			{`{"priority":null}`, "a", []string{"one"}, 400}, {`{"priority":"manual","Priority":"background"}`, "a", []string{"one"}, 400},
			{`{"rootPath":"/private"}`, "a", []string{"one"}, 400}, {`{"identity":{"digest":"arbitrary"}}`, "a", []string{"one"}, 400},
			{`{"argv":["-write","private"]}`, "a", []string{"one"}, 400}, {`{"priority":"urgent"}`, "a", []string{"one"}, 400},
			{strings.Repeat(" ", 65537), "a", []string{"one"}, 413},
		} {
			w := jobRequest(h, "POST", path, tc.body, tc.token, tc.keys...)
			if w.Code != tc.status {
				t.Fatalf("%s body %q: %d want %d", path, tc.body[:min(len(tc.body), 80)], w.Code, tc.status)
			}
		}
		if w := jobRequest(h, "POST", path+"?tool=private", "{}", "a", "one"); w.Code != 400 {
			t.Fatal("query bypass")
		}
	}
	for _, body := range []string{`{"probe":null}`, `{"probe":"true"}`, `{"probe":1}`, `{"probe":true,"probe":false}`, `{"probe":true,"Probe":false}`} {
		if w := jobRequest(h, "POST", "/api/v1/libraries/"+libraryID+"/scan", body, "a", "one"); w.Code != 400 {
			t.Fatalf("invalid probe boolean accepted: %s", body)
		}
	}
	for _, path := range []string{"/api/v1/libraries/" + libraryID + "/probe/rebuild", "/api/v1/items/" + itemID + "/probe/rebuild"} {
		if w := jobRequest(h, "POST", path, `{"probe":false}`, "a", "one"); w.Code != 400 {
			t.Fatal("rebuild accepted opt-out")
		}
	}
	if calls != 0 {
		t.Fatal("invalid requests reached repository")
	}
}

func TestProbeRouteProjectsOnlyPublicIntentAndPreservesReplay(t *testing.T) {
	for _, tc := range []struct{ name, path, body, library, scope, target, priority string }{
		{"scan opt-in", "/api/v1/libraries/" + libraryID + "/scan", `{"probe":true}`, libraryID, domain.ProbeScopeIncremental, "", domain.JobPriorityManual},
		{"scan explicit opt-out", "/api/v1/libraries/" + libraryID + "/scan", `{"probe":false}`, libraryID, "", "", domain.JobPriorityManual},
		{"scan default", "/api/v1/libraries/" + libraryID + "/scan", `{}`, libraryID, "", "", domain.JobPriorityManual},
		{"library rebuild", "/api/v1/libraries/" + libraryID + "/probe/rebuild", `{"priority":"background"}`, libraryID, domain.ProbeScopeLibraryRebuild, "", domain.JobPriorityBackground},
		{"item rebuild", "/api/v1/items/" + itemID + "/probe/rebuild", `{}`, "", domain.ProbeScopeItemRebuild, itemID, domain.JobPriorityManual},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			h := newProbeHTTP(t, httpProbeJobs{submit: func(ctx context.Context, a domain.Actor, library, key, priority string, intent domain.ProbeIntent, policy domain.JobPolicy, identity *domain.ProbeIdentity) (domain.Job, bool, error) {
				calls++
				if a.UserID != userID || a.SessionID != sessionID || key != "probe-1" || library != tc.library || priority != tc.priority || intent.Scope != tc.scope || intent.TargetItemID != tc.target || policy.MaxEntries != 100000 {
					t.Fatal("incorrect public projection")
				}
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("unbounded repository request")
				}
				if (identity != nil) != (tc.scope != "") {
					t.Fatal("identity supplied for wrong intent")
				}
				if identity != nil && *identity != httpProbeIdentity() {
					t.Fatal("untrusted identity")
				}
				return domain.Job{ID: sourceID, LibraryID: libraryID, State: domain.JobQueued}, calls == 2, nil
			}}, httpAvailableProbe)
			for _, status := range []int{202, 200} {
				w := jobRequest(h, "POST", tc.path, tc.body, "a", "probe-1")
				if w.Code != status || w.Header().Get("Location") != "/api/v1/jobs/"+sourceID {
					t.Fatalf("unexpected result %d %s", w.Code, w.Body)
				}
				if (w.Header().Get("Idempotency-Replayed") == "true") != (status == 200) {
					t.Fatal("replay header")
				}
			}
		})
	}
}

func TestProbeSummaryIsAdminOnlyAndHasNoExecutionData(t *testing.T) {
	calls := 0
	h := newProbeHTTP(t, httpProbeJobs{summary: func(ctx context.Context, a domain.Actor, id string) (domain.ProbeJobSummary, error) {
		calls++
		if id != itemID || a.UserID != userID || a.SessionID != sessionID {
			t.Fatal("incorrect summary target")
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("summary has no deadline")
		}
		return domain.ProbeJobSummary{JobID: id, LibraryID: libraryID, Enabled: true, Scope: domain.ProbeScopeIncremental, Phase: domain.ProbeSummaryDone, Processed: 4, Hits: 2, NegativeHits: 1, Succeeded: 1}, nil
	}}, httpAvailableProbe)
	path := "/api/v1/jobs/" + itemID + "/probe"
	for token, status := range map[string]int{"": 401, "u": 403, "a": 200} {
		w := jobRequest(h, "GET", path, "", token)
		if w.Code != status {
			t.Fatalf("summary status %d want %d", w.Code, status)
		}
		if status == 200 {
			var envelope struct {
				Data map[string]any `json:"data"`
			}
			if json.Unmarshal(w.Body.Bytes(), &envelope) != nil {
				t.Fatal("invalid summary JSON")
			}
			if len(envelope.Data) != 12 || envelope.Data["processed"] != float64(4) || envelope.Data["hits"] != float64(2) {
				t.Fatalf("unexpected summary fields %v", envelope.Data)
			}
		}
	}
	for _, bad := range []string{path + "?root=private", "/api/v1/jobs/not-uuid/probe"} {
		if w := jobRequest(h, "GET", bad, "", "a"); w.Code != 400 {
			t.Fatal("invalid summary request accepted")
		}
	}
	if calls != 1 {
		t.Fatal("invalid summary reached repository")
	}
}

func TestProbeCapabilityChangesAndErrorsRemainSafe(t *testing.T) {
	capability := httpAvailableProbe()
	h := newProbeHTTP(t, httpProbeJobs{submit: func(_ context.Context, _ domain.Actor, _, _, _ string, _ domain.ProbeIntent, _ domain.JobPolicy, identity *domain.ProbeIdentity) (domain.Job, bool, error) {
		if identity == nil {
			return domain.Job{}, false, domain.ErrProbeDisabled
		}
		return domain.Job{ID: itemID}, false, nil
	}}, func() domain.ProbeCapability { return capability })
	for _, tc := range []struct {
		cap    domain.ProbeCapability
		status int
		code   string
	}{
		{httpAvailableProbe(), 202, ""},
		{domain.ProbeCapability{State: "disabled", Reason: "configuration_disabled"}, 409, "probe_disabled"},
		{domain.ProbeCapability{Enabled: true, State: "unavailable", Reason: "runtime_unavailable"}, 503, "probe_runtime_unavailable"},
		{httpAvailableProbe(), 202, ""},
	} {
		capability = tc.cap
		w := jobRequest(h, "POST", "/api/v1/libraries/"+libraryID+"/scan", `{"probe":true}`, "a", "probe-1")
		if w.Code != tc.status || tc.code != "" && !strings.Contains(w.Body.String(), tc.code) {
			t.Fatalf("capability did not govern request: %d %s", w.Code, w.Body)
		}
		w = jobRequest(h, "GET", "/api/v1/system", "", "")
		var response struct {
			Data struct {
				Probe        domain.ProbeCapability `json:"probe"`
				Capabilities map[string]bool        `json:"capabilities"`
			} `json:"data"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil || response.Data.Probe != tc.cap || response.Data.Capabilities["probe"] != tc.cap.Available {
			t.Fatal("system capability is stale")
		}
	}
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{domain.ErrProbeCacheCapacity, 409, "probe_cache_capacity"}, {domain.ErrProbeIdentityMismatch, 409, "probe_identity_mismatch"}, {domain.ErrProbeInvalidated, 409, "probe_invalidated"},
		{domain.ErrForbidden, 403, "forbidden"}, {domain.ErrUnauthenticated, 401, "authentication_required"}, {errors.New("/private/token=secret"), 500, "internal_error"},
	} {
		h := newProbeHTTP(t, httpProbeJobs{submit: func(context.Context, domain.Actor, string, string, string, domain.ProbeIntent, domain.JobPolicy, *domain.ProbeIdentity) (domain.Job, bool, error) {
			return domain.Job{}, false, tc.err
		}}, httpAvailableProbe)
		w := jobRequest(h, "POST", "/api/v1/libraries/"+libraryID+"/probe/rebuild", "{}", "a", "probe-1")
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.code) || strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "secret") || w.Header().Get("Content-Language") != "zh-TW" {
			t.Fatalf("unsafe probe error %d %s", w.Code, w.Body)
		}
	}
}

func TestProbeOpenAPIReflectsRoutesStrictInputsAndAdminRole(t *testing.T) {
	cfg := validConfig()
	cfg.EnableAccounts, cfg.EnableJobs = true, true
	spec := Specification(cfg)
	paths := spec["paths"].(map[string]any)
	for path, method := range map[string]string{"/api/v1/libraries/{id}/probe/rebuild": "post", "/api/v1/items/{id}/probe/rebuild": "post", "/api/v1/jobs/{id}/probe": "get"} {
		op := paths[path].(map[string]any)[method].(map[string]any)
		if op["x-jelee-role"] != "administrator" || len(op["security"].([]any)) != 1 {
			t.Fatal("probe operation has no admin auth declaration")
		}
		if method == "post" {
			found := false
			for _, p := range op["parameters"].([]any) {
				v := p.(map[string]any)
				if v["name"] == "Idempotency-Key" && v["required"] == true {
					found = true
				}
			}
			if !found || op["responses"].(map[string]any)["202"] == nil {
				t.Fatal("rebuild enqueue/replay contract absent")
			}
		}
	}
	schemas := spec["components"].(map[string]any)["schemas"].(map[string]any)
	scan := schemas["ScanRequest"].(map[string]any)
	if scan["additionalProperties"] != false || scan["properties"].(map[string]any)["probe"].(map[string]any)["default"] != false {
		t.Fatal("opt-in scan schema is not strict")
	}
	if properties := schemas["ProbeRebuildRequest"].(map[string]any)["properties"].(map[string]any); len(properties) != 1 || properties["priority"] == nil {
		t.Fatal("rebuild accepts unexpected inputs")
	}
	cfg.EnableJobs = false
	for path := range Specification(cfg)["paths"].(map[string]any) {
		if strings.Contains(path, "/probe") {
			t.Fatal("disabled job rollout advertises probe route")
		}
	}
}
