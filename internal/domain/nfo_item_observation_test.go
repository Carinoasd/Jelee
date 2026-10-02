package domain

import (
	"strings"
	"testing"
	"time"
)

func TestNFOItemObservationRejectsInventedProvenance(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	scope := NFOItemScope{ItemID: id, LibraryID: id, SourceID: id, RootID: id, Kind: "Movie", Revision: 1, Generation: 1, MediaPath: "dir/Film.mkv", Source: NFOSource{RootPath: "/trusted", RelativePath: "dir/Film.nfo"}}
	now := time.Now().UTC()
	stamp := NFOStamp{Size: 10, SHA256: strings.Repeat("a", 64), FingerprintVersion: NFOFingerprintVersion}
	missing := NFOItemObservationState{Status: NFOItemObservedMissing, Identity: DefaultNFOIdentity(), ReadAt: now, Selection: NFOItemSelection{CandidateDigest: NFOCandidateDigest([]string{})}}
	invalid := missing
	invalid.Status, invalid.Stamp = NFOItemObservedInvalid, stamp
	invalid.Selection.RelativePath = "dir/Film.nfo"
	invalid.Selection.CandidateDigest = NFOCandidateDigest([]string{"dir/Film.nfo"})
	valid := invalid
	valid.Status = NFOItemObservedValid
	valid.Selection.Fields = NFOItemFields{Version: NFOItemFieldsVersion, Kind: "Movie", Identity: valid.Identity, Stamp: stamp, ReadAt: now, Fields: []NFOTextField{{Field: "title", Value: "Local"}}}
	for _, state := range []NFOItemObservationState{valid, missing, invalid} {
		if !ValidNFOItemObservationState(scope, state) {
			t.Fatal("valid observation rejected", state.Status)
		}
	}
	for _, test := range []struct {
		name string
		base NFOItemObservationState
		edit func(*NFOItemObservationState)
	}{
		{"unknown-status", missing, func(v *NFOItemObservationState) { v.Status = "unavailable" }},
		{"missing-path", missing, func(v *NFOItemObservationState) { v.Selection.RelativePath = "dir/Film.nfo" }},
		{"missing-candidates", missing, func(v *NFOItemObservationState) { v.Selection.CandidateDigest = invalid.Selection.CandidateDigest }},
		{"missing-stamp", missing, func(v *NFOItemObservationState) { v.Stamp = stamp }},
		{"missing-fields", missing, func(v *NFOItemObservationState) { v.Selection.Fields = valid.Selection.Fields }},
		{"missing-lock", missing, func(v *NFOItemObservationState) { v.Selection.Fields.LockData = true }},
		{"missing-locked-fields", missing, func(v *NFOItemObservationState) { v.Selection.Fields.LockedFields = []string{"title"} }},
		{"missing-projection", missing, func(v *NFOItemObservationState) { v.Selection.Fields.Version = NFOItemFieldsVersion }},
		{"missing-field-identity", missing, func(v *NFOItemObservationState) { v.Selection.Fields.Identity = DefaultNFOIdentity() }},
		{"invalid-path", invalid, func(v *NFOItemObservationState) { v.Selection.RelativePath = "other/Film.nfo" }},
		{"invalid-without-hash", invalid, func(v *NFOItemObservationState) { v.Stamp.SHA256 = "" }},
		{"invalid-over-bound", invalid, func(v *NFOItemObservationState) { v.Stamp.Size = v.Identity.MaxSourceBytes + 1 }},
		{"invalid-partial-fields", invalid, func(v *NFOItemObservationState) { v.Selection.Fields = valid.Selection.Fields }},
		{"invalid-lock", invalid, func(v *NFOItemObservationState) { v.Selection.Fields.LockData = true }},
		{"invalid-empty-digest", invalid, func(v *NFOItemObservationState) { v.Selection.CandidateDigest = "" }},
		{"invalid-identity", invalid, func(v *NFOItemObservationState) { v.Identity.ParserVersion = "unknown" }},
		{"zero-read-time", missing, func(v *NFOItemObservationState) { v.ReadAt = time.Time{} }},
		{"nonfinite-read-time", missing, func(v *NFOItemObservationState) { v.ReadAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) }},
		{"valid-stamp-mismatch", valid, func(v *NFOItemObservationState) { v.Stamp.SHA256 = strings.Repeat("b", 64) }},
		{"valid-identity-mismatch", valid, func(v *NFOItemObservationState) { v.Identity.MaxSourceBytes /= 2 }},
		{"valid-time-mismatch", valid, func(v *NFOItemObservationState) { v.ReadAt = now.Add(time.Second) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := test.base
			test.edit(&state)
			if ValidNFOItemObservationState(scope, state) {
				t.Fatal("invented provenance accepted")
			}
		})
	}
}
