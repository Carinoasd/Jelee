package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
)

type httpNFOWriteJobs struct {
	submit func(context.Context, domain.Actor, string, string, string, []string, domain.JobPolicy) (domain.Job, bool, error)
}

func (r httpNFOWriteJobs) SubmitNFOWriteJob(ctx context.Context, a domain.Actor, library, key, priority string, preparations []string, policy domain.JobPolicy) (domain.Job, bool, error) {
	return r.submit(ctx, a, library, key, priority, preparations, policy)
}

type httpNFOWritePreparer struct{ prepared domain.NFOWritePreparation }

func (p httpNFOWritePreparer) PrepareNFOWrite(_ context.Context, scope domain.NFOItemScope, request domain.NFOWritePrepareRequest) (domain.NFOWritePreparation, error) {
	out := p.prepared
	out.Request, out.Scope = request, scope
	return out, nil
}

type httpNFOWritePreparationRepo struct {
	scope  domain.NFOItemScope
	saved  *domain.NFOWritePreparation
	replay bool
	find   func(context.Context, domain.Actor, string, string) (domain.NFOWritePreparation, error)
}

func (r *httpNFOWritePreparationRepo) ResolveItemNFO(_ context.Context, _ domain.Actor, item string, revision int64) (domain.NFOItemScope, error) {
	s := r.scope
	s.ItemID, s.Revision = item, revision
	return s, nil
}
func (r *httpNFOWritePreparationRepo) FindNFOWritePreparation(ctx context.Context, a domain.Actor, key, digest string) (domain.NFOWritePreparation, error) {
	if r.find != nil {
		return r.find(ctx, a, key, digest)
	}
	if r.saved != nil && r.replay {
		return *r.saved, nil
	}
	return domain.NFOWritePreparation{}, domain.ErrNotFound
}
func (r *httpNFOWritePreparationRepo) SaveNFOWritePreparation(_ context.Context, _ domain.Actor, _ string, p domain.NFOWritePreparation) (domain.NFOWritePreparation, bool, error) {
	r.saved = &p
	return p, r.replay, nil
}

const nfoWriteOriginal = "<movie><title>Old</title></movie>"
const nfoWriteReplacement = "<movie><title>New</title></movie>"

func nfoWriteFixture(t *testing.T) (*httpNFOWritePreparationRepo, httpNFOWritePreparer) {
	t.Helper()
	original := sha256.Sum256([]byte(nfoWriteOriginal))
	scope := domain.NFOItemScope{ItemID: itemID, LibraryID: libraryID, SourceID: sourceID, RootID: sourceID, Kind: "Movie", Revision: 1, Generation: 1, MediaPath: "movie/a.mkv", Source: domain.NFOSource{RootPath: "/private/root", RelativePath: "movie/a.nfo"}}
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	prepared := domain.NFOWritePreparation{ID: sourceID, Version: domain.NFOWritePreparationVersion, Scope: scope,
		Stamp:    domain.NFOStamp{Size: int64(len(nfoWriteOriginal)), SHA256: hex.EncodeToString(original[:]), FingerprintVersion: domain.NFOFingerprintVersion},
		Original: []byte(nfoWriteOriginal), Replacement: []byte(nfoWriteReplacement), CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour)}
	return &httpNFOWritePreparationRepo{scope: scope}, httpNFOWritePreparer{prepared: prepared}
}

