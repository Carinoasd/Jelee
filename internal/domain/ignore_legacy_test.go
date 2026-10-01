package domain

import (
	"encoding/json"
	"testing"
)

func legacyObservationFixture() LegacyIgnoreObservation {
	id := "11111111-1111-4111-8111-111111111111"
	root := IgnoreDirectoryProof{RootID: id, Directory: ".", Identity: [32]byte{1}}
	middle := IgnoreDirectoryProof{RootID: id, Directory: "a", Identity: [32]byte{2}, ParentIdentity: root.Identity, RulePresent: true, RuleIdentity: [32]byte{4}, RuleSHA256: [32]byte{5}}
	leaf := IgnoreDirectoryProof{RootID: id, Directory: "a/b", Identity: [32]byte{3}, ParentIdentity: middle.Identity}
	return LegacyIgnoreObservation{Version: LegacyIgnoreProofVersion, Directory: "a/b", Proofs: []LegacyIgnoreDirectoryProof{{IgnoreDirectoryProof: root}, {IgnoreDirectoryProof: middle, Checked: true}, {IgnoreDirectoryProof: leaf, Checked: true}}}
}
func TestLegacyObservationShape(t *testing.T) {
	valid := legacyObservationFixture()
	if ValidateLegacyIgnoreObservation(valid) != nil {
		t.Fatal("valid nearest source rejected")
	}
	raw, err := json.Marshal(valid)
	if err != nil || string(raw) != "{}" {
		t.Fatal("private observation leaked")
	}
	for _, mutate := range []func(*LegacyIgnoreObservation){
		func(o *LegacyIgnoreObservation) { o.Version = IgnoreProofVersion },
		func(o *LegacyIgnoreObservation) { o.Directory = "a" },
		func(o *LegacyIgnoreObservation) { o.Proofs = nil },
		func(o *LegacyIgnoreObservation) { o.Proofs[2].Checked = false },
		func(o *LegacyIgnoreObservation) { o.Proofs[1].Checked = false },
		func(o *LegacyIgnoreObservation) { o.Proofs[0].Checked = true },
		func(o *LegacyIgnoreObservation) { o.Proofs[2].RootID = "22222222-2222-4222-8222-222222222222" },
		func(o *LegacyIgnoreObservation) { o.Proofs[2].ParentIdentity = [32]byte{7} },
		func(o *LegacyIgnoreObservation) {
			o.Proofs[2].MissingDirectory = true
			o.Proofs[2].Identity = [32]byte{}
		},
		func(o *LegacyIgnoreObservation) {
			o.Proofs[2].RulePresent = true
			o.Proofs[2].RuleIdentity = [32]byte{6}
			o.Proofs[2].RuleSHA256 = [32]byte{7}
		},
	} {
		candidate := legacyObservationFixture()
		mutate(&candidate)
		if ValidateLegacyIgnoreObservation(candidate) != ErrInvalid {
			t.Fatal("invalid chain accepted")
		}
	}
}
func TestLegacyObservationNoSourceRequiresRootSearch(t *testing.T) {
	o := legacyObservationFixture()
	middle := &o.Proofs[1]
	middle.RulePresent = false
	middle.RuleIdentity = [32]byte{}
	middle.RuleSHA256 = [32]byte{}
	if ValidateLegacyIgnoreObservation(o) != ErrInvalid {
		t.Fatal("shadowed root treated as absent")
	}
	o.Proofs[0].Checked = true
	if ValidateLegacyIgnoreObservation(o) != nil {
		t.Fatal("complete absence rejected")
	}
	o.Proofs = o.Proofs[:1]
	o.Directory = "."
	if ValidateLegacyIgnoreObservation(o) != nil {
		t.Fatal("root-only absence rejected")
	}
}

func TestLegacyRepeatedProofCompatibility(t *testing.T) {
	observed := legacyObservationFixture().Proofs[1]
	shadowed := observed
	shadowed.Checked = false
	shadowed.RulePresent = false
	shadowed.RuleIdentity = [32]byte{}
	shadowed.RuleSHA256 = [32]byte{}
	if !LegacyIgnoreProofsCompatible(observed, shadowed) || !LegacyIgnoreProofsCompatible(shadowed, observed) {
		t.Fatal("unread to observed transition rejected")
	}
	absent := shadowed
	absent.Checked = true
	if LegacyIgnoreProofsCompatible(absent, observed) {
		t.Fatal("observed absence changed into presence")
	}
	changed := observed
	changed.RuleSHA256 = [32]byte{8}
	if LegacyIgnoreProofsCompatible(observed, changed) {
		t.Fatal("changed rule accepted")
	}
	changed = shadowed
	changed.Identity = [32]byte{9}
	if LegacyIgnoreProofsCompatible(observed, changed) {
		t.Fatal("changed directory accepted")
	}
	if !LegacyIgnoreProofsCompatible(observed, observed) {
		t.Fatal("identical observation rejected")
	}
}
