package jobs

import (
	"context"
	"errors"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

type FamilyIgnoreOptions struct {
	Repository app.FamilyIgnoreExecutionRepository
	Scanner    app.FamilyIgnoreScanner
	Available  func() bool
}

func (r *Runner) executeFamilyIgnore(ctx context.Context, l domain.JobLease, request domain.IgnoreRequest) (error, bool) {
	if !r.familyIgnoreAvailable() {
		return domain.ErrIgnoreUnavailable, false
	}
	repo := r.options.FamilyIgnore.Repository
	scanner := r.options.FamilyIgnore.Scanner
	var progress domain.IgnoreExecutionProgress
	err := r.ignoreDB(ctx, func(c context.Context) error {
		var e error
		progress, e = repo.ReadFamilyIgnoreProgress(c, l)
		return e
	})
	if err != nil {
		return err, true
	}
	if !progress.ComparisonStarted {
		if e, storage := r.executeFamilyInventory(ctx, l, request.Intent); e != nil {
			return e, storage
		}
		if err = r.ignoreDB(ctx, func(c context.Context) error { return repo.BeginFamilyIgnoreBaselineComparison(c, l) }); err != nil {
			return err, true
		}
	}
	for {
		if err = ctx.Err(); err != nil {
			return err, false
		}
		var page domain.IgnoreBaselinePage
		err = r.ignoreDB(ctx, func(c context.Context) error {
			var e error
			page, e = repo.NextFamilyIgnoreBaselinePage(c, l)
			return e
		})
		if err != nil {
			return err, true
		}
		if page.Complete {
			break
		}
		if len(page.Unseen) > 128 {
			return domain.ErrScanLimit, true
		}
		evaluations, evaluationErr, storage := r.evaluateFamilyBaselinePage(ctx, l, page.Unseen, request.Intent)
		if evaluationErr != nil {
			return evaluationErr, storage
		}
		if err = r.ignoreDB(ctx, func(c context.Context) error {
			return repo.CommitFamilyIgnoreBaselinePage(c, l, page.Token, evaluations)
		}); err != nil {
			return err, true
		}
	}
	if e, storage := r.executeStages(ctx, l, true); e != nil {
		return e, storage
	}
	err = r.ignoreDB(ctx, func(c context.Context) error {
		var e error
		progress, e = repo.ReadFamilyIgnoreProgress(c, l)
		return e
	})
	if err != nil {
		return err, true
	}
	if progress.Unknown {
		return nil, false
	}
	if err = r.prepareInventoryPublication(ctx, l); err != nil {
		return err, true
	}
	if err = r.ignoreDB(ctx, func(c context.Context) error { return repo.BeginFamilyIgnoreVerification(c, l) }); err != nil {
		return err, true
	}
	if e, storage := verifyFamilyStream(ctx, r, l, 128,
		func(c context.Context) (domain.IgnoreVerificationToken, []domain.IgnoreDirectoryProof, bool, error) {
			p, e := repo.NextFamilyIgnoreVerificationPage(c, l)
			return p.Token, p.Proofs, p.Complete, e
		},
		func(p domain.IgnoreDirectoryProof) string {
			if domain.ValidateIgnoreDirectoryProof(p) != nil {
				return ""
			}
			return p.RootID
		},
		scanner.ReobserveIgnoreProof,
		func(c context.Context, t domain.IgnoreVerificationToken, p []domain.IgnoreDirectoryProof) error {
			return repo.CommitFamilyIgnoreVerificationPage(c, l, t, p)
		}); e != nil {
		return e, storage
	}
	if e, storage := verifyFamilyStream(ctx, r, l, 16,
		func(c context.Context) (domain.LegacyIgnoreVerificationToken, []domain.LegacyIgnoreObservation, bool, error) {
			p, e := repo.NextLegacyIgnoreVerificationPage(c, l)
			return p.Token, p.Observations, p.Complete, e
		},
		func(p domain.LegacyIgnoreObservation) string {
			if domain.ValidateLegacyIgnoreObservation(p) != nil {
				return ""
			}
			return p.Proofs[0].RootID
		},
		scanner.ReobserveLegacyIgnore,
		func(c context.Context, t domain.LegacyIgnoreVerificationToken, p []domain.LegacyIgnoreObservation) error {
			return repo.CommitLegacyIgnoreVerificationPage(c, l, t, p)
		}); e != nil {
		return e, storage
	}
	if e, storage := verifyFamilyStream(ctx, r, l, 16,
		func(c context.Context) (domain.LegacyIgnoreBaselineVerificationToken, []domain.LegacyIgnoreBaselineObservation, bool, error) {
			p, e := repo.NextLegacyIgnoreBaselineVerificationPage(c, l)
			return p.Token, p.Observations, p.Complete, e
		},
		func(p domain.LegacyIgnoreBaselineObservation) string {
			if domain.ValidateLegacyIgnoreBaselineObservation(p) != nil {
				return ""
			}
			return p.Source.Proofs[0].RootID
		},
		scanner.ReobserveLegacyIgnoreBaseline,
		func(c context.Context, t domain.LegacyIgnoreBaselineVerificationToken, p []domain.LegacyIgnoreBaselineObservation) error {
			return repo.CommitLegacyIgnoreBaselineVerificationPage(c, l, t, p)
		}); e != nil {
		return e, storage
	}
	err = r.ignoreDB(ctx, func(c context.Context) error { return repo.SealFamilyIgnoreVerification(c, l) })
	return err, err != nil
}

// Each stream retains its own token type. The helper shares bounded iteration
// and storage error handling, while the repository enforces exact proof replay.
func verifyFamilyStream[T any, P any](ctx context.Context, r *Runner, l domain.JobLease, limit int, next func(context.Context) (P, []T, bool, error), rootID func(T) string, observe func(context.Context, string, T) (T, error), commit func(context.Context, P, []T) error) (error, bool) {
	for {
		if err := ctx.Err(); err != nil {
			return err, false
		}
		var token P
		var retained []T
		var complete bool
		err := r.ignoreDB(ctx, func(c context.Context) error { var e error; token, retained, complete, e = next(c); return e })
		if err != nil {
			return err, true
		}
		if complete {
			return nil, false
		}
		if len(retained) > limit {
			return domain.ErrScanLimit, true
		}
		observed := make([]T, 0, len(retained))
		for _, p := range retained {
			id := rootID(p)
			if !domain.ValidID(id) {
				return domain.ErrInventoryInvalidated, false
			}
			var root string
			err = r.ignoreDB(ctx, func(c context.Context) error {
				var e error
				root, e = r.options.FamilyIgnore.Repository.ReadFamilyIgnoreRoot(c, l, id)
				return e
			})
			if err != nil {
				return err, true
			}
			value, e := observe(ctx, root, p)
			if e != nil {
				return e, false
			}
			observed = append(observed, value)
		}
		if err = r.ignoreDB(ctx, func(c context.Context) error { return commit(c, token, observed) }); err != nil {
			return err, true
		}
	}
}

func (r *Runner) executeFamilyInventory(ctx context.Context, l domain.JobLease, intent domain.IgnoreIntent) (error, bool) {
	if !r.familyIgnoreAvailable() {
		return domain.ErrIgnoreUnavailable, false
	}
	repo := r.options.FamilyIgnore.Repository
	for {
		if err := ctx.Err(); err != nil {
			return err, false
		}
		var d domain.ScanDirectory
		err := r.ignoreDB(ctx, func(c context.Context) error { var e error; d, e = repo.NextFamilyIgnoreScanDirectory(c, l); return e })
		if errors.Is(err, domain.ErrNotFound) {
			return nil, false
		}
		if err != nil {
			return err, true
		}
		done, storage := false, false
		var callbackErr error
		err = r.scanFamilyIgnoreDirectory(ctx, d, intent, func(b domain.FamilyIgnoreScanBatch) error {
			if callbackErr != nil {
				return callbackErr
			}
			if done {
				callbackErr = domain.ErrScanIO
				return callbackErr
			}
			if len(b.Inventory.Entries)+len(b.Inventory.Directories)+len(b.Excluded) > 128 || b.Inventory.Skipped < 0 {
				callbackErr = domain.ErrScanLimit
				return callbackErr
			}
			callbackErr = r.ignoreDB(ctx, func(c context.Context) error { return repo.SaveFamilyIgnoreScanBatch(c, l, d, b) })
			storage = callbackErr != nil
			done = callbackErr == nil && b.Inventory.Done
			return callbackErr
		})
		if callbackErr != nil {
			return callbackErr, storage
		}
		if err != nil {
			return err, false
		}
		if !done {
			return domain.ErrScanIO, false
		}
	}
}

func (r *Runner) familyIgnoreAvailable() bool {
	o := r.options.FamilyIgnore
	return o != nil && (o.Available == nil || o.Available())
}

func (r *Runner) evaluateFamilyBaselinePage(ctx context.Context, l domain.JobLease, candidates []domain.IgnoreBaselineCandidate, intent domain.IgnoreIntent) ([]domain.FamilyBaselineEvaluation, error, bool) {
	result := make([]domain.FamilyBaselineEvaluation, 0, len(candidates))
	scanner := r.options.FamilyIgnore.Scanner
	for first := 0; first < len(candidates); {
		last := first + 1
		for last < len(candidates) && candidates[last].RootID == candidates[first].RootID {
			last++
		}
		group := candidates[first:last]
		var root string
		err := r.ignoreDB(ctx, func(c context.Context) error {
			var e error
			root, e = r.options.FamilyIgnore.Repository.ReadFamilyIgnoreRoot(c, l, group[0].RootID)
			return e
		})
		if err != nil {
			return nil, err, true
		}
		var values []domain.FamilyBaselineEvaluation
		if batch, ok := scanner.(app.FamilyIgnoreBaselineBatchScanner); ok {
			values, err = batch.EvaluateFamilyIgnoreBaselineBatch(ctx, root, group, intent)
			if err != nil && !errors.Is(err, domain.ErrIgnoreUnavailable) {
				return nil, err, false
			}
			if err == nil && len(values) != len(group) {
				return nil, domain.ErrInventoryInvalidated, false
			}
		}
		if values == nil || err != nil {
			values = make([]domain.FamilyBaselineEvaluation, 0, len(group))
			for _, candidate := range group {
				value, e := scanner.EvaluateFamilyIgnoreBaseline(ctx, root, candidate, intent)
				if e != nil {
					if ctx.Err() != nil {
						return nil, ctx.Err(), false
					}
					if !errors.Is(e, domain.ErrIgnoreUnavailable) {
						return nil, e, false
					}
					value = domain.FamilyBaselineEvaluation{Decision: domain.FamilyIgnoreBaselineDecision{RootID: candidate.RootID, Path: candidate.Path, Outcome: domain.IgnoreBaselineUnknown, Reason: domain.IgnoreUnknownSource}}
				}
				values = append(values, value)
			}
		}
		for i, value := range values {
			if value.Decision.RootID != group[i].RootID || value.Decision.Path != group[i].Path || domain.ValidateFamilyBaselineEvaluation(value) != nil {
				return nil, domain.ErrInventoryInvalidated, false
			}
		}
		result = append(result, values...)
		first = last
	}
	return result, nil, false
}