func newNFOWriteHTTP(t *testing.T, repo httpNFOJobs, writes app.NFOWriteJobRepository, preparations *app.NFOWritePreparations) http.Handler {
	t.Helper()
	identity := domain.DefaultNFOIdentity()
	probeIdentity := httpProbeIdentity()
	jobs, err := app.NewJobsWithScanStages(httpJobRepo{}, config.DefaultJobsConfig().Policy(), repo, app.ScanServices{Probes: httpProbeJobs{}, ProbeIdentity: &probeIdentity, ProbeCapability: httpAvailableProbe, NFOAdmin: repo, NFOQueries: repo, Images: repo, NFOIdentity: &identity, NFOAvailable: func() bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	if writes != nil {
		if jobs, err = app.NewJobsWithNFOWrite(jobs, writes); err != nil {
			t.Fatal(err)
		}
	}
	var options []Option
	if preparations != nil {
		options = append(options, WithNFOWritePreparations(preparations))
	}
	return newJobsHTTPService(t, jobs, true, options...)
}

func nfoWritePreparations(t *testing.T) (*app.NFOWritePreparations, *httpNFOWritePreparationRepo) {
	t.Helper()
	repo, preparer := nfoWriteFixture(t)
	service, err := app.NewNFOWritePreparations(repo, preparer)
	if err != nil {
		t.Fatal(err)
	}
	return service, repo
}

func TestNFOWritePolicyRoute(t *testing.T) {
	var got []bool
	repo := httpNFOJobs{setWritePolicy: func(ctx context.Context, a domain.Actor, id string, enabled bool) (domain.NFOLibraryPolicy, error) {
		nfoHTTPBoundedActor(t, ctx, a)
		got = append(got, enabled)
		if id != libraryID {
			t.Fatal("wrong library")
		}
		if enabled && len(got) == 3 {
			return domain.NFOLibraryPolicy{}, domain.ErrConflict
		}
		return domain.NFOLibraryPolicy{LibraryID: id, Mode: domain.NFOModeReadOnly, Generation: 2, WriteEnabled: enabled}, nil
	}}
	h := newNFOWriteHTTP(t, repo, nil, nil)
	path := "/api/v1/libraries/" + libraryID + "/nfo/write-policy"
	w := jobRequest(h, "PUT", path, `{"enabled":true}`, "a")
	if w.Code != 200 || string(nfoHTTPData(t, w.Body.Bytes())["writeEnabled"]) != "true" {
		t.Fatalf("enable: %d %s", w.Code, w.Body)
	}
	if w = jobRequest(h, "PUT", path, `{"enabled":false}`, "a"); w.Code != 200 || string(nfoHTTPData(t, w.Body.Bytes())["writeEnabled"]) != "false" {
		t.Fatalf("disable: %d %s", w.Code, w.Body)
	}
	if w = jobRequest(h, "PUT", path, `{"enabled":true}`, "a"); w.Code != 409 || !strings.Contains(w.Body.String(), `"conflict"`) {
		t.Fatalf("conflict: %d %s", w.Code, w.Body)
	}
	for token, status := range map[string]int{"": 401, "u": 403} {
		if w = jobRequest(h, "PUT", path, `{"enabled":true}`, token); w.Code != status {
			t.Fatalf("auth %q: %d", token, w.Code)
		}
	}
	for _, body := range []string{``, `null`, `{}`, `{"enabled":null}`, `{"enabled":"true"}`, `{"enabled":true,"root":"/x"}`, `{"enabled":true} {}`} {
		if w = jobRequest(h, "PUT", path, body, "a"); w.Code != 400 || !strings.Contains(w.Body.String(), "invalid_request") {
			t.Fatalf("body %q: %d %s", body, w.Code, w.Body)
		}
	}
	if len(got) != 3 {
		t.Fatalf("rejected input reached the service: %v", got)
	}
}

func TestNFOWritePrepareRoute(t *testing.T) {
	service, repo := nfoWritePreparations(t)
	h := newNFOWriteHTTP(t, httpNFOJobs{}, nil, service)
	path := "/api/v1/items/" + itemID + "/nfo/write-preparations"
	body := `{"revision":1,"edits":[{"field":"title","value":"New"},{"field":"plot","value":"Story"}]}`
	w := jobRequest(h, "POST", path, body, "a", "prep-1")
	if w.Code != 201 || w.Header().Get("Idempotency-Replayed") != "" {
		t.Fatalf("first: %d %s", w.Code, w.Body)
	}
	data := nfoHTTPData(t, w.Body.Bytes())
	replacement := sha256.Sum256([]byte(nfoWriteReplacement))
	var fields []string
	_ = json.Unmarshal(data["fields"], &fields)
	if len(data) != 11 || string(data["id"]) != `"`+sourceID+`"` || string(data["itemId"]) != `"`+itemID+`"` || string(data["libraryId"]) != `"`+libraryID+`"` || len(fields) != 2 || fields[0] != "title" || fields[1] != "plot" ||
		string(data["replacementSha256"]) != `"`+hex.EncodeToString(replacement[:])+`"` || string(data["originalBytes"]) != "33" || string(data["replacementBytes"]) != "33" {
		t.Fatalf("summary: %s", w.Body)
	}
	for _, leak := range []string{"private", "movie/a", "<movie>", "Story", "Old"} {
		if strings.Contains(w.Body.String(), leak) {
			t.Fatalf("response leaks %q: %s", leak, w.Body)
		}
	}
	request := repo.saved.Request
	if request.ItemID != itemID || request.CreateMissing || request.MaxBytes != domain.NFODefaultSourceBytes || request.Backups != 3 || len(request.Edits) != 2 {
		t.Fatalf("defaults: %+v", request)
	}
	repo.replay = true
	if w = jobRequest(h, "POST", path, body, "a", "prep-1"); w.Code != 200 || w.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("replay: %d %s", w.Code, w.Body)
	}
	repo.replay = false
	explicit := `{"revision":2,"edits":[{"field":"title","value":"x"}],"bom":"omit","indent":"  ","maxBytes":1024,"backups":0}`
	if w = jobRequest(h, "POST", path, explicit, "a", "prep-2"); w.Code != 201 {
		t.Fatalf("explicit: %d %s", w.Code, w.Body)
	}
	if r := repo.saved.Request; r.Revision != 2 || r.BOM != "omit" || r.Indent != "  " || r.MaxBytes != 1024 || r.Backups != 0 {
		t.Fatalf("explicit options: %d %q %q %d %d", r.Revision, r.BOM, r.Indent, r.MaxBytes, r.Backups)
	}
	for token, status := range map[string]int{"": 401, "u": 403} {
		if w = jobRequest(h, "POST", path, body, token, "prep-1"); w.Code != status {
			t.Fatalf("auth %q: %d", token, w.Code)
		}
	}
	for _, keys := range [][]string{nil, {"a", "b"}, {"bad key"}} {
		if w = jobRequest(h, "POST", path, body, "a", keys...); w.Code != 400 {
			t.Fatalf("key %v: %d", keys, w.Code)
		}
	}
	for _, bad := range []string{``, `null`, `{}`, `{"revision":1}`, `{"revision":0,"edits":[]}`, `{"revision":1,"edits":null}`,
		`{"revision":1,"edits":[{"field":"title","value":"x"},{"field":"title","value":"y"}]}`, `{"revision":1,"edits":[{"field":"uniqueid","value":"x"}]}`,
		`{"revision":1,"edits":[{"field":"title","value":"x","extra":1}]}`, `{"revision":1,"edits":[],"createMissing":true}`, `{"revision":1,"edits":[],"itemId":"` + sourceID + `"}`,
		`{"revision":1,"edits":[],"bom":"always"}`, `{"revision":1,"edits":[],"indent":"x"}`, `{"revision":1,"edits":[],"maxBytes":0}`, `{"revision":1,"edits":[],"backups":17}`, `{"revision":1,"edits":[],"backups":-1}`,
		`{"revision":"1","edits":[]}`, `{"revision":1,"edits":[]} {}`} {
		if w = jobRequest(h, "POST", path, bad, "a", "prep-bad"); w.Code != 400 || !strings.Contains(w.Body.String(), "invalid_request") {
			t.Fatalf("body %s: %d %s", bad, w.Code, w.Body)
		}
	}
	if w = jobRequest(h, "POST", "/api/v1/items/not-a-uuid/nfo/write-preparations", body, "a", "prep-1"); w.Code != 400 {
		t.Fatalf("item id: %d", w.Code)
	}
}

func TestNFOWritePrepareUnavailableAndErrors(t *testing.T) {
	path := "/api/v1/items/" + itemID + "/nfo/write-preparations"
	body := `{"revision":1,"edits":[]}`
	if w := jobRequest(newNFOWriteHTTP(t, httpNFOJobs{}, nil, nil), "POST", path, body, "a", "k"); w.Code != 503 || !strings.Contains(w.Body.String(), "nfo_reader_unavailable") {
		t.Fatalf("unavailable: %d %s", w.Code, w.Body)
	}
	repo, preparer := nfoWriteFixture(t)
	repo.find = func(context.Context, domain.Actor, string, string) (domain.NFOWritePreparation, error) {
		return domain.NFOWritePreparation{}, errors.New("/private/root failed")
	}
	service, err := app.NewNFOWritePreparations(repo, preparer)
	if err != nil {
		t.Fatal(err)
	}
	if w := jobRequest(newNFOWriteHTTP(t, httpNFOJobs{}, nil, service), "POST", path, body, "a", "k"); w.Code != 500 || strings.Contains(w.Body.String(), "private") {
		t.Fatalf("internal: %d %s", w.Code, w.Body)
	}
}

func TestNFOWriteSubmitRoute(t *testing.T) {
	calls := 0
	repo := httpNFOWriteJobs{submit: func(ctx context.Context, a domain.Actor, library, key, priority string, preparations []string, policy domain.JobPolicy) (domain.Job, bool, error) {
		nfoHTTPBoundedActor(t, ctx, a)
		calls++
		if library != libraryID || key != "one" || len(preparations) != 2 || preparations[0] != sourceID || policy.MaxEntries != 100000 {
			t.Fatal("wrong submission")
		}
		switch priority {
		case domain.JobPriorityManual, domain.JobPriorityBackground:
		default:
			t.Fatal("bad priority")
		}
		return domain.Job{ID: itemID, LibraryID: library, Kind: domain.JobNFOWrite, State: domain.JobQueued, Priority: priority}, calls == 2, nil
	}}
	h := newNFOWriteHTTP(t, httpNFOJobs{}, repo, nil)
	path := "/api/v1/libraries/" + libraryID + "/nfo/writes"
	body := `{"preparations":["` + sourceID + `","` + userID + `"]}`
	for _, want := range []int{202, 200} {
		w := jobRequest(h, "POST", path, body, "a", "one")
		if w.Code != want || w.Header().Get("Location") != "/api/v1/jobs/"+itemID || (w.Header().Get("Idempotency-Replayed") == "true") != (want == 200) || string(nfoHTTPData(t, w.Body.Bytes())["kind"]) != `"nfo_write"` {
			t.Fatalf("submit %d: %d %s", want, w.Code, w.Body)
		}
	}
	if w := jobRequest(h, "POST", path, `{"preparations":["`+sourceID+`","`+userID+`"],"priority":"background"}`, "a", "one"); w.Code != 202 && w.Code != 200 {
		t.Fatalf("background: %d", w.Code)
	}
	for token, status := range map[string]int{"": 401, "u": 403} {
		if w := jobRequest(h, "POST", path, body, token, "one"); w.Code != status {
			t.Fatalf("auth %q: %d", token, w.Code)
		}
	}
	for _, keys := range [][]string{nil, {"a", "b"}, {"bad key"}} {
		if w := jobRequest(h, "POST", path, body, "a", keys...); w.Code != 400 {
			t.Fatalf("key %v: %d", keys, w.Code)
		}
	}
	many := make([]string, 101)
	for i := range many {
		many[i] = `"` + sourceID + `"`
	}
	for _, bad := range []string{``, `null`, `{}`, `{"preparations":[]}`, `{"preparations":null}`, `{"preparations":["not-a-uuid"]}`, `{"preparations":[` + strings.Join(many, ",") + `]}`,
		`{"preparations":["` + sourceID + `","` + sourceID + `"]}`, `{"preparations":["` + sourceID + `"],"priority":"urgent"}`, `{"preparations":["` + sourceID + `"],"priority":null}`,
		`{"preparations":["` + sourceID + `"],"path":"/x"}`, `{"preparations":["` + sourceID + `"]} {}`} {
		if w := jobRequest(h, "POST", path, bad, "a", "one"); w.Code != 400 || !strings.Contains(w.Body.String(), "invalid_request") {
			t.Fatalf("body %s: %d %s", bad, w.Code, w.Body)
		}
	}
	if calls != 3 {
		t.Fatalf("rejected input reached the repository: %d", calls)
	}
}

func TestNFOWriteDisabledMapsToConflict(t *testing.T) {
	body := `{"preparations":["` + sourceID + `"]}`
	path := "/api/v1/libraries/" + libraryID + "/nfo/writes"
	repo := httpNFOWriteJobs{submit: func(context.Context, domain.Actor, string, string, string, []string, domain.JobPolicy) (domain.Job, bool, error) {
		return domain.Job{}, false, errors.New("wrapped: " + domain.ErrNFOWriteDisabled.Error())
	}}
	if w := jobRequest(newNFOWriteHTTP(t, httpNFOJobs{}, repo, nil), "POST", path, body, "a", "one"); w.Code != 500 {
		t.Fatalf("an unwrapped string must not match: %d", w.Code)
	}
	repo.submit = func(context.Context, domain.Actor, string, string, string, []string, domain.JobPolicy) (domain.Job, bool, error) {
		return domain.Job{}, false, domain.ErrNFOWriteDisabled
	}
	if w := jobRequest(newNFOWriteHTTP(t, httpNFOJobs{}, repo, nil), "POST", path, body, "a", "one"); w.Code != 409 || !strings.Contains(w.Body.String(), "nfo_write_disabled") {
		t.Fatalf("disabled: %d %s", w.Code, w.Body)
	}
	// Without the write repository (runner disabled) no library is writable.
	if w := jobRequest(newNFOWriteHTTP(t, httpNFOJobs{}, nil, nil), "POST", path, body, "a", "one"); w.Code != 409 || !strings.Contains(w.Body.String(), "nfo_write_disabled") {
		t.Fatalf("no repository: %d %s", w.Code, w.Body)
	}
}
