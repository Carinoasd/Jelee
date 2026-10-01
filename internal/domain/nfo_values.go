package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"time"
)

func ValidateNFOCachePolicy(p NFOCachePolicy) error {
	if p.MaxRows < 1000 || p.MaxRows > 1000000 || p.LibraryMaxRows < 1000 || p.LibraryMaxRows > 500000 || p.LibraryMaxRows > p.MaxRows ||
		p.MaxBytes < 16<<20 || p.MaxBytes > 16<<30 || p.LibraryMaxBytes < 16<<20 || p.LibraryMaxBytes > p.MaxBytes ||
		p.MaxLibraries < 1 || p.MaxLibraries > 1024 || p.PositiveTTL < time.Hour || p.PositiveTTL > 90*24*time.Hour ||
		p.NegativeTTL < time.Minute || p.NegativeTTL > 24*time.Hour || p.NegativeTTL > p.PositiveTTL ||
		p.PositiveTTL%time.Second != 0 || p.NegativeTTL%time.Second != 0 {
		return ErrInvalid
	}
	return nil
}

func ValidNFOMode(mode string) bool { return mode == NFOModeOff || mode == NFOModeReadOnly }

func ValidateNFOLibraryPolicy(p NFOLibraryPolicy) error {
	if !ValidID(p.LibraryID) || !ValidNFOMode(p.Mode) || p.Generation < 1 {
		return ErrInvalid
	}
	return nil
}

// Keys use the same visible ASCII boundary as job admission. Retained ledger
// comparisons include the entire library/mode/expected-generation intent.
func ValidateNFOPolicyUpdate(libraryID, key string, expectedGeneration int64, mode string) error {
	if !ValidID(libraryID) || !ValidNFOMode(mode) || expectedGeneration < 1 || len(key) < 1 || len(key) > 128 {
		return ErrInvalid
	}
	for _, value := range key {
		if value < 33 || value > 126 {
			return ErrInvalid
		}
	}
	return nil
}

func ValidateNFOIdentity(v NFOIdentity) error {
	if v.ParserVersion != NFOParserVersion || v.SummarySchemaVersion != NFOSummarySchemaVersion ||
		v.FingerprintVersion != NFOFingerprintVersion || v.MaxSourceBytes < 1 || v.MaxSourceBytes > NFOMaxSourceBytes {
		return ErrInvalid
	}
	return nil
}

// The ordered encoding and domain prefix distinguish these keys from media
// probe identities. No caller map ordering, platform or mutable manifest enters.
func NFOIdentityDigest(v NFOIdentity) (string, error) {
	if err := ValidateNFOIdentity(v); err != nil {
		return "", err
	}
	fields := []string{v.ParserVersion, strconv.Itoa(v.SummarySchemaVersion), v.FingerprintVersion, strconv.FormatInt(v.MaxSourceBytes, 10)}
	encoded, _ := json.Marshal(fields) // Fixed strings and integers cannot fail.
	digest := sha256.Sum256(append([]byte("jelee-nfo-identity-v1\x00"), encoded...))
	return hex.EncodeToString(digest[:]), nil
}

func ValidateNFOStamp(v NFOStamp) error {
	if v.Size < 0 || v.Size > NFOMaxSourceBytes || v.FingerprintVersion != NFOFingerprintVersion || !probeHex(v.SHA256, 64) {
		return ErrInvalid
	}
	return nil
}

func ValidateNFOPageToken(v NFOPageToken) error {
	if v.Revision < 1 || v.AfterID != "" && !ValidID(v.AfterID) {
		return ErrInvalid
	}
	return nil
}

func ValidateNFOCandidate(v NFOCandidate) error {
	if !ValidID(v.InventoryID) {
		return ErrInvalid
	}
	return ValidateNFOStamp(v.Stamp)
}

func ValidateNFOCandidateBatch(v []NFOCandidate) error {
	if len(v) < 1 || len(v) > NFOBatchMax {
		return ErrInvalid
	}
	for i, candidate := range v {
		if ValidateNFOCandidate(candidate) != nil || i > 0 && candidate.InventoryID <= v[i-1].InventoryID {
			return ErrInvalid
		}
	}
	return nil
}

func ValidNFOParseFailureCode(v NFOFailureCode) bool {
	switch v {
	case NFOFailureInvalidXML, NFOFailureUnsafeXML, NFOFailureInvalidEncoding, NFOFailureUnsupportedEncoding, NFOFailureTooComplex:
		return true
	}
	return false
}

