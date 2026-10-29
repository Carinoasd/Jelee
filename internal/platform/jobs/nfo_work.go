package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

func (r *Runner) executeNFO(ctx context.Context, l domain.JobLease, q domain.NFORequest) (error, bool) {
	for {
		if ctx.Err() != nil {
			return ctx.Err(), false
		}
		if !r.nfoAvailable() {
			return &nfoAbort{domain.NFOPhaseUnavailable, true}, false
		}
		dbCtx, cancel := context.WithTimeout(ctx, r.options.DBOperationTimeout)
		page, err := r.nfoRepository.NextNFOPage(dbCtx, l, domain.NFOBatchMax)
		cancel()
		if errors.Is(err, domain.ErrNotFound) {
			dbCtx, cancel = context.WithTimeout(ctx, r.options.DBOperationTimeout)
			phase, err := r.nfoRepository.FinishNFOPhase(dbCtx, l)
			cancel()
			if err != nil {
				return nfoRepositoryError(err)
			}
			if !nfoPhaseMatches(phase, q) || domain.ValidateNFOPhase(phase) != nil || phase.State != domain.NFOPhaseDone {
				return &nfoAbort{domain.NFOPhaseIdentityMismatch, false}, false
			}
			return nil, false
		}
		if err != nil {
			return nfoRepositoryError(err)
		}
		if domain.ValidateNFOPageToken(page.Token) != nil || len(page.Entries) < 1 || len(page.Entries) > domain.NFOBatchMax {
			return &nfoAbort{domain.NFOPhaseIdentityMismatch, true}, false
		}
		for i, e := range page.Entries {
			if !domain.ValidID(e.Inventory.ID) || !domain.ValidID(e.Inventory.RootID) || e.Inventory.Kind != "nfo" || e.Source.RootPath == "" || e.Source.RelativePath != e.Inventory.Path || i > 0 && e.Inventory.ID <= page.Entries[i-1].Inventory.ID {
				return &nfoAbort{domain.NFOPhaseIdentityMismatch, true}, false
			}
		}
		retry, err, storage := r.nfoItem(ctx, l, q, page.Token, page.Entries[0])
		if err != nil {
			if storage {
				return nfoRepositoryError(err)
			}
			return err, false
		}
		if retry {
			if !r.wait(ctx, r.options.PollInterval) {
				return ctx.Err(), false
			}
			dbCtx, cancel = context.WithTimeout(ctx, r.options.DBOperationTimeout)
			work, err := r.nfoRepository.LoadNFOWork(dbCtx, l)
			cancel()
			if err != nil {
				return nfoRepositoryError(err)
			}
			if err := validateNFOWork(l, work); err != nil {
				return err, false
			}
			if work.Request == nil || *work.Request != q || work.Phase == nil {
				return &nfoAbort{domain.NFOPhaseIdentityMismatch, true}, false
			}
			if work.Phase.State == domain.NFOPhaseDone {
				return nil, false
			}
			if work.Phase.State != domain.NFOPhaseRunning {
				return &nfoAbort{domain.NFOPhaseIdentityMismatch, true}, false
			}
		}
	}
}

func nfoSourceOutcome(err error) string {
	switch {
	case errors.Is(err, domain.ErrNFOSourceChanged):
		return domain.NFOCompletionChanged
	case errors.Is(err, domain.ErrNFOSourceLimit):
		return domain.NFOCompletionRejected
	case errors.Is(err, domain.ErrNFOInputUnavailable), errors.Is(err, context.DeadlineExceeded):
		return domain.NFOCompletionUnavailable
	default:
		return ""
	}
}
func nfoSourceCompletion(id, kind string) domain.NFOCompletion {
	c := domain.NFOCompletion{Candidate: domain.NFOCandidate{InventoryID: id}, Kind: kind}
	if kind == domain.NFOCompletionRejected {
		c.FailureCode = domain.NFOFailureTooLarge
	}
	return c
}

