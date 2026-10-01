package domain

// ValidateNFORequest permits execution only by the current trusted parser.
// Loading retained old identities for history/abort must not use this validator
// to prevent cleanup. A nil identity for an off enqueue is resolved beforehand.
func ValidateNFORequest(v NFORequest) error {
	if !ValidID(v.JobID) || !ValidID(v.LibraryID) || !ValidNFOMode(v.Mode) || v.LibraryGeneration < 1 ||
		v.Requested != (v.Mode == NFOModeReadOnly) || v.ErrorCode != "" && !ValidNFOPhaseError(v.ErrorCode) ||
		!v.Requested && v.ErrorCode != "" {
		return ErrInvalid
	}
	digest, err := NFOIdentityDigest(v.Identity)
	if err != nil || digest != v.IdentityDigest {
		return ErrInvalid
	}
	return nil
}

// Matching does not authorize execution; validate the identity and the live DB
// fence separately. It also remains useful for retained old-version records.
func NFORequestMatchesPhase(v NFORequest, phase NFOPhase) bool {
	return v.JobID == phase.JobID && v.LibraryID == phase.LibraryID && v.Mode == phase.Mode &&
		v.Identity == phase.Identity && v.IdentityDigest == phase.IdentityDigest &&
		v.LibraryGeneration == phase.LibraryGeneration
}

func (v NFOJobSummary) Progress() NFOProgress {
	return NFOProgress{Processed: v.Processed, Hits: v.Hits, NegativeHits: v.NegativeHits,
		Parsed: v.Parsed, Valid: v.Valid, Invalid: v.Invalid, WarningFiles: v.WarningFiles,
		Changed: v.Changed, Unavailable: v.Unavailable, Rejected: v.Rejected}
}

func ValidNFOJobError(code string) bool {
	if ValidNFOPhaseError(NFOPhaseError(code)) {
		return true
	}
	switch code {
	case "scan_unavailable", "scan_io", "scan_limit", "scan_failed", "job_timeout", "job_attempts_exhausted":
		return true
	}
	return false
}

func ValidateNFOJobSummary(v NFOJobSummary) error {
	if !ValidID(v.JobID) || !ValidID(v.LibraryID) || !ValidNFOMode(v.Mode) || ValidateNFOProgress(v.Progress()) != nil {
		return ErrInvalid
	}
	if v.Mode == NFOModeOff {
		if v.Phase != NFOSummaryDisabled || v.ErrorCode != "" || v.Progress() != (NFOProgress{}) {
			return ErrInvalid
		}
		return nil
	}
	switch v.Phase {
	case NFOSummaryWaitingScan:
		if v.ErrorCode != "" || v.Progress() != (NFOProgress{}) {
			return ErrInvalid
		}
	case NFOSummaryRunning, NFOSummaryDone, NFOSummaryCancelled:
		if v.ErrorCode != "" {
			return ErrInvalid
		}
	case NFOSummaryAborted:
		if !ValidNFOJobError(v.ErrorCode) {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}