func ValidNFOPhaseError(v NFOPhaseError) bool {
	switch v {
	case NFOPhaseDisabled, NFOPhaseInvalidated, NFOPhaseCapacity, NFOPhaseIdentityMismatch, NFOPhaseUnavailable, NFOPhaseCancelled:
		return true
	}
	return false
}

func ValidateNFOCompletion(v NFOCompletion) error {
	if !ValidID(v.Candidate.InventoryID) {
		return ErrInvalid
	}
	switch v.Kind {
	case NFOCompletionHit, NFOCompletionNegativeHit:
		if v.Summary != nil || v.FailureCode != "" || ValidateNFOCandidate(v.Candidate) != nil {
			return ErrInvalid
		}
	case NFOCompletionParsed:
		if v.Summary == nil || v.FailureCode != "" || ValidateNFOCandidate(v.Candidate) != nil {
			return ErrInvalid
		}
		_, err := MarshalNFOSummary(*v.Summary)
		return err
	case NFOCompletionChanged, NFOCompletionUnavailable:
		if v.Summary != nil || v.FailureCode != "" || v.Candidate.Stamp != (NFOStamp{}) {
			return ErrInvalid
		}
	case NFOCompletionRejected:
		if v.Summary != nil || v.FailureCode != NFOFailureTooLarge || v.Candidate.Stamp != (NFOStamp{}) {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

func ValidateNFOCompletionBatch(v []NFOCompletion) error {
	if len(v) < 1 || len(v) > NFOBatchMax {
		return ErrInvalid
	}
	for i, completion := range v {
		if err := ValidateNFOCompletion(completion); err != nil {
			return err
		}
		if i > 0 && completion.Candidate.InventoryID <= v[i-1].Candidate.InventoryID ||
			len(v) > 1 && completion.Kind != NFOCompletionHit && completion.Kind != NFOCompletionNegativeHit {
			return ErrInvalid
		}
	}
	return nil
}

func ValidateNFOProgress(v NFOProgress) error {
	for _, count := range []int64{v.Processed, v.Hits, v.NegativeHits, v.Parsed, v.Valid, v.Invalid, v.WarningFiles, v.Changed, v.Unavailable, v.Rejected} {
		if count < 0 || count > 500000 {
			return ErrInvalid
		}
	}
	if v.Processed != v.Hits+v.NegativeHits+v.Parsed+v.Changed+v.Unavailable+v.Rejected ||
		v.Valid+v.Invalid != v.Hits+v.NegativeHits+v.Parsed || v.Valid < v.Hits || v.Invalid < v.NegativeHits || v.WarningFiles > v.Valid+v.Invalid {
		return ErrInvalid
	}
	return nil
}

// ValidateNFOPhase checks a phase for the current parser. Database cleanup must
// still allow a fenced Abort of a retained old-version phase without repinning
// its immutable identity; this validator does not authorize such execution.
func ValidateNFOPhase(v NFOPhase) error {
	if !ValidID(v.JobID) || !ValidID(v.LibraryID) || !ValidNFOMode(v.Mode) || v.LibraryGeneration < 1 ||
		ValidateNFOPageToken(v.Token) != nil || ValidateNFOProgress(v.Progress) != nil {
		return ErrInvalid
	}
	digest, err := NFOIdentityDigest(v.Identity)
	if err != nil || digest != v.IdentityDigest {
		return ErrInvalid
	}
	if v.Mode == NFOModeOff && (v.State != NFOPhaseAborted || v.ErrorCode != NFOPhaseDisabled || v.Progress != (NFOProgress{}) || v.Token.AfterID != "") {
		return ErrInvalid
	}
	switch v.State {
	case NFOPhaseWaiting:
		if v.Progress != (NFOProgress{}) || v.Token.AfterID != "" || v.ErrorCode != "" {
			return ErrInvalid
		}
	case NFOPhaseRunning, NFOPhaseDone:
		if v.ErrorCode != "" {
			return ErrInvalid
		}
	case NFOPhaseAborted:
		if !ValidNFOPhaseError(v.ErrorCode) {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	if (v.Progress.Processed == 0) != (v.Token.AfterID == "") {
		return ErrInvalid
	}
	return nil
}
