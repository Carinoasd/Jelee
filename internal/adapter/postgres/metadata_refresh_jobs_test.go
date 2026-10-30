package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/nfo"
	"github.com/MoYuanCN/Jelee/internal/adapter/scan"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/jobs"
)

// refreshTMDB is an in-process stand-in for the TMDB adapter: movies by ID,
// a title search that filters by year like TMDB, an IMDb find, and IDs and
// titles whose requests fail. No request leaves the process.
type refreshTMDB struct {
	app.MetadataProvider
	mu      sync.Mutex
	movies  map[int32]domain.MovieCandidate
	imdb    map[string][]int32
	failIDs map[int32]bool
	fail    map[string]bool
}

func newRefreshTMDB() *refreshTMDB {
	return &refreshTMDB{movies: map[int32]domain.MovieCandidate{}, imdb: map[string][]int32{}, failIDs: map[int32]bool{}, fail: map[string]bool{}}
}

func (p *refreshTMDB) add(id int32, title, date string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.movies[id] = domain.MovieCandidate{ProviderID: id, Source: "TMDB", SourceURL: domain.TMDBSourceURL("movie", id), Title: title, OriginalTitle: title, Overview: title + " overview", ReleaseDate: date}
}

func (p *refreshTMDB) failing(id int32, title string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if id != 0 {
		p.failIDs[id] = true
	}
	if title != "" {
		p.fail[title] = true
	}
}

func (p *refreshTMDB) Movie(_ context.Context, id int32, language string) (domain.MovieCandidate, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failIDs[id] {
		return domain.MovieCandidate{}, errors.New("tmdb unavailable")
	}
	v, ok := p.movies[id]
	if !ok {
		return domain.MovieCandidate{}, domain.ErrNotFound
	}
	v.Language, v.FetchedAt = language, time.Now().UTC()
	return v, nil
}

func (p *refreshTMDB) Series(context.Context, int32, string) (domain.SeriesCandidate, error) {
	return domain.SeriesCandidate{}, domain.ErrNotFound
}

func (p *refreshTMDB) SearchMovies(_ context.Context, input domain.MovieSearchInput) ([]domain.MovieCandidate, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fail[input.Query] {
		return nil, errors.New("tmdb unavailable")
	}
	var out []domain.MovieCandidate
	for _, v := range p.movies {
		if domain.TitleSimilarity(v.Title, input.Query) >= 60 && (input.Year == 0 || strings.HasPrefix(v.ReleaseDate, fmt.Sprint(input.Year))) {
			v.Language, v.FetchedAt = input.Language, time.Now().UTC()
			out = append(out, v)
		}
	}
	slices.SortFunc(out, func(a, b domain.MovieCandidate) int { return int(a.ProviderID - b.ProviderID) })
	return out, nil
}

func (p *refreshTMDB) SearchSeries(context.Context, domain.SeriesSearchInput) ([]domain.SeriesCandidate, error) {
	return nil, nil
}

func (p *refreshTMDB) FindByExternalID(_ context.Context, source, id, language string) (domain.ExternalIDMatches, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	v := domain.ExternalIDMatches{ExternalSource: source, ExternalID: id, Language: language, Movies: []domain.MovieCandidate{}, Series: []domain.SeriesCandidate{}}
	for _, movie := range p.imdb[id] {
		v.Movies = append(v.Movies, p.movies[movie])
	}
	return v, nil
}

func refreshTestMetadata(t *testing.T, f jobFixture, provider app.MetadataProvider) *app.Metadata {
	t.Helper()
	m, err := app.NewMetadata(provider)
	if err != nil {
		t.Fatal(err)
	}
	if m, err = m.WithLibraryPreferences(f.s); err != nil {
		t.Fatal(err)
	}
	if m, err = m.WithItemMetadata(f.s); err != nil {
		t.Fatal(err)
	}
	reader, err := nfo.NewSummaryReader(domain.NFODefaultSourceBytes)
	if err != nil {
		t.Fatal(err)
	}
	if m, err = m.WithNFOItemFields(reader); err != nil {
		t.Fatal(err)
	}
	return m
}

