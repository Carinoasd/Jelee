package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// refreshProvider is an in-memory TMDB: movies and series by ID, a title
// search that filters by year like TMDB does, an external ID find, and
// titles whose search fails.
type refreshProvider struct {
	MetadataProvider
	mu       sync.Mutex
	movies   map[int32]domain.MovieCandidate
	series   map[int32]domain.SeriesCandidate
	find     map[string][]int32
	fail     map[string]bool
	searches int
}

func newRefreshProvider() *refreshProvider {
	return &refreshProvider{movies: map[int32]domain.MovieCandidate{}, series: map[int32]domain.SeriesCandidate{}, find: map[string][]int32{}, fail: map[string]bool{}}
}

func (p *refreshProvider) movie(id int32, title, date string) {
	p.movies[id] = domain.MovieCandidate{ProviderID: id, Source: "TMDB", SourceURL: domain.TMDBSourceURL("movie", id), Title: title, Overview: title + " overview", ReleaseDate: date}
}

func (p *refreshProvider) Movie(_ context.Context, id int32, language string) (domain.MovieCandidate, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	v, ok := p.movies[id]
	if !ok {
		return domain.MovieCandidate{}, domain.ErrNotFound
	}
	v.Language, v.FetchedAt = language, time.Now().UTC()
	return v, nil
}

func (p *refreshProvider) Series(_ context.Context, id int32, language string) (domain.SeriesCandidate, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	v, ok := p.series[id]
	if !ok {
		return domain.SeriesCandidate{}, domain.ErrNotFound
	}
	v.Language, v.FetchedAt = language, time.Now().UTC()
	return v, nil
}

func (p *refreshProvider) SearchMovies(_ context.Context, input domain.MovieSearchInput) ([]domain.MovieCandidate, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.searches++
	if p.fail[input.Query] {
		return nil, errors.New("provider down")
	}
	var out []domain.MovieCandidate
	for _, v := range p.movies {
		if domain.TitleSimilarity(v.Title, input.Query) >= 50 && (input.Year == 0 || strings.HasPrefix(v.ReleaseDate, fmt.Sprint(input.Year))) {
			v.Language, v.FetchedAt = input.Language, time.Now().UTC()
			out = append(out, v)
		}
	}
	slices.SortFunc(out, func(a, b domain.MovieCandidate) int { return int(a.ProviderID - b.ProviderID) })
	return out, nil
}

func (p *refreshProvider) SearchSeries(_ context.Context, input domain.SeriesSearchInput) ([]domain.SeriesCandidate, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []domain.SeriesCandidate
	for _, v := range p.series {
		if domain.TitleSimilarity(v.Title, input.Query) >= 50 {
			v.Language, v.FetchedAt = input.Language, time.Now().UTC()
			out = append(out, v)
		}
	}
	return out, nil
}

func (p *refreshProvider) FindByExternalID(_ context.Context, source, id, language string) (domain.ExternalIDMatches, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	v := domain.ExternalIDMatches{ExternalSource: source, ExternalID: id, Language: language, Movies: []domain.MovieCandidate{}, Series: []domain.SeriesCandidate{}}
	for _, movie := range p.find[id] {
		v.Movies = append(v.Movies, p.movies[movie])
	}
	return v, nil
}

// refreshItems is an in-memory item store whose provider apply follows the
// field priority of domain.TMDBMetadataSkip.
type refreshItems struct {
	mu    sync.Mutex
	items map[string]domain.ItemMetadata
}

func (r *refreshItems) ItemMetadata(_ context.Context, _ domain.Actor, item string) (domain.ItemMetadata, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.items[item]
	if !ok {
		return domain.ItemMetadata{}, domain.ErrNotFound
	}
	return domain.CloneItemMetadata(v), nil
}

func (r *refreshItems) UpdateItemMetadata(context.Context, domain.Actor, string, int64, []domain.ItemMetadataPatch) (domain.ItemMetadata, error) {
	return domain.ItemMetadata{}, domain.ErrInvalid
}

