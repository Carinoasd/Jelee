package app

import (
	"context"
	"slices"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// MetadataRefreshJobRepository admits and reports metadata_refresh jobs,
// stores the per-library settings and the reviews (G13.6, G14.4).
type MetadataRefreshJobRepository interface {
	SubmitMetadataRefreshJob(ctx context.Context, actor domain.Actor, library, key, priority string, policy domain.JobPolicy) (domain.Job, bool, error)
	GetMetadataRefreshState(ctx context.Context, actor domain.Actor, job string) (domain.MetadataRefreshJobState, error)
	GetMetadataRefreshSettings(ctx context.Context, actor domain.Actor, library string) (domain.MetadataRefreshSettings, error)
	PutMetadataRefreshSettings(ctx context.Context, actor domain.Actor, settings domain.MetadataRefreshSettings) (domain.MetadataRefreshSettings, error)
	// ListMetadataRefreshReviews pages the reviews of a library in item
	// order; state "" lists both states.
	ListMetadataRefreshReviews(ctx context.Context, actor domain.Actor, library, state, cursor string, limit int) ([]domain.MetadataRefreshReview, error)
	GetMetadataRefreshReview(ctx context.Context, actor domain.Actor, item string) (domain.MetadataRefreshReview, error)
	// RejectMetadataRefreshReview marks a pending review rejected;
	// a rejected one is domain.ErrConflict.
	RejectMetadataRefreshReview(ctx context.Context, actor domain.Actor, item string) (domain.MetadataRefreshReview, error)
	// ResolveMetadataRefreshReview removes the review of an item whose
	// chosen candidate was applied and audits the decision.
	ResolveMetadataRefreshReview(ctx context.Context, actor domain.Actor, item string, provider int32, revision int64) error
}

// NewJobsWithMetadataRefresh lets base run metadata refreshes through
// repository. metadata applies accepted reviews; without a provider a
// submission or an accept is domain.ErrMetadataUnavailable while settings,
// progress and reviews stay readable.
func NewJobsWithMetadataRefresh(base *Jobs, repository MetadataRefreshJobRepository, metadata *Metadata) (*Jobs, error) {
	if base == nil || repository == nil {
		return nil, domain.ErrInvalid
	}
	value := *base
	value.metadataRefreshJobs, value.metadataRefresh = repository, metadata
	return &value, nil
}

func (j *Jobs) metadataRefreshAdmitted() bool {
	return j.metadataRefresh != nil && j.metadataRefresh.HasProvider() && j.metadataRefresh.HasItemMetadata()
}

func (j *Jobs) metadataRefreshRepository(ctx context.Context) (MetadataRefreshJobRepository, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if j.metadataRefreshJobs == nil {
		return nil, domain.ErrMetadataUnavailable
	}
	return j.metadataRefreshJobs, nil
}

// SubmitMetadataRefresh queues a metadata refresh of one library.
func (j *Jobs) SubmitMetadataRefresh(ctx context.Context, actor domain.Actor, library, key, priority string) (domain.Job, bool, error) {
	if ctx == nil || j == nil || !validTarget(actor, library) || !validKey(key) || (priority != domain.JobPriorityManual && priority != domain.JobPriorityBackground) {
		return domain.Job{}, false, domain.ErrInvalid
	}
	repository, err := j.metadataRefreshRepository(ctx)
	if err != nil {
		return domain.Job{}, false, err
	}
	if !j.metadataRefreshAdmitted() {
		return domain.Job{}, false, domain.ErrMetadataUnavailable
	}
	return repository.SubmitMetadataRefreshJob(ctx, actor, library, key, priority, j.policy)
}

// MetadataRefreshState returns the progress of a metadata_refresh job; any
// other job kind is domain.ErrNotFound.
func (j *Jobs) MetadataRefreshState(ctx context.Context, actor domain.Actor, job string) (domain.MetadataRefreshJobState, error) {
	if ctx == nil || j == nil || !validTarget(actor, job) {
		return domain.MetadataRefreshJobState{}, domain.ErrInvalid
	}
	repository, err := j.metadataRefreshRepository(ctx)
	if err != nil {
		return domain.MetadataRefreshJobState{}, err
	}
	return repository.GetMetadataRefreshState(ctx, actor, job)
}

// MetadataRefreshSettings reads a library's settings; a library nobody
// configured has the defaults.
func (j *Jobs) MetadataRefreshSettings(ctx context.Context, actor domain.Actor, library string) (domain.MetadataRefreshSettings, error) {
	if ctx == nil || j == nil || !validTarget(actor, library) {
		return domain.MetadataRefreshSettings{}, domain.ErrInvalid
	}
	repository, err := j.metadataRefreshRepository(ctx)
	if err != nil {
		return domain.MetadataRefreshSettings{}, err
	}
	return repository.GetMetadataRefreshSettings(ctx, actor, library)
}

// PutMetadataRefreshSettings replaces a library's settings.
func (j *Jobs) PutMetadataRefreshSettings(ctx context.Context, actor domain.Actor, settings domain.MetadataRefreshSettings) (domain.MetadataRefreshSettings, error) {
	if ctx == nil || j == nil || !validTarget(actor, settings.LibraryID) || !domain.ValidMetadataRefreshSettings(settings) {
		return domain.MetadataRefreshSettings{}, domain.ErrInvalid
	}
	repository, err := j.metadataRefreshRepository(ctx)
	if err != nil {
		return domain.MetadataRefreshSettings{}, err
	}
	settings.Sources = slices.Clone(settings.Sources)
	return repository.PutMetadataRefreshSettings(ctx, actor, settings)
}

// MetadataRefreshReviews pages a library's reviews.
func (j *Jobs) MetadataRefreshReviews(ctx context.Context, actor domain.Actor, library, state, cursor string, limit int) ([]domain.MetadataRefreshReview, error) {
	if ctx == nil || j == nil || !validTarget(actor, library) || !validJobPage(actor, cursor, limit) ||
		state != "" && state != domain.MetadataReviewPending && state != domain.MetadataReviewRejected {
		return nil, domain.ErrInvalid
	}
	repository, err := j.metadataRefreshRepository(ctx)
	if err != nil {
		return nil, err
	}
	return repository.ListMetadataRefreshReviews(ctx, actor, library, state, cursor, limit)
}

// RejectMetadataRefreshReview refuses every candidate of a pending review;
// automatic refreshes leave the item alone until a person applies a match.
func (j *Jobs) RejectMetadataRefreshReview(ctx context.Context, actor domain.Actor, item string) (domain.MetadataRefreshReview, error) {
	if ctx == nil || j == nil || !validTarget(actor, item) {
		return domain.MetadataRefreshReview{}, domain.ErrInvalid
	}
	repository, err := j.metadataRefreshRepository(ctx)
	if err != nil {
		return domain.MetadataRefreshReview{}, err
	}
	return repository.RejectMetadataRefreshReview(ctx, actor, item)
}

// AcceptMetadataRefreshReview applies one candidate of a pending review
// through the confirmed TMDB apply, so locks, manual values, trusted NFO and
// the revision check hold exactly as for a manual apply, then removes the
// review. A candidate the review does not list is domain.ErrInvalid; a
// rejected review is domain.ErrConflict.
func (j *Jobs) AcceptMetadataRefreshReview(ctx context.Context, actor domain.Actor, item string, input domain.MetadataRefreshAcceptance) (domain.MetadataApplyResult, error) {
	if ctx == nil || j == nil || !validTarget(actor, item) || input.ProviderID <= 0 || input.ExpectedRevision < 1 || input.ExpectedRevision >= domain.ItemMetadataRevisionMax {
		return domain.MetadataApplyResult{}, domain.ErrInvalid
	}
	repository, err := j.metadataRefreshRepository(ctx)
	if err != nil {
		return domain.MetadataApplyResult{}, err
	}
	review, err := repository.GetMetadataRefreshReview(ctx, actor, item)
	if err != nil {
		return domain.MetadataApplyResult{}, err
	}
	if review.State != domain.MetadataReviewPending {
		return domain.MetadataApplyResult{}, domain.ErrConflict
	}
	if !slices.ContainsFunc(review.Candidates, func(c domain.MetadataRefreshCandidate) bool { return c.ProviderID == input.ProviderID }) {
		return domain.MetadataApplyResult{}, domain.ErrInvalid
	}
	if !j.metadataRefreshAdmitted() {
		return domain.MetadataApplyResult{}, domain.ErrMetadataUnavailable
	}
	result, err := j.metadataRefresh.ApplyTMDB(ctx, actor, item, domain.TMDBMetadataApplyInput{Resource: review.Resource, ProviderID: input.ProviderID, ExpectedRevision: input.ExpectedRevision, Confirmed: true})
	if err != nil {
		return domain.MetadataApplyResult{}, err
	}
	if err = repository.ResolveMetadataRefreshReview(context.WithoutCancel(ctx), actor, item, input.ProviderID, result.Metadata.Revision); err != nil {
		return domain.MetadataApplyResult{}, err
	}
	return result, nil
}
