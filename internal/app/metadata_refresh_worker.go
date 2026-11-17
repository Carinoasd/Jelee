package app

import (
	"context"
	"errors"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// MetadataRefreshExecutionRepository is the durable side of a
// metadata_refresh run. Every method is fenced by the job lease.
type MetadataRefreshExecutionRepository interface {
	// NextMetadataRefreshBatch returns up to limit movies, home videos and
	// series of the job's library after the job's cursor in ascending ID
	// order, with the library's settings and the job's actor bound to a live
	// administrator session. It answers context.Canceled once cancellation
	// was requested and domain.ErrForbidden when the actor is no longer an
	// active administrator with a live session.
	NextMetadataRefreshBatch(ctx context.Context, lease domain.JobLease, limit int) (domain.MetadataRefreshBatch, error)
	// RecordMetadataRefresh records results in ascending item order after the
	// cursor in one transaction: a review is stored unless the item's review
	// was rejected meanwhile, an applied item loses its pending review, and
	// the cursor moves to the last result with the counters.
	RecordMetadataRefresh(ctx context.Context, lease domain.JobLease, results []domain.MetadataRefreshResult) error
	FinishMetadataRefresh(ctx context.Context, lease domain.JobLease, state, code string) error
}

// MetadataRefreshFinder resolves an IMDb or TVDB identifier to provider
// candidates. The TMDB adapter implements it; a provider without it leaves
// those identifiers unused.
type MetadataRefreshFinder interface {
	FindByExternalID(ctx context.Context, source, id, language string) (domain.ExternalIDMatches, error)
}

// MetadataRefreshWorker runs one metadata_refresh job. Matching reads the
// provider through Metadata (language fallback, budget and cache live in the
// adapter) and every apply goes through Metadata.ApplyTMDB with the job's
// actor, so field locks, manual values, trusted NFO and the revision check
// are the interactive path's. Each item is checkpointed on its own.
type MetadataRefreshWorker struct {
	metadata  *Metadata
	repo      MetadataRefreshExecutionRepository
	dbTimeout time.Duration
}

// NewMetadataRefreshWorker accepts a Metadata without a provider: every job
// it runs then fails with domain.ErrMetadataUnavailable, so a job queued
// under another configuration ends instead of blocking its library. dbTimeout
// bounds each repository call; zero uses two seconds.
func NewMetadataRefreshWorker(metadata *Metadata, repository MetadataRefreshExecutionRepository, dbTimeout time.Duration) (*MetadataRefreshWorker, error) {
	if metadata == nil || repository == nil || dbTimeout < 0 {
		return nil, domain.ErrInvalid
	}
	if dbTimeout == 0 {
		dbTimeout = 2 * time.Second
	}
	return &MetadataRefreshWorker{metadata: metadata, repo: repository, dbTimeout: dbTimeout}, nil
}

func (w *MetadataRefreshWorker) db(ctx context.Context, call func(context.Context) error) error {
	c, cancel := context.WithTimeout(ctx, w.dbTimeout)
	defer cancel()
	return call(c)
}

// errMetadataRefreshOutage ends a run after too many provider failures in a
// row; the runner records it as metadata_refresh_failed.
var errMetadataRefreshOutage = errors.New("metadata provider unavailable for consecutive items")

// Run refreshes the library's items after the stored cursor until none
// remain. An item that fails on its own never ends the run; storage, lease,
// authorization and context errors do, keeping every recorded item, and so
// does a provider outage of MetadataRefreshMaxConsecutiveFailures items.
func (w *MetadataRefreshWorker) Run(ctx context.Context, lease domain.JobLease) error {
	if ctx == nil || lease.Job.Kind != domain.JobMetadataRefresh {
		return domain.ErrInvalid
	}
	if !w.metadata.HasProvider() || !w.metadata.HasItemMetadata() {
		return domain.ErrMetadataUnavailable
	}
	failures := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var batch domain.MetadataRefreshBatch
		if err := w.db(ctx, func(c context.Context) (err error) {
			batch, err = w.repo.NextMetadataRefreshBatch(c, lease, domain.MetadataRefreshBatchSize)
			return err
		}); err != nil {
			return err
		}
		if len(batch.Items) == 0 {
			return nil
		}
		preferences, err := w.metadata.LibraryPreferences(ctx, batch.Actor, batch.Settings.LibraryID)
		if err != nil {
			return err
		}
		for _, target := range batch.Items {
			if err := ctx.Err(); err != nil {
				return err
			}
			result, provider, err := w.refreshItem(ctx, batch, preferences.Language, target)
			if err != nil {
				return err
			}
			// Each item is recorded even when the run stops right after,
			// so a cancelled job keeps exact counts and a resume never
			// repeats a recorded item.
			if err := w.db(context.WithoutCancel(ctx), func(c context.Context) error {
				return w.repo.RecordMetadataRefresh(c, lease, []domain.MetadataRefreshResult{result})
			}); err != nil {
				return err
			}
			switch {
			case provider:
				failures++
			case result.Outcome != domain.MetadataRefreshSkipped:
				failures = 0
			}
			if failures >= domain.MetadataRefreshMaxConsecutiveFailures {
				return errMetadataRefreshOutage
			}
		}
	}
}

