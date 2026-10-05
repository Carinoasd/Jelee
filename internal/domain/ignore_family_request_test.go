package domain

import "testing"

func TestFamilyIgnoreRequestContract(t *testing.T) {
	base := ignoreTestRequest()
	base.Intent.Mode = IgnoreModeFamily
	base.Identity = IgnoreIdentity{ProgramVersion: IgnoreModeFamily, ProofVersion: IgnoreFamilyProofVersion}
	for _, c := range []string{IgnoreCaseSensitive, IgnoreCaseASCIIInsensitive} {
		v := base
		v.Intent.CaseMode = c
		if ValidateFamilyIgnoreRequest(v) != nil {
			t.Fatal("valid retained contract rejected")
		}
		if ValidateIgnoreRequest(v) != ErrInvalid || ValidateIgnoreIntent(v.Intent) != ErrInvalid {
			t.Fatal("public contract widened")
		}
	}
	for _, change := range []func(*IgnoreRequest){
		func(v *IgnoreRequest) { v.JobID = "invalid" },
		func(v *IgnoreRequest) { v.LibraryID = "invalid" },
		func(v *IgnoreRequest) { v.Intent.Mode = IgnoreModeJeleeignore },
		func(v *IgnoreRequest) { v.Intent.CaseMode = "" },
		func(v *IgnoreRequest) { v.Identity = DefaultIgnoreIdentity() },
		func(v *IgnoreRequest) { v.Identity.ProofVersion = LegacyIgnoreProofVersion },
	} {
		v := base
		change(&v)
		if ValidateFamilyIgnoreRequest(v) != ErrInvalid {
			t.Fatal("mixed contract accepted")
		}
	}
}

func TestFamilyScanIntentContract(t *testing.T) {
	for _, mode := range []string{"", IgnoreModeJeleeignore, IgnoreModeFamily} {
		for _, caseMode := range []string{IgnoreCaseSensitive, IgnoreCaseASCIIInsensitive} {
			intent := ScanIntent{NFO: true, Probe: ProbeIntent{Scope: ProbeScopeIncremental}}
			if mode != "" {
				intent.Ignore = IgnoreIntent{Mode: mode, CaseMode: caseMode}
			}
			if ValidateScanIntentWithFamilyIgnore(intent) != nil {
				t.Fatal("supported combination rejected")
			}
			if mode == IgnoreModeFamily && ValidateScanIntent(intent) != ErrInvalid {
				t.Fatal("old contract widened")
			}
		}
	}
	for _, intent := range []ScanIntent{
		{Ignore: IgnoreIntent{Mode: IgnoreModeFamily}},
		{Ignore: IgnoreIntent{Mode: IgnoreModeFamily, CaseMode: "unicode-insensitive"}},
		{Ignore: IgnoreIntent{Mode: "unknown", CaseMode: IgnoreCaseSensitive}},
		{Ignore: IgnoreIntent{Mode: IgnoreModeFamily, CaseMode: IgnoreCaseSensitive}, Probe: ProbeIntent{Scope: ProbeScopeLibraryRebuild}},
	} {
		if ValidateScanIntentWithFamilyIgnore(intent) != ErrInvalid {
			t.Fatal("invalid combination accepted")
		}
	}
	r := ignoreTestRequest()
	r.Intent.Mode = IgnoreModeFamily
	r.Identity = DefaultFamilyIgnoreIdentity()
	if ValidateFamilyIgnoreRequest(r) != nil {
		t.Fatal("compiled identity disagrees with contract")
	}
}
