package domain

import (
	"strings"
	"testing"
	"time"
)

func TestItemMetadataPatchBoundaries(t *testing.T) {
	id := "11111111-1111-4111-8111-111111111111"
	value := "標題"
	lock := true
	if !ValidItemMetadataPatches(id, 1, []ItemMetadataPatch{{Field: "title", Value: &value, Locked: &lock}}) {
		t.Fatal("valid update rejected")
	}
	for _, c := range []struct{ field, value string }{{"title", ""}, {"title", " \t\n"}, {"title", strings.Repeat("界", 342)}, {"overview", strings.Repeat("x", 16385)}, {"originalTitle", strings.Repeat("x", 1025)}, {"overview", "x\x00y"}, {"overview", string([]byte{0xff})}, {"date", "2023-02-29"}, {"date", "0000-01-01"}, {"date", "2024-2-29"}, {"date", "2024-02-30"}, {"unknown", "x"}} {
		if ValidItemMetadataValue(c.field, c.value) {
			t.Fatalf("accepted invalid field %s", c.field)
		}
	}
	for _, c := range []struct{ field, value string }{{"title", strings.Repeat("x", 1024)}, {"overview", strings.Repeat("x", 16384)}, {"originalTitle", ""}, {"overview", ""}, {"date", ""}, {"date", "2024-02-29"}} {
		if !ValidItemMetadataValue(c.field, c.value) {
			t.Fatal("rejected valid boundary", c.field)
		}
	}
	for _, patches := range [][]ItemMetadataPatch{nil, {{Field: "title"}}, {{Field: "url", Locked: &lock}}, {{Field: "title", Locked: &lock}, {Field: "title", Value: &value}}} {
		if ValidItemMetadataPatches(id, 1, patches) {
			t.Fatal("invalid patch accepted")
		}
	}
	if ValidItemMetadataPatches(id, ItemMetadataRevisionMax, []ItemMetadataPatch{{Field: "title", Value: &value}}) {
		t.Fatal("overflow revision accepted")
	}
}

func TestItemMetadataCloneOwnsTimestampAndFields(t *testing.T) {
	now := time.Now().UTC()
	original := ItemMetadata{Fields: []ItemMetadataField{{Field: "title", Value: "Title", UpdatedAt: &now}}}
	copy := CloneItemMetadata(original)
	copy.Fields[0].Value = "changed"
	*copy.Fields[0].UpdatedAt = time.Time{}
	if original.Fields[0].Value != "Title" || original.Fields[0].UpdatedAt.IsZero() {
		t.Fatal("clone shared mutable state")
	}
}
