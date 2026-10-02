package jobs

import (
	"context"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

type familyBatchRepo struct {
	app.FamilyIgnoreExecutionRepository
	roots int
	err   error
}

func (f *familyBatchRepo) ReadFamilyIgnoreRoot(context.Context, domain.JobLease, string) (string, error) {
	f.roots++
	return "/owned", f.err
}

type familyBatchScanner struct {
	app.FamilyIgnoreScanner
	mode             string
	batches, singles int
}

func unknownFamilyCandidate(c domain.IgnoreBaselineCandidate) domain.FamilyBaselineEvaluation {
	return domain.FamilyBaselineEvaluation{Decision: domain.FamilyIgnoreBaselineDecision{RootID: c.RootID, Path: c.Path, Outcome: domain.IgnoreBaselineUnknown, Reason: domain.IgnoreUnknownSource}}
}
func (f *familyBatchScanner) EvaluateFamilyIgnoreBaseline(_ context.Context, _ string, c domain.IgnoreBaselineCandidate, _ domain.IgnoreIntent) (domain.FamilyBaselineEvaluation, error) {
	f.singles++
	return unknownFamilyCandidate(c), nil
}
func (f *familyBatchScanner) EvaluateFamilyIgnoreBaselineBatch(_ context.Context, _ string, c []domain.IgnoreBaselineCandidate, _ domain.IgnoreIntent) ([]domain.FamilyBaselineEvaluation, error) {
	f.batches++
	if f.mode == "unavailable" {
		return nil, domain.ErrIgnoreUnavailable
	}
	if f.mode == "failure" {
		return nil, domain.ErrScanIO
	}
	var result []domain.FamilyBaselineEvaluation
	for _, candidate := range c {
		result = append(result, unknownFamilyCandidate(candidate))
	}
	if f.mode == "short" {
		return result[:len(result)-1], nil
	}
	if f.mode == "wrong-path" {
		result[0].Decision.Path = "other.mkv"
	}
	return result, nil
}

func TestFamilyRunnerBaselineBatchDispatchAndRejection(t *testing.T) {
	for _, mode := range []string{"success", "unavailable", "failure", "short", "wrong-path", "root-failure"} {
		t.Run(mode, func(t *testing.T) {
			repo := &familyBatchRepo{}
			if mode == "root-failure" {
				repo.err = domain.ErrDatabase
			}
			scanner := &familyBatchScanner{mode: mode}
			r := &Runner{options: Options{DBOperationTimeout: time.Second, FamilyIgnore: &FamilyIgnoreOptions{Repository: repo, Scanner: scanner}}}
			candidates := []domain.IgnoreBaselineCandidate{{RootID: "00000000-0000-4000-8000-000000000001", Path: "a.mkv"}, {RootID: "00000000-0000-4000-8000-000000000001", Path: "b.mkv"}, {RootID: "00000000-0000-4000-8000-000000000002", Path: "c.mkv"}}
			got, err, storage := r.evaluateFamilyBaselinePage(context.Background(), domain.JobLease{}, candidates, domain.IgnoreIntent{})
			switch mode {
			case "success", "unavailable":
				wantSingles := 0
				if mode == "unavailable" {
					wantSingles = 3
				}
				if err != nil || storage || len(got) != 3 || repo.roots != 2 || scanner.batches != 2 || scanner.singles != wantSingles {
					t.Fatal("root grouping or fallback mismatch", len(got), repo.roots, scanner.batches, scanner.singles, err)
				}
			case "root-failure":
				if err != domain.ErrDatabase || !storage || scanner.batches != 0 {
					t.Fatal("root failure bypassed", err)
				}
			case "failure":
				if err != domain.ErrScanIO || storage || scanner.singles != 0 {
					t.Fatal("batch failure was hidden", err)
				}
			default:
				if err != domain.ErrInventoryInvalidated || storage {
					t.Fatal("malformed batch accepted", err)
				}
			}
		})
	}
}
