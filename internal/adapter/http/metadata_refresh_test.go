package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
)

// httpRefreshRepo is a MetadataRefreshJobRepository recording what reached
// it.
type httpRefreshRepo struct {
	calls    []string
	settings domain.MetadataRefreshSettings
	review   domain.MetadataRefreshReview
	err      error
}

func (r *httpRefreshRepo) SubmitMetadataRefreshJob(_ context.Context, _ domain.Actor, library, key, priority string, _ domain.JobPolicy) (domain.Job, bool, error) {
	r.calls = append(r.calls, "submit:"+priority)
	return domain.Job{ID: itemID, LibraryID: library, Kind: domain.JobMetadataRefresh, State: domain.JobQueued, Priority: priority}, key == "again", r.err
}

func (r *httpRefreshRepo) GetMetadataRefreshState(_ context.Context, _ domain.Actor, job string) (domain.MetadataRefreshJobState, error) {
	r.calls = append(r.calls, "state")
	if job != sourceID {
		return domain.MetadataRefreshJobState{}, domain.ErrNotFound
	}
	return domain.MetadataRefreshJobState{JobID: job, LibraryID: libraryID, Trigger: domain.MetadataRefreshTriggerCatalogSync, Applied: 2, Review: 1, UpdatedAt: time.Date(2026, 10, 30, 1, 2, 3, 0, time.UTC)}, nil
}

func (r *httpRefreshRepo) GetMetadataRefreshSettings(_ context.Context, _ domain.Actor, library string) (domain.MetadataRefreshSettings, error) {
	r.calls = append(r.calls, "settings")
	return domain.DefaultMetadataRefreshSettings(library), nil
}

func (r *httpRefreshRepo) PutMetadataRefreshSettings(_ context.Context, _ domain.Actor, s domain.MetadataRefreshSettings) (domain.MetadataRefreshSettings, error) {
	r.calls = append(r.calls, "put")
	r.settings = s
	return s, nil
}

func (r *httpRefreshRepo) ListMetadataRefreshReviews(_ context.Context, _ domain.Actor, _, state, cursor string, limit int) ([]domain.MetadataRefreshReview, error) {
	r.calls = append(r.calls, "list:"+state)
	if cursor != "" {
		return []domain.MetadataRefreshReview{}, nil
	}
	rows := []domain.MetadataRefreshReview{r.review}
	return rows[:min(limit, len(rows))], nil
}

func (r *httpRefreshRepo) GetMetadataRefreshReview(context.Context, domain.Actor, string) (domain.MetadataRefreshReview, error) {
	r.calls = append(r.calls, "review")
	return r.review, nil
}

func (r *httpRefreshRepo) RejectMetadataRefreshReview(context.Context, domain.Actor, string) (domain.MetadataRefreshReview, error) {
	r.calls = append(r.calls, "reject")
	v := r.review
	v.State = domain.MetadataReviewRejected
	return v, r.err
}

func (r *httpRefreshRepo) ResolveMetadataRefreshReview(context.Context, domain.Actor, string, int32, int64) error {
	r.calls = append(r.calls, "resolve")
	return nil
}

type httpRefreshProvider struct{ app.MetadataProvider }

type httpRefreshItems struct{ app.ItemMetadataRepository }

func newMetadataRefreshHTTP(t *testing.T, repo *httpRefreshRepo, provider bool) http.Handler {
	t.Helper()
	jobs, err := app.NewJobs(httpJobRepo{}, config.DefaultJobsConfig().Policy())
	if err != nil {
		t.Fatal(err)
	}
	var metadata *app.Metadata
	if provider {
		if metadata, err = app.NewMetadata(httpRefreshProvider{}); err != nil {
			t.Fatal(err)
		}
		if metadata, err = metadata.WithItemMetadata(httpRefreshItems{}); err != nil {
			t.Fatal(err)
		}
	}
	if repo != nil {
		if jobs, err = app.NewJobsWithMetadataRefresh(jobs, repo, metadata); err != nil {
			t.Fatal(err)
		}
	}
	return newJobsHTTPService(t, jobs, true)
}