// The shared gate spans reading, lookup, parsing, final reading and commit.
// Only one entry's immutable source is retained; the directory/page is not a
// collection of up to sixteen full source buffers.
func (r *Runner) nfoItem(ctx context.Context, l domain.JobLease, q domain.NFORequest, token domain.NFOPageToken, e domain.NFOEntry) (bool, error, bool) {
	select {
	case r.nfoGate <- struct{}{}:
		defer func() { <-r.nfoGate }()
	case <-ctx.Done():
		return false, ctx.Err(), false
	}
	if ctx.Err() != nil {
		return false, ctx.Err(), false
	}
	if !r.nfoAvailable() {
		return false, &nfoAbort{domain.NFOPhaseUnavailable, true}, false
	}
	if r.options.NFO.Reader.Identity() != q.Identity {
		return false, &nfoAbort{domain.NFOPhaseIdentityMismatch, true}, false
	}
	// An illegal location is reported before any byte of the file is read: the
	// refusal is the same one item selection makes, now with its reason.
	code, err := r.inspectNFOPath(ctx, e.Source)
	if ctx.Err() != nil {
		return false, ctx.Err(), false
	}
	if err != nil {
		return false, r.unavailableNFO(), false
	}
	if code != "" {
		completion, ok := nfoPathCompletion(e.Inventory, code, r.nfoIdentity.MaxSourceBytes)
		if !ok {
			return false, r.unavailableNFO(), false
		}
		return r.commitNFOCompletion(ctx, l, q, token, e, completion)
	}
	first, stamp, err := r.readNFO(ctx, e.Source)
	if ctx.Err() != nil {
		return false, ctx.Err(), false
	}
	completion := domain.NFOCompletion{Candidate: domain.NFOCandidate{InventoryID: e.Inventory.ID, Stamp: stamp}}
	if err != nil {
		kind := nfoSourceOutcome(err)
		if kind == "" {
			return false, r.unavailableNFO(), false
		}
		completion = nfoSourceCompletion(e.Inventory.ID, kind)
	} else if stamp.Size != e.Inventory.Size || stamp.ModifiedUnixNano != e.Inventory.ModifiedUnixNano {
		completion = nfoSourceCompletion(e.Inventory.ID, domain.NFOCompletionChanged)
	} else {
		dbCtx, cancel := context.WithTimeout(ctx, r.options.DBOperationTimeout)
		lookups, err := r.nfoRepository.LookupNFOBatch(dbCtx, l, token, []domain.NFOCandidate{completion.Candidate})
		cancel()
		if errors.Is(err, domain.ErrConflict) {
			return true, nil, false
		}
		if err != nil {
			return false, err, true
		}
		if len(lookups) != 1 || lookups[0].InventoryID != e.Inventory.ID {
			return false, &nfoAbort{domain.NFOPhaseIdentityMismatch, true}, false
		}
		switch lookups[0].Kind {
		case domain.NFOLookupHit, domain.NFOLookupNegativeHit:
			completion.Kind = lookups[0].Kind
		case domain.NFOLookupMiss:
			summary, err := r.parseNFO(ctx, first)
			if ctx.Err() != nil {
				return false, ctx.Err(), false
			}
			if err != nil {
				kind := nfoSourceOutcome(err)
				if kind == "" {
					return false, r.unavailableNFO(), false
				}
				completion = nfoSourceCompletion(e.Inventory.ID, kind)
			} else {
				completion.Kind, completion.Summary = domain.NFOCompletionParsed, &summary
			}
		default:
			return false, &nfoAbort{domain.NFOPhaseIdentityMismatch, true}, false
		}
		if completion.Kind == domain.NFOCompletionParsed || completion.Kind == domain.NFOCompletionHit || completion.Kind == domain.NFOCompletionNegativeHit {
			_, final, err := r.readNFO(ctx, e.Source)
			if ctx.Err() != nil {
				return false, ctx.Err(), false
			}
			if err != nil {
				kind := nfoSourceOutcome(err)
				if kind == "" {
					return false, r.unavailableNFO(), false
				}
				completion = nfoSourceCompletion(e.Inventory.ID, kind)
			} else if stamp != final {
				completion = nfoSourceCompletion(e.Inventory.ID, domain.NFOCompletionChanged)
			}
		}
	}
	return r.commitNFOCompletion(ctx, l, q, token, e, completion)
}

// commitNFOCompletion records the outcome of one entry and checks that the
// phase advanced by exactly that entry.
func (r *Runner) commitNFOCompletion(ctx context.Context, l domain.JobLease, q domain.NFORequest, token domain.NFOPageToken, e domain.NFOEntry, completion domain.NFOCompletion) (bool, error, bool) {
	if ctx.Err() != nil {
		return false, ctx.Err(), false
	}
	if !r.nfoAvailable() {
		return false, &nfoAbort{domain.NFOPhaseUnavailable, true}, false
	}
	if r.options.NFO.Reader.Identity() != q.Identity {
		return false, &nfoAbort{domain.NFOPhaseIdentityMismatch, true}, false
	}
	dbCtx, cancel := context.WithTimeout(ctx, r.options.DBOperationTimeout)
	phase, err := r.nfoRepository.CommitNFOBatch(dbCtx, l, token, []domain.NFOCompletion{completion})
	cancel()
	if errors.Is(err, domain.ErrConflict) {
		return true, nil, false
	}
	if err != nil {
		return false, err, true
	}
	if !nfoPhaseMatches(phase, q) || domain.ValidateNFOPhase(phase) != nil || phase.State != domain.NFOPhaseRunning || phase.Token.Revision != token.Revision+1 || phase.Token.AfterID != e.Inventory.ID {
		return false, &nfoAbort{domain.NFOPhaseIdentityMismatch, true}, false
	}
	return false, nil, false
}

