package domain

import (
	"encoding/json"
	"testing"
)

func TestNFOWriteCommitFileEvidenceShape(t *testing.T) {
	var parent, target, output, rollback [48]byte
	for i, value := range []*[48]byte{&parent, &target, &output, &rollback} {
		value[0], value[1], value[2], value[16] = 1, 2, 1, byte(i+1)
	}
	parent[2] = 2
	plan := NFOWriteCommitFilePlan{1, "movie.nfo", parent, target}
	ready := NFOWriteCommitFilesReady{output, rollback}
	if ValidateNFOWriteCommitFilePlan(plan) != nil || ValidateNFOWriteCommitFilesReady(ready) != nil {
		t.Fatal("bounded evidence rejected")
	}
	for _, value := range []any{plan, ready, NFOWriteCommitRecord{Token: "private-token"}} {
		encoded, _ := json.Marshal(value)
		if string(encoded) != "{}" {
			t.Fatal("private evidence exposed")
		}
	}
	for _, name := range []string{"", "../movie.nfo", "folder/movie.nfo", "folder\\movie.nfo", "movie:nfo", "movie\nnfo", "movie.txt"} {
		changed := plan
		changed.TargetName = name
		if ValidateNFOWriteCommitFilePlan(changed) == nil {
			t.Fatal("invalid filename accepted")
		}
	}
	changed := plan
	changed.Version = 2
	if ValidateNFOWriteCommitFilePlan(changed) == nil {
		t.Fatal("unknown plan version accepted")
	}
	changed = plan
	changed.TargetIdentity[1] = 1
	if ValidateNFOWriteCommitFilePlan(changed) == nil {
		t.Fatal("cross-platform plan accepted")
	}
	badReady := ready
	badReady.OutputIdentity = badReady.RollbackIdentity
	if ValidateNFOWriteCommitFilesReady(badReady) == nil {
		t.Fatal("same physical output/rollback accepted")
	}
}