func TestMetadataRefreshSubmitAndStateRoutes(t *testing.T) {
	repo := &httpRefreshRepo{}
	h := newMetadataRefreshHTTP(t, repo, true)
	path := "/api/v1/libraries/" + libraryID + "/metadata/refresh"
	if w := jobRequest(h, "POST", path, "{}", "a", "one"); w.Code != 202 || w.Header().Get("Location") != "/api/v1/jobs/"+itemID || string(nfoHTTPData(t, w.Body.Bytes())["kind"]) != `"metadata_refresh"` {
		t.Fatalf("submit: %d %s", w.Code, w.Body)
	}
	if w := jobRequest(h, "POST", path, `{"priority":"background"}`, "a", "again"); w.Code != 200 || w.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("replay: %d", w.Code)
	}
	for _, bad := range []string{`{"priority":"urgent"}`, `{"library":"x"}`, `[]`} {
		if w := jobRequest(h, "POST", path, bad, "a", "one"); w.Code != 400 {
			t.Fatalf("body %s: %d", bad, w.Code)
		}
	}
	if w := jobRequest(h, "POST", path, "{}", "a"); w.Code != 400 {
		t.Fatalf("missing key: %d", w.Code)
	}
	for token, status := range map[string]int{"": 401, "u": 403} {
		if w := jobRequest(h, "POST", path, "{}", token, "one"); w.Code != status {
			t.Fatalf("auth %q: %d", token, w.Code)
		}
	}
	w := jobRequest(h, "GET", "/api/v1/jobs/"+sourceID+"/metadata-refresh", "", "a")
	data := nfoHTTPData(t, w.Body.Bytes())
	if w.Code != 200 || string(data["trigger"]) != `"catalog_sync"` || string(data["applied"]) != "2" || string(data["cursorItemId"]) != "null" {
		t.Fatalf("state: %d %s", w.Code, w.Body)
	}
	if w := jobRequest(h, "GET", "/api/v1/jobs/"+itemID+"/metadata-refresh", "", "a"); w.Code != 404 {
		t.Fatalf("other job: %d", w.Code)
	}
	if strings.Join(repo.calls, ",") != "submit:manual,submit:background,state,state" {
		t.Fatal("calls", repo.calls)
	}
	// Without a provider a submission is refused; progress stays readable.
	offline := newMetadataRefreshHTTP(t, &httpRefreshRepo{}, false)
	if w := jobRequest(offline, "POST", path, "{}", "a", "one"); w.Code != 503 || !strings.Contains(w.Body.String(), "metadata_unavailable") {
		t.Fatalf("offline submit: %d %s", w.Code, w.Body)
	}
	if w := jobRequest(offline, "GET", "/api/v1/jobs/"+sourceID+"/metadata-refresh", "", "a"); w.Code != 200 {
		t.Fatalf("offline state: %d", w.Code)
	}
	if w := jobRequest(newMetadataRefreshHTTP(t, nil, true), "GET", "/api/v1/libraries/"+libraryID+"/metadata/refresh/settings", "", "a"); w.Code != 503 {
		t.Fatalf("no repository: %d", w.Code)
	}
}

func TestMetadataRefreshSettingsRoutes(t *testing.T) {
	repo := &httpRefreshRepo{}
	h := newMetadataRefreshHTTP(t, repo, true)
	path := "/api/v1/libraries/" + libraryID + "/metadata/refresh/settings"
	w := jobRequest(h, "GET", path, "", "a")
	if data := nfoHTTPData(t, w.Body.Bytes()); w.Code != 200 || string(data["auto"]) != "false" || string(data["autoApply"]) != `"high"` || string(data["sources"]) != `["nfo","tmdb"]` {
		t.Fatalf("defaults: %d %s", w.Code, w.Body)
	}
	if w := jobRequest(h, "PUT", path, `{"auto":true,"autoApply":"medium","sources":["tmdb"]}`, "a"); w.Code != 200 || !repo.settings.Auto || repo.settings.AutoApply != "medium" || repo.settings.LibraryID != libraryID {
		t.Fatalf("put: %d %s", w.Code, w.Body)
	}
	for _, bad := range []string{`{"auto":true,"autoApply":"medium"}`, `{"auto":true,"sources":["tmdb"]}`, `{"autoApply":"high","sources":["tmdb"]}`, `{"auto":true,"autoApply":"low","sources":["tmdb"]}`,
		`{"auto":true,"autoApply":"high","sources":["nfo"]}`, `{"auto":true,"autoApply":"high","sources":["omdb","tmdb"]}`, `{"auto":true,"autoApply":"high","sources":[]}`} {
		if w := jobRequest(h, "PUT", path, bad, "a"); w.Code != 400 {
			t.Fatalf("settings %s: %d", bad, w.Code)
		}
	}
	if strings.Join(repo.calls, ",") != "settings,put" {
		t.Fatal("invalid settings reached the repository", repo.calls)
	}
}