// refreshTestItem registers a movie the way synchronisation does (file-name
// title and year, title source scan) with optional uniqueid identifiers.
func refreshTestItem(t *testing.T, f jobFixture, kind, title string, year int, ids string) string {
	t.Helper()
	var item string
	if err := f.s.Pool.QueryRow(f.ctx, `INSERT INTO items(library_id,title,kind) VALUES($1::uuid,$2,$3) RETURNING id::text`, f.registration.Library.ID, title, kind).Scan(&item); err != nil {
		t.Fatal(err)
	}
	if kind == "Movie" || kind == "Series" {
		digest := sha256.Sum256([]byte(item))
		var y *int
		if year != 0 {
			y = &year
		}
		if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO catalog_scan_items(item_id,library_id,kind,group_digest,parser_version,scan_title,year) VALUES($1::uuid,$2::uuid,$3,$4,'test',$5,$6)`, item, f.registration.Library.ID, kind, digest[:], title, y); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO item_metadata_state(item_id,revision) VALUES($1::uuid,2)`, item); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO item_metadata_fields(item_id,field,value,source,locked,updated_at) VALUES($1::uuid,'title',$2,'scan',false,clock_timestamp())`, item, title); err != nil {
		t.Fatal(err)
	}
	if ids != "" {
		if _, err := f.s.UpdateItemMetadataWithFacts(f.ctx, f.a, item, 2, nil, []domain.ItemMetadataFactPatch{{Field: "uniqueIds", Value: json.RawMessage(ids)}}); err != nil {
			t.Fatal("identifiers", err)
		}
	}
	return item
}

func refreshTestField(t *testing.T, f jobFixture, item, name string) domain.ItemMetadataField {
	t.Helper()
	md, err := f.s.ItemMetadata(f.ctx, f.a, item)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range md.Fields {
		if field.Field == name {
			return field
		}
	}
	return domain.ItemMetadataField{}
}

func refreshTestRevision(t *testing.T, f jobFixture, item string) int64 {
	t.Helper()
	md, err := f.s.ItemMetadata(f.ctx, f.a, item)
	if err != nil {
		t.Fatal(err)
	}
	return md.Revision
}

// hookedMetadataRefresh observes the checkpoints of a run.
type hookedMetadataRefresh struct {
	*Store
	mu      sync.Mutex
	records int
	after   func(n int, lease domain.JobLease)
}

func (h *hookedMetadataRefresh) RecordMetadataRefresh(ctx context.Context, lease domain.JobLease, results []domain.MetadataRefreshResult) error {
	err := h.Store.RecordMetadataRefresh(ctx, lease, results)
	h.mu.Lock()
	h.records++
	n := h.records
	h.mu.Unlock()
	if err == nil && h.after != nil {
		h.after(n, lease)
	}
	return err
}

func startMetadataRefreshRunner(t *testing.T, f jobFixture, metadata *app.Metadata, repository app.MetadataRefreshExecutionRepository, sync bool) func() {
	t.Helper()
	worker, err := app.NewMetadataRefreshWorker(metadata, repository, 0)
	if err != nil {
		t.Fatal(err)
	}
	opts := jobs.DefaultOptions()
	opts.Workers, opts.PollInterval = 1, 100*time.Millisecond
	opts.MetadataRefresh = &jobs.MetadataRefreshOptions{Worker: worker, Repository: f.s}
	if sync {
		opts.CatalogSync = &jobs.CatalogSyncOptions{Repository: f.s}
	}
	runner, err := jobs.New(f.s, scan.New(), opts, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err = runner.Start(f.ctx); err != nil {
		t.Fatal(err)
	}
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := runner.Stop(ctx); err != nil {
			t.Error(err)
		}
	}
}

func submitMetadataRefresh(t *testing.T, f jobFixture, key string) domain.Job {
	t.Helper()
	job, replay, err := f.s.SubmitMetadataRefreshJob(f.ctx, f.a, f.registration.Library.ID, key, domain.JobPriorityManual, f.policy)
	if err != nil || replay || job.Kind != domain.JobMetadataRefresh || job.State != domain.JobQueued {
		t.Fatal("submit metadata refresh", err, replay, job)
	}
	return job
}

func refreshCounts(t *testing.T, f jobFixture, job string) domain.MetadataRefreshJobState {
	t.Helper()
	state, err := f.s.GetMetadataRefreshState(f.ctx, f.a, job)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func putRefreshSettings(t *testing.T, f jobFixture, auto bool, threshold string, sources ...string) {
	t.Helper()
	if len(sources) == 0 {
		sources = domain.DefaultMetadataRefreshSources()
	}
	if _, err := f.s.PutMetadataRefreshSettings(f.ctx, f.a, domain.MetadataRefreshSettings{LibraryID: f.registration.Library.ID, Auto: auto, AutoApply: threshold, Sources: sources}); err != nil {
		t.Fatal(err)
	}
}

func TestMetadataRefreshJobAppliesProtectsAndReviews(t *testing.T) {
	f := newJobFixtureWithin(t, 3*time.Minute)
	provider := newRefreshTMDB()
	provider.add(949, "Heat", "1995-12-15")
	provider.add(27205, "Inception", "2010-07-15")
	provider.add(155, "The Dark Knight", "2008-07-16")
	provider.add(603, "The Matrix", "1999-03-31")
	provider.add(604, "The Matrix", "1999-11-01")
	provider.add(105, "Back to the Future", "1985-07-03")
	provider.imdb["tt0088763"] = []int32{105}
	metadata := refreshTestMetadata(t, f, provider)

	heat := refreshTestItem(t, f, "Movie", "Heat", 1995, `[{"type":"tmdb","value":"949"}]`)
	back := refreshTestItem(t, f, "Movie", "Back to the Future", 1985, `[{"type":"imdb","value":"tt0088763"}]`)
	// A locked manual title and a manual overview survive the apply.
	inception := refreshTestItem(t, f, "Movie", "Inception", 2010, `[{"type":"tmdb","value":"27205"}]`)
	manual, lock := "My Inception", true
	note := "my own words"
	if _, err := f.s.UpdateItemMetadata(f.ctx, f.a, inception, 3, []domain.ItemMetadataPatch{{Field: "title", Value: &manual, Locked: &lock}, {Field: "overview", Value: &note}}); err != nil {
		t.Fatal(err)
	}
	// Every provider field protected: not even looked up.
	protected := refreshTestItem(t, f, "Movie", "Heat", 1995, `[{"type":"tmdb","value":"949"}]`)
	empty := ""
	if _, err := f.s.UpdateItemMetadata(f.ctx, f.a, protected, 3, []domain.ItemMetadataPatch{{Field: "title", Locked: &lock}, {Field: "originalTitle", Value: &empty}, {Field: "overview", Value: &empty}, {Field: "date", Value: &empty}}); err != nil {
		t.Fatal(err)
	}
	knight := refreshTestItem(t, f, "Movie", "The Dark Knight", 2008, "")
	matrix := refreshTestItem(t, f, "Movie", "The Matrix", 1999, "")
	nothing := refreshTestItem(t, f, "Movie", "Nothing Like It", 2001, "")
	season := refreshTestItem(t, f, "Season", "Season 1", 0, "")
	revisions := map[string]int64{}
	for _, item := range []string{protected, knight, matrix, nothing, season} {
		revisions[item] = refreshTestRevision(t, f, item)
	}

	stop := startMetadataRefreshRunner(t, f, metadata, f.s, false)
	defer stop()
	job := submitMetadataRefresh(t, f, "refresh-1")
	if done := waitNFOApplyJob(t, f, job.ID); done.State != domain.JobSucceeded || done.Files != 6 || done.Skipped != 1 {
		t.Fatal("refresh job", done)
	}
	state := refreshCounts(t, f, job.ID)
	if state.Applied != 3 || state.Review != 2 || state.Unmatched != 1 || state.Skipped != 1 || state.Failed != 0 || state.Unchanged != 0 || state.Trigger != domain.MetadataRefreshTriggerManual || state.CursorItemID == nil {
		t.Fatalf("counters %+v", state)
	}
	if f := refreshTestField(t, f, heat, "title"); f.Value != "Heat" || f.Source != "tmdb" || f.ProviderOrigin == nil || f.ProviderOrigin.ProviderID != 949 {
		t.Fatal("NFO TMDB ID not applied", f)
	}
	if f := refreshTestField(t, f, back, "overview"); f.ProviderOrigin == nil || f.ProviderOrigin.ProviderID != 105 {
		t.Fatal("IMDb identifier not applied", f)
	}
	if title, overview, date := refreshTestField(t, f, inception, "title"), refreshTestField(t, f, inception, "overview"), refreshTestField(t, f, inception, "date"); title.Value != manual || !title.Locked || title.Source != "manual" ||
		overview.Value != note || overview.Source != "manual" || date.Value != "2010-07-15" || date.Source != "tmdb" {
		t.Fatal("locked or manual field overwritten", title, overview, date)
	}
	for item, revision := range revisions {
		if refreshTestRevision(t, f, item) != revision {
			t.Fatal("protected, reviewed or unmatched item written", item)
		}
	}
	var applies int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM audit_logs WHERE event='item.tmdb_metadata_applied' AND actor_id=$1::uuid`, f.a.UserID).Scan(&applies); err != nil || applies != 3 {
		t.Fatal("apply audit", applies, err)
	}

	reviews, err := f.s.ListMetadataRefreshReviews(f.ctx, f.a, f.registration.Library.ID, "", "", 100)
	if err != nil || len(reviews) != 2 {
		t.Fatal("reviews", err, reviews)
	}
	byItem := map[string]domain.MetadataRefreshReview{}
	for _, r := range reviews {
		byItem[r.ItemID] = r
	}
	if r := byItem[knight]; r.State != domain.MetadataReviewPending || r.Confidence != domain.MetadataConfidenceMedium || r.Reason != domain.MetadataMatchTitleYear || r.Candidates[0].ProviderID != 155 ||
		r.QueryTitle != "The Dark Knight" || r.QueryYear == nil || *r.QueryYear != 2008 || r.JobID == nil || *r.JobID != job.ID || r.Revision != revisions[knight] {
		t.Fatalf("medium review %+v", r)
	}
	if r := byItem[matrix]; r.Confidence != domain.MetadataConfidenceLow || r.Reason != domain.MetadataMatchTitleYearAmbiguous || len(r.Candidates) != 2 {
		t.Fatalf("ambiguous review %+v", r)
	}
	if page, err := f.s.ListMetadataRefreshReviews(f.ctx, f.a, f.registration.Library.ID, domain.MetadataReviewRejected, "", 100); err != nil || len(page) != 0 {
		t.Fatal("state filter", err, page)
	}

	// Accepting goes through the confirmed apply; rejecting keeps the item
	// out of later refreshes.
	base, err := app.NewJobs(f.s, f.policy)
	if err != nil {
		t.Fatal(err)
	}
	service, err := app.NewJobsWithMetadataRefresh(base, f.s, metadata)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.AcceptMetadataRefreshReview(f.ctx, f.a, knight, domain.MetadataRefreshAcceptance{ProviderID: 155, ExpectedRevision: revisions[knight] + 5}); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("stale accept", err)
	}
	if _, err = service.AcceptMetadataRefreshReview(f.ctx, f.a, knight, domain.MetadataRefreshAcceptance{ProviderID: 603, ExpectedRevision: revisions[knight]}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("unlisted candidate accepted", err)
	}
	result, err := service.AcceptMetadataRefreshReview(f.ctx, f.a, knight, domain.MetadataRefreshAcceptance{ProviderID: 155, ExpectedRevision: revisions[knight]})
	if err != nil || len(result.Applied) == 0 {
		t.Fatal("accept", err, result)
	}
	if _, err = f.s.GetMetadataRefreshReview(f.ctx, f.a, knight); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("accepted review kept", err)
	}
	rejected, err := service.RejectMetadataRefreshReview(f.ctx, f.a, matrix)
	if err != nil || rejected.State != domain.MetadataReviewRejected {
		t.Fatal("reject", err, rejected)
	}
	if _, err = service.RejectMetadataRefreshReview(f.ctx, f.a, matrix); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("second reject", err)
	}
	if _, err = service.AcceptMetadataRefreshReview(f.ctx, f.a, matrix, domain.MetadataRefreshAcceptance{ProviderID: 603, ExpectedRevision: revisions[matrix]}); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("rejected review accepted", err)
	}
	for event, want := range map[string]int{"metadata_refresh.review_accepted": 1, "metadata_refresh.review_rejected": 1, "metadata_refresh.submitted": 1} {
		var n int
		if err = f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM audit_logs WHERE event=$1`, event).Scan(&n); err != nil || n != want {
			t.Fatal("audit", event, n, err)
		}
	}

	// A second run refreshes matched items by their provider ID, leaves the
	// rejected item alone and keeps its rejection.
	job = submitMetadataRefresh(t, f, "refresh-2")
	if done := waitNFOApplyJob(t, f, job.ID); done.State != domain.JobSucceeded {
		t.Fatal("second refresh", done)
	}
	state = refreshCounts(t, f, job.ID)
	if state.Applied != 0 || state.Unchanged != 4 || state.Skipped != 2 || state.Unmatched != 1 || state.Review != 0 || state.Failed != 0 {
		t.Fatalf("second run counters %+v", state)
	}
	if r, err := f.s.GetMetadataRefreshReview(f.ctx, f.a, matrix); err != nil || r.State != domain.MetadataReviewRejected {
		t.Fatal("rejection lost", err, r)
	}
}

func TestMetadataRefreshJobIsolatesFailuresCancelsAndResumes(t *testing.T) {
	f := newJobFixtureWithin(t, 4*time.Minute)
	provider := newRefreshTMDB()
	const n = 40
	var items []string
	ids := map[string]int32{}
	for i := range n {
		id := int32(1000 + i)
		provider.add(id, fmt.Sprintf("Film %02d", i), "2001-01-01")
		item := refreshTestItem(t, f, "Movie", fmt.Sprintf("Film %02d", i), 2001, fmt.Sprintf(`[{"type":"tmdb","value":"%d"}]`, id))
		items, ids[item] = append(items, item), id
	}
	slices.Sort(items)
	metadata := refreshTestMetadata(t, f, provider)

	// Cancellation after the third item keeps exactly the recorded items.
	job := submitMetadataRefresh(t, f, "cancel-me")
	repo := &hookedMetadataRefresh{Store: f.s, after: func(n int, lease domain.JobLease) {
		if n == 3 {
			if _, err := f.s.CancelJob(f.ctx, f.a, lease.Job.ID); err != nil {
				t.Error(err)
			}
		}
	}}
	stop := startMetadataRefreshRunner(t, f, metadata, repo, false)
	if done := waitNFOApplyJob(t, f, job.ID); done.State != domain.JobCancelled {
		stop()
		t.Fatal("job not cancelled", done)
	}
	stop()
	state := refreshCounts(t, f, job.ID)
	if state.CursorItemID == nil {
		t.Fatal("no checkpoint")
	}
	applied := map[string]bool{}
	for _, item := range items {
		origin := refreshTestField(t, f, item, "overview").ProviderOrigin
		if item <= *state.CursorItemID {
			applied[item] = true
			if origin == nil || origin.ProviderID != ids[item] {
				t.Fatal("recorded item not applied", item)
			}
		} else if origin != nil {
			t.Fatal("item after the cursor applied", item)
		}
	}
	if state.Applied != len(applied) || len(applied) < 3 || len(applied) >= n {
		t.Fatalf("cancelled counters %+v for %d items", state, len(applied))
	}

	// One provider failure fails only its item; a lease lost after the first
	// checkpoint resumes after the cursor without counting an item twice.
	broken := items[n-5]
	provider.failing(ids[broken], "")
	job = submitMetadataRefresh(t, f, "resume-me")
	repo = &hookedMetadataRefresh{Store: f.s, after: func(n int, lease domain.JobLease) {
		if n == 1 {
			if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, lease.Job.ID); err != nil {
				t.Error(err)
			}
		}
	}}
	stop = startMetadataRefreshRunner(t, f, metadata, repo, false)
	defer stop()
	done := waitNFOApplyJob(t, f, job.ID)
	if done.State != domain.JobSucceeded || done.Attempts != 2 {
		t.Fatal("job did not resume once and succeed", done)
	}
	state = refreshCounts(t, f, job.ID)
	if total := state.Applied + state.Unchanged + state.Failed + state.Skipped + state.Review + state.Unmatched; total != n || state.Failed != 1 || state.Applied+state.Unchanged != n-1 {
		t.Fatalf("resumed counters %+v", state)
	}
	for _, item := range items {
		origin := refreshTestField(t, f, item, "overview").ProviderOrigin
		if item != broken && (origin == nil || origin.ProviderID != ids[item]) {
			t.Fatal("item not refreshed after resume", item)
		}
		if item == broken && applied[item] != (origin != nil) {
			t.Fatal("failed item changed", item)
		}
	}
}

func TestMetadataRefreshQueuedAfterCatalogSync(t *testing.T) {
	f := newJobFixtureWithin(t, 4*time.Minute)
	root := syncRootPath(t, f)
	writeSyncFiles(t, root, "Heat (1995).mp4", "Inception (2010)/Inception (2010).mkv")
	provider := newRefreshTMDB()
	provider.add(949, "Heat", "1995-12-15")
	provider.add(27205, "Inception", "2010-07-15")
	metadata := refreshTestMetadata(t, f, provider)
	enableAutoSync(t, f)
	putRefreshSettings(t, f, true, domain.MetadataAutoApplyMedium)
	stop := startMetadataRefreshRunner(t, f, metadata, f.s, true)
	f.submit(t, "scan-1")
	waitJobsIdle(t, f)
	var job, trigger, actor string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT j.id::text,s.trigger,j.actor_id::text FROM jobs j JOIN metadata_refresh_job_state s ON s.job_id=j.id WHERE j.kind='metadata_refresh' AND j.state='succeeded'`).Scan(&job, &trigger, &actor); err != nil {
		t.Fatal("no refresh after synchronisation", err)
	}
	if state := refreshCounts(t, f, job); trigger != domain.MetadataRefreshTriggerCatalogSync || actor != f.a.UserID || state.Applied != 2 {
		t.Fatalf("automatic refresh %s %s %+v", trigger, actor, state)
	}
	var heat string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT id::text FROM items WHERE kind='Movie' AND title='Heat'`).Scan(&heat); err != nil {
		t.Fatal(err)
	}
	if origin := refreshTestField(t, f, heat, "overview").ProviderOrigin; origin == nil || origin.ProviderID != 949 {
		t.Fatal("synchronised movie not refreshed", origin)
	}

	// A provider failure fails only the refresh item: synchronisation
	// succeeds and the refresh job finishes with the failure counted.
	writeSyncFiles(t, root, "Alien (1979).mkv")
	provider.failing(0, "Alien")
	f.submit(t, "scan-2")
	waitJobsIdle(t, f)
	if syncCount(t, f, `SELECT count(*) FROM jobs WHERE kind='catalog_sync' AND state='succeeded'`) != 2 || syncCount(t, f, `SELECT count(*) FROM jobs WHERE kind='metadata_refresh' AND state='succeeded'`) != 2 {
		t.Fatal("synchronisation or refresh did not finish after a provider failure")
	}
	if syncCount(t, f, `SELECT max(failed) FROM metadata_refresh_job_state`) != 1 {
		t.Fatal("provider failure not counted")
	}

	// Without the opt-in no refresh is queued.
	putRefreshSettings(t, f, false, domain.MetadataAutoApplyMedium)
	writeSyncFiles(t, root, "Brazil (1985).mkv")
	f.submit(t, "scan-3")
	waitJobsIdle(t, f)
	if syncCount(t, f, `SELECT count(*) FROM jobs WHERE kind='catalog_sync'`) != 3 || syncCount(t, f, `SELECT count(*) FROM jobs WHERE kind='metadata_refresh'`) != 2 {
		t.Fatal("refresh queued without opt-in")
	}
	stop()

	// A synchronisation without an active administrator behind it defers
	// the refresh and still succeeds.
	putRefreshSettings(t, f, true, domain.MetadataAutoApplyHigh)
	sync, _, err := f.s.SubmitCatalogSync(f.ctx, f.a, f.registration.Library.ID, "manual-sync", domain.JobPriorityManual, f.policy)
	if err != nil {
		t.Fatal(err)
	}
	lease := syncLease(t, f, "deferring-owner")
	if lease.Job.ID != sync.ID || !driveSync(t, f, lease, 100) {
		t.Fatal("manual synchronisation did not complete")
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE jobs SET actor_id=NULL WHERE id=$1::uuid`, sync.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.s.FinishJob(f.ctx, lease, domain.JobSucceeded, ""); err != nil {
		t.Fatal("finish without administrator", err)
	}
	var reason string
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT after_state->>'reason' FROM audit_logs WHERE event='metadata_refresh.deferred' AND target_id=$1::uuid`, sync.ID).Scan(&reason); err != nil || reason != "no_administrator" {
		t.Fatal("deferral not audited", reason, err)
	}
	if syncCount(t, f, `SELECT count(*) FROM jobs WHERE kind='metadata_refresh'`) != 2 || syncCount(t, f, `SELECT count(*) FROM jobs WHERE id=$1::uuid AND state='succeeded'`, sync.ID) != 1 {
		t.Fatal("deferred refresh queued or synchronisation failed")
	}
}

func TestMetadataRefreshAdmissionAndSettings(t *testing.T) {
	f := newJobFixture(t)
	library := f.registration.Library.ID
	settings, err := f.s.GetMetadataRefreshSettings(f.ctx, f.a, library)
	if err != nil || settings.Auto || settings.AutoApply != domain.MetadataAutoApplyHigh || !slices.Equal(settings.Sources, []string{"nfo", "tmdb"}) {
		t.Fatal("default settings", err, settings)
	}
	want := domain.MetadataRefreshSettings{LibraryID: library, Auto: true, AutoApply: domain.MetadataAutoApplyNever, Sources: []string{"tmdb"}}
	if _, err = f.s.PutMetadataRefreshSettings(f.ctx, f.a, want); err != nil {
		t.Fatal(err)
	}
	if got, err := f.s.GetMetadataRefreshSettings(f.ctx, f.a, library); err != nil || got.Auto != want.Auto || got.AutoApply != want.AutoApply || !slices.Equal(got.Sources, want.Sources) {
		t.Fatal("stored settings", err, got)
	}
	bad := want
	bad.Sources = []string{"nfo"}
	if _, err = f.s.PutMetadataRefreshSettings(f.ctx, f.a, bad); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("settings without a provider accepted", err)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE metadata_refresh_settings SET sources='{nfo,nfo}'`); err == nil {
		t.Fatal("schema accepted repeated sources")
	}
	missing := "00000000-0000-4000-8000-000000000001"
	if _, err = f.s.GetMetadataRefreshSettings(f.ctx, f.a, missing); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("unknown library settings", err)
	}
	if _, err = f.s.ListMetadataRefreshReviews(f.ctx, f.a, missing, "", "", 10); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("unknown library reviews", err)
	}
	if _, err = f.s.GetMetadataRefreshReview(f.ctx, f.a, missing); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("unknown review", err)
	}
	if _, _, err = f.s.SubmitMetadataRefreshJob(f.ctx, f.a, missing, "nowhere", domain.JobPriorityManual, f.policy); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("unknown library admitted", err)
	}
	job := submitMetadataRefresh(t, f, "admit")
	if again, replay, err := f.s.SubmitMetadataRefreshJob(f.ctx, f.a, library, "admit", domain.JobPriorityManual, f.policy); err != nil || !replay || again.ID != job.ID {
		t.Fatal("replay", err, replay)
	}
	if _, _, err = f.s.SubmitMetadataRefreshJob(f.ctx, f.a, library, "admit", domain.JobPriorityBackground, f.policy); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("replay with other priority", err)
	}
	if _, _, err = f.s.SubmitMetadataRefreshJob(f.ctx, f.a, library, "busy", domain.JobPriorityManual, f.policy); !errors.Is(err, domain.ErrJobBusy) {
		t.Fatal("second job admitted", err)
	}
	if _, err = f.s.GetNFOApplyState(f.ctx, f.a, job.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("refresh job read as another kind", err)
	}
	if _, err = f.s.CancelJob(f.ctx, f.a, job.ID); err != nil {
		t.Fatal(err)
	}
	scanJob := f.submit(t, "other")
	if _, err = f.s.GetMetadataRefreshState(f.ctx, f.a, scanJob.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("other kind read as refresh", err)
	}
	var audits int
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM audit_logs WHERE event='library.metadata_refresh_changed' AND target_id=$1::uuid`, library).Scan(&audits); err != nil || audits != 1 {
		t.Fatal("settings audit", audits, err)
	}
}

func TestMetadataRefreshMigrationRefusesRetainedState(t *testing.T) {
	f := newJobFixture(t)
	if _, _, err := f.s.SubmitMetadataRefreshJob(f.ctx, f.a, f.registration.Library.ID, "kept", domain.JobPriorityManual, f.policy); err != nil {
		t.Fatal(err)
	}
	refuseRetainedDowngrade(t, f, "metadata_refresh_jobs", "a retained metadata refresh job was downgraded")
}

func TestMetadataRefreshMigrationRefusesOptedInLibrary(t *testing.T) {
	f := newJobFixture(t)
	putRefreshSettings(t, f, true, domain.MetadataAutoApplyHigh)
	refuseRetainedDowngrade(t, f, "metadata_refresh_jobs", "an opted-in library was downgraded")
}

func TestMetadataRefreshMigrationRoundTrip(t *testing.T) {
	f := newJobFixture(t)
	item := refreshTestItem(t, f, "Movie", "Heat", 1995, "")
	// Default settings and a pending review are not retained state.
	putRefreshSettings(t, f, false, domain.MetadataAutoApplyHigh)
	if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO metadata_refresh_reviews(item_id,library_id,state,resource,confidence,reason,query_title,candidates) VALUES($1::uuid,$2::uuid,'pending','movie','low','year_mismatch','Heat','[{"providerId":949,"title":"Heat","originalTitle":"","date":"1995-12-15","similarity":100}]')`, item, f.registration.Library.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO metadata_refresh_reviews(item_id,library_id,state,resource,confidence,reason,query_title,candidates) VALUES($1::uuid,$2::uuid,'pending','movie','none','no_candidates','x','[]')`, refreshTestItem(t, f, "Movie", "Other", 0, ""), f.registration.Library.ID); err == nil {
		t.Fatal("schema accepted a review without candidates")
	}
	version := downgradeAboveMigration(t, f, "metadata_refresh_jobs")
	dsn := f.s.Pool.Config().ConnString()
	previous, dirty, err := Migrate(f.ctx, dsn, "down")
	if err != nil || dirty || previous >= version {
		t.Fatalf("downgrade: version=%d dirty=%t error=%v", previous, dirty, err)
	}
	var exists bool
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT to_regclass('metadata_refresh_reviews') IS NOT NULL OR to_regclass('metadata_refresh_job_state') IS NOT NULL OR to_regclass('metadata_refresh_settings') IS NOT NULL`).Scan(&exists); err != nil || exists {
		t.Fatal("downgrade kept refresh tables", err)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `INSERT INTO job_metric_totals(kind,priority) VALUES('metadata_refresh','manual')`); itemImageSQLState(err) != "23514" {
		t.Fatal("schema below refresh accepted the kind", err)
	}
	if v, dirty, err := Migrate(f.ctx, dsn, "up"); err != nil || dirty || v != SchemaVersion {
		t.Fatalf("upgrade: version=%d dirty=%t error=%v", v, dirty, err)
	}
	if md, err := f.s.ItemMetadata(f.ctx, f.a, item); err != nil || md.Fields[0].Value != "Heat" {
		t.Fatal("round trip lost the item", err)
	}
	submitMetadataRefresh(t, f, "after-round-trip")
}