// nfoPathCompletion is the outcome of an entry whose location is illegal. The
// file was never read, so its stamp is the inventory's size and modification
// time with a digest derived from the code alone; the stored summary holds the
// path issue and nothing else.
func nfoPathCompletion(inventory domain.InventoryEntry, code string, maxBytes int64) (domain.NFOCompletion, bool) {
	summary, ok := domain.NewNFOPathSummary(code)
	if !ok || inventory.Size < 0 || inventory.Size > maxBytes {
		return domain.NFOCompletion{}, false
	}
	digest := sha256.Sum256([]byte("jelee nfo path issue\x00" + code))
	stamp := domain.NFOStamp{Size: inventory.Size, ModifiedUnixNano: inventory.ModifiedUnixNano, SHA256: hex.EncodeToString(digest[:]), FingerprintVersion: domain.NFOFingerprintVersion}
	return domain.NFOCompletion{Candidate: domain.NFOCandidate{InventoryID: inventory.ID, Stamp: stamp}, Kind: domain.NFOCompletionParsed, Summary: &summary}, true
}

// inspectNFOPath asks a reader that can to classify the location of an entry.
// Readers that cannot never report a path issue.
func (r *Runner) inspectNFOPath(ctx context.Context, path domain.NFOSource) (code string, result error) {
	inspector, ok := r.options.NFO.Reader.(app.NFOPathInspector)
	if !ok {
		return "", nil
	}
	defer func() {
		if recover() != nil {
			code, result = "", domain.ErrNFOReaderUnavailable
		}
	}()
	release, err := r.acquireWork(ctx, app.WorkIO)
	if err != nil {
		return "", err
	}
	defer release()
	fileCtx, cancel := context.WithTimeout(ctx, r.options.NFO.FileTimeout)
	defer cancel()
	code, result = inspector.InspectNFOPath(fileCtx, path)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if fileCtx.Err() != nil {
		return "", fileCtx.Err()
	}
	if result == nil && code != "" && !domain.ValidNFOPathCode(code) {
		return "", domain.ErrNFOReaderUnavailable
	}
	return code, result
}

func (r *Runner) readNFO(ctx context.Context, path domain.NFOSource) (source app.NFOReadSource, stamp domain.NFOStamp, result error) {
	defer func() {
		if recover() != nil {
			source, stamp, result = nil, domain.NFOStamp{}, domain.ErrNFOReaderUnavailable
		}
	}()
	release, err := r.acquireWork(ctx, app.WorkIO)
	if err != nil {
		return nil, domain.NFOStamp{}, err
	}
	defer release()
	fileCtx, cancel := context.WithTimeout(ctx, r.options.NFO.FileTimeout)
	defer cancel()
	source, result = r.options.NFO.Reader.Read(fileCtx, path)
	if ctx.Err() != nil {
		return nil, domain.NFOStamp{}, ctx.Err()
	}
	if fileCtx.Err() != nil {
		return nil, domain.NFOStamp{}, fileCtx.Err()
	}
	if result != nil {
		return nil, domain.NFOStamp{}, result
	}
	if source == nil {
		return nil, domain.NFOStamp{}, domain.ErrNFOReaderUnavailable
	}
	stamp = source.Stamp()
	if domain.ValidateNFOStamp(stamp) != nil || stamp.Size > r.nfoIdentity.MaxSourceBytes {
		return nil, domain.NFOStamp{}, domain.ErrNFOReaderUnavailable
	}
	return source, stamp, nil
}
func (r *Runner) parseNFO(ctx context.Context, source app.NFOReadSource) (summary domain.NFOValidationSummary, result error) {
	defer func() {
		if recover() != nil {
			summary, result = domain.NFOValidationSummary{}, domain.ErrNFOReaderUnavailable
		}
	}()
	release, err := r.acquireWork(ctx, app.WorkCPU)
	if err != nil {
		return domain.NFOValidationSummary{}, err
	}
	defer release()
	fileCtx, cancel := context.WithTimeout(ctx, r.options.NFO.FileTimeout)
	defer cancel()
	summary, result = source.Parse(fileCtx)
	if ctx.Err() != nil {
		return domain.NFOValidationSummary{}, ctx.Err()
	}
	if fileCtx.Err() != nil {
		return domain.NFOValidationSummary{}, fileCtx.Err()
	}
	if result != nil {
		return domain.NFOValidationSummary{}, result
	}
	if _, err := domain.MarshalNFOSummary(summary); err != nil {
		return domain.NFOValidationSummary{}, domain.ErrNFOReaderUnavailable
	}
	return summary, nil
}
