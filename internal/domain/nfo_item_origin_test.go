package domain

import "testing"

func TestNFOLocksHaveIndependentOriginAndCloneOwnership(t *testing.T) {
	fields := NFOItemFields{LockedFields: []string{"Name", "Overview", "PremiereDate"}}
	if !NFOFieldLocked(fields, "title") || !NFOFieldLocked(fields, "overview") || !NFOFieldLocked(fields, "date") || NFOFieldLocked(fields, "originalTitle") {
		t.Fatal("NFO lock mapping differs")
	}
	fields.LockData = true
	if !NFOFieldLocked(fields, "originalTitle") {
		t.Fatal("NFO lockdata not preserved")
	}
	old := ItemMetadataField{Field: "title", Source: "nfo", Value: "Local", NFOOrigin: &NFOItemOrigin{Locked: true}}
	if TMDBMetadataSkip(old, true) != "locked" {
		t.Fatal("NFO lock not enforced")
	}
	old.NFOOrigin.Locked = false
	if TMDBMetadataSkip(old, true) != "nfo" {
		t.Fatal("NFO source not protected")
	}
	clone := CloneItemMetadata(ItemMetadata{Fields: []ItemMetadataField{old}})
	clone.Fields[0].NFOOrigin.Locked = true
	if old.NFOOrigin.Locked {
		t.Fatal("NFO source pointer leaked")
	}
}
