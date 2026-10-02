package postgres

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestLegacyIgnoreManifestFreezeAndPages(t *testing.T) {
	f, l, o := legacyManifestFixture(t)
	if err := f.s.FreezeLegacyIgnoreManifest(f.ctx, l); err != domain.ErrConflict {
		t.Fatal("missing root query", err)
	}
	if err := f.s.RecordLegacyIgnoreObservation(f.ctx, l, o); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ReadLegacyIgnoreProofPage(f.ctx, l, domain.IgnoreProofCursor{}); err != domain.ErrConflict {
		t.Fatal("mutable read", err)
	}
	want := []domain.LegacyIgnoreDirectoryProof{o.Proofs[0]}
	for i := 0; i < 130; i++ {
		p := manifestChild(o.Proofs[0].IgnoreDirectoryProof, fmt.Sprintf("child-%03d", i))
		q := domain.LegacyIgnoreObservation{Version: o.Version, Directory: p.Directory, Proofs: []domain.LegacyIgnoreDirectoryProof{o.Proofs[0], {IgnoreDirectoryProof: p, Checked: true}}}
		if err := f.s.RecordLegacyIgnoreObservation(f.ctx, l, q); err != nil {
			t.Fatal(err)
		}
		want = append(want, q.Proofs[1])
	}
	for i := 0; i < 2; i++ {
		if err := f.s.FreezeLegacyIgnoreManifest(f.ctx, l); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.s.RecordLegacyIgnoreObservation(f.ctx, l, o); err != nil {
		t.Fatal("frozen replay", err)
	}
	p := manifestChild(o.Proofs[0].IgnoreDirectoryProof, "new")
	q := domain.LegacyIgnoreObservation{Version: o.Version, Directory: p.Directory, Proofs: []domain.LegacyIgnoreDirectoryProof{o.Proofs[0], {IgnoreDirectoryProof: p, Checked: true}}}
	if err := f.s.RecordLegacyIgnoreObservation(f.ctx, l, q); err != domain.ErrConflict {
		t.Fatal("frozen append", err)
	}
	var got []domain.LegacyIgnoreDirectoryProof
	var cursor domain.IgnoreProofCursor
	for i := 0; i < 3; i++ {
		page, err := f.s.ReadLegacyIgnoreProofPage(f.ctx, l, cursor)
		if err != nil || len(page) > 128 {
			t.Fatal("page", err)
		}
		if len(page) == 0 {
			break
		}
		got = append(got, page...)
		last := page[len(page)-1]
		cursor = domain.IgnoreProofCursor{RootID: last.RootID, Directory: last.Directory}
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatal("page lost or repeated evidence")
	}
}

func TestLegacyIgnoreManifestLeaseCancelAndEpoch(t *testing.T) {
	for _, kind := range []string{"lease", "cancel", "epoch"} {
		t.Run(kind, func(t *testing.T) {
			f, l, o := legacyManifestFixture(t)
			if err := f.s.RecordLegacyIgnoreObservation(f.ctx, l, o); err != nil {
				t.Fatal(err)
			}
			if err := f.s.FreezeLegacyIgnoreManifest(f.ctx, l); err != nil {
				t.Fatal(err)
			}
			var want error
			switch kind {
			case "lease":
				_, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, l.Job.ID)
				if err != nil {
					t.Fatal(err)
				}
				want = domain.ErrJobLeaseLost
			case "cancel":
				_, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET cancel_requested=true WHERE id=$1::uuid`, l.Job.ID)
				if err != nil {
					t.Fatal(err)
				}
				want = context.Canceled
			case "epoch":
				_, err := f.s.Pool.Exec(f.ctx, `UPDATE libraries SET inventory_generation=inventory_generation+1 WHERE id=$1::uuid`, l.Job.LibraryID)
				if err != nil {
					t.Fatal(err)
				}
				want = domain.ErrInventoryInvalidated
			}
			if err := f.s.RecordLegacyIgnoreObservation(f.ctx, l, o); err != want {
				t.Fatal("record fence", err)
			}
			if err := f.s.FreezeLegacyIgnoreManifest(f.ctx, l); err != want {
				t.Fatal("freeze fence", err)
			}
			if page, err := f.s.ReadLegacyIgnoreProofPage(f.ctx, l, domain.IgnoreProofCursor{}); err != want || len(page) != 0 {
				t.Fatal("read fence", err)
			}
			if page, err := f.s.ReadLegacyIgnoreObservationPage(f.ctx, l, domain.IgnoreProofCursor{}); err != want || len(page) != 0 {
				t.Fatal("query restore fence", err)
			}
		})
	}
}

func legacyManifestFixture(t *testing.T, setup ...func(*testing.T, jobFixture)) (jobFixture, domain.JobLease, domain.LegacyIgnoreObservation) {
	t.Helper()
	f, l, p := manifestFixture(t, setup...)
	if _, err := f.s.Pool.Exec(f.ctx, `DELETE FROM job_ignore_requests WHERE job_id=$1::uuid`, l.Job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO job_ignore_requests(job_id,library_id,mode,case_mode,program_version,proof_version) VALUES($1::uuid,$2::uuid,'jeleeignore-legacy-v1','sensitive','jeleeignore-legacy-v1','jeleeignore-legacy-proof-v1')`, l.Job.ID, l.Job.LibraryID); err != nil {
		t.Fatal(err)
	}
	return f, l, domain.LegacyIgnoreObservation{Version: domain.LegacyIgnoreProofVersion, Directory: ".", Proofs: []domain.LegacyIgnoreDirectoryProof{{IgnoreDirectoryProof: p, Checked: true}}}
}

func legacyCounts(t *testing.T, f jobFixture, l domain.JobLease) (int64, int64, int64, int64, bool) {
	t.Helper()
	var n, q, b, c int64
	var invalid bool
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT rows,queries,source_bytes,charge_bytes,invalidated FROM job_ignore_legacy_manifests WHERE job_id=$1::uuid`, l.Job.ID).Scan(&n, &q, &b, &c, &invalid); err != nil {
		t.Fatal(err)
	}
	return n, q, b, c, invalid
}

func TestLegacyIgnoreManifestReplayUpgradeAndConflict(t *testing.T) {
	f, l, root := legacyManifestFixture(t)
	p := root.Proofs[0].IgnoreDirectoryProof
	shadow := p
	shadow.RulePresent = false
	shadow.RuleIdentity = [32]byte{}
	shadow.RuleSize = 0
	shadow.RuleModifiedNano = 0
	shadow.RuleSHA256 = [32]byte{}
	child := manifestChild(p, "child")
	child.RulePresent = true
	child.RuleIdentity = [32]byte{5}
	child.RuleSHA256 = [32]byte{6}
	child.RuleSize = 8
	o := domain.LegacyIgnoreObservation{Version: domain.LegacyIgnoreProofVersion, Directory: "child", Proofs: []domain.LegacyIgnoreDirectoryProof{{IgnoreDirectoryProof: shadow}, {IgnoreDirectoryProof: child, Checked: true}}}
	if err := f.s.RecordLegacyIgnoreObservation(f.ctx, l, o); err != nil {
		t.Fatal(err)
	}
	if err := f.s.RecordLegacyIgnoreObservation(f.ctx, l, root); err != nil {
		t.Fatal("upgrade shadowed root", err)
	}
	n, q, b, c, invalid := legacyCounts(t, f, l)
	if n != 2 || q != 2 || b != 12 || invalid {
		t.Fatal("bad accounting", n, q, b, invalid)
	}
	if err := f.s.RecordLegacyIgnoreObservation(f.ctx, l, o); err != nil {
		t.Fatal("replay shadowed query", err)
	}
	n2, q2, b2, c2, _ := legacyCounts(t, f, l)
	if n2 != n || q2 != q || b2 != b || c2 != c {
		t.Fatal("replay charged twice")
	}
	// A new descendant plus changed checked source must retain neither change.
	changed := p
	changed.RuleSHA256 = [32]byte{9}
	newChild := manifestChild(changed, "new")
	bad := domain.LegacyIgnoreObservation{Version: domain.LegacyIgnoreProofVersion, Directory: "new", Proofs: []domain.LegacyIgnoreDirectoryProof{{IgnoreDirectoryProof: changed, Checked: true}, {IgnoreDirectoryProof: newChild, Checked: true}}}
	if err := f.s.RecordLegacyIgnoreObservation(f.ctx, l, bad); err != domain.ErrInventoryInvalidated {
		t.Fatal("changed source", err)
	}
	n2, q2, b2, c2, invalid = legacyCounts(t, f, l)
	if n2 != n || q2 != q || b2 != b || c2 != c || !invalid {
		t.Fatal("conflicting batch altered accepted evidence")
	}
	if err := f.s.RecordLegacyIgnoreObservation(f.ctx, l, root); err != domain.ErrInventoryInvalidated {
		t.Fatal("invalidated ledger accepted replay", err)
	}
}

func TestLegacyIgnoreManifestLimitsAndFences(t *testing.T) {
	t.Run("budget", func(t *testing.T) {
		f, l, o := legacyManifestFixture(t)
		// Persist the policy limit: caller lease policy is not authoritative.
		if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET max_directories=1 WHERE id=$1::uuid`, l.Job.ID); err != nil {
			t.Fatal(err)
		}
		p := manifestChild(o.Proofs[0].IgnoreDirectoryProof, "child")
		o.Directory = "child"
		o.Proofs = append(o.Proofs, domain.LegacyIgnoreDirectoryProof{IgnoreDirectoryProof: p, Checked: true})
		if err := f.s.RecordLegacyIgnoreObservation(f.ctx, l, o); err != domain.ErrScanLimit {
			t.Fatal("budget", err)
		}
		var count int
		if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM job_ignore_legacy_manifests WHERE job_id=$1::uuid`, l.Job.ID).Scan(&count); err != nil || count != 0 {
			t.Fatal("failed batch retained state", err)
		}
	})
	t.Run("old mode", func(t *testing.T) {
		f, l, p := manifestFixture(t)
		o := domain.LegacyIgnoreObservation{Version: domain.LegacyIgnoreProofVersion, Directory: ".", Proofs: []domain.LegacyIgnoreDirectoryProof{{IgnoreDirectoryProof: p, Checked: true}}}
		if err := f.s.RecordLegacyIgnoreObservation(f.ctx, l, o); err != domain.ErrConflict {
			t.Fatal("old mode accepted", err)
		}
	})
}
