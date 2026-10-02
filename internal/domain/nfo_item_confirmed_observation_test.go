package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestConfirmedNFOObservationContainsNoPrivateSelection(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	scope := NFOItemScope{ItemID: id, LibraryID: id, SourceID: id, RootID: id, Kind: "Movie", Revision: 1, Generation: 2, MediaPath: "private/Secret.mkv", Source: NFOSource{RootPath: "/private-root", RelativePath: "private/Secret.nfo"}}
	now := time.Now().UTC()
	for _, status := range []string{NFOItemObservedValid, NFOItemObservedMissing, NFOItemObservedInvalid} {
		t.Run(status, func(t *testing.T) {
			state := NFOItemObservationState{Status: status, Identity: DefaultNFOIdentity(), ReadAt: now, Selection: NFOItemSelection{CandidateDigest: NFOCandidateDigest([]string{})}}
			if status != NFOItemObservedMissing {
				state.Selection.RelativePath = scope.Source.RelativePath
				state.Selection.CandidateDigest = NFOCandidateDigest([]string{scope.Source.RelativePath})
				state.Stamp = NFOStamp{Size: 10, SHA256: strings.Repeat("a", 64), FingerprintVersion: NFOFingerprintVersion}
			}
			if status == NFOItemObservedValid {
				state.Selection.Fields = NFOItemFields{Version: NFOItemFieldsVersion, Kind: "Movie", Identity: state.Identity, Stamp: state.Stamp, ReadAt: now, Fields: []NFOTextField{{Field: "title", Value: "Private field title"}}}
			}
			value, err := ConfirmedNFOObservation(scope, state)
			if err != nil || !ValidLastConfirmedNFOObservation(value) || value.AcceptedRevision != 2 || value.Generation != 2 || value.Status != status || !value.ReadAt.Equal(now) {
				t.Fatal("accepted review proof lost", err)
			}
			encoded, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{scope.MediaPath, scope.Source.RootPath, scope.Source.RelativePath, "Private field title", "RelativePath", "Fields"} {
				if strings.Contains(string(encoded), secret) {
					t.Fatal("confirmed observation leaked private selection")
				}
			}
			if status == NFOItemObservedMissing {
				if value.Stamp != nil || !strings.Contains(string(encoded), `"stamp":null`) {
					t.Fatal("missing observation invented file stamp")
				}
			} else {
				clone := CloneLastConfirmedNFOObservation(value)
				clone.Stamp.SHA256 = strings.Repeat("b", 64)
				if value.Stamp.SHA256 != state.Stamp.SHA256 {
					t.Fatal("caller changed retained file stamp")
				}
			}
			bad := state
			bad.ReadAt = time.Time{}
			if _, err := ConfirmedNFOObservation(scope, bad); err != ErrInvalid {
				t.Fatal("invalid observation converted into accepted proof", err)
			}
			for _, mutate := range []func(*LastConfirmedNFOObservation){
				func(v *LastConfirmedNFOObservation) { v.Version = "future" },
				func(v *LastConfirmedNFOObservation) { v.SourceID = "private-path" },
				func(v *LastConfirmedNFOObservation) { v.AcceptedRevision = 1 },
				func(v *LastConfirmedNFOObservation) { v.AcceptedRevision = ItemMetadataRevisionMax + 1 },
				func(v *LastConfirmedNFOObservation) { v.Status = "unknown" },
				func(v *LastConfirmedNFOObservation) { v.Generation = 0 },
				func(v *LastConfirmedNFOObservation) { v.IdentityDigest = "invalid" },
				func(v *LastConfirmedNFOObservation) {
					if v.Status == NFOItemObservedMissing {
						v.Stamp = &NFOItemObservationStamp{Size: 10, SHA256: strings.Repeat("a", 64), FingerprintVersion: NFOFingerprintVersion}
					} else {
						v.Stamp = nil
					}
				},
				func(v *LastConfirmedNFOObservation) {
					if v.Status == NFOItemObservedMissing {
						v.CandidateDigest = strings.Repeat("a", 64)
					} else {
						v.CandidateDigest = NFOCandidateDigest([]string{})
					}
				},
			} {
				bad := CloneLastConfirmedNFOObservation(value)
				mutate(&bad)
				if ValidLastConfirmedNFOObservation(bad) {
					t.Fatal("untrusted retained observation accepted")
				}
			}
		})
	}
}
