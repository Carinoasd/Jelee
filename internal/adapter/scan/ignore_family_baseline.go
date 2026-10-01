package scan

import (
	"context"
	"path"
	"strings"

	ignoresource "github.com/MoYuanCN/Jelee/internal/adapter/media/ignore"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/ignore"
	"github.com/MoYuanCN/Jelee/internal/platform/legacyignore"
)

type FamilyBaselineEvaluation struct {
	Decision           domain.FamilyIgnoreBaselineDecision      `json:"-"`
	CustomProofs       []domain.IgnoreDirectoryProof            `json:"-"`
	LegacyObservations []domain.LegacyIgnoreBaselineObservation `json:"-"`
}

func (FamilyBaselineEvaluation) String() string { return "family baseline evaluation (data redacted)" }
func (FamilyBaselineEvaluation) GoString() string {
	return "family baseline evaluation (data redacted)"
}

// EvaluateFamilyIgnoreBaseline follows the same reachable ancestors as a scan.
// A repository must establish that the candidate is unseen under complete
// coverage before treating the provisional included_missing result as absence.
func (s *FamilyIgnoreScanner) EvaluateFamilyIgnoreBaseline(ctx context.Context, root string, candidate domain.IgnoreBaselineCandidate, intent domain.IgnoreIntent) (FamilyBaselineEvaluation, error) {
	empty := FamilyBaselineEvaluation{}
	if ctx == nil || s == nil || s.custom == nil || s.legacy == nil || s.evaluator == nil || !domain.ValidID(candidate.RootID) || !domain.ValidNFOObservationPath(candidate.Path) || intent.Mode != domain.IgnoreModeFamily || (intent.CaseMode != domain.IgnoreCaseSensitive && intent.CaseMode != domain.IgnoreCaseASCIIInsensitive) {
		return empty, domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	parts := strings.Split(candidate.Path, "/")
	if len(parts) > ignore.MaxPathComponents {
		return empty, domain.ErrScanLimit
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		return empty, domain.ErrIgnoreUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, ignoresource.MaxDuration)
	defer cancel()
	options := ignore.Options{}
	if intent.CaseMode == domain.IgnoreCaseASCIIInsensitive {
		options.Case = ignore.CaseASCIIInsensitive
	}
	result := FamilyBaselineEvaluation{Decision: domain.FamilyIgnoreBaselineDecision{RootID: candidate.RootID, Path: candidate.Path, Outcome: domain.IgnoreBaselineMissing}}
	custom := make(map[string]domain.IgnoreDirectoryProof)
	legacy := make(map[string]domain.LegacyIgnoreDirectoryProof)
	identities := make(map[string]domain.IgnoreDirectoryProof)
	// Both source families must agree on directory identity and the first
	// absent boundary; their rule-file fields intentionally differ.
	addIdentity := func(p domain.IgnoreDirectoryProof) bool {
		if old, ok := identities[p.Directory]; ok && (old.RootID != p.RootID || old.Identity != p.Identity || old.ParentIdentity != p.ParentIdentity || old.MissingDirectory != p.MissingDirectory) {
			return false
		}
		identities[p.Directory] = p
		return true
	}
	for i := range parts {
		prefix := strings.Join(parts[:i+1], "/")
		isDirectory := i != len(parts)-1
		kind := ignore.File
		if isDirectory {
			kind = ignore.Directory
		}
		observed, err := s.custom.EvaluateBaseline(ctx, root, prefix, kind, options)
		if err != nil {
			return empty, ignoreWorkerError(ctx, err)
		}
		for _, native := range observed.DirectoryProofs() {
			p := domainIgnoreProof(candidate.RootID, native)
			if old, ok := custom[p.Directory]; ok && old != p {
				return empty, domain.ErrInventoryInvalidated
			}
			if !addIdentity(p) {
				return empty, domain.ErrInventoryInvalidated
			}
			if _, ok := custom[p.Directory]; !ok {
				result.CustomProofs = append(result.CustomProofs, p)
			}
			custom[p.Directory] = p
		}
		if observed.Match.Outcome == ignore.Exclude {
			result.Decision.Outcome = domain.IgnoreBaselineExcluded
			result.Decision.Family = domain.IgnoreFamilyCustom
			result.Decision.Reason = domain.IgnoreReasonRule
			result.Decision.RuleDirectory = path.Dir(observed.Match.Source)
			result.Decision.RuleLine = observed.Match.Line
			result.Decision.MatchedPath = observed.Match.MatchedPath
			break
		}
		if observed.Match.Outcome == ignore.Include {
			continue
		}
		lookup := path.Dir(prefix)
		if isDirectory {
			lookup = prefix
		}
		matched, err := s.legacy.MatchLegacyIgnoreBaseline(ctx, domain.ScanDirectory{RootID: candidate.RootID, RootPath: root, Path: lookup}, []LegacyCandidate{{Path: prefix, Directory: isDirectory}}, s.evaluator)
		if err != nil {
			return empty, err
		}
		for _, p := range matched.Observation.Source.Proofs {
			if !addIdentity(p.IgnoreDirectoryProof) {
				return empty, domain.ErrInventoryInvalidated
			}
			if old, ok := legacy[p.Directory]; ok {
				if !domain.LegacyIgnoreProofsCompatible(old, p) {
					return empty, domain.ErrInventoryInvalidated
				}
				if old.Checked {
					continue
				}
			}
			legacy[p.Directory] = p
		}
		if p := matched.Observation.MissingDirectory; p.MissingDirectory && !addIdentity(p) {
			return empty, domain.ErrInventoryInvalidated
		}
		result.LegacyObservations = append(result.LegacyObservations, matched.Observation)
		decision := matched.Result.Decisions[0]
		if decision.Kind == legacyignore.RuleExclude || decision.Kind == legacyignore.BlankExclude || decision.Kind == legacyignore.InvalidSourceExclude {
			result.Decision.Outcome = domain.IgnoreBaselineExcluded
			result.Decision.Family = domain.IgnoreFamilyLegacy
			result.Decision.Reason = domain.IgnoreReasonRule
			if decision.Kind == legacyignore.BlankExclude {
				result.Decision.Reason = domain.IgnoreReasonBlank
			}
			if decision.Kind == legacyignore.InvalidSourceExclude {
				result.Decision.Reason = domain.IgnoreReasonInvalid
			}
			result.Decision.RuleDirectory = matched.SourceDirectory
			result.Decision.RuleLine = decision.Line
			result.Decision.MatchedPath = prefix
			break
		}
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if len(result.CustomProofs) == 0 || domain.ValidateFamilyIgnoreBaselineDecision(result.Decision) != nil {
		return empty, domain.ErrInventoryInvalidated
	}
	return result, nil
}
