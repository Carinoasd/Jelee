package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"runtime"
	"strings"
	"testing"
)

func TestNFOWritePreparationRequestBoundsAndOwnedBytes(t *testing.T) {
	request := NFOWritePrepareRequest{ItemID: nfoID1, Revision: 1, Edits: []NFOWriteTextEdit{{Field: "title", Value: "新值"}}, MaxBytes: NFODefaultSourceBytes, Backups: 1}
	digest, err := NFOWriteRequestDigest(request)
	if err != nil || len(digest) != 64 {
		t.Fatal("valid request rejected")
	}
	for _, mutate := range []func(*NFOWritePrepareRequest){
		func(v *NFOWritePrepareRequest) { v.ItemID = "bad" },
		func(v *NFOWritePrepareRequest) { v.Revision = ItemMetadataRevisionMax },
		func(v *NFOWritePrepareRequest) { v.MaxBytes = NFOMaxSourceBytes + 1 },
		func(v *NFOWritePrepareRequest) { v.Backups = 17 },
		func(v *NFOWritePrepareRequest) { v.Edits[0].Field = "uniqueid" },
		func(v *NFOWritePrepareRequest) { v.Edits = append(v.Edits, v.Edits[0]) },
		func(v *NFOWritePrepareRequest) { v.Edits[0].Value = "\xff" },
		func(v *NFOWritePrepareRequest) { v.BOM = "utf16" },
		func(v *NFOWritePrepareRequest) { v.Indent = "<tag>" },
	} {
		bad := CloneNFOWritePrepareRequest(request)
		mutate(&bad)
		if _, err := NFOWriteRequestDigest(bad); err != ErrInvalid {
			t.Fatal("invalid request accepted")
		}
	}
	copy := CloneNFOWritePrepareRequest(request)
	copy.Backups++
	other, _ := NFOWriteRequestDigest(copy)
	if other == digest {
		t.Fatal("backup intent omitted from digest")
	}
	bytes := []byte("<movie/>")
	hash := sha256.Sum256(bytes)
	scope := NFOItemScope{ItemID: nfoID1, LibraryID: nfoID2, SourceID: nfoID1, RootID: nfoID2, Kind: "Movie", Revision: 1, Generation: 1, MediaPath: "film.mkv", Source: NFOSource{RootPath: "private", RelativePath: "film.nfo"}}
	value := NFOWritePreparation{Version: NFOWritePreparationVersion, Request: request, Scope: scope, Stamp: NFOStamp{Size: int64(len(bytes)), SHA256: hex.EncodeToString(hash[:]), FingerprintVersion: NFOFingerprintVersion}, Original: bytes, Replacement: append([]byte(nil), bytes...)}
	if err := ValidateNFOWritePreparation(value); err != nil {
		t.Fatal(err)
	}
	owned := CloneNFOWritePreparation(value)
	owned.Original[0] = 0
	owned.Replacement[0] = 0
	owned.Request.Edits[0].Value = "caller"
	if value.Original[0] == 0 || value.Replacement[0] == 0 || value.Request.Edits[0].Value == "caller" {
		t.Fatal("preparation ownership leaked")
	}
	value.Stamp.SHA256 = "bad"
	if ValidateNFOWritePreparation(value) != ErrInvalid {
		t.Fatal("stamp mismatch accepted")
	}
}

func TestNFOWritePreparationJSONSizeBeforeAllocation(t *testing.T) {
	request := NFOWritePrepareRequest{ItemID: nfoID1, Revision: 1, Edits: []NFOWriteTextEdit{{Field: "plot"}}, MaxBytes: NFOMaxSourceBytes, Backups: 1}
	for _, value := range []string{"", "plain", "<>&\"\\\b\f\n\r\t\x00\x01", "繁體中文🙂\u2028\u2029"} {
		request.Edits[0].Value = value
		encoded, _ := json.Marshal(request)
		if nfoWriteRequestBytes(request) != int64(len(encoded)) {
			t.Fatal("JSON escape accounting differs")
		}
	}
	request.Edits[0].Value = ""
	envelope, _ := json.Marshal(request)
	request.Edits[0].Value = strings.Repeat("x", NFOMaxSourceBytes-len(envelope))
	if ValidateNFOWritePrepareRequest(request) != nil {
		t.Fatal("exact maximum encoded request rejected")
	}
	request.Edits[0].Value += "x"
	if ValidateNFOWritePrepareRequest(request) != ErrInvalid {
		t.Fatal("encoded request above maximum accepted")
	}
	request.Edits[0].Value = strings.Repeat("<", 8<<20)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	err := ValidateNFOWritePrepareRequest(request)
	runtime.ReadMemStats(&after)
	if err != ErrInvalid || after.TotalAlloc-before.TotalAlloc > 1<<20 {
		t.Fatal("oversized escaped input allocated a complete JSON payload")
	}
}
