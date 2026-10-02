package postgres

import (
	"context"
	"fmt"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func legacyBaselineFixture(t *testing.T) (jobFixture, domain.JobLease, domain.LegacyIgnoreBaselineObservation) {
	t.Helper()
	f, l, source := legacyManifestFixture(t)
	root := source.Proofs[0]
	return f, l, domain.LegacyIgnoreBaselineObservation{Version: domain.LegacyIgnoreBaselineProofVersion, LookupDirectory: "gone/deep", Source: source, MissingDirectory: domain.IgnoreDirectoryProof{RootID: root.RootID, Directory: "gone", ParentIdentity: root.Identity, MissingDirectory: true}}
}

func TestLegacyBaselineStoragePagesReplayAndMigration(t *testing.T) {
	f, l, o := legacyBaselineFixture(t)
	var observations []domain.LegacyIgnoreBaselineObservation
	for i := 0; i < 18; i++ {
		q := o
		q.LookupDirectory = fmt.Sprintf("gone/deep-%02d", i)
		observations = append(observations, q)
	}
	for i := 0; i < 2; i++ {
		if err := f.s.RecordLegacyIgnoreBaselineObservations(f.ctx, l, observations); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT baseline_queries FROM job_ignore_legacy_manifests WHERE job_id=$1::uuid`, l.Job.ID).Scan(&count); err != nil || count != 18 {
		t.Fatal("replay accounting", err, count)
	}
	if err := f.s.FreezeLegacyIgnoreManifest(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	if err := f.s.RecordLegacyIgnoreBaselineObservations(f.ctx, l, observations); err != nil {
		t.Fatal("frozen replay", err)
	}
	var cursor domain.IgnoreProofCursor
	total := 0
	for i := 0; i < 3; i++ {
		page, err := f.s.ReadLegacyIgnoreBaselinePage(f.ctx, l, cursor)
		if err != nil || len(page) > 16 {
			t.Fatal("page", err)
		}
		for _, got := range page {
			if got.LookupDirectory != observations[total].LookupDirectory || got.MissingDirectory != o.MissingDirectory || domain.ValidateLegacyIgnoreBaselineObservation(got) != nil {
				t.Fatal("restored evidence")
			}
			total++
			cursor = domain.IgnoreProofCursor{RootID: got.Source.Proofs[0].RootID, Directory: got.LookupDirectory}
		}
	}
	if total != 18 {
		t.Fatal("pagination lost queries", total)
	}
	o.LookupDirectory = "gone/new"
	if err := f.s.RecordLegacyIgnoreBaselineObservations(f.ctx, l, []domain.LegacyIgnoreBaselineObservation{o}); err != domain.ErrConflict {
		t.Fatal("frozen append", err)
	}
	nfoMigrationDenied(t, f, "000016_ignore_legacy_baseline.down.sql")
	if _, err := f.s.Pool.Exec(f.ctx, `DELETE FROM jobs WHERE id=$1::uuid`, l.Job.ID); err != nil {
		t.Fatal("cascade", err)
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
	nfoMigrateVersion(t, f, "up", SchemaVersion)
}

func TestLegacyBaselineStorageContradictoryPresence(t *testing.T) {
	for _, first := range []string{"missing", "present", "same-batch"} {
		t.Run(first, func(t *testing.T) {
			f, l, o := legacyBaselineFixture(t)
			child := manifestChild(o.Source.Proofs[0].IgnoreDirectoryProof, "gone")
			present := domain.LegacyIgnoreObservation{Version: o.Source.Version, Directory: "gone", Proofs: []domain.LegacyIgnoreDirectoryProof{o.Source.Proofs[0], {IgnoreDirectoryProof: child, Checked: true}}}
			var err error
			switch first {
			case "missing":
				if err = f.s.RecordLegacyIgnoreBaselineObservations(f.ctx, l, []domain.LegacyIgnoreBaselineObservation{o}); err != nil {
					t.Fatal(err)
				}
				err = f.s.RecordLegacyIgnoreObservation(f.ctx, l, present)
			case "present":
				if err = f.s.RecordLegacyIgnoreObservation(f.ctx, l, present); err != nil {
					t.Fatal(err)
				}
				err = f.s.RecordLegacyIgnoreBaselineObservations(f.ctx, l, []domain.LegacyIgnoreBaselineObservation{o})
			case "same-batch":
				second := domain.LegacyIgnoreBaselineObservation{Version: o.Version, LookupDirectory: "gone", Source: present}
				err = f.s.RecordLegacyIgnoreBaselineObservations(f.ctx, l, []domain.LegacyIgnoreBaselineObservation{o, second})
			}
			if err != domain.ErrInventoryInvalidated {
				t.Fatal("contradiction accepted", err)
			}
			n, q, _, _, invalid := legacyCounts(t, f, l)
			if !invalid {
				t.Fatal("invalidation not persisted")
			}
			if first == "same-batch" && (n != 0 || q != 0) {
				t.Fatal("partial source prefix survived")
			}
			if first == "missing" && (n != 1 || q != 1) {
				t.Fatal("new present directory survived")
			}
		})
	}
}

func TestLegacyBaselineStorageBudgetRollsBackSource(t *testing.T) {
	f, l, o := legacyBaselineFixture(t)
	if err := f.s.RecordLegacyIgnoreObservation(f.ctx, l, o.Source); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE job_ignore_legacy_manifests SET charge_bytes=$2 WHERE job_id=$1::uuid`, l.Job.ID, domain.IgnoreManifestMaxBytes); err != nil {
		t.Fatal(err)
	}
	if err := f.s.RecordLegacyIgnoreBaselineObservations(f.ctx, l, []domain.LegacyIgnoreBaselineObservation{o}); err != domain.ErrScanLimit {
		t.Fatal("budget", err)
	}
	var rows int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM job_ignore_legacy_baseline_queries WHERE job_id=$1::uuid`, l.Job.ID).Scan(&rows); err != nil || rows != 0 {
		t.Fatal("overbudget evidence retained", err)
	}
}

func TestLegacyBaselineStorageLeaseAndCancelFences(t *testing.T) {
	for _, mode := range []string{"lease", "cancel", "epoch"} {
		t.Run(mode, func(t *testing.T) {
			f, l, o := legacyBaselineFixture(t)
			if err := f.s.RecordLegacyIgnoreBaselineObservations(f.ctx, l, []domain.LegacyIgnoreBaselineObservation{o}); err != nil {
				t.Fatal(err)
			}
			if err := f.s.FreezeLegacyIgnoreManifest(f.ctx, l); err != nil {
				t.Fatal(err)
			}
			var query string
			var want error
			switch mode {
			case "lease":
				query = `UPDATE jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`
				want = domain.ErrJobLeaseLost
			case "cancel":
				query = `UPDATE jobs SET cancel_requested=true WHERE id=$1::uuid`
				want = context.Canceled
			case "epoch":
				query = `UPDATE libraries SET inventory_generation=inventory_generation+1 WHERE id=(SELECT library_id FROM jobs WHERE id=$1::uuid)`
				want = domain.ErrInventoryInvalidated
			}
			if _, err := f.s.Pool.Exec(f.ctx, query, l.Job.ID); err != nil {
				t.Fatal(err)
			}
			if err := f.s.RecordLegacyIgnoreBaselineObservations(f.ctx, l, []domain.LegacyIgnoreBaselineObservation{o}); err != want {
				t.Fatal("record fence", err, want)
			}
			if page, err := f.s.ReadLegacyIgnoreBaselinePage(f.ctx, l, domain.IgnoreProofCursor{}); err != want || len(page) != 0 {
				t.Fatal("read fence", err, want)
			}
		})
	}
}
