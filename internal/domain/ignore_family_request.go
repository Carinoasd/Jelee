package domain

const IgnoreFamilyProofVersion = "jeleeignore-legacy-proof-v1"

// ValidateFamilyIgnoreRequest describes the retained composed contract.
// Public admission and worker availability are checked separately.
func ValidateFamilyIgnoreRequest(v IgnoreRequest) error {
	if !ValidID(v.JobID) || !ValidID(v.LibraryID) || v.Intent.Mode != IgnoreModeFamily ||
		(v.Intent.CaseMode != IgnoreCaseSensitive && v.Intent.CaseMode != IgnoreCaseASCIIInsensitive) ||
		v.Identity.ProgramVersion != IgnoreModeFamily || v.Identity.ProofVersion != IgnoreFamilyProofVersion {
		return ErrInvalid
	}
	return nil
}