func (r *refreshItems) ApplyTMDBMetadata(_ context.Context, _ domain.Actor, item string, expected int64, update domain.TMDBMetadataUpdate) (domain.MetadataApplyResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v := r.items[item]
	if v.Revision != expected {
		return domain.MetadataApplyResult{}, domain.ErrConflict
	}
	result := domain.MetadataApplyResult{Applied: []string{}, Skipped: []domain.MetadataFieldSkip{}}
	for _, f := range update.Fields {
		i := slices.IndexFunc(v.Fields, func(old domain.ItemMetadataField) bool { return old.Field == f.Field })
		if i < 0 {
			v.Fields = append(v.Fields, domain.ItemMetadataField{Field: f.Field})
			i = len(v.Fields) - 1
		}
		if reason := domain.TMDBMetadataSkip(v.Fields[i], update.ReplaceExistingTitle); reason != "" {
			result.Skipped = append(result.Skipped, domain.MetadataFieldSkip{Field: f.Field, Reason: reason})
			continue
		}
		if v.Fields[i].Value == f.Value && v.Fields[i].Source == "tmdb" {
			continue
		}
		origin := f.Origin
		v.Fields[i] = domain.ItemMetadataField{Field: f.Field, Value: f.Value, Source: "tmdb", Locked: v.Fields[i].Locked, ProviderOrigin: &origin}
		result.Applied = append(result.Applied, f.Field)
	}
	v.Revision++
	r.items[item] = v
	result.Metadata = domain.CloneItemMetadata(v)
	return result, nil
}

func (r *refreshItems) MetadataPreferences(_ context.Context, _ domain.Actor, library string) (domain.MetadataPreferences, error) {
	return domain.MetadataPreferences{LibraryID: library, Language: "en-US"}, nil
}

func (r *refreshItems) UpdateMetadataPreferences(context.Context, domain.Actor, string, string, int64, ...[]string) (domain.MetadataPreferences, error) {
	return domain.MetadataPreferences{}, domain.ErrInvalid
}

// refreshRun is an in-memory execution repository.
type refreshRun struct {
	settings domain.MetadataRefreshSettings
	targets  []domain.MetadataRefreshTarget
	cursor   string
	results  []domain.MetadataRefreshResult
	cancel   func(n int) bool
}

func (r *refreshRun) NextMetadataRefreshBatch(_ context.Context, _ domain.JobLease, limit int) (domain.MetadataRefreshBatch, error) {
	if r.cancel != nil && r.cancel(len(r.results)) {
		return domain.MetadataRefreshBatch{}, context.Canceled
	}
	batch := domain.MetadataRefreshBatch{Actor: refreshActor, Settings: r.settings}
	for _, t := range r.targets {
		if t.ItemID > r.cursor && len(batch.Items) < limit {
			batch.Items = append(batch.Items, t)
		}
	}
	return batch, nil
}

func (r *refreshRun) RecordMetadataRefresh(_ context.Context, _ domain.JobLease, results []domain.MetadataRefreshResult) error {
	for _, v := range results {
		if !domain.ValidMetadataRefreshResult(v) || v.ItemID <= r.cursor {
			return domain.ErrInvalid
		}
		r.cursor = v.ItemID
	}
	r.results = append(r.results, results...)
	return nil
}

func (r *refreshRun) FinishMetadataRefresh(context.Context, domain.JobLease, string, string) error {
	return nil
}

func (r *refreshRun) outcomes() map[string]string {
	out := map[string]string{}
	for _, v := range r.results {
		out[v.ItemID] = v.Outcome
	}
	return out
}

var refreshActor = domain.Actor{UserID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", SessionID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"}

const refreshLibrary = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"

