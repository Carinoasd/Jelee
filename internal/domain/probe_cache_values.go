package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

func probeText(s string, max int) bool {
	return len(s) > 0 && len(s) <= max && utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl)
}
func probeHex(s string, size int) bool {
	if len(s) != size {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func ValidateProbeCachePolicy(p ProbeCachePolicy) error {
	if p.MaxRows < 1000 || p.MaxRows > 1000000 || p.LibraryMaxRows < 1000 || p.LibraryMaxRows > 500000 || p.LibraryMaxRows > p.MaxRows ||
		p.MaxBytes < 16<<20 || p.MaxBytes > 16<<30 || p.LibraryMaxBytes < 16<<20 || p.LibraryMaxBytes > p.MaxBytes ||
		p.MaxLibraries < 1 || p.MaxLibraries > 1024 || p.MaxToolVersions < 1 || p.MaxToolVersions > 32 || p.MaxLeases < 1 || p.MaxLeases > 8 ||
		p.PositiveTTL < time.Hour || p.PositiveTTL > 90*24*time.Hour || p.NegativeTTL < time.Minute || p.NegativeTTL > 24*time.Hour ||
		p.LeaseDuration < 10*time.Second || p.LeaseDuration > 5*time.Minute ||
		p.PositiveTTL%time.Second != 0 || p.NegativeTTL%time.Second != 0 || p.LeaseDuration%time.Second != 0 {
		return ErrInvalid
	}
	return nil
}

func ValidateProbeIdentity(v ProbeIdentity) error {
	if v.Platform != "linux-amd64" || !probeText(v.VendorVersion, 256) || !probeText(v.UpstreamVersion, 64) || !probeHex(v.SourceRevision, 40) ||
		!probeHex(v.ExecutableSHA256, 64) || !probeHex(v.RuntimeSHA256, 64) || !probeHex(v.ArgumentsSHA256, 64) ||
		v.ParserVersion != ProbeParserVersion || v.MetadataSchemaVersion != ProbeMetadataSchemaVersion || v.FingerprintVersion != ProbeFingerprintVersion ||
		!probeText(v.SandboxVersion, 64) {
		return ErrInvalid
	}
	return nil
}

// ProbeIdentityDigest has a versioned, unambiguous encoding. JSON field order
// below is explicit; no caller map ordering or optional-field elision is used.
func ProbeIdentityDigest(v ProbeIdentity) (string, error) {
	if err := ValidateProbeIdentity(v); err != nil {
		return "", err
	}
	fields := []string{v.Platform, v.VendorVersion, v.UpstreamVersion, v.SourceRevision, v.ExecutableSHA256, v.RuntimeSHA256, v.ParserVersion, strconv.Itoa(v.MetadataSchemaVersion), v.ArgumentsSHA256, v.SandboxVersion, v.FingerprintVersion}
	encoded, err := json.Marshal(fields)
	if err != nil {
		return "", ErrInvalid
	}
	checksum := sha256.Sum256(append([]byte("jelee-probe-identity-v1\x00"), encoded...))
	return hex.EncodeToString(checksum[:]), nil
}

func ValidateProbeIdentityRef(v ProbeIdentityRef) error {
	if !ValidID(v.ID) || !probeHex(v.Digest, 64) {
		return ErrInvalid
	}
	return nil
}
func ValidateProbeStamp(v ProbeStamp) error {
	if v.Size < 0 || v.FingerprintVersion != ProbeFingerprintVersion || !probeHex(v.Fingerprint, 64) {
		return ErrInvalid
	}
	return nil
}
func ValidateProbePhaseStart(v ProbePhaseStart) error {
	if ValidateProbeIdentityRef(v.Identity) != nil {
		return ErrInvalid
	}
	switch v.Scope {
	case ProbeScopeIncremental, ProbeScopeLibraryRebuild:
		if v.TargetItemID != "" {
			return ErrInvalid
		}
	case ProbeScopeItemRebuild:
		if !ValidID(v.TargetItemID) {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}
func ValidateProbePageToken(v ProbePageToken) error {
	if v.Revision < 1 || v.AfterID != "" && !ValidID(v.AfterID) {
		return ErrInvalid
	}
	return nil
}
func ValidateProbeCandidate(v ProbeCandidate) error {
	if !ValidID(v.InventoryID) {
		return ErrInvalid
	}
	return ValidateProbeStamp(v.Stamp)
}
func ValidateProbeCandidateBatch(v []ProbeCandidate) error {
	if len(v) < 1 || len(v) > ProbeBatchMax {
		return ErrInvalid
	}
	for i, e := range v {
		if ValidateProbeCandidate(e) != nil || i > 0 && e.InventoryID <= v[i-1].InventoryID {
			return ErrInvalid
		}
	}
	return nil
}
func validProbePath(path string) bool {
	return path != "." && probeText(path, ScanPathMaxBytes) && fs.ValidPath(path) && !strings.ContainsAny(path, "\\:") && strings.Count(path, "/") < 128
}
func ValidateProbeLease(v ProbeLease) error {
	if !ValidID(v.InventoryID) || !ValidID(v.RootID) || !ValidID(v.LibraryID) || !ValidID(v.JobID) || !validProbePath(v.Path) ||
		!probeText(v.Owner, 128) || v.Generation < 1 || v.JobGeneration < 1 || v.LibraryGeneration < 1 || v.RootGeneration < 1 || v.ExpiresAt.IsZero() ||
		ValidateProbeIdentityRef(v.Identity) != nil || ValidateProbeStamp(v.Stamp) != nil {
		return ErrInvalid
	}
	if v.ItemID == "" {
		if v.ItemGeneration != 0 {
			return ErrInvalid
		}
	} else if !ValidID(v.ItemID) || v.ItemGeneration < 1 {
		return ErrInvalid
	}
	return nil
}
func ValidProbeFailureCode(v ProbeFailureCode) bool {
	switch v {
	case ProbeFailureMedia, ProbeFailureMetadataInvalid, ProbeFailureMetadataLimit, ProbeFailureOutputLimit, ProbeFailureTimeout:
		return true
	}
	return false
}
func ValidProbePhaseError(v ProbePhaseError) bool {
	switch v {
	case ProbePhaseRuntimeUnavailable, ProbePhaseCapacity, ProbePhaseInvalidated, ProbePhaseIdentityMismatch:
		return true
	}
	return false
}
func ValidateProbeCompletion(v ProbeCompletion) error {
	if !ValidID(v.Candidate.InventoryID) {
		return ErrInvalid
	}
	if v.Lease != nil && (ValidateProbeLease(*v.Lease) != nil || v.Lease.InventoryID != v.Candidate.InventoryID || v.Lease.Stamp != v.Candidate.Stamp) {
		return ErrInvalid
	}
	switch v.Kind {
	case ProbeCompletionHit, ProbeCompletionNegativeHit:
		if v.Lease != nil || v.Metadata != nil || v.FailureCode != "" || ValidateProbeCandidate(v.Candidate) != nil {
			return ErrInvalid
		}
	case ProbeCompletionSucceeded:
		if v.Lease == nil || v.Metadata == nil || v.FailureCode != "" {
			return ErrInvalid
		}
		if _, err := MarshalProbeMetadata(*v.Metadata); err != nil {
			return err
		}
	case ProbeCompletionFailed:
		if v.Lease == nil || v.Metadata != nil || !ValidProbeFailureCode(v.FailureCode) {
			return ErrInvalid
		}
	case ProbeCompletionChanged, ProbeCompletionUnavailable:
		if v.Metadata != nil || v.FailureCode != "" || v.Candidate.Stamp != (ProbeStamp{}) && ValidateProbeStamp(v.Candidate.Stamp) != nil {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}
func ValidateProbeCompletionBatch(v []ProbeCompletion) error {
	if len(v) < 1 || len(v) > ProbeBatchMax {
		return ErrInvalid
	}
	for i, e := range v {
		if ValidateProbeCompletion(e) != nil || i > 0 && e.Candidate.InventoryID <= v[i-1].Candidate.InventoryID || len(v) > 1 && e.Kind != ProbeCompletionHit && e.Kind != ProbeCompletionNegativeHit {
			return ErrInvalid
		}
	}
	return nil
}
