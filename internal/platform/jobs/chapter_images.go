package jobs

import (
	"bytes"
	"context"
	"errors"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// ChapterImageOptions is trusted local wiring of the chapter image pass
// (G40.1). FileTimeout bounds one file's reads including their sandboxed
// children; zero uses 60 seconds. Available reports whether the sandboxed
// tool is still usable.
type ChapterImageOptions struct {
	Repository  app.ChapterImageRepository
	Extractor   app.ChapterImageExtractor
	Store       app.EmbeddedCoverStore
	FileTimeout time.Duration
	Available   func() bool
}

// extractChapterImages walks the candidates once, by item ID, and reads at
// most domain.ChapterImageMaxPerJob files. Each read runs outside any
// transaction under the IO work budget, one file at a time; only its result
// is written, in its own fenced transaction. A transient failure (a changed
// file, a timeout, a busy or unavailable tool, a failed store write) is not
// remembered, so the next catalog sync tries again; it never fails the job.
// Lease and storage errors do.
func (r *Runner) extractChapterImages(ctx context.Context, lease domain.JobLease) (storage bool, err error) {
	options := r.options.CatalogSync.ChapterImages
	if options == nil || options.Repository == nil || options.Extractor == nil || options.Store == nil {
		return false, nil
	}
	timeout := options.FileTimeout
	if timeout <= 0 || timeout > 5*time.Minute {
		timeout = 60 * time.Second
	}
	after, started := "", 0
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if options.Available != nil && !options.Available() {
			return false, nil
		}
		var page domain.ChapterImagePage
		err = r.ignoreDB(ctx, func(c context.Context) error {
			var err error
			page, err = options.Repository.NextChapterImageCandidates(c, lease, after, domain.ChapterImageBatch)
			return err
		})
		if err != nil {
			return true, err
		}
		for _, candidate := range page.Candidates {
			if started >= domain.ChapterImageMaxPerJob {
				return false, nil
			}
			started++
			result, next, err := r.chapterImages(ctx, options, candidate, timeout)
			if err != nil {
				return false, err
			}
			if next == embeddedCoverStop {
				return false, nil
			}
			if next == embeddedCoverSkip {
				continue
			}
			err = r.ignoreDB(ctx, func(c context.Context) error {
				_, err := options.Repository.RecordChapterImages(c, lease, result)
				return err
			})
			if err != nil {
				return true, err
			}
		}
		if page.Next == "" {
			return false, nil
		}
		after = page.Next
	}
}

// chapterImages reads one file and turns it into a recordable result. Skip is
// an outcome that must not be remembered; stop ends the pass when the
// sandboxed tool is unavailable. err is set only when the job is cancelled.
func (r *Runner) chapterImages(ctx context.Context, options *ChapterImageOptions, candidate domain.ChapterImageCandidate, timeout time.Duration) (domain.ChapterImageResult, embeddedCoverStep, error) {
	release, err := r.acquireWork(ctx, app.WorkIO)
	if err != nil {
		return domain.ChapterImageResult{}, embeddedCoverStop, ctx.Err()
	}
	defer release()
	fileCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	images, err := options.Extractor.ExtractChapterImages(fileCtx, candidate)
	if ctx.Err() != nil {
		return domain.ChapterImageResult{}, embeddedCoverStop, ctx.Err()
	}
	result := domain.ChapterImageResult{Candidate: candidate}
	switch {
	case err == nil:
	case errors.Is(err, domain.ErrChapterImagesAbsent):
		result.Outcome = domain.ChapterImageAbsent
		return result, embeddedCoverRecord, nil
	case errors.Is(err, domain.ErrChapterImagesInvalid):
		result.Outcome = domain.ChapterImageInvalid
		return result, embeddedCoverRecord, nil
	case errors.Is(err, domain.ErrChapterImagesTooLarge):
		result.Outcome = domain.ChapterImageTooLarge
		return result, embeddedCoverRecord, nil
	case errors.Is(err, domain.ErrProbeRuntimeUnavailable):
		return result, embeddedCoverStop, nil
	default:
		return result, embeddedCoverSkip, nil
	}
	if !domain.ValidChapterImages(images) {
		result.Outcome = domain.ChapterImageInvalid
		return result, embeddedCoverRecord, nil
	}
	fetched := time.Now().UTC().Truncate(time.Microsecond)
	result.Images = make([]domain.ChapterImageContent, 0, len(images))
	for _, image := range images {
		digest, size, err := options.Store.PutOriginal(fileCtx, bytes.NewReader(image.Data), domain.ChapterImageMaxBytes)
		if ctx.Err() != nil {
			return domain.ChapterImageResult{}, embeddedCoverStop, ctx.Err()
		}
		if err != nil || digest != image.SHA256 || size != int64(len(image.Data)) {
			return result, embeddedCoverSkip, nil
		}
		result.Images = append(result.Images, domain.ChapterImageContent{Index: image.Index, Content: domain.ItemImageContent{SHA256: digest[:],
			Width: image.Width, Height: image.Height, Format: image.Format, Bytes: size, FetchedAt: fetched}})
	}
	result.Outcome = domain.ChapterImageStored
	return result, embeddedCoverRecord, nil
}