func refreshItemID(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012d", n) }

type metadataWorld struct {
	provider *refreshProvider
	items    *refreshItems
	run      *refreshRun
	worker   *MetadataRefreshWorker
	metadata *Metadata
}

func newMetadataWorld(t *testing.T) *metadataWorld {
	t.Helper()
	w := &metadataWorld{provider: newRefreshProvider(), items: &refreshItems{items: map[string]domain.ItemMetadata{}}, run: &refreshRun{settings: domain.DefaultMetadataRefreshSettings(refreshLibrary)}}
	metadata, err := NewMetadata(w.provider)
	if err != nil {
		t.Fatal(err)
	}
	if metadata, err = metadata.WithItemMetadata(w.items); err != nil {
		t.Fatal(err)
	}
	if w.metadata, err = metadata.WithLibraryPreferences(w.items); err != nil {
		t.Fatal(err)
	}
	if w.worker, err = NewMetadataRefreshWorker(w.metadata, w.run, 0); err != nil {
		t.Fatal(err)
	}
	return w
}

// add registers a scanned item with a file-name title and year and optional
// NFO identifiers and fields.
func (w *metadataWorld) add(n int, kind, title string, year int, ids string, fields ...domain.ItemMetadataField) string {
	id := refreshItemID(n)
	md := domain.ItemMetadata{ItemID: id, LibraryID: refreshLibrary, Kind: kind, NFOMode: domain.NFOModeOff, Revision: 1,
		Fields: append([]domain.ItemMetadataField{{Field: "title", Value: title, Source: "scan"}}, fields...)}
	if ids != "" {
		md.Facts = []domain.ItemMetadataFact{{Field: "uniqueIds", Value: json.RawMessage(ids), Source: "nfo"}}
	}
	w.items.items[id] = md
	w.run.targets = append(w.run.targets, domain.MetadataRefreshTarget{ItemID: id, Kind: kind, ScanTitle: title, ScanYear: year})
	slices.SortFunc(w.run.targets, func(a, b domain.MetadataRefreshTarget) int { return strings.Compare(a.ItemID, b.ItemID) })
	return id
}

func (w *metadataWorld) field(item, name string) domain.ItemMetadataField {
	for _, f := range w.items.items[item].Fields {
		if f.Field == name {
			return f
		}
	}
	return domain.ItemMetadataField{}
}

var metadataLease = domain.JobLease{Job: domain.Job{ID: "dddddddd-dddd-4ddd-8ddd-dddddddddddd", Kind: domain.JobMetadataRefresh}}

func TestMetadataRefreshWorkerMatchesAppliesAndReviews(t *testing.T) {
	w := newMetadataWorld(t)
	w.provider.movie(949, "Heat", "1995-12-15")
	w.provider.movie(155, "The Dark Knight", "2008-07-16")
	w.provider.movie(603, "The Matrix", "1999-03-31")
	w.provider.movie(604, "The Matrix", "1999-11-01")
	w.provider.movie(78, "Blade Runner", "1982-06-25")
	w.provider.find["tt0133093"] = []int32{603}
	byTMDB := w.add(1, "Movie", "heat", 1995, `[{"type":"tmdb","value":"949"}]`)
	byIMDb := w.add(2, "Movie", "matrix", 0, `[{"type":"imdb","value":"tt0133093"}]`)
	byTitle := w.add(3, "Movie", "The Dark Knight", 2008, "")
	ambiguous := w.add(4, "Movie", "The Matrix", 1999, "")
	wrongYear := w.add(5, "Movie", "Blade Runner", 1990, "")
	unmatched := w.add(6, "Movie", "Nothing Like It", 2001, "")
	locked := w.add(7, "Movie", "Heat", 1995, `[{"type":"tmdb","value":"949"}]`)
	for _, name := range []string{"title", "originalTitle", "overview", "date"} {
		i := slices.IndexFunc(w.items.items[locked].Fields, func(f domain.ItemMetadataField) bool { return f.Field == name })
		if i < 0 {
			w.items.items[locked] = func(v domain.ItemMetadata) domain.ItemMetadata {
				v.Fields = append(v.Fields, domain.ItemMetadataField{Field: name, Source: "manual"})
				return v
			}(w.items.items[locked])
			continue
		}
		w.items.items[locked].Fields[i].Locked = true
	}
	season := w.add(8, "Season", "Season 1", 0, "")
	if err := w.worker.Run(context.Background(), metadataLease); err != nil {
		t.Fatal(err)
	}
	got := w.run.outcomes()
	want := map[string]string{byTMDB: domain.MetadataRefreshApplied, byIMDb: domain.MetadataRefreshApplied, byTitle: domain.MetadataRefreshReviewed, ambiguous: domain.MetadataRefreshReviewed,
		wrongYear: domain.MetadataRefreshReviewed, unmatched: domain.MetadataRefreshUnmatched, locked: domain.MetadataRefreshSkipped, season: domain.MetadataRefreshSkipped}
	for item, outcome := range want {
		if got[item] != outcome {
			t.Errorf("item %s: %s, want %s", item[len(item)-2:], got[item], outcome)
		}
	}
	if f := w.field(byTMDB, "title"); f.Value != "Heat" || f.Source != "tmdb" || f.ProviderOrigin.ProviderID != 949 {
		t.Fatal("NFO TMDB ID not applied", f)
	}
	if f := w.field(byIMDb, "title"); f.ProviderOrigin == nil || f.ProviderOrigin.ProviderID != 603 {
		t.Fatal("IMDb find not applied", f)
	}
	reviews := map[string]*domain.MetadataRefreshReviewInput{}
	for _, r := range w.run.results {
		reviews[r.ItemID] = r.Review
	}
	if r := reviews[byTitle]; r.Confidence != domain.MetadataConfidenceMedium || r.Reason != domain.MetadataMatchTitleYear || r.Candidates[0].ProviderID != 155 || r.QueryYear != 2008 {
		t.Fatal("unique title and year not medium", r)
	}
	if r := reviews[ambiguous]; r.Reason != domain.MetadataMatchTitleYearAmbiguous || len(r.Candidates) != 2 {
		t.Fatal("ambiguous title", r)
	}
	if r := reviews[wrongYear]; r.Reason != domain.MetadataMatchYearMismatch || r.Candidates[0].ProviderID != 78 {
		t.Fatal("year mismatch not found by the yearless search", r)
	}
	if w.items.items[byTitle].Revision != 1 || w.items.items[locked].Revision != 1 {
		t.Fatal("reviewed or protected item written")
	}

	// A second run with the medium threshold applies the unique title match
	// and refreshes already matched items by their provider ID.
	w.run.cursor, w.run.results = "", nil
	w.run.settings.AutoApply = domain.MetadataAutoApplyMedium
	w.provider.movie(949, "Heat", "1995-12-15")
	if err := w.worker.Run(context.Background(), metadataLease); err != nil {
		t.Fatal(err)
	}
	got = w.run.outcomes()
	if got[byTitle] != domain.MetadataRefreshApplied || got[byTMDB] != domain.MetadataRefreshUnchanged || got[ambiguous] != domain.MetadataRefreshReviewed {
		t.Fatal("medium threshold", got)
	}

	// never sends even identifier matches to review.
	w.run.cursor, w.run.results = "", nil
	w.run.settings.AutoApply = domain.MetadataAutoApplyNever
	if err := w.worker.Run(context.Background(), metadataLease); err != nil {
		t.Fatal(err)
	}
	if got := w.run.outcomes(); got[byTMDB] != domain.MetadataRefreshReviewed {
		t.Fatal("never threshold applied", got[byTMDB])
	}
}

func TestMetadataRefreshWorkerSourcesConflictsAndRejections(t *testing.T) {
	w := newMetadataWorld(t)
	w.provider.movie(949, "Heat", "1995-12-15")
	w.provider.movie(950, "Heat", "1995-01-01")
	origin := &domain.MetadataProviderOrigin{Resource: "movie", ProviderID: 950}
	conflict := w.add(1, "Movie", "Heat", 1995, `[{"type":"tmdb","value":"949"}]`, domain.ItemMetadataField{Field: "date", Value: "1995-01-01", Source: "tmdb", ProviderOrigin: origin})
	existing := w.add(2, "Movie", "Heat", 1995, "", domain.ItemMetadataField{Field: "date", Value: "1995-01-01", Source: "tmdb", ProviderOrigin: origin})
	rejected := w.add(3, "Movie", "Heat", 1995, `[{"type":"tmdb","value":"949"}]`)
	w.run.targets[2].Review = domain.MetadataReviewRejected
	lockedHome := w.add(4, "HomeVideo", "Heat", 1995, `[{"type":"tmdb","value":"949"}]`)
	w.run.targets[3].KindLocked = true
	gone := w.add(5, "Movie", "Heat", 1995, "")
	delete(w.items.items, gone)
	noNFO := w.add(6, "Movie", "Heat", 1995, `[{"type":"tmdb","value":"949"}]`)
	if err := w.worker.Run(context.Background(), metadataLease); err != nil {
		t.Fatal(err)
	}
	got := w.run.outcomes()
	if got[conflict] != domain.MetadataRefreshReviewed || got[existing] != domain.MetadataRefreshApplied || got[rejected] != domain.MetadataRefreshSkipped ||
		got[lockedHome] != domain.MetadataRefreshSkipped || got[gone] != domain.MetadataRefreshSkipped || got[noNFO] != domain.MetadataRefreshApplied {
		t.Fatal("outcomes", got)
	}
	if r := w.run.results[0].Review; r.Reason != domain.MetadataMatchExternalConflict || len(r.Candidates) != 2 {
		t.Fatal("conflicting identifier not reviewed", r)
	}
	if f := w.field(existing, "title"); f.ProviderOrigin == nil || f.ProviderOrigin.ProviderID != 950 {
		t.Fatal("existing provider not refreshed", f)
	}
	// Without the nfo source the identifiers are ignored: the title search
	// finds two same-year candidates.
	w.run.cursor, w.run.results = "", nil
	w.run.settings.Sources = []string{domain.MetadataSourceTMDB}
	fresh := w.add(7, "Movie", "Heat", 1995, `[{"type":"tmdb","value":"949"}]`)
	if err := w.worker.Run(context.Background(), metadataLease); err != nil {
		t.Fatal(err)
	}
	for _, r := range w.run.results {
		if r.ItemID == fresh && (r.Outcome != domain.MetadataRefreshReviewed || r.Review.Reason != domain.MetadataMatchTitleYearAmbiguous) {
			t.Fatal("identifier used although the nfo source is off", r.Outcome)
		}
	}
}

func TestMetadataRefreshWorkerIsolatesProviderFailures(t *testing.T) {
	w := newMetadataWorld(t)
	w.provider.movie(949, "Heat", "1995-12-15")
	ok1 := w.add(1, "Movie", "Heat", 1995, `[{"type":"tmdb","value":"949"}]`)
	broken := w.add(2, "Movie", "Broken", 2000, "")
	w.provider.fail["Broken"] = true
	ok2 := w.add(3, "Movie", "Heat", 1995, `[{"type":"tmdb","value":"949"}]`)
	if err := w.worker.Run(context.Background(), metadataLease); err != nil {
		t.Fatal(err)
	}
	got := w.run.outcomes()
	if got[ok1] != domain.MetadataRefreshApplied || got[broken] != domain.MetadataRefreshFailed || got[ok2] != domain.MetadataRefreshApplied {
		t.Fatal("one failure stopped the run", got)
	}

	// An outage ends the run after the consecutive failure limit, keeping
	// every recorded item.
	w = newMetadataWorld(t)
	for i := range domain.MetadataRefreshMaxConsecutiveFailures + 5 {
		title := fmt.Sprintf("Down %d", i)
		w.provider.fail[title] = true
		w.add(i+1, "Movie", title, 2000, "")
	}
	if err := w.worker.Run(context.Background(), metadataLease); !errors.Is(err, errMetadataRefreshOutage) {
		t.Fatal("outage did not stop the run", err)
	}
	if len(w.run.results) != domain.MetadataRefreshMaxConsecutiveFailures {
		t.Fatal("outage results", len(w.run.results))
	}
}

func TestMetadataRefreshWorkerCancellationAndUnavailable(t *testing.T) {
	w := newMetadataWorld(t)
	w.provider.movie(949, "Heat", "1995-12-15")
	for i := range 40 {
		w.add(i+1, "Movie", "Heat", 1995, `[{"type":"tmdb","value":"949"}]`)
	}
	w.run.cancel = func(n int) bool { return n >= domain.MetadataRefreshBatchSize }
	if err := w.worker.Run(context.Background(), metadataLease); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation", err)
	}
	if len(w.run.results) != domain.MetadataRefreshBatchSize {
		t.Fatal("cancelled run lost or repeated items", len(w.run.results))
	}
	// A resume continues after the cursor without repeating an item.
	w.run.cancel = nil
	if err := w.worker.Run(context.Background(), metadataLease); err != nil {
		t.Fatal(err)
	}
	if len(w.run.results) != 40 {
		t.Fatal("resume", len(w.run.results))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.worker.Run(ctx, metadataLease); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled context", err)
	}
	if err := w.worker.Run(context.Background(), domain.JobLease{Job: domain.Job{Kind: domain.JobImageRefresh}}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("other kind", err)
	}
	local, _ := NewLocalMetadata(w.items)
	worker, _ := NewMetadataRefreshWorker(local, w.run, time.Second)
	if err := worker.Run(context.Background(), metadataLease); !errors.Is(err, domain.ErrMetadataUnavailable) {
		t.Fatal("worker without provider", err)
	}
	if _, err := NewMetadataRefreshWorker(nil, w.run, 0); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("nil metadata accepted")
	}
}

