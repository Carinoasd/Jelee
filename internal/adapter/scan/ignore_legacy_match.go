package scan

import (
	"context"
	"path"
	"path/filepath"
	"strings"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/legacyignore"
)

type LegacyCandidate struct {
	Path      string `json:"-"`
	Directory bool   `json:"-"`
}

func (LegacyCandidate) String() string   { return "legacy candidate (data redacted)" }
func (LegacyCandidate) GoString() string { return "legacy candidate (data redacted)" }

type LegacyMatchBatch struct {
	Observation     domain.LegacyIgnoreObservation `json:"-"`
	SourceDirectory string                         `json:"-"`
	Result          legacyignore.BatchResult       `json:"-"`
}

func (LegacyMatchBatch) String() string   { return "legacy matches (data redacted)" }
func (LegacyMatchBatch) GoString() string { return "legacy matches (data redacted)" }

type legacyBatchEvaluator interface {
	Evaluate(context.Context, legacyignore.Batch) (legacyignore.BatchResult, error)
}

// MatchLegacyIgnore evaluates candidates sharing one upstream lookup directory:
// files use their parent; a directory uses itself, including its own rules.
// The bounded helper accepts text and normalized full paths, never file access.
// Fresh observation after matching prevents publishing a stale source decision.
func (s *IgnoreScanner) MatchLegacyIgnore(ctx context.Context, d domain.ScanDirectory, candidates []LegacyCandidate, evaluator legacyBatchEvaluator) (LegacyMatchBatch, error) {
	empty := LegacyMatchBatch{}
	if ctx == nil || s == nil || s.resolver == nil || evaluator == nil || !domain.ValidID(d.RootID) || len(candidates) < 1 || len(candidates) > legacyignore.MaxBatchPaths {
		return empty, domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	paths := make([]string, len(candidates))
	for i, c := range candidates {
		if !(c.Directory && c.Path == ".") && !domain.ValidNFOObservationPath(c.Path) {
			return empty, domain.ErrInvalid
		}
		lookup := path.Dir(c.Path)
		if c.Directory {
			lookup = c.Path
		}
		if lookup != d.Path {
			return empty, domain.ErrInvalid
		}
		full := filepath.ToSlash(filepath.Join(d.RootPath, filepath.FromSlash(c.Path)))
		if c.Directory {
			full = strings.TrimRight(full, "/") + "/"
		}
		paths[i] = full
	}
	first, err := s.resolver.ObserveLegacy(ctx, d.RootPath, d.Path)
	if err != nil {
		return empty, ignoreWorkerError(ctx, err)
	}
	result := legacyignore.BatchResult{Decisions: make([]legacyignore.Decision, len(candidates))}
	if first.Present {
		text, e := legacyignore.DecodeSource(ctx, first.Bytes())
		if e != nil {
			if ctx.Err() != nil {
				return empty, ctx.Err()
			}
			return empty, domain.ErrScanLimit
		}
		batch := legacyignore.Batch{Source: text, Paths: paths}
		if _, e = legacyignore.EncodeBatch(ctx, batch); e != nil {
			return empty, domain.ErrInvalid
		}
		result, e = evaluator.Evaluate(ctx, batch)
		if e != nil {
			if ctx.Err() != nil {
				return empty, ctx.Err()
			}
			return empty, domain.ErrIgnoreUnavailable
		}
		if e = legacyignore.ValidateResult(ctx, batch, result); e != nil {
			return empty, domain.ErrIgnoreUnavailable
		}
	}
	last, err := s.resolver.ObserveLegacy(ctx, d.RootPath, d.Path)
	if err != nil {
		return empty, ignoreWorkerError(ctx, err)
	}
	if first.Token() != last.Token() {
		return empty, domain.ErrInventoryInvalidated
	}
	o := domain.LegacyIgnoreObservation{Version: domain.LegacyIgnoreProofVersion, Directory: d.Path}
	for _, p := range last.DirectoryProofs() {
		if p.Version != o.Version {
			return empty, domain.ErrInventoryInvalidated
		}
		o.Proofs = append(o.Proofs, domain.LegacyIgnoreDirectoryProof{IgnoreDirectoryProof: domainIgnoreProof(d.RootID, p.DirectoryProof), Checked: p.Checked})
	}
	if domain.ValidateLegacyIgnoreObservation(o) != nil {
		return empty, domain.ErrInventoryInvalidated
	}
	return LegacyMatchBatch{Observation: o, SourceDirectory: last.SourceDirectory, Result: result}, nil
}
