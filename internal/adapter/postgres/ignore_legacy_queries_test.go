package postgres

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestLegacyIgnoreQueryPagesRestoreOriginalSelection(t *testing.T) {
	f, l, root := legacyManifestFixture(t)
	if err := f.s.RecordLegacyIgnoreObservation(f.ctx, l, root); err != nil {
		t.Fatal(err)
	}
	if page, err := f.s.ReadLegacyIgnoreObservationPage(f.ctx, l, domain.IgnoreProofCursor{}); err != domain.ErrConflict || len(page) != 0 {
		t.Fatal("mutable page", err)
	}
	want := []domain.LegacyIgnoreObservation{root}
	for i := 0; i < 20; i++ {
		ancestor := root.Proofs[0]
		p := manifestChild(ancestor.IgnoreDirectoryProof, fmt.Sprintf("child-%02d", i))
		if i%2 == 0 {
			p.RulePresent = true
			p.RuleIdentity = [32]byte{7}
			p.RuleSHA256 = [32]byte{8}
			p.RuleSize = 3
			ancestor.Checked = false
			ancestor.RulePresent = false
			ancestor.RuleIdentity = [32]byte{}
			ancestor.RuleSHA256 = [32]byte{}
			ancestor.RuleSize = 0
			ancestor.RuleModifiedNano = 0
		}
		o := domain.LegacyIgnoreObservation{Version: domain.LegacyIgnoreProofVersion, Directory: p.Directory, Proofs: []domain.LegacyIgnoreDirectoryProof{ancestor, {IgnoreDirectoryProof: p, Checked: true}}}
		if err := f.s.RecordLegacyIgnoreObservation(f.ctx, l, o); err != nil {
			t.Fatal(err)
		}
		want = append(want, o)
	}
	if err := f.s.FreezeLegacyIgnoreManifest(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	var got []domain.LegacyIgnoreObservation
	var cursor domain.IgnoreProofCursor
	for i := 0; i < 3; i++ {
		page, err := f.s.ReadLegacyIgnoreObservationPage(f.ctx, l, cursor)
		if err != nil || len(page) > legacyObservationPageSize {
			t.Fatal("bounded restore", err)
		}
		if len(page) == 0 {
			break
		}
		got = append(got, page...)
		last := page[len(page)-1]
		cursor = domain.IgnoreProofCursor{RootID: last.Proofs[0].RootID, Directory: last.Directory}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("restored query selection differs from original")
	}
	root.Proofs[0].RuleSHA256 = [32]byte{99}
	if err := f.s.RecordLegacyIgnoreObservation(f.ctx, l, root); err != domain.ErrInventoryInvalidated {
		t.Fatal(err)
	}
	if page, err := f.s.ReadLegacyIgnoreObservationPage(f.ctx, l, domain.IgnoreProofCursor{}); err != domain.ErrConflict || len(page) != 0 {
		t.Fatal("invalidated page", err)
	}
}
