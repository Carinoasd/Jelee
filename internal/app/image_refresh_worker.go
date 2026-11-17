package app

import (
	"context"
	"errors"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// ImageRefreshExecutionRepository is the durable side of an image_refresh
// run. Every method is fenced by the job lease.
type ImageRefreshExecutionRepository interface {
	// NextImageRefreshBatch returns up to limit remote rows of the job's
	// library without content, neither locked nor manual, after the job's
	// cursor in ascending ID order. It answers context.Canceled once
	// cancellation was requested.
	NextImageRefreshBatch(ctx context.Context, lease domain.JobLease, limit int) ([]domain.ImageRefreshCandidate, error)
	// RecordImageRefresh applies results in one transaction: content goes
	// into a row only while it is still an unlocked, non-manual remote row
	// of the same URL without content, a failure is remembered with its
	// retry time, and the cursor moves to the last result with the
	// counters.
	RecordImageRefresh(ctx context.Context, lease domain.JobLease, results []domain.ImageRefreshResult) error
	FinishImageRefresh(ctx context.Context, lease domain.JobLease, state, code string) error
}

// ItemImageFetcher downloads one remote image into the persistent image
// store. A failure that concerns only this image is an
// *domain.ImageFetchFailure; cancellation is the context error; anything else
// stops the run.
type ItemImageFetcher interface {
	FetchItemImage(ctx context.Context, url string) (domain.ItemImageContent, error)
}

// ImageRefreshWorker runs one image_refresh job. It fetches one row at a time
// through the governed fetcher (address policy, per-host spacing, retries and
// size limits live there) and checkpoints every fetch, so a cancelled or
// resumed job never fetches a recorded row again.
type ImageRefreshWorker struct {
	fetcher   ItemImageFetcher
	repo      ImageRefreshExecutionRepository
	dbTimeout time.Duration
}

// NewImageRefreshWorker accepts a nil fetcher: without remote fetching or an
// image store every job it runs fails with domain.ErrImageUnavailable, so a
// job queued under another configuration ends instead of waiting forever.
// dbTimeout bounds each repository call; zero uses two seconds.
func NewImageRefreshWorker(fetcher ItemImageFetcher, repository ImageRefreshExecutionRepository, dbTimeout time.Duration) (*ImageRefreshWorker, error) {
	if repository == nil || dbTimeout < 0 {
		return nil, domain.ErrInvalid
	}
	if dbTimeout == 0 {
		dbTimeout = 2 * time.Second
	}
	return &ImageRefreshWorker{fetcher: fetcher, repo: repository, dbTimeout: dbTimeout}, nil
}

func (w *ImageRefreshWorker) db(ctx context.Context, call func(context.Context) error) error {
	c, cancel := context.WithTimeout(ctx, w.dbTimeout)
	defer cancel()
	return call(c)
}

// Run fetches the library's candidates after the stored cursor until none
// remain. A failed fetch is recorded for its row and never ends the run;
// storage, lease and context errors do, keeping every recorded row.
func (w *ImageRefreshWorker) Run(ctx context.Context, lease domain.JobLease) error {
	if ctx == nil || lease.Job.Kind != domain.JobImageRefresh {
		return domain.ErrInvalid
	}
	if w.fetcher == nil {
		return domain.ErrImageUnavailable
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var batch []domain.ImageRefreshCandidate
		if err := w.db(ctx, func(c context.Context) (err error) {
			batch, err = w.repo.NextImageRefreshBatch(c, lease, domain.ImageRefreshBatchSize)
			return err
		}); err != nil {
			return err
		}
		if len(batch) == 0 {
			return nil
		}
		var pending []domain.ImageRefreshResult
		// Results are recorded even when the run stops, so a cancelled job
		// keeps exact counts and a resume never fetches a recorded row again.
		flush := func() error {
			if len(pending) == 0 {
				return nil
			}
			results := pending
			pending = nil
			return w.db(context.WithoutCancel(ctx), func(c context.Context) error {
				return w.repo.RecordImageRefresh(c, lease, results)
			})
		}
		var stop error
		for _, candidate := range batch {
			if stop = ctx.Err(); stop != nil {
				break
			}
			if candidate.Deferred {
				pending = append(pending, domain.ImageRefreshResult{Candidate: candidate, Deferred: true})
				continue
			}
			result, err := w.fetch(ctx, candidate)
			if err != nil {
				stop = err
				break
			}
			pending = append(pending, result)
			if err := flush(); err != nil {
				return err
			}
		}
		if err := flush(); err != nil {
			return err
		}
		if stop != nil {
			return stop
		}
	}
}

// fetch classifies one fetch. The error is non-nil only when the run must
// stop: cancellation, an expired runtime or an error that is not about this
// image.
func (w *ImageRefreshWorker) fetch(ctx context.Context, candidate domain.ImageRefreshCandidate) (domain.ImageRefreshResult, error) {
	content, err := w.fetcher.FetchItemImage(ctx, candidate.URL)
	if err == nil {
		return domain.ImageRefreshResult{Candidate: candidate, Content: &content}, nil
	}
	if ctx.Err() != nil {
		return domain.ImageRefreshResult{}, ctx.Err()
	}
	var failure *domain.ImageFetchFailure
	if !errors.As(err, &failure) || !domain.ValidImageFetchReason(failure.Reason) {
		return domain.ImageRefreshResult{}, err
	}
	failures := min(candidate.Failures+1, domain.ImageFetchMaxFailures)
	return domain.ImageRefreshResult{Candidate: candidate, Failure: failure, Failures: failures,
		RetryDelay: domain.ImageFetchRetryDelay(failures, failure.Permanent)}, nil
}
