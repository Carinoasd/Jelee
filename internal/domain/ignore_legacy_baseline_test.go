package domain

import "testing"

func TestLegacyBaselineBoundaryCannotInventDescendantEvidence(t *testing.T) {
	source := legacyObservationFixture()
	leaf := source.Proofs[len(source.Proofs)-1]
	o := LegacyIgnoreBaselineObservation{Version: LegacyIgnoreBaselineProofVersion, LookupDirectory: "a/b/gone/deep", Source: source, MissingDirectory: IgnoreDirectoryProof{RootID: leaf.RootID, Directory: "a/b/gone", ParentIdentity: leaf.Identity, MissingDirectory: true}}
	if err := ValidateLegacyIgnoreBaselineObservation(o); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*LegacyIgnoreBaselineObservation){
		func(b *LegacyIgnoreBaselineObservation) { b.LookupDirectory = "a/b/other" },
		func(b *LegacyIgnoreBaselineObservation) { b.MissingDirectory.Directory = "a/b/gone/deep" },
		func(b *LegacyIgnoreBaselineObservation) { b.MissingDirectory.ParentIdentity = [32]byte{99} },
		func(b *LegacyIgnoreBaselineObservation) { b.MissingDirectory.Identity = [32]byte{99} },
		func(b *LegacyIgnoreBaselineObservation) { b.MissingDirectory = IgnoreDirectoryProof{} },
	} {
		bad := o
		mutate(&bad)
		if ValidateLegacyIgnoreBaselineObservation(bad) != ErrInvalid {
			t.Fatal("invented absence accepted")
		}
	}
	o.MissingDirectory = IgnoreDirectoryProof{}
	o.LookupDirectory = source.Directory
	if err := ValidateLegacyIgnoreBaselineObservation(o); err != nil {
		t.Fatal("existing source", err)
	}
}
