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

// A rewrite request is encoded exactly as before create existed, so the
// digests of stored preparations and job intents are unchanged; only a create
// request carries the key.
func TestNFOWriteRequestCreateKeyIsOmittedForRewrites(t *testing.T) {
	request := NFOWritePrepareRequest{ItemID: "a0000000-0000-4000-8000-000000000001", Revision: 1, Edits: []NFOWriteTextEdit{{Field: "title", Value: "x"}}, MaxBytes: NFODefaultSourceBytes, Backups: 3}
	encoded, err := json.Marshal(request)
	if err != nil || strings.Contains(string(encoded), "create\"") {
		t.Fatal("a rewrite request carries the create key", string(encoded))
	}
	want := `{"itemId":"a0000000-0000-4000-8000-000000000001","revision":1,"edits":[{"field":"title","value":"x"}],"createMissing":false,"bom":"","indent":"","maxBytes":8388608,"backups":3}`
	if string(encoded) != want {
		t.Fatal("rewrite encoding changed", string(encoded))
	}
	request.Create, request.Backups = true, 0
	if encoded, err = json.Marshal(request); err != nil || !strings.HasSuffix(string(encoded), `"backups":0,"create":true}`) {
		t.Fatal("a create request lacks its key", string(encoded), err)
	}
	var decoded NFOWritePrepareRequest
	if json.Unmarshal(encoded, &decoded) != nil || !decoded.Create {
		t.Fatal("create does not round trip")
	}
}

func TestNFOWriteCommitCreateShapes(t *testing.T) {
	file := func(n byte) [48]byte {
		var id [48]byte
		id[0], id[1], id[2], id[16] = 1, 2, 1, n
		return id
	}
	directory := func(n byte) [48]byte {
		id := file(n)
		id[2] = 2
		return id
	}
	plan := NFOWriteCommitFilePlan{Version: NFOWriteCommitCreatePlanVersion, TargetName: "film.nfo", ParentIdentity: directory(1)}
	if ValidateNFOWriteCommitFilePlan(plan) != nil || !plan.Create() {
		t.Fatal("create plan refused")
	}
	withTarget := plan
	withTarget.TargetIdentity = file(2)
	rewrite := withTarget
	rewrite.Version = NFOWriteCommitPlanVersion
	noTarget := plan
	noTarget.Version = NFOWriteCommitPlanVersion
	if ValidateNFOWriteCommitFilePlan(withTarget) == nil || ValidateNFOWriteCommitFilePlan(noTarget) == nil || ValidateNFOWriteCommitFilePlan(rewrite) != nil || rewrite.Create() {
		t.Fatal("plan shapes are not exclusive")
	}
	if ValidateNFOWriteCommitCreateReady(NFOWriteCommitFilesReady{OutputIdentity: file(3)}) != nil || ValidateNFOWriteCommitFilesReady(NFOWriteCommitFilesReady{OutputIdentity: file(3)}) == nil {
		t.Fatal("create ready shape differs")
	}
	if ValidateNFOWriteCommitCreateReady(NFOWriteCommitFilesReady{OutputIdentity: file(3), RollbackIdentity: file(4)}) == nil {
		t.Fatal("a create ready carries a rollback object")
	}
	// A create reservation charges two names and no original; a rewrite keeps
	// its strict bounds.
	empty := sha256.Sum256(nil)
	reservation := NFOWriteCommitAttemptReservation{OriginalHash: empty, ReplacementBytes: 100, RetainedBytes: NFOWriteCommitCreateAttemptRetainedBytes(100)}
	if reservation.RetainedBytes != 800 || ValidateNFOWriteCommitAttemptReservation(reservation) != nil {
		t.Fatal("create reservation refused", reservation.RetainedBytes)
	}
	reservation.OriginalHash = [32]byte{1}
	if ValidateNFOWriteCommitAttemptReservation(reservation) == nil {
		t.Fatal("a rewrite-looking reservation without an original was accepted")
	}
	// An absent-target receipt names no target and a rewrite receipt requires one.
	root, media, parent := directory(10), file(11), directory(12)
	absent, err := NewNFONativePreparationAbsentReceipt(root, media, [][48]byte{root, parent})
	if err != nil || !absent.TargetAbsent() || absent.NFOFileIdentity() != ([48]byte{}) {
		t.Fatal("absent receipt refused", err)
	}
	data, err := absent.MarshalBinary()
	if err != nil || data[3] != 1 {
		t.Fatal("absent receipt encoding lacks its flag")
	}
	parsed, err := ParseNFONativePreparationReceipt(data)
	if err != nil || !parsed.Equal(absent) || !parsed.TargetAbsent() {
		t.Fatal("absent receipt does not round trip", err)
	}
	data[3] = 2
	if _, err := ParseNFONativePreparationReceipt(data); err == nil {
		t.Fatal("unknown receipt flag accepted")
	}
	data[3] = 1
	five := file(5)
	copy(data[104:152], five[:])
	if _, err := ParseNFONativePreparationReceipt(data); err == nil {
		t.Fatal("an absent receipt with a target identity was accepted")
	}
	present, err := NewNFONativePreparationReceipt(root, media, file(5), [][48]byte{root, parent})
	if err != nil || present.TargetAbsent() || present.Equal(absent) {
		t.Fatal("present receipt confused with absent", err)
	}
	rewriteData, _ := present.MarshalBinary()
	rewriteData[3] = 1
	if _, err := ParseNFONativePreparationReceipt(rewriteData); err == nil {
		t.Fatal("a flagged receipt with a target was accepted")
	}
}
