package jobs

import (
	"context"
	"errors"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

type IgnoreOptions struct {
	Repository app.IgnoreExecutionRepository
	Scanner    app.IgnoreInventoryScanner
	Observer   app.IgnoreBaselineObserver
}

func (r *Runner) ignoreDB(ctx context.Context, call func(context.Context) error) error {
	dbCtx, cancel := context.WithTimeout(ctx, r.options.DBOperationTimeout)
	defer cancel()
	return call(dbCtx)
}

func (r *Runner) execute(ctx context.Context, l domain.JobLease) (result error, storage bool) {
	defer func() {
		if recover() != nil {
			result, storage = domain.ErrScanIO, false
		}
	}()
	if l.Job.Kind == domain.JobCatalogImport {
		return r.executeCatalogImport(ctx, l)
	}
	repository, ok := r.repository.(app.IgnoreExecutionRepository)
	if !ok {
		return r.executeStages(ctx, l, false)
	}
	var request *domain.IgnoreRequest
	err := r.ignoreDB(ctx, func(c context.Context) error {
		var e error
		request, e = func() (*domain.IgnoreRequest, error) {
			if reader, ok := r.repository.(app.ExecutionIgnoreRequestReader); ok {
				return reader.ReadExecutionIgnoreRequest(c, l)
			}
			return repository.ReadIgnoreRequest(c, l)
		}()
		return e
	})
	if err != nil {
		return err, true
	}
	if request == nil {
		return r.executeStages(ctx, l, false)
	}
	if request.JobID != l.Job.ID || request.LibraryID != l.Job.LibraryID {
		return domain.ErrIgnoreUnavailable, false
	}
	if request.Intent.Mode == domain.IgnoreModeFamily {
		if r.options.FamilyIgnore == nil || domain.ValidateFamilyIgnoreRequest(*request) != nil {
			return domain.ErrIgnoreUnavailable, false
		}
		return r.executeFamilyIgnore(ctx, l, *request)
	}
	if r.options.Ignore == nil || domain.ValidateIgnoreRequest(*request) != nil {
		return domain.ErrIgnoreUnavailable, false
	}
	return r.executeIgnore(ctx, l, *request)
}

func (r *Runner) executeIgnore(ctx context.Context, l domain.JobLease, request domain.IgnoreRequest) (error, bool) {
	repo := r.options.Ignore.Repository
	var progress domain.IgnoreExecutionProgress
	err := r.ignoreDB(ctx, func(c context.Context) error { var e error; progress, e = repo.ReadIgnoreProgress(c, l); return e })
	if err != nil {
		return err, true
	}
	if !progress.ComparisonStarted {
		if err, storage := r.executeIgnoreInventory(ctx, l, request.Intent); err != nil {
			return err, storage
		}
		if err = r.ignoreDB(ctx, func(c context.Context) error { return repo.BeginIgnoreBaselineComparison(c, l) }); err != nil {
			return err, true
		}
	}
	for {
		if err = ctx.Err(); err != nil {
			return err, false
		}
		var page domain.IgnoreBaselinePage
		err = r.ignoreDB(ctx, func(c context.Context) error { var e error; page, e = repo.NextIgnoreBaselinePage(c, l); return e })
		if err != nil {
			return err, true
		}
		if page.Complete {
			break
		}
		if len(page.Unseen) > 128 {
			return domain.ErrScanLimit, true
		}
		decisions := make([]domain.IgnoreBaselineDecision, 0, len(page.Unseen))
		for _, candidate := range page.Unseen {
			var root string
			err = r.ignoreDB(ctx, func(c context.Context) error {
				var e error
				root, e = repo.ReadIgnoreRoot(c, l, candidate.RootID)
				return e
			})
			if err != nil {
				return err, true
			}
			decision, proofs, e := r.options.Ignore.Observer.EvaluateIgnoreBaseline(ctx, root, candidate, request.Intent)
			if e != nil {
				if ctx.Err() != nil {
					return ctx.Err(), false
				}
				if !errors.Is(e, domain.ErrIgnoreUnavailable) {
					return e, false
				}
				decision = domain.IgnoreBaselineDecision{RootID: candidate.RootID, Path: candidate.Path, Outcome: domain.IgnoreBaselineUnknown, Reason: domain.IgnoreUnknownSource}
			} else {
				if len(proofs) == 0 || len(proofs) > 128 {
					return domain.ErrInventoryInvalidated, false
				}
				err = r.ignoreDB(ctx, func(c context.Context) error { return repo.RecordIgnoreProofs(c, l, proofs) })
				if err != nil {
					return err, true
				}
			}
			if decision.RootID != candidate.RootID || decision.Path != candidate.Path || domain.ValidateIgnoreBaselineDecision(decision) != nil {
				return domain.ErrInventoryInvalidated, false
			}
			decisions = append(decisions, decision)
		}
		if err = r.ignoreDB(ctx, func(c context.Context) error { return repo.CommitIgnoreBaselinePage(c, l, page.Token, decisions) }); err != nil {
			return err, true
		}
	}
	if err, storage := r.executeStages(ctx, l, true); err != nil {
		return err, storage
	}
	err = r.ignoreDB(ctx, func(c context.Context) error { var e error; progress, e = repo.ReadIgnoreProgress(c, l); return e })
	if err != nil {
		return err, true
	}
	if progress.Unknown {
		return nil, false
	}
	if err = r.ignoreDB(ctx, func(c context.Context) error { return repo.BeginIgnoreVerification(c, l) }); err != nil {
		return err, true
	}
	for {
		if err = ctx.Err(); err != nil {
			return err, false
		}
		var page domain.IgnoreVerificationPage
		err = r.ignoreDB(ctx, func(c context.Context) error { var e error; page, e = repo.NextIgnoreVerificationPage(c, l); return e })
		if err != nil {
			return err, true
		}
		if page.Complete {
			break
		}
		if len(page.Proofs) > 128 {
			return domain.ErrScanLimit, true
		}
		observed := make([]domain.IgnoreDirectoryProof, 0, len(page.Proofs))
		for _, proof := range page.Proofs {
			var root string
			err = r.ignoreDB(ctx, func(c context.Context) error {
				var e error
				root, e = repo.ReadIgnoreRoot(c, l, proof.RootID)
				return e
			})
			if err != nil {
				return err, true
			}
			p, e := r.options.Ignore.Observer.ReobserveIgnoreProof(ctx, root, proof)
			if e != nil {
				return e, false
			}
			observed = append(observed, p)
		}
		if err = r.ignoreDB(ctx, func(c context.Context) error { return repo.CommitIgnoreVerificationPage(c, l, page.Token, observed) }); err != nil {
			return err, true
		}
	}
	return r.ignoreDB(ctx, func(c context.Context) error { return repo.SealIgnoreVerification(c, l) }), true
}

func (r *Runner) executeIgnoreInventory(ctx context.Context, l domain.JobLease, intent domain.IgnoreIntent) (error, bool) {
	repo := r.options.Ignore.Repository
	for {
		if err := ctx.Err(); err != nil {
			return err, false
		}
		var directory domain.ScanDirectory
		err := r.ignoreDB(ctx, func(c context.Context) error {
			var e error
			directory, e = repo.NextIgnoreScanDirectory(c, l)
			return e
		})
		if errors.Is(err, domain.ErrNotFound) {
			return nil, false
		}
		if err != nil {
			return err, true
		}
		completed, storage := false, false
		var callbackErr error
		err = r.options.Ignore.Scanner.ScanIgnoreDirectory(ctx, directory, intent, func(batch domain.IgnoreScanBatch) error {
			if callbackErr != nil {
				return callbackErr
			}
			if completed {
				callbackErr = domain.ErrScanIO
				return callbackErr
			}
			if len(batch.Inventory.Entries)+len(batch.Inventory.Directories)+len(batch.Excluded) > 128 || batch.Inventory.Skipped < 0 {
				callbackErr = domain.ErrScanLimit
				return callbackErr
			}
			callbackErr = r.ignoreDB(ctx, func(c context.Context) error { return repo.SaveIgnoreScanBatch(c, l, directory, batch) })
			storage = callbackErr != nil
			completed = callbackErr == nil && batch.Inventory.Done
			return callbackErr
		})
		if callbackErr != nil {
			return callbackErr, storage
		}
		if err != nil {
			return err, false
		}
		if !completed {
			return domain.ErrScanIO, false
		}
	}
}

// finishJob keeps failure/cancellation on the existing terminal path and routes
// enabled success through the repository's verification seal and protected merge.
func (r *Runner) finishJob(ctx context.Context, l domain.JobLease, state, code string) error {
	if l.Job.Kind == domain.JobCatalogImport {
		if r.options.CatalogImport == nil {
			return domain.ErrInvalid
		}
		return r.options.CatalogImport.Repository.FinishCatalogImport(ctx, l, state, code)
	}
	if state == domain.JobSucceeded {
		if repo, ok := r.repository.(app.IgnoreExecutionRepository); ok {
			request, err := func() (*domain.IgnoreRequest, error) {
				if reader, ok := r.repository.(app.ExecutionIgnoreRequestReader); ok {
					return reader.ReadExecutionIgnoreRequest(ctx, l)
				}
				return repo.ReadIgnoreRequest(ctx, l)
			}()
			if err != nil {
				return err
			}
			if request != nil {
				if request.Intent.Mode == domain.IgnoreModeFamily {
					if r.options.FamilyIgnore == nil || domain.ValidateFamilyIgnoreRequest(*request) != nil {
						return domain.ErrIgnoreUnavailable
					}
					return r.options.FamilyIgnore.Repository.FinishFamilyIgnoreJob(ctx, l)
				}
				if r.options.Ignore == nil {
					return domain.ErrIgnoreUnavailable
				}
				return repo.FinishIgnoreJob(ctx, l)
			}
		}
	}
	return r.repository.FinishJob(ctx, l, state, code)
}
