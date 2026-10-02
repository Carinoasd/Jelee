package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func legacyVerificationFixture(t *testing.T) (jobFixture, domain.JobLease) {
	t.Helper()
	f, l, o := legacyManifestFixture(t)
	if err := f.s.RecordLegacyIgnoreObservation(f.ctx, l, o); err != nil {
		t.Fatal(err)
	}
	if err := f.s.FreezeLegacyIgnoreManifest(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	if err := f.s.BeginLegacyIgnoreVerification(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	return f, l
}

func TestLegacyIgnoreVerificationCheckpointAndReplay(t *testing.T) {
	f, l := legacyVerificationFixture(t)
	var deadline time.Time
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT deadline FROM job_ignore_legacy_verifications WHERE job_id=$1::uuid`, l.Job.ID).Scan(&deadline); err != nil {
		t.Fatal(err)
	}
	if err := f.s.BeginLegacyIgnoreVerification(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	var repeated time.Time
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT deadline FROM job_ignore_legacy_verifications WHERE job_id=$1::uuid`, l.Job.ID).Scan(&repeated); err != nil || !deadline.Equal(repeated) {
		t.Fatal("begin renewed deadline", err)
	}
	page, err := f.s.NextLegacyIgnoreVerificationPage(f.ctx, l)
	if err != nil || len(page.Observations) != 1 || page.Complete {
		t.Fatal("initial page", err)
	}
	if err = f.s.CommitLegacyIgnoreVerificationPage(f.ctx, l, page.Token, nil); err != domain.ErrConflict {
		t.Fatal("truncated page", err)
	}
	if err = f.s.CommitLegacyIgnoreVerificationPage(f.ctx, l, page.Token, page.Observations); err != nil {
		t.Fatal(err)
	}
	if err = f.s.CommitLegacyIgnoreVerificationPage(f.ctx, l, page.Token, page.Observations); err != domain.ErrConflict {
		t.Fatal("stale page replay", err)
	}
	end, err := f.s.NextLegacyIgnoreVerificationPage(f.ctx, l)
	if err != nil || end.Complete || len(end.Observations) != 0 || end.Token.Sequence != 1 {
		t.Fatal("EOF needs acknowledgement", err)
	}
	if err = f.s.CommitLegacyIgnoreVerificationPage(f.ctx, l, end.Token, nil); err != nil {
		t.Fatal(err)
	}
	done, err := f.s.NextLegacyIgnoreVerificationPage(f.ctx, l)
	if err != nil || !done.Complete || done.Token.Sequence != 2 {
		t.Fatal("completion", err)
	}
	nfoMigrationDenied(t, f, "000014_ignore_legacy_verification.down.sql")
	if _, err = f.s.Pool.Exec(f.ctx, `DELETE FROM jobs WHERE id=$1::uuid`, l.Job.ID); err != nil {
		t.Fatal(err)
	}
	nfoMigrateVersion(t, f, "down", 29)
	nfoMigrateVersion(t, f, "down", 28)
	nfoMigrateVersion(t, f, "down", 27)
	nfoMigrateVersion(t, f, "down", 26)
	nfoMigrateVersion(t, f, "down", 25)
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
	nfoMigrateVersion(t, f, "up", SchemaVersion)
}

func TestLegacyIgnoreVerificationRejectsChangeAndExpiry(t *testing.T) {
	for _, kind := range []string{"source", "expired", "generation", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			f, l := legacyVerificationFixture(t)
			page, err := f.s.NextLegacyIgnoreVerificationPage(f.ctx, l)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "cancel":
				if _, err = f.s.Pool.Exec(f.ctx, `UPDATE jobs SET cancel_requested=true WHERE id=$1::uuid`, l.Job.ID); err != nil {
					t.Fatal(err)
				}
				if err = f.s.BeginLegacyIgnoreVerification(f.ctx, l); err != context.Canceled {
					t.Fatal("canceled begin", err)
				}
				if _, err = f.s.NextLegacyIgnoreVerificationPage(f.ctx, l); err != context.Canceled {
					t.Fatal("canceled read", err)
				}
				if err = f.s.CommitLegacyIgnoreVerificationPage(f.ctx, l, page.Token, page.Observations); err != context.Canceled {
					t.Fatal("canceled commit", err)
				}
			case "source":
				page.Observations[0].Proofs[0].RuleSHA256 = [32]byte{99}
				if err = f.s.CommitLegacyIgnoreVerificationPage(f.ctx, l, page.Token, page.Observations); err != domain.ErrInventoryInvalidated {
					t.Fatal("changed proof", err)
				}
				if _, err = f.s.NextLegacyIgnoreVerificationPage(f.ctx, l); err != domain.ErrInventoryInvalidated {
					t.Fatal("manifest not invalidated", err)
				}
			case "expired":
				if _, err = f.s.Pool.Exec(f.ctx, `UPDATE job_ignore_legacy_verifications SET deadline=clock_timestamp()-interval '1 second' WHERE job_id=$1::uuid`, l.Job.ID); err != nil {
					t.Fatal(err)
				}
				if err = f.s.BeginLegacyIgnoreVerification(f.ctx, l); err != domain.ErrInventoryInvalidated {
					t.Fatal("expired evidence renewed", err)
				}
				if err = f.s.CommitLegacyIgnoreVerificationPage(f.ctx, l, page.Token, page.Observations); err != domain.ErrInventoryInvalidated {
					t.Fatal("expired page committed", err)
				}
			case "generation":
				if err = f.s.CommitLegacyIgnoreVerificationPage(f.ctx, l, page.Token, page.Observations); err != nil {
					t.Fatal(err)
				}
				fresh, e := scanLease(f.s.Pool.QueryRow(f.ctx, `UPDATE jobs SET generation=generation+1,lease_until=clock_timestamp()+interval '1 minute' WHERE id=$1::uuid RETURNING `+leaseColumns, l.Job.ID))
				if e != nil {
					t.Fatal(e)
				}
				if _, err = f.s.NextLegacyIgnoreVerificationPage(f.ctx, fresh); err != domain.ErrJobLeaseLost {
					t.Fatal("old checkpoint reused", err)
				}
				if err = f.s.BeginLegacyIgnoreVerification(f.ctx, fresh); err != nil {
					t.Fatal(err)
				}
				restarted, e := f.s.NextLegacyIgnoreVerificationPage(f.ctx, fresh)
				if e != nil || restarted.Token.Sequence != 0 || len(restarted.Observations) != 1 || restarted.Complete {
					t.Fatal("new generation did not restart", e)
				}
				if _, err = f.s.NextLegacyIgnoreVerificationPage(f.ctx, l); err != domain.ErrJobLeaseLost {
					t.Fatal("old lease survived", err)
				}
			}
		})
	}
}

func TestLegacyIgnoreVerificationDeadlineAtCommit(t *testing.T) {
	f, l := legacyVerificationFixture(t)
	page, err := f.s.NextLegacyIgnoreVerificationPage(f.ctx, l)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.Pool.Exec(f.ctx, `CREATE FUNCTION legacy_verification_delay() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(0.3); RETURN NEW; END $$; CREATE TRIGGER legacy_verification_delay BEFORE UPDATE ON jobs FOR EACH ROW EXECUTE FUNCTION legacy_verification_delay()`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE job_ignore_legacy_verifications SET deadline=clock_timestamp()+interval '150 milliseconds' WHERE job_id=$1::uuid`, l.Job.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.s.CommitLegacyIgnoreVerificationPage(f.ctx, l, page.Token, page.Observations); err != domain.ErrInventoryInvalidated {
		t.Fatal("late commit accepted", err)
	}
	var count, sequence int64
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT verified_queries,sequence FROM job_ignore_legacy_verifications WHERE job_id=$1::uuid`, l.Job.ID).Scan(&count, &sequence); err != nil || count != 0 || sequence != 0 {
		t.Fatal("expired checkpoint committed", err)
	}
}