func TestMetadataRefreshReviewRoutes(t *testing.T) {
	year := 1995
	repo := &httpRefreshRepo{review: domain.MetadataRefreshReview{ItemID: itemID, LibraryID: libraryID, Kind: "Movie", Title: "Heat", Revision: 3, State: domain.MetadataReviewPending,
		Resource: "movie", Confidence: domain.MetadataConfidenceMedium, Reason: domain.MetadataMatchTitleYear, QueryTitle: "Heat", QueryYear: &year,
		Candidates: []domain.MetadataRefreshCandidate{{ProviderID: 949, Title: "Heat", Date: "1995-12-15", Similarity: 100}}, UpdatedAt: time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)}}
	h := newMetadataRefreshHTTP(t, repo, true)
	list := "/api/v1/libraries/" + libraryID + "/metadata/refresh/reviews"
	w := jobRequest(h, "GET", list+"?state=pending&limit=1", "", "a")
	var page struct {
		Data struct {
			Reviews    []map[string]json.RawMessage `json:"reviews"`
			Pagination struct {
				NextCursor string `json:"nextCursor"`
			} `json:"pagination"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || w.Code != 200 || len(page.Data.Reviews) != 1 || page.Data.Pagination.NextCursor != itemID || string(page.Data.Reviews[0]["revision"]) != "3" {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	for _, bad := range []string{"?state=accepted", "?limit=0", "?cursor=nope", "?sort=title"} {
		if w := jobRequest(h, "GET", list+bad, "", "a"); w.Code != 400 {
			t.Fatalf("list %s: %d", bad, w.Code)
		}
	}
	accept := "/api/v1/items/" + itemID + "/metadata/refresh/accept"
	for _, bad := range []string{`{}`, `{"providerId":949}`, `{"expectedRevision":3}`, `{"providerId":0,"expectedRevision":3}`, `{"providerId":949,"expectedRevision":0}`} {
		if w := jobRequest(h, "POST", accept, bad, "a"); w.Code != 400 {
			t.Fatalf("accept %s: %d", bad, w.Code)
		}
	}
	if w := jobRequest(h, "POST", accept, `{"providerId":5,"expectedRevision":3}`, "a"); w.Code != 400 {
		t.Fatalf("unlisted candidate: %d", w.Code)
	}
	repo.review.State = domain.MetadataReviewRejected
	if w := jobRequest(h, "POST", accept, `{"providerId":949,"expectedRevision":3}`, "a"); w.Code != 409 {
		t.Fatalf("rejected review: %d", w.Code)
	}
	repo.review.State = domain.MetadataReviewPending
	reject := "/api/v1/items/" + itemID + "/metadata/refresh/reject"
	if w := jobRequest(h, "POST", reject, `{"x":1}`, "a"); w.Code != 400 {
		t.Fatalf("reject body: %d", w.Code)
	}
	w = jobRequest(h, "POST", reject, `{}`, "a")
	if data := nfoHTTPData(t, w.Body.Bytes()); w.Code != 200 || string(data["state"]) != `"rejected"` {
		t.Fatalf("reject: %d %s", w.Code, w.Body)
	}
	repo.err = domain.ErrConflict
	if w := jobRequest(h, "POST", reject, `{}`, "a"); w.Code != 409 {
		t.Fatalf("second reject: %d", w.Code)
	}
	if w := jobRequest(h, "POST", reject, `{}`, "u"); w.Code != 403 {
		t.Fatalf("user reject: %d", w.Code)
	}
	for _, call := range repo.calls {
		if call == "resolve" {
			t.Fatal("a refused accept resolved the review")
		}
	}
}
