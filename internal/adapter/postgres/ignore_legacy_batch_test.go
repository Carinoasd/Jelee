package postgres

import (
	"fmt"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestLegacyIgnoreBatchSharedAncestorsAndReplay(t *testing.T) {
	f, l, root := legacyManifestFixture(t)
	observations := []domain.LegacyIgnoreObservation{root}
	for i := 0; i < 128; i++ {
		child := manifestChild(root.Proofs[0].IgnoreDirectoryProof, fmt.Sprintf("batch-%03d", i))
		observations = append(observations, domain.LegacyIgnoreObservation{Version: root.Version, Directory: child.Directory, Proofs: []domain.LegacyIgnoreDirectoryProof{root.Proofs[0], {IgnoreDirectoryProof: child, Checked: true}}})
	}
	if err := f.s.RecordLegacyIgnoreObservations(f.ctx, l, observations); err != nil {
		t.Fatal(err)
	}
	n, q, b, c, invalid := legacyCounts(t, f, l)
	if n != 129 || q != 129 || b != root.Proofs[0].RuleSize || invalid {
		t.Fatal("shared proofs counted repeatedly", n, q, b, invalid)
	}
	if err := f.s.FreezeLegacyIgnoreManifest(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	// Reverse input order to ensure replay does not depend on enumeration order.
	for i, j := 0, len(observations)-1; i < j; i, j = i+1, j-1 {
		observations[i], observations[j] = observations[j], observations[i]
	}
	if err := f.s.RecordLegacyIgnoreObservations(f.ctx, l, observations); err != nil {
		t.Fatal(err)
	}
	n2, q2, b2, c2, invalid := legacyCounts(t, f, l)
	if n2 != n || q2 != q || b2 != b || c2 != c || invalid {
		t.Fatal("batch replay changed accounting")
	}
}

func TestLegacyIgnoreBatchConflictRetainsNoPrefix(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprint(existing), func(t *testing.T) {
			f, l, root := legacyManifestFixture(t)
			if existing {
				if err := f.s.RecordLegacyIgnoreObservation(f.ctx, l, root); err != nil {
					t.Fatal(err)
				}
			}
			p := manifestChild(root.Proofs[0].IgnoreDirectoryProof, "new")
			child := domain.LegacyIgnoreObservation{Version: root.Version, Directory: p.Directory, Proofs: []domain.LegacyIgnoreDirectoryProof{root.Proofs[0], {IgnoreDirectoryProof: p, Checked: true}}}
			changed := root
			changed.Proofs = append([]domain.LegacyIgnoreDirectoryProof(nil), root.Proofs...)
			changed.Proofs[0].RuleSHA256 = [32]byte{99}
			observations := []domain.LegacyIgnoreObservation{child, changed}
			if err := f.s.RecordLegacyIgnoreObservations(f.ctx, l, observations); err != domain.ErrInventoryInvalidated {
				t.Fatal("conflict accepted", err)
			}
			n, q, b, _, invalid := legacyCounts(t, f, l)
			var want int64
			if existing {
				want = 1
			}
			if n != want || q != want || !invalid || b != want*root.Proofs[0].RuleSize {
				t.Fatal("conflict retained prefix", n, q, b, invalid)
			}
			var rows int
			if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM job_ignore_legacy_proofs WHERE job_id=$1::uuid`, l.Job.ID).Scan(&rows); err != nil {
				t.Fatal(err)
			}
			if int64(rows) != want {
				t.Fatal("unaccounted prefix retained")
			}
		})
	}
}

func TestLegacyIgnoreBatchRejectsUnboundedInput(t *testing.T) {
	for _, count := range []int{0, 130} {
		if _, err := prepareLegacyObservations(make([]domain.LegacyIgnoreObservation, count)); err != domain.ErrInvalid {
			t.Fatal("unbounded batch", count, err)
		}
	}
}

func TestLegacyIgnoreBatchShadowUpgradePreservesQuery(t *testing.T) {
	f, l, root := legacyManifestFixture(t)
	shadow := root.Proofs[0]
	shadow.Checked = false
	shadow.RulePresent = false
	shadow.RuleIdentity = [32]byte{}
	shadow.RuleSHA256 = [32]byte{}
	shadow.RuleSize = 0
	shadow.RuleModifiedNano = 0
	child := manifestChild(root.Proofs[0].IgnoreDirectoryProof, "child")
	child.RulePresent = true
	child.RuleIdentity = [32]byte{5}
	child.RuleSHA256 = [32]byte{6}
	child.RuleSize = 8
	o := domain.LegacyIgnoreObservation{Version: root.Version, Directory: "child", Proofs: []domain.LegacyIgnoreDirectoryProof{shadow, {IgnoreDirectoryProof: child, Checked: true}}}
	// Upgrade a shadowed shared ancestor within the same batch, with duplicate queries.
	if err := f.s.RecordLegacyIgnoreObservations(f.ctx, l, []domain.LegacyIgnoreObservation{o, root, o}); err != nil {
		t.Fatal(err)
	}
	n, q, b, _, invalid := legacyCounts(t, f, l)
	if n != 2 || q != 2 || b != 12 || invalid {
		t.Fatal("upgrade accounting", n, q, b, invalid)
	}
	if err := f.s.FreezeLegacyIgnoreManifest(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	restored, err := f.s.ReadLegacyIgnoreObservationPage(f.ctx, l, domain.IgnoreProofCursor{})
	if err != nil || len(restored) != 2 {
		t.Fatal("restore", err)
	}
	if restored[1].Directory != "child" || restored[1].Proofs[0].Checked || !restored[1].Proofs[1].RulePresent {
		t.Fatal("batch merge lost shadow boundary")
	}
}

func TestLegacyIgnoreBatchBudgetRollsBackAllQueries(t *testing.T) {
	f, l, root := legacyManifestFixture(t)
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET max_directories=1 WHERE id=$1::uuid`, l.Job.ID); err != nil {
		t.Fatal(err)
	}
	p := manifestChild(root.Proofs[0].IgnoreDirectoryProof, "child")
	child := domain.LegacyIgnoreObservation{Version: root.Version, Directory: p.Directory, Proofs: []domain.LegacyIgnoreDirectoryProof{root.Proofs[0], {IgnoreDirectoryProof: p, Checked: true}}}
	if err := f.s.RecordLegacyIgnoreObservations(f.ctx, l, []domain.LegacyIgnoreObservation{root, child}); err != domain.ErrScanLimit {
		t.Fatal("budget", err)
	}
	var count int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM job_ignore_legacy_manifests WHERE job_id=$1::uuid`, l.Job.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("overbudget batch persisted", err, count)
	}
}