// refreshItem matches and applies or records one item. provider reports a
// failure because the provider was unavailable. The error is non-nil only
// when the run must stop.
func (w *MetadataRefreshWorker) refreshItem(ctx context.Context, batch domain.MetadataRefreshBatch, language string, target domain.MetadataRefreshTarget) (domain.MetadataRefreshResult, bool, error) {
	result := domain.MetadataRefreshResult{ItemID: target.ItemID, Outcome: domain.MetadataRefreshSkipped}
	resource := domain.MetadataRefreshResource(target.Kind)
	// A rejected review keeps automatic refreshes away; a locked HomeVideo
	// kind would be changed to Movie by an apply.
	if target.Review == domain.MetadataReviewRejected || resource == "" || target.Kind == "HomeVideo" && target.KindLocked {
		return result, false, nil
	}
	md, err := w.metadata.ItemFields(ctx, batch.Actor, target.ItemID)
	if err != nil {
		return w.itemError(ctx, result, err)
	}
	subject := domain.NewMetadataRefreshSubject(target, md, batch.Settings.UsesSource(domain.MetadataSourceNFO))
	if subject.Protected {
		return result, false, nil
	}
	match, err := w.match(ctx, subject, language)
	if err != nil {
		return w.itemError(ctx, result, err)
	}
	if domain.MetadataAutoApplies(batch.Settings.AutoApply, match.Confidence) {
		return w.apply(ctx, batch.Actor, md, match, result)
	}
	if match.Confidence == domain.MetadataConfidenceNone {
		result.Outcome = domain.MetadataRefreshUnmatched
		return result, false, nil
	}
	review := domain.MetadataRefreshReviewInput{Resource: match.Resource, Confidence: match.Confidence, Reason: match.Reason, QueryTitle: subject.Title, QueryYear: subject.Year, Candidates: match.Candidates}
	if len(review.QueryTitle) > 1024 {
		review.QueryTitle = ""
	}
	if !domain.ValidMetadataRefreshReviewInput(review) {
		result.Outcome = domain.MetadataRefreshFailed
		return result, false, nil
	}
	result.Outcome, result.Review = domain.MetadataRefreshReviewed, &review
	return result, false, nil
}

