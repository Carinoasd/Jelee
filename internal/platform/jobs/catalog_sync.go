package jobs

import (
	"bytes"
	"context"
	"errors"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// CatalogSyncOptions enables claiming catalog_sync jobs. Every batch is one
// fenced transaction that also stores its checkpoint, so a replacement lease
// continues where the last committed batch stopped.
//
// With Sidecars and Inspector set, the job ends with a pass over the
// library's external tracks that still lack a fingerprint: each file gets a
// bounded read (edge fingerprint, text subtitle charset) outside any
// transaction, and the results are stored in fenced pages.
//
// With EmbeddedCovers set (G40.4, off by default), the job then copies the
// attached pictures of probed media files into the image store.
type CatalogSyncOptions struct {
	Repository     app.CatalogSyncExecutionRepository
	Sidecars       app.SidecarInspectionRepository
	Inspector      app.SidecarInspector
	EmbeddedCovers *EmbeddedCoverOptions
}

// EmbeddedCoverOptions is trusted local wiring of the embedded cover pass.
// FileTimeout bounds one extraction including its sandboxed child; zero uses
// 30 seconds. Available reports whether the sandboxed tool is still usable.
type EmbeddedCoverOptions struct {
	Repository  app.EmbeddedCoverRepository
	Extractor   app.EmbeddedCoverExtractor
	Store       app.EmbeddedCoverStore
	FileTimeout time.Duration
	Available   func() bool
}

func (r *Runner) executeCatalogSync(ctx context.Context, lease domain.JobLease) (error, bool) {
	options := r.options.CatalogSync
	if options == nil {
		return domain.ErrScanUnavailable, false
	}
	for {
		if err := ctx.Err(); err != nil {
			return err, false
		}
		var done bool
		err := r.ignoreDB(ctx, func(c context.Context) error {
			var err error
			done, err = options.Repository.AdvanceCatalogSync(c, lease)
			return err
		})
		if err != nil {
			return err, true
		}
		if done {
			if err, storage := r.inspectSidecars(ctx, lease); err != nil {
				return err, storage
			}
			storage, err := r.extractEmbeddedCovers(ctx, lease)
			return err, storage
		}
	}
}

// inspectSidecars walks the uninspected tracks once, by ID. A file that
// cannot be read now (gone, replaced, changed) is skipped and stays
// uninspected; the next scan replaces or removes its row.
func (r *Runner) inspectSidecars(ctx context.Context, lease domain.JobLease) (error, bool) {
	options := r.options.CatalogSync
	if options.Sidecars == nil || options.Inspector == nil {
		return nil, false
	}
	after := ""
	for {
		if err := ctx.Err(); err != nil {
			return err, false
		}
		var page []domain.SidecarInspectionTarget
		err := r.ignoreDB(ctx, func(c context.Context) error {
			var err error
			page, err = options.Sidecars.NextSidecarInspections(c, lease, after, domain.SidecarInspectionBatch)
			return err
		})
		if err != nil {
			return err, true
		}
		if len(page) == 0 {
			return nil, false
		}
		results := make([]domain.SidecarInspection, 0, len(page))
		for _, target := range page {
			value, err := options.Inspector.InspectSidecar(ctx, target)
			if err := ctx.Err(); err != nil {
				return err, false
			}
			if err == nil {
				results = append(results, value)
			}
		}
		if len(results) > 0 {
			err = r.ignoreDB(ctx, func(c context.Context) error {
				_, err := options.Sidecars.RecordSidecarInspections(c, lease, results)
				return err
			})
			if err != nil {
				return err, true
			}
		}
		if len(page) < domain.SidecarInspectionBatch {
			return nil, false
		}
		after = page[len(page)-1].ID
	}
}

// extractEmbeddedCovers walks the candidates once, by item ID, and starts at
// most domain.EmbeddedCoverMaxPerJob extractions. Each extraction runs
// outside any transaction under the IO work budget; only its result is
// written, in its own fenced transaction. A transient failure (a changed
// file, a timeout, a busy or unavailable tool) is not remembered, so the next
// catalog sync tries again; it never fails the job. Lease and storage errors
// do.
func (r *Runner) extractEmbeddedCovers(ctx context.Context, lease domain.JobLease) (storage bool, err error) {
	options := r.options.CatalogSync.EmbeddedCovers
	if options == nil || options.Repository == nil || options.Extractor == nil || options.Store == nil {
		return false, nil
	}
	timeout := options.FileTimeout
	if timeout <= 0 || timeout > 5*time.Minute {
		timeout = 30 * time.Second
	}
	after, started := "", 0
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if options.Available != nil && !options.Available() {
			return false, nil
		}
		var page domain.EmbeddedCoverPage
		err = r.ignoreDB(ctx, func(c context.Context) error {
			var err error
			page, err = options.Repository.NextEmbeddedCoverCandidates(c, lease, after, domain.EmbeddedCoverBatch)
			return err
		})
		if err != nil {
			return true, err
		}
		for _, candidate := range page.Candidates {
			if started >= domain.EmbeddedCoverMaxPerJob {
				return false, nil
			}
			started++
			result, next, err := r.embeddedCover(ctx, options, candidate, timeout)
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
				_, err := options.Repository.RecordEmbeddedCover(c, lease, result)
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

type embeddedCoverStep int

const (
	embeddedCoverRecord embeddedCoverStep = iota
	embeddedCoverSkip
	embeddedCoverStop
)

// embeddedCover runs one extraction and turns it into a recordable result.
// Skip is an outcome that must not be remembered; stop ends the pass when the
// sandboxed tool is unavailable. err is set only when the job is cancelled.
func (r *Runner) embeddedCover(ctx context.Context, options *EmbeddedCoverOptions, candidate domain.EmbeddedCoverCandidate, timeout time.Duration) (domain.EmbeddedCoverResult, embeddedCoverStep, error) {
	release, err := r.acquireWork(ctx, app.WorkIO)
	if err != nil {
		return domain.EmbeddedCoverResult{}, embeddedCoverStop, ctx.Err()
	}
	defer release()
	fileCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cover, err := options.Extractor.ExtractCover(fileCtx, candidate)
	if ctx.Err() != nil {
		return domain.EmbeddedCoverResult{}, embeddedCoverStop, ctx.Err()
	}
	result := domain.EmbeddedCoverResult{Candidate: candidate}
	switch {
	case err == nil:
	case errors.Is(err, domain.ErrEmbeddedCoverAbsent):
		result.Outcome = domain.EmbeddedCoverAbsent
		return result, embeddedCoverRecord, nil
	case errors.Is(err, domain.ErrEmbeddedCoverInvalid):
		result.Outcome = domain.EmbeddedCoverInvalid
		return result, embeddedCoverRecord, nil
	case errors.Is(err, domain.ErrEmbeddedCoverTooLarge):
		result.Outcome = domain.EmbeddedCoverTooLarge
		return result, embeddedCoverRecord, nil
	case errors.Is(err, domain.ErrProbeRuntimeUnavailable):
		return result, embeddedCoverStop, nil
	default:
		return result, embeddedCoverSkip, nil
	}
	if !domain.ValidEmbeddedCover(cover) {
		result.Outcome = domain.EmbeddedCoverInvalid
		return result, embeddedCoverRecord, nil
	}
	digest, size, err := options.Store.PutOriginal(fileCtx, bytes.NewReader(cover.Data), domain.EmbeddedCoverMaxBytes)
	if ctx.Err() != nil {
		return domain.EmbeddedCoverResult{}, embeddedCoverStop, ctx.Err()
	}
	if err != nil || digest != cover.SHA256 || size != int64(len(cover.Data)) {
		return result, embeddedCoverSkip, nil
	}
	result.Outcome = domain.EmbeddedCoverStored
	result.Content = &domain.ItemImageContent{SHA256: digest[:], Width: cover.Width, Height: cover.Height, Format: cover.Format, Bytes: size, FetchedAt: time.Now().UTC().Truncate(time.Microsecond)}
	return result, embeddedCoverRecord, nil
}
