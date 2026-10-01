package domain

import (
	"reflect"
	"testing"
)

func TestLegacyObservationRestoreKeepsQueryBoundary(t *testing.T) {
	want := legacyObservationFixture()
	ledger := append([]LegacyIgnoreDirectoryProof(nil), want.Proofs...)
	ledger[0].Checked = true
	ledger[0].RulePresent = true
	ledger[0].RuleIdentity = [32]byte{8}
	ledger[0].RuleSHA256 = [32]byte{9}
	ledger[0].RuleSize = 7
	selected := "a"
	got, err := RestoreLegacyIgnoreObservation(want.Directory, &selected, ledger)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("lost shadowed ancestor boundary", err)
	}
	if !ledger[0].Checked || !ledger[0].RulePresent || ledger[0].RuleSize != 7 {
		t.Fatal("mutated retained ledger")
	}
	got.Proofs[0].Identity = [32]byte{17}
	if ledger[0].Identity != want.Proofs[0].Identity {
		t.Fatal("returned aliases")
	}
	root, err := RestoreLegacyIgnoreObservation(".", nil, []LegacyIgnoreDirectoryProof{{IgnoreDirectoryProof: want.Proofs[0].IgnoreDirectoryProof, Checked: true}})
	if err != nil || len(root.Proofs) != 1 || root.Proofs[0].RulePresent {
		t.Fatal("root absence", err)
	}
}

func TestLegacyObservationRestoreRejectsIncompleteEvidence(t *testing.T) {
	for _, kind := range []string{"unchecked", "changed-source", "unknown-selection", "missing-parent", "wrong-query", "extra-source", "nil-selection", "broken-identity"} {
		t.Run(kind, func(t *testing.T) {
			o := legacyObservationFixture()
			selected := "a"
			choice := &selected
			switch kind {
			case "unchecked":
				o.Proofs[2].Checked = false
			case "changed-source":
				o.Proofs[1] = LegacyIgnoreDirectoryProof{IgnoreDirectoryProof: IgnoreDirectoryProof{RootID: o.Proofs[1].RootID, Directory: "a", Identity: o.Proofs[1].Identity, ParentIdentity: o.Proofs[1].ParentIdentity}, Checked: true}
			case "unknown-selection":
				selected = "outside"
			case "missing-parent":
				o.Proofs = o.Proofs[1:]
			case "wrong-query":
				o.Directory = "a"
			case "extra-source":
				o.Proofs[2].RulePresent = true
				o.Proofs[2].RuleIdentity = [32]byte{11}
				o.Proofs[2].RuleSHA256 = [32]byte{12}
			case "nil-selection":
				choice = nil
			case "broken-identity":
				o.Proofs[1].ParentIdentity = [32]byte{99}
			}
			got, err := RestoreLegacyIgnoreObservation(o.Directory, choice, o.Proofs)
			if err != ErrInvalid || len(got.Proofs) != 0 {
				t.Fatal("incomplete evidence restored", err)
			}
		})
	}
}