// apply applies a chosen match with the revision just read; a revision
// conflict is retried once with fresh state.
func (w *MetadataRefreshWorker) apply(ctx context.Context, actor domain.Actor, md domain.ItemMetadata, match domain.MetadataRefreshMatch, result domain.MetadataRefreshResult) (domain.MetadataRefreshResult, bool, error) {
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			if md, err = w.metadata.ItemFields(ctx, actor, result.ItemID); err != nil {
				break
			}
		}
		var applied domain.MetadataApplyResult
		applied, err = w.metadata.ApplyTMDB(ctx, actor, result.ItemID, domain.TMDBMetadataApplyInput{Resource: match.Resource, ProviderID: match.ProviderID, ExpectedRevision: md.Revision, Confirmed: true})
		if err == nil {
			result.Outcome = domain.MetadataRefreshUnchanged
			if metadataRefreshChanged(md, applied.Metadata) {
				result.Outcome = domain.MetadataRefreshApplied
			}
			return result, false, nil
		}
		if !errors.Is(err, domain.ErrConflict) {
			break
		}
	}
	return w.itemError(ctx, result, err)
}

// metadataRefreshChanged reports whether an apply changed a value or a
// source; rewriting the same provider values only renews their fetch time.
func metadataRefreshChanged(before, after domain.ItemMetadata) bool {
	values := map[string]domain.ItemMetadataField{}
	for _, f := range before.Fields {
		values[f.Field] = f
	}
	for _, f := range after.Fields {
		old, ok := values[f.Field]
		if !ok || old.Value != f.Value || old.Source != f.Source || old.Locked != f.Locked || (old.ProviderOrigin == nil) != (f.ProviderOrigin == nil) ||
			old.ProviderOrigin != nil && f.ProviderOrigin != nil && (old.ProviderOrigin.ProviderID != f.ProviderOrigin.ProviderID || old.ProviderOrigin.Resource != f.ProviderOrigin.Resource) {
			return true
		}
	}
	return before.Kind != after.Kind && after.Kind != ""
}

// itemError classifies an error of one item. Cancellation, lease loss, lost
// authorization and storage failures stop the run; a vanished item is
// skipped; a provider or item-local failure fails only this item.
func (w *MetadataRefreshWorker) itemError(ctx context.Context, result domain.MetadataRefreshResult, err error) (domain.MetadataRefreshResult, bool, error) {
	if ctx.Err() != nil {
		return result, false, ctx.Err()
	}
	switch {
	case errors.Is(err, domain.ErrNotFound):
		result.Outcome = domain.MetadataRefreshSkipped
		return result, false, nil
	case errors.Is(err, domain.ErrMetadataUnavailable):
		result.Outcome = domain.MetadataRefreshFailed
		return result, true, nil
	case errors.Is(err, domain.ErrInvalid), errors.Is(err, domain.ErrConflict):
		result.Outcome = domain.MetadataRefreshFailed
		return result, false, nil
	}
	return result, false, err
}

// match resolves the subject in source order: an NFO TMDB ID, then an IMDb
// or TVDB ID through the provider's find, then the provider ID an earlier
// apply chose, then the title and year.
func (w *MetadataRefreshWorker) match(ctx context.Context, s domain.MetadataRefreshSubject, language string) (domain.MetadataRefreshMatch, error) {
	if s.TMDBID > 0 {
		candidate, err := w.byID(ctx, s.Resource, s.TMDBID, language)
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			return domain.MetadataRefreshMatch{}, err
		}
		if err == nil {
			if m := domain.MatchMetadataByIdentifier(s.Resource, domain.MetadataMatchNFOTMDB, []domain.MetadataRefreshCandidate{candidate}); m.Confidence != domain.MetadataConfidenceNone {
				return domain.ReconcileMetadataMatch(s, m), nil
			}
		}
	}
	for _, id := range []struct{ source, value, reason string }{{domain.ExternalSourceIMDb, s.IMDbID, domain.MetadataMatchNFOIMDb}, {domain.ExternalSourceTVDB, s.TVDBID, domain.MetadataMatchNFOTVDB}} {
		if id.value == "" {
			continue
		}
		candidates, err := w.find(ctx, s.Resource, id.source, id.value, language)
		if err != nil {
			return domain.MetadataRefreshMatch{}, err
		}
		if m := domain.MatchMetadataByIdentifier(s.Resource, id.reason, candidates); m.Confidence != domain.MetadataConfidenceNone {
			return domain.ReconcileMetadataMatch(s, m), nil
		}
	}
	if s.ExistingProviderID > 0 {
		return domain.ExistingMetadataMatch(s), nil
	}
	if s.Title == "" {
		return domain.MetadataRefreshMatch{Resource: s.Resource, Confidence: domain.MetadataConfidenceNone, Reason: domain.MetadataMatchNoCandidates}, nil
	}
	candidates, err := w.search(ctx, s.Resource, s.Title, s.Year, language)
	if err == nil && len(candidates) == 0 && s.Year != 0 {
		// Search filters by year: without a hit, look again without it so a
		// wrong year shows up as a mismatch instead of no match.
		candidates, err = w.search(ctx, s.Resource, s.Title, 0, language)
	}
	if errors.Is(err, domain.ErrInvalid) {
		return domain.MetadataRefreshMatch{Resource: s.Resource, Confidence: domain.MetadataConfidenceNone, Reason: domain.MetadataMatchNoCandidates}, nil
	}
	if err != nil {
		return domain.MetadataRefreshMatch{}, err
	}
	return domain.MatchMetadataByTitle(s.Resource, s.Title, s.Year, candidates), nil
}

