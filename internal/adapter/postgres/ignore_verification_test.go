package postgres

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestIgnoreVerificationRequiresCompleteKnownClassification(t *testing.T) {
	for _, mode := range []string{"incomplete", "unknown", "reset"} {
		t.Run(mode, func(t *testing.T) {
			f, l, _ := baselineComparisonFixture(t, 1, 0)
			if mode == "reset" {
				if _, err := f.s.Pool.Exec(f.ctx, `UPDATE library_inventory_baseline SET inventory_generation=inventory_generation+1 WHERE library_id=$1::uuid`, f.registration.Library.ID); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.s.BeginIgnoreBaselineComparison(f.ctx, l); err != nil {
				t.Fatal(err)
			}
			if mode == "unknown" {
				for {
					p, err := f.s.NextIgnoreBaselinePage(f.ctx, l)
					if err != nil {
						t.Fatal(err)
					}
					if p.Complete {
						break
					}
					if err = f.s.CommitIgnoreBaselinePage(f.ctx, l, p.Token, decisionPage(p, domain.IgnoreBaselineUnknown)); err != nil {
						t.Fatal(err)
					}
				}
			}
			err := f.s.BeginIgnoreVerification(f.ctx, l)
			if mode == "reset" {
				if err != nil {
					t.Fatal("complete new scope blocked", err)
				}
				finishVerification(t, f, l)
			} else if err != domain.ErrConflict {
				t.Fatal("incomplete evidence entered verification", err)
			}
		})
	}
}

func TestIgnoreVerificationRechecksScopeAndCancellation(t *testing.T) {
	for _, mode := range []string{"epoch", "revision", "cancel", "lease"} {
		t.Run(mode, func(t *testing.T) {
			f, l := verificationFixture(t, 0)
			if err := f.s.BeginIgnoreVerification(f.ctx, l); err != nil {
				t.Fatal(err)
			}
			p, err := f.s.NextIgnoreVerificationPage(f.ctx, l)
			if err != nil {
				t.Fatal(err)
			}
			before := verificationSnapshot(t, f, l.Job.ID)
			want := domain.ErrInventoryInvalidated
			switch mode {
			case "epoch":
				_, err = f.s.Pool.Exec(f.ctx, `UPDATE library_roots SET path=path||'-changed' WHERE id=$1::uuid`, f.registration.RootID)
			case "revision":
				_, err = f.s.Pool.Exec(f.ctx, `UPDATE libraries SET inventory_baseline_revision=inventory_baseline_revision+1 WHERE id=$1::uuid`, f.registration.Library.ID)
			case "cancel":
				_, err = f.s.Pool.Exec(f.ctx, `UPDATE jobs SET cancel_requested=true WHERE id=$1::uuid`, l.Job.ID)
				want = context.Canceled
			case "lease":
				_, err = f.s.Pool.Exec(f.ctx, `UPDATE jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, l.Job.ID)
				want = domain.ErrJobLeaseLost
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = f.s.CommitIgnoreVerificationPage(f.ctx, l, p.Token, p.Proofs); err != want {
				t.Fatal("stale evidence committed", err)
			}
			if verificationSnapshot(t, f, l.Job.ID) != before {
				t.Fatal("rejected commit changed verification")
			}
		})
	}
}

func verificationFixture(t *testing.T, children int) (jobFixture, domain.JobLease) {
	t.Helper()
	f, l, root := baselineComparisonFixture(t, 0, 0)
	for start := 0; start < children; start += 128 {
		var batch []domain.IgnoreDirectoryProof
		for i := start; i < min(start+128, children); i++ {
			batch = append(batch, manifestChild(root, fmt.Sprintf("d-%04d", i)))
		}
		if err := f.s.RecordIgnoreProofs(f.ctx, l, batch); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.s.BeginIgnoreBaselineComparison(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	p, err := f.s.NextIgnoreBaselinePage(f.ctx, l)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.CommitIgnoreBaselinePage(f.ctx, l, p.Token, nil); err != nil {
		t.Fatal(err)
	}
	return f, l
}

func finishVerification(t *testing.T, f jobFixture, l domain.JobLease) {
	t.Helper()
	for {
		p, err := f.s.NextIgnoreVerificationPage(f.ctx, l)
		if err != nil {
			t.Fatal(err)
		}
		if p.Complete {
			return
		}
		if err = f.s.CommitIgnoreVerificationPage(f.ctx, l, p.Token, p.Proofs); err != nil {
			t.Fatal(err)
		}
	}
}

func verificationSnapshot(t *testing.T, f jobFixture, id string) string {
	t.Helper()
	var out string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT to_jsonb(v)::text FROM job_ignore_verifications v WHERE job_id=$1::uuid`, id).Scan(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestIgnoreVerificationPagesReclaimAndSeal(t *testing.T) {
	f, l := verificationFixture(t, 260)
	if err := f.s.BeginIgnoreVerification(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	initial := verificationSnapshot(t, f, l.Job.ID)
	if err := f.s.BeginIgnoreVerification(f.ctx, l); err != nil || verificationSnapshot(t, f, l.Job.ID) != initial {
		t.Fatal("begin renewed or mutated verification", err)
	}
	p, err := f.s.NextIgnoreVerificationPage(f.ctx, l)
	if err != nil || len(p.Proofs) != 128 {
		t.Fatal("bounded page", err)
	}
	if err = f.s.CommitIgnoreVerificationPage(f.ctx, l, p.Token, p.Proofs[:127]); err != domain.ErrConflict {
		t.Fatal("partial prefix", err)
	}
	if verificationSnapshot(t, f, l.Job.ID) != initial {
		t.Fatal("rejected prefix changed checkpoint")
	}
	if err = f.s.SealIgnoreVerification(f.ctx, l); err != domain.ErrConflict {
		t.Fatal("early seal", err)
	}
	if err = f.s.CommitIgnoreVerificationPage(f.ctx, l, p.Token, p.Proofs); err != nil {
		t.Fatal(err)
	}
	if err = f.s.CommitIgnoreVerificationPage(f.ctx, l, p.Token, p.Proofs); err != domain.ErrConflict {
		t.Fatal("old prefix recounted", err)
	}
	if err = f.s.ReleaseJob(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	next := ignoreManufacturedLease(t, f, l.Job.ID)
	if _, err = f.s.NextIgnoreVerificationPage(f.ctx, l); err != domain.ErrJobLeaseLost {
		t.Fatal("old lease read", err)
	}
	if _, err = f.s.NextIgnoreVerificationPage(f.ctx, next); err != domain.ErrJobLeaseLost {
		t.Fatal("new lease inherited verification", err)
	}
	if err = f.s.BeginIgnoreVerification(f.ctx, next); err != nil {
		t.Fatal(err)
	}
	first, err := f.s.NextIgnoreVerificationPage(f.ctx, next)
	if err != nil || first.Token.Sequence != 0 || first.Proofs[0] != p.Proofs[0] {
		t.Fatal("reclaim did not restart", err)
	}
	finishVerification(t, f, next)
	if err = f.s.SealIgnoreVerification(f.ctx, next); err != nil {
		t.Fatal(err)
	}
	sealed := verificationSnapshot(t, f, l.Job.ID)
	if err = f.s.SealIgnoreVerification(f.ctx, next); err != nil || verificationSnapshot(t, f, l.Job.ID) != sealed {
		t.Fatal("seal renewed", err)
	}
	var count, seq int64
	var bounded bool
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT verified_rows,sequence,sealed_until<=deadline AND sealed_until<=clock_timestamp()+interval '30 seconds' FROM job_ignore_verifications WHERE job_id=$1::uuid`, l.Job.ID).Scan(&count, &seq, &bounded); err != nil || count != 261 || seq != 4 || !bounded {
		t.Fatal("seal metadata", err)
	}
	if err = f.s.FinishJob(f.ctx, next, domain.JobSucceeded, ""); err != domain.ErrIgnoreUnavailable {
		t.Fatal("seal bypassed execution guard", err)
	}
}

func TestIgnoreVerificationChangedSourceInvalidatesWithoutPrefix(t *testing.T) {
	f, l := verificationFixture(t, 2)
	if err := f.s.BeginIgnoreVerification(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	p, err := f.s.NextIgnoreVerificationPage(f.ctx, l)
	if err != nil {
		t.Fatal(err)
	}
	before := verificationSnapshot(t, f, l.Job.ID)
	p.Proofs[1].Identity[0]++
	if err = f.s.CommitIgnoreVerificationPage(f.ctx, l, p.Token, p.Proofs); err != domain.ErrInventoryInvalidated {
		t.Fatal("changed source accepted", err)
	}
	if verificationSnapshot(t, f, l.Job.ID) != before {
		t.Fatal("changed suffix retained verified prefix")
	}
	_, _, _, invalid := manifestCounts(t, f, l.Job.ID)
	if !invalid {
		t.Fatal("source mismatch not durable")
	}
	if err = f.s.BeginIgnoreVerification(f.ctx, l); err != domain.ErrInventoryInvalidated {
		t.Fatal("invalid source revived", err)
	}
}

func TestIgnoreVerificationFixedDeadlinesAndLateRollback(t *testing.T) {
	for _, mode := range []string{"deadline", "seal", "late"} {
		t.Run(mode, func(t *testing.T) {
			f, l := verificationFixture(t, 0)
			if err := f.s.BeginIgnoreVerification(f.ctx, l); err != nil {
				t.Fatal(err)
			}
			p, err := f.s.NextIgnoreVerificationPage(f.ctx, l)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "seal" {
				finishVerification(t, f, l)
				if err = f.s.SealIgnoreVerification(f.ctx, l); err != nil {
					t.Fatal(err)
				}
				_, err = f.s.Pool.Exec(f.ctx, `UPDATE job_ignore_verifications SET sealed_until=clock_timestamp()-interval '1 second' WHERE job_id=$1::uuid`, l.Job.ID)
			} else if mode == "deadline" {
				_, err = f.s.Pool.Exec(f.ctx, `UPDATE job_ignore_verifications SET deadline=clock_timestamp()-interval '1 second' WHERE job_id=$1::uuid`, l.Job.ID)
			} else {
				_, err = f.s.Pool.Exec(f.ctx, `CREATE SEQUENCE verification_entered; CREATE FUNCTION hold_verification() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM nextval('verification_entered'); PERFORM pg_sleep(0.2); RETURN NEW; END $$; CREATE TRIGGER hold_verification BEFORE UPDATE OF sequence ON job_ignore_verifications FOR EACH ROW EXECUTE FUNCTION hold_verification()`)
				if err == nil {
					_, err = f.s.Pool.Exec(f.ctx, `UPDATE job_ignore_verifications SET deadline=clock_timestamp()+interval '120 milliseconds' WHERE job_id=$1::uuid`, l.Job.ID)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			before := verificationSnapshot(t, f, l.Job.ID)
			if mode == "late" {
				err = f.s.CommitIgnoreVerificationPage(f.ctx, l, p.Token, p.Proofs)
			} else {
				err = f.s.BeginIgnoreVerification(f.ctx, l)
			}
			if err != domain.ErrInventoryInvalidated || verificationSnapshot(t, f, l.Job.ID) != before {
				t.Fatal("expired evidence renewed or committed", err)
			}
			if mode == "late" {
				var entered bool
				if err = f.s.Pool.QueryRow(f.ctx, `SELECT is_called FROM verification_entered`).Scan(&entered); err != nil || !entered {
					t.Fatal("late test did not exercise delayed write", err)
				}
			}
		})
	}
}

func TestIgnoreVerificationFreezeHeartbeatAndMigration(t *testing.T) {
	f, l := verificationFixture(t, 0)
	if err := f.s.BeginIgnoreVerification(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	before := verificationSnapshot(t, f, l.Job.ID)
	if _, err := f.s.HeartbeatJob(f.ctx, l, time.Minute); err != nil {
		t.Fatal(err)
	}
	if verificationSnapshot(t, f, l.Job.ID) != before {
		t.Fatal("heartbeat changed evidence")
	}
	root := domain.IgnoreDirectoryProof{RootID: f.registration.RootID, Directory: "new", ParentIdentity: [32]byte{1}, Identity: [32]byte{7}}
	if err := f.s.RecordIgnoreProofs(f.ctx, l, []domain.IgnoreDirectoryProof{root}); err != domain.ErrConflict {
		t.Fatal("manifest appended while verifying", err)
	}
	nfoMigrationDenied(t, f, "000011_ignore_verification.down.sql")
	if err := f.s.FinishJob(f.ctx, l, domain.JobFailed, "scan_io"); err != nil {
		t.Fatal(err)
	}
	nfoMigrationDenied(t, f, "000011_ignore_verification.down.sql")
	if _, err := f.s.Pool.Exec(f.ctx, `DELETE FROM jobs WHERE id=$1::uuid`, l.Job.ID); err != nil {
		t.Fatal(err)
	}
	nfoMigrateVersion(t, f, "down", 24)
	nfoMigrateVersion(t, f, "down", 23)
	nfoMigrateVersion(t, f, "down", 22)
	nfoMigrateVersion(t, f, "down", 21)
	nfoMigrateVersion(t, f, "down", 20)
	nfoMigrateVersion(t, f, "down", 19)
	nfoMigrateVersion(t, f, "down", 18)
	nfoMigrateVersion(t, f, "down", 17)
	nfoMigrateVersion(t, f, "down", 16)
	nfoMigrateVersion(t, f, "down", 15)
	nfoMigrateVersion(t, f, "down", 14)
	nfoMigrateVersion(t, f, "down", 13)
	nfoMigrateVersion(t, f, "down", 12)
	nfoMigrateVersion(t, f, "down", 11)
	nfoMigrateVersion(t, f, "down", 10)
	nfoMigrateVersion(t, f, "up", SchemaVersion)
}

func TestIgnoreVerificationHashIncludesEveryField(t *testing.T) {
	seed := sha256.Sum256([]byte("seed"))
	p := domain.IgnoreDirectoryProof{RootID: "root", Directory: "path", ParentIdentity: [32]byte{1}, Identity: [32]byte{2}, RulePresent: true, RuleIdentity: [32]byte{3}, RuleSize: 10, RuleModifiedNano: -2, RuleSHA256: [32]byte{4}}
	base := extendIgnoreVerification(seed, p)
	changes := []func(*domain.IgnoreDirectoryProof){func(p *domain.IgnoreDirectoryProof) { p.RootID += "x" }, func(p *domain.IgnoreDirectoryProof) { p.Directory += "x" }, func(p *domain.IgnoreDirectoryProof) { p.ParentIdentity[0]++ }, func(p *domain.IgnoreDirectoryProof) { p.Identity[0]++ }, func(p *domain.IgnoreDirectoryProof) { p.RuleIdentity[0]++ }, func(p *domain.IgnoreDirectoryProof) { p.RuleSHA256[0]++ }, func(p *domain.IgnoreDirectoryProof) { p.RuleSize++ }, func(p *domain.IgnoreDirectoryProof) { p.RuleModifiedNano++ }, func(p *domain.IgnoreDirectoryProof) { p.MissingDirectory = true }, func(p *domain.IgnoreDirectoryProof) { p.RulePresent = false }}
	for i, change := range changes {
		q := p
		change(&q)
		if extendIgnoreVerification(seed, q) == base {
			t.Fatal("field missing from digest", i)
		}
	}
	seed[0]++
	if extendIgnoreVerification(seed, p) == base {
		t.Fatal("previous digest ignored")
	}
}