// refreshReviews is an in-memory MetadataRefreshJobRepository.
type refreshReviews struct {
	reviews  map[string]domain.MetadataRefreshReview
	resolved []string
	settings domain.MetadataRefreshSettings
}

func (r *refreshReviews) SubmitMetadataRefreshJob(_ context.Context, _ domain.Actor, library, _, priority string, _ domain.JobPolicy) (domain.Job, bool, error) {
	return domain.Job{LibraryID: library, Kind: domain.JobMetadataRefresh, Priority: priority}, false, nil
}
func (r *refreshReviews) GetMetadataRefreshState(context.Context, domain.Actor, string) (domain.MetadataRefreshJobState, error) {
	return domain.MetadataRefreshJobState{}, domain.ErrNotFound
}
func (r *refreshReviews) GetMetadataRefreshSettings(_ context.Context, _ domain.Actor, library string) (domain.MetadataRefreshSettings, error) {
	return domain.DefaultMetadataRefreshSettings(library), nil
}
func (r *refreshReviews) PutMetadataRefreshSettings(_ context.Context, _ domain.Actor, s domain.MetadataRefreshSettings) (domain.MetadataRefreshSettings, error) {
	r.settings = s
	return s, nil
}
func (r *refreshReviews) ListMetadataRefreshReviews(context.Context, domain.Actor, string, string, string, int) ([]domain.MetadataRefreshReview, error) {
	return nil, nil
}
func (r *refreshReviews) GetMetadataRefreshReview(_ context.Context, _ domain.Actor, item string) (domain.MetadataRefreshReview, error) {
	v, ok := r.reviews[item]
	if !ok {
		return v, domain.ErrNotFound
	}
	return v, nil
}
func (r *refreshReviews) RejectMetadataRefreshReview(_ context.Context, _ domain.Actor, item string) (domain.MetadataRefreshReview, error) {
	v := r.reviews[item]
	v.State = domain.MetadataReviewRejected
	r.reviews[item] = v
	return v, nil
}
func (r *refreshReviews) ResolveMetadataRefreshReview(_ context.Context, _ domain.Actor, item string, _ int32, _ int64) error {
	r.resolved = append(r.resolved, item)
	delete(r.reviews, item)
	return nil
}

