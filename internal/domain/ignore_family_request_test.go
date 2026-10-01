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
