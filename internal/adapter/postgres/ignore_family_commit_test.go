package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestFamilyBaselineCommitRollbackAfterCustomWrite(t *testing.T) {
	for _, mode := range []string{"legacy-source", "cross-family-absence"} {
		t.Run(mode, func(t *testing.T) {
			f, l := familyComparisonFixture(t)
			name := "gone"
			if mode == "cross-family-absence" {
				name = "hidden"
			}
			if _, err := f.s.Pool.Exec(f.ctx, `DELETE FROM library_inventory_baseline WHERE library_id=$1::uuid AND path<>'f-000001.mkv'`, f.registration.Library.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.Pool.Exec(f.ctx, `UPDATE library_inventory_baseline SET path=$2 WHERE library_id=$1::uuid`, f.registration.Library.ID, name+"/movie.mkv"); err != nil {
				t.Fatal(err)
			}
			if err := f.s.BeginFamilyIgnoreBaselineComparison(f.ctx, l); err != nil {
				t.Fatal(err)
			}
			p, err := f.s.NextFamilyIgnoreBaselinePage(f.ctx, l)
			if err != nil {
				t.Fatal(err)
			}
			e := familyPageEvaluations(t, f, l, p)
			missing := domain.IgnoreDirectoryProof{RootID: e[0].Decision.RootID, Directory: name, ParentIdentity: e[0].CustomProofs[0].Identity, MissingDirectory: true}
			e[0].CustomProofs = append(e[0].CustomProofs, missing)
			if mode == "legacy-source" {
				e[0].LegacyObservations[0].LookupDirectory = name
				e[0].LegacyObservations[0].MissingDirectory = missing
				e[0].LegacyObservations[0].Source.Proofs[0].RuleSHA256[0] ^= 1
			} else {
				e[0].LegacyObservations = nil
			}
			if err = f.s.CommitFamilyIgnoreBaselinePage(f.ctx, l, p.Token, e); err != domain.ErrInventoryInvalidated {
				t.Fatal("inconsistent evidence accepted", err)
			}
			n, _, _, invalid := manifestCounts(t, f, l.Job.ID)
			_, _, _, _, legacyInvalid := legacyCounts(t, f, l)
			if n != 1 || !invalid || !legacyInvalid {
				t.Fatal("new custom missing proof survived rollback", n, invalid, legacyInvalid)
			}
			counts, seq, _ := comparisonCounts(t, f, l.Job.ID)
			if counts != (domain.IgnoreComparisonCounts{}) || seq != 0 {
				t.Fatal("partial progress survived")
			}
		})
	}
}

func TestFamilyBaselineCommitFences(t *testing.T) {
	for _, mode := range []string{"epoch", "cancelled", "generation", "late"} {
		t.Run(mode, func(t *testing.T) {
			f, l := familyComparisonFixture(t)
			if _, err := f.s.Pool.Exec(f.ctx, `DELETE FROM library_inventory_baseline WHERE library_id=$1::uuid AND path<>'f-000001.mkv'`, f.registration.Library.ID); err != nil {
				t.Fatal(err)
			}
			if err := f.s.BeginFamilyIgnoreBaselineComparison(f.ctx, l); err != nil {
				t.Fatal(err)
			}
			p, err := f.s.NextFamilyIgnoreBaselinePage(f.ctx, l)
			if err != nil {
				t.Fatal(err)
			}
			e := familyPageEvaluations(t, f, l, p)
			want := domain.ErrInventoryInvalidated
			switch mode {
			case "epoch":
				_, err = f.s.Pool.Exec(f.ctx, `UPDATE library_roots SET path=path||'-changed' WHERE id=$1::uuid`, f.registration.RootID)
			case "cancelled":
				_, err = f.s.Pool.Exec(f.ctx, `UPDATE jobs SET cancel_requested=true WHERE id=$1::uuid`, l.Job.ID)
				want = context.Canceled
			case "generation":
				_, err = f.s.Pool.Exec(f.ctx, `UPDATE jobs SET generation=generation+1 WHERE id=$1::uuid`, l.Job.ID)
				want = domain.ErrJobLeaseLost
			case "late":
				_, err = f.s.Pool.Exec(f.ctx, `CREATE SEQUENCE family_commit_entered; CREATE FUNCTION family_commit_delay() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM nextval('family_commit_entered'); PERFORM pg_sleep(0.3); RETURN NEW; END $$; CREATE TRIGGER family_commit_delay BEFORE INSERT ON job_ignore_comparison_pages FOR EACH ROW EXECUTE FUNCTION family_commit_delay()`)
				if err != nil {
					t.Fatal(err)
				}
				_, err = f.s.Pool.Exec(f.ctx, `UPDATE jobs SET lease_until=clock_timestamp()+interval '150 milliseconds' WHERE id=$1::uuid`, l.Job.ID)
				want = domain.ErrJobLeaseLost
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = f.s.CommitFamilyIgnoreBaselinePage(f.ctx, l, p.Token, e); !errors.Is(err, want) {
				t.Fatal(mode, err)
			}
			if mode == "late" {
				var entered bool
				if err = f.s.Pool.QueryRow(f.ctx, `SELECT is_called FROM family_commit_entered`).Scan(&entered); err != nil || !entered {
					t.Fatal("late test did not reach receipt", err)
				}
			}
			var decisions, queries int
			if err = f.s.Pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM job_ignore_family_decisions WHERE job_id=$1::uuid),(SELECT count(*) FROM job_ignore_legacy_baseline_queries WHERE job_id=$1::uuid)`, l.Job.ID).Scan(&decisions, &queries); err != nil || decisions != 0 || queries != 0 {
				t.Fatal("failed commit retained partial data", err)
			}
		})
	}
}

func familyPageEvaluations(t *testing.T, f jobFixture, l domain.JobLease, p domain.IgnoreBaselinePage) []domain.FamilyBaselineEvaluation {
	t.Helper()
	var out []domain.FamilyBaselineEvaluation
	for _, candidate := range p.Unseen {
		root, err := scanIgnoreProof(f.s.Pool.QueryRow(f.ctx, `SELECT `+ignoreProofColumns+` FROM job_ignore_proofs WHERE job_id=$1::uuid AND root_id=$2::uuid AND directory='.'`, l.Job.ID, candidate.RootID))
		if err != nil {
			t.Fatal(err)
		}
		legacy, err := scanLegacyProof(f.s.Pool.QueryRow(f.ctx, `SELECT `+legacyProofColumns+` FROM job_ignore_legacy_proofs WHERE job_id=$1::uuid AND root_id=$2::uuid AND directory='.'`, l.Job.ID, candidate.RootID))
		if err != nil {
			t.Fatal(err)
		}
		e := domain.FamilyBaselineEvaluation{Decision: domain.FamilyIgnoreBaselineDecision{RootID: candidate.RootID, Path: candidate.Path, Outcome: domain.IgnoreBaselineMissing}, CustomProofs: []domain.IgnoreDirectoryProof{root}, LegacyObservations: []domain.LegacyIgnoreBaselineObservation{{Version: domain.LegacyIgnoreBaselineProofVersion, LookupDirectory: ".", Source: domain.LegacyIgnoreObservation{Version: domain.LegacyIgnoreProofVersion, Directory: ".", Proofs: []domain.LegacyIgnoreDirectoryProof{legacy}}}}}
		out = append(out, e)
	}
	return out
}

func TestFamilyBaselineCommitPagesReplayAndEOF(t *testing.T) {
	f, l := familyComparisonFixture(t)
	if err := f.s.BeginFamilyIgnoreBaselineComparison(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	p, err := f.s.NextFamilyIgnoreBaselinePage(f.ctx, l)
	if err != nil {
		t.Fatal(err)
	}
	e := familyPageEvaluations(t, f, l, p)
	e[0].Decision.Outcome = domain.IgnoreBaselineExcluded
	e[0].Decision.Family = domain.IgnoreFamilyLegacy
	e[0].Decision.Reason = domain.IgnoreReasonBlank
	e[0].Decision.RuleDirectory = "."
	e[0].Decision.MatchedPath = e[0].Decision.Path
	e[1].Decision.Outcome = domain.IgnoreBaselineUnknown
	e[1].Decision.Reason = domain.IgnoreUnknownSource
	e[1].CustomProofs = nil
	e[1].LegacyObservations = nil
	if err = f.s.CommitFamilyIgnoreBaselinePage(f.ctx, l, p.Token, e[:127]); err != domain.ErrConflict {
		t.Fatal("truncated page", err)
	}
	for range 2 {
		if err = f.s.CommitFamilyIgnoreBaselinePage(f.ctx, l, p.Token, e); err != nil {
			t.Fatal("commit/replay", err)
		}
	}
	e[2].CustomProofs[0].RuleModifiedNano++
	if err = f.s.CommitFamilyIgnoreBaselinePage(f.ctx, l, p.Token, e); err != domain.ErrConflict {
		t.Fatal("changed replay evidence", err)
	}
	counts, sequence, complete := comparisonCounts(t, f, l.Job.ID)
	if counts.Missing != 126 || counts.Excluded != 1 || counts.Unknown != 1 || sequence != 1 || complete {
		t.Fatal("replay changed counters")
	}
	for range 2 {
		p, err = f.s.NextFamilyIgnoreBaselinePage(f.ctx, l)
		if err != nil {
			t.Fatal(err)
		}
		if err = f.s.CommitFamilyIgnoreBaselinePage(f.ctx, l, p.Token, familyPageEvaluations(t, f, l, p)); err != nil {
			t.Fatal(err)
		}
	}
	counts, sequence, complete = comparisonCounts(t, f, l.Job.ID)
	if counts.Missing != 128 || counts.Excluded != 1 || counts.Unknown != 1 || sequence != 3 || !complete {
		t.Fatal("EOF counts")
	}
	if err = f.s.CommitFamilyIgnoreBaselinePage(f.ctx, l, p.Token, nil); err != nil {
		t.Fatal("EOF replay", err)
	}
}

func TestFamilyBaselineCommitConflictRollsBackWholePage(t *testing.T) {
	f, l := familyComparisonFixture(t)
	if err := f.s.BeginFamilyIgnoreBaselineComparison(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	p, err := f.s.NextFamilyIgnoreBaselinePage(f.ctx, l)
	if err != nil {
		t.Fatal(err)
	}
	e := familyPageEvaluations(t, f, l, p)
	e[len(e)-1].CustomProofs[0].RuleSHA256[0] ^= 1
	if err = f.s.CommitFamilyIgnoreBaselinePage(f.ctx, l, p.Token, e); err != domain.ErrInventoryInvalidated {
		t.Fatal("source conflict", err)
	}
	_, _, _, invalid := manifestCounts(t, f, l.Job.ID)
	_, _, _, _, legacyInvalid := legacyCounts(t, f, l)
	var queries, decisions int
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM job_ignore_legacy_baseline_queries WHERE job_id=$1::uuid),(SELECT count(*) FROM job_ignore_family_decisions WHERE job_id=$1::uuid)`, l.Job.ID).Scan(&queries, &decisions); err != nil {
		t.Fatal(err)
	}
	counts, sequence, complete := comparisonCounts(t, f, l.Job.ID)
	if !invalid || !legacyInvalid || queries != 0 || decisions != 0 || counts != (domain.IgnoreComparisonCounts{}) || sequence != 0 || complete {
		t.Fatal("partial page survived source conflict")
	}
}