func TestJobsMetadataRefreshReviewDecisions(t *testing.T) {
	w := newMetadataWorld(t)
	w.provider.movie(949, "Heat", "1995-12-15")
	item := w.add(1, "Movie", "Heat", 1995, "")
	reviews := &refreshReviews{reviews: map[string]domain.MetadataRefreshReview{item: {ItemID: item, State: domain.MetadataReviewPending, Resource: "movie",
		Candidates: []domain.MetadataRefreshCandidate{{ProviderID: 949, Title: "Heat"}}}}}
	base, err := NewJobs(jobRepositoryFake{}, jobTestPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = base.SubmitMetadataRefresh(context.Background(), refreshActor, refreshLibrary, "k", domain.JobPriorityManual); !errors.Is(err, domain.ErrMetadataUnavailable) {
		t.Fatal("submission without repository", err)
	}
	jobs, err := NewJobsWithMetadataRefresh(base, reviews, w.metadata)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if job, _, err := jobs.SubmitMetadataRefresh(ctx, refreshActor, refreshLibrary, "k", domain.JobPriorityManual); err != nil || job.Kind != domain.JobMetadataRefresh {
		t.Fatal("submission", err)
	}
	if _, err = jobs.PutMetadataRefreshSettings(ctx, refreshActor, domain.MetadataRefreshSettings{LibraryID: refreshLibrary, AutoApply: "low", Sources: []string{"tmdb"}}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("invalid settings accepted", err)
	}
	if _, err = jobs.MetadataRefreshReviews(ctx, refreshActor, refreshLibrary, "accepted", "", 10); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("unknown review state accepted", err)
	}
	if _, err = jobs.AcceptMetadataRefreshReview(ctx, refreshActor, item, domain.MetadataRefreshAcceptance{ProviderID: 5, ExpectedRevision: 1}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("unlisted candidate accepted", err)
	}
	if _, err = jobs.AcceptMetadataRefreshReview(ctx, refreshActor, item, domain.MetadataRefreshAcceptance{ProviderID: 949, ExpectedRevision: 7}); !errors.Is(err, domain.ErrConflict) || len(reviews.resolved) != 0 {
		t.Fatal("stale revision accepted", err)
	}
	result, err := jobs.AcceptMetadataRefreshReview(ctx, refreshActor, item, domain.MetadataRefreshAcceptance{ProviderID: 949, ExpectedRevision: 1})
	if err != nil || len(result.Applied) == 0 || len(reviews.resolved) != 1 || w.field(item, "title").ProviderOrigin.ProviderID != 949 {
		t.Fatal("accept", err, result)
	}
	reviews.reviews[item] = domain.MetadataRefreshReview{ItemID: item, State: domain.MetadataReviewPending, Resource: "movie", Candidates: []domain.MetadataRefreshCandidate{{ProviderID: 949}}}
	if v, err := jobs.RejectMetadataRefreshReview(ctx, refreshActor, item); err != nil || v.State != domain.MetadataReviewRejected {
		t.Fatal("reject", err)
	}
	if _, err = jobs.AcceptMetadataRefreshReview(ctx, refreshActor, item, domain.MetadataRefreshAcceptance{ProviderID: 949, ExpectedRevision: 2}); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("rejected review accepted", err)
	}
	local, _ := NewLocalMetadata(w.items)
	offline, _ := NewJobsWithMetadataRefresh(base, reviews, local)
	if _, _, err = offline.SubmitMetadataRefresh(ctx, refreshActor, refreshLibrary, "k", domain.JobPriorityManual); !errors.Is(err, domain.ErrMetadataUnavailable) {
		t.Fatal("submission without provider", err)
	}
	if _, err = offline.MetadataRefreshSettings(ctx, refreshActor, refreshLibrary); err != nil {
		t.Fatal("settings unreadable without provider", err)
	}
}