func (w *MetadataRefreshWorker) byID(ctx context.Context, resource string, id int32, language string) (domain.MetadataRefreshCandidate, error) {
	if resource == "movie" {
		movie, err := w.metadata.Movie(ctx, id, language)
		return movieRefreshCandidate(movie), err
	}
	series, err := w.metadata.Series(ctx, id, language)
	return seriesRefreshCandidate(series), err
}

func (w *MetadataRefreshWorker) find(ctx context.Context, resource, source, id, language string) ([]domain.MetadataRefreshCandidate, error) {
	finder, ok := w.metadata.provider.(MetadataRefreshFinder)
	if !ok {
		return nil, nil
	}
	value, err := finder.FindByExternalID(ctx, source, id, language)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(err, domain.ErrNotFound) {
			return nil, nil
		}
		return nil, domain.ErrMetadataUnavailable
	}
	if value.ExternalSource != source || value.ExternalID != id || len(value.Movies) > domain.MaxExternalIDResults || len(value.Series) > domain.MaxExternalIDResults {
		return nil, domain.ErrMetadataUnavailable
	}
	var candidates []domain.MetadataRefreshCandidate
	if resource == "movie" {
		for _, movie := range value.Movies {
			candidates = append(candidates, movieRefreshCandidate(movie))
		}
	} else {
		for _, series := range value.Series {
			candidates = append(candidates, seriesRefreshCandidate(series))
		}
	}
	return candidates, nil
}

func (w *MetadataRefreshWorker) search(ctx context.Context, resource, title string, year int, language string) ([]domain.MetadataRefreshCandidate, error) {
	input := domain.MetadataSearchInput{Query: title, Year: year, Language: language}
	var candidates []domain.MetadataRefreshCandidate
	if resource == "movie" {
		matches, err := w.metadata.SearchMovies(ctx, input)
		if err != nil {
			return nil, err
		}
		for _, match := range matches.Candidates {
			candidates = append(candidates, movieRefreshCandidate(match.Movie))
		}
		return candidates, nil
	}
	matches, err := w.metadata.SearchSeries(ctx, input)
	if err != nil {
		return nil, err
	}
	for _, match := range matches.Candidates {
		candidates = append(candidates, seriesRefreshCandidate(match.Series))
	}
	return candidates, nil
}

func movieRefreshCandidate(v domain.MovieCandidate) domain.MetadataRefreshCandidate {
	return domain.MetadataRefreshCandidate{ProviderID: v.ProviderID, Title: v.Title, OriginalTitle: v.OriginalTitle, Date: v.ReleaseDate}
}

func seriesRefreshCandidate(v domain.SeriesCandidate) domain.MetadataRefreshCandidate {
	return domain.MetadataRefreshCandidate{ProviderID: v.ProviderID, Title: v.Title, OriginalTitle: v.OriginalTitle, Date: v.FirstAirDate}
}
