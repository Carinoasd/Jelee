package domain

import "testing"

func familyEvidenceFixture() FamilyBaselineEvaluation {
	root := IgnoreDirectoryProof{RootID: ignoreTestJobID, Directory: ".", Identity: [32]byte{1}}
	child := IgnoreDirectoryProof{RootID: ignoreTestJobID, Directory: "hidden", ParentIdentity: root.Identity, Identity: [32]byte{2}, RulePresent: true, RuleIdentity: [32]byte{3}, RuleSHA256: [32]byte{4}}
	return FamilyBaselineEvaluation{
		Decision:           FamilyIgnoreBaselineDecision{RootID: ignoreTestJobID, Path: "hidden/movie.mkv", Outcome: IgnoreBaselineExcluded, Family: IgnoreFamilyLegacy, Reason: IgnoreReasonBlank, RuleDirectory: "hidden", MatchedPath: "hidden"},
		CustomProofs:       []IgnoreDirectoryProof{root},
		LegacyObservations: []LegacyIgnoreBaselineObservation{{Version: LegacyIgnoreBaselineProofVersion, LookupDirectory: "hidden", Source: LegacyIgnoreObservation{Version: LegacyIgnoreProofVersion, Directory: "hidden", Proofs: []LegacyIgnoreDirectoryProof{{IgnoreDirectoryProof: root}, {IgnoreDirectoryProof: child, Checked: true}}}}},
	}
}

func TestFamilyBaselineEvidenceBinding(t *testing.T) {
	if ValidateFamilyBaselineEvaluation(familyEvidenceFixture()) != nil {
		t.Fatal("valid own-directory legacy source rejected")
	}
	for name, change := range map[string]func(*FamilyBaselineEvaluation){
		"missing_custom":          func(e *FamilyBaselineEvaluation) { e.CustomProofs = nil },
		"missing_legacy":          func(e *FamilyBaselineEvaluation) { e.LegacyObservations = nil },
		"different_root_identity": func(e *FamilyBaselineEvaluation) { e.CustomProofs[0].Identity = [32]byte{9} },
		"absent_selected_source": func(e *FamilyBaselineEvaluation) {
			p := &e.LegacyObservations[0].Source.Proofs[1]
			p.RulePresent = false
			p.RuleIdentity = [32]byte{}
			p.RuleSHA256 = [32]byte{}
			e.LegacyObservations[0].Source.Proofs[0].Checked = true
		},
		"shadowed_source": func(e *FamilyBaselineEvaluation) { e.Decision.RuleDirectory = "." },
		"unrelated_query": func(e *FamilyBaselineEvaluation) { e.LegacyObservations[0].LookupDirectory = "other" },
		"source_under_excluded_ancestor": func(e *FamilyBaselineEvaluation) {
			p := e.LegacyObservations[0].Source.Proofs[1].IgnoreDirectoryProof
			p.RulePresent = false
			p.RuleIdentity = [32]byte{}
			p.RuleSHA256 = [32]byte{}
			e.CustomProofs = append(e.CustomProofs, p)
		},
		"unknown_partial_evidence": func(e *FamilyBaselineEvaluation) {
			e.Decision = FamilyIgnoreBaselineDecision{RootID: ignoreTestJobID, Path: "hidden/movie.mkv", Outcome: IgnoreBaselineUnknown, Reason: IgnoreUnknownSource}
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := familyEvidenceFixture()
			change(&e)
			if ValidateFamilyBaselineEvaluation(e) != ErrInvalid {
				t.Fatal("invalid evidence accepted")
			}
		})
	}
}

func TestFamilyBaselineEvidenceMissingBoundary(t *testing.T) {
	e := familyEvidenceFixture()
	e.Decision = FamilyIgnoreBaselineDecision{RootID: ignoreTestJobID, Path: "gone/deep/movie.mkv", Outcome: IgnoreBaselineMissing}
	root := e.CustomProofs[0]
	missing := IgnoreDirectoryProof{RootID: ignoreTestJobID, Directory: "gone", ParentIdentity: root.Identity, MissingDirectory: true}
	e.CustomProofs = append(e.CustomProofs, missing)
	e.LegacyObservations = []LegacyIgnoreBaselineObservation{{Version: LegacyIgnoreBaselineProofVersion, LookupDirectory: "gone/deep", Source: LegacyIgnoreObservation{Version: LegacyIgnoreProofVersion, Directory: ".", Proofs: []LegacyIgnoreDirectoryProof{{IgnoreDirectoryProof: root, Checked: true}}}, MissingDirectory: missing}}
	if ValidateFamilyBaselineEvaluation(e) != nil {
		t.Fatal("confirmed boundary rejected")
	}
	e.CustomProofs = e.CustomProofs[:1]
	if ValidateFamilyBaselineEvaluation(e) != ErrInvalid {
		t.Fatal("incomplete custom ancestor chain accepted")
	}
	e.CustomProofs = append(e.CustomProofs, missing)
	e.CustomProofs[1].MissingDirectory = false
	e.CustomProofs[1].Identity = [32]byte{8}
	if ValidateFamilyBaselineEvaluation(e) != ErrInvalid {
		t.Fatal("cross-family absence contradiction accepted")
	}
}
