package domain

import "time"

const NFOItemObservationVersion = "nfo-item-observation-v1"

// LastConfirmedNFOObservation describes an accepted review, not current file
// freshness. It contains neither selected filenames nor parser field values.
type LastConfirmedNFOObservation struct {
	Version          string                   `json:"version"`
	Status           string                   `json:"status"`
	SourceID         string                   `json:"sourceId"`
	RootID           string                   `json:"rootId"`
	Generation       int64                    `json:"generation"`
	IdentityDigest   string                   `json:"identityDigest"`
	CandidateDigest  string                   `json:"candidateDigest"`
	ReadAt           time.Time                `json:"readAt"`
	AcceptedRevision int64                    `json:"acceptedRevision"`
	Stamp            *NFOItemObservationStamp `json:"stamp"`
}

type NFOItemObservationStamp struct {
	Size               int64  `json:"size"`
	ModifiedUnixNano   int64  `json:"modifiedUnixNano"`
	SHA256             string `json:"sha256"`
	FingerprintVersion string `json:"fingerprintVersion"`
}

func ConfirmedNFOObservation(scope NFOItemScope, state NFOItemObservationState) (LastConfirmedNFOObservation, error) {
	if !ValidNFOItemObservationState(scope, state) {
		return LastConfirmedNFOObservation{}, ErrInvalid
	}
	identity, err := NFOIdentityDigest(state.Identity)
	if err != nil {
		return LastConfirmedNFOObservation{}, err
	}
	value := LastConfirmedNFOObservation{Version: NFOItemObservationVersion, Status: state.Status, SourceID: scope.SourceID, RootID: scope.RootID, Generation: scope.Generation, IdentityDigest: identity, CandidateDigest: state.Selection.CandidateDigest, ReadAt: state.ReadAt.UTC(), AcceptedRevision: scope.Revision + 1}
	if state.Status != NFOItemObservedMissing {
		stamp := state.Stamp
		value.Stamp = &NFOItemObservationStamp{stamp.Size, stamp.ModifiedUnixNano, stamp.SHA256, stamp.FingerprintVersion}
	}
	if !ValidLastConfirmedNFOObservation(value) {
		return LastConfirmedNFOObservation{}, ErrInvalid
	}
	return value, nil
}

func ValidLastConfirmedNFOObservation(value LastConfirmedNFOObservation) bool {
	if value.Version != NFOItemObservationVersion || !ValidID(value.SourceID) || !ValidID(value.RootID) || value.Generation < 1 || !probeHex(value.IdentityDigest, 64) || !probeHex(value.CandidateDigest, 64) || value.ReadAt.IsZero() || value.ReadAt.Year() < 1 || value.ReadAt.Year() > 9999 || value.AcceptedRevision < 2 || value.AcceptedRevision > ItemMetadataRevisionMax {
		return false
	}
	switch value.Status {
	case NFOItemObservedMissing:
		return value.Stamp == nil && value.CandidateDigest == NFOCandidateDigest([]string{})
	case NFOItemObservedValid, NFOItemObservedInvalid:
		if value.Stamp == nil || value.CandidateDigest == NFOCandidateDigest([]string{}) {
			return false
		}
		stamp := value.Stamp
		return ValidateNFOStamp(NFOStamp{Size: stamp.Size, ModifiedUnixNano: stamp.ModifiedUnixNano, SHA256: stamp.SHA256, FingerprintVersion: stamp.FingerprintVersion}) == nil && stamp.Size <= NFOMaxSourceBytes
	}
	return false
}

func CloneLastConfirmedNFOObservation(value LastConfirmedNFOObservation) LastConfirmedNFOObservation {
	if value.Stamp != nil {
		stamp := *value.Stamp
		value.Stamp = &stamp
	}
	return value
}
