package domain

import (
	"strings"
	"testing"
)

func TestMetadataRemoveLocalFallbackTitle(t *testing.T) {
	for _, tc := range []struct {
		relative  string
		directory bool
		want      string
		ok        bool
	}{
		{"film.mkv", false, "film", true},
		{"Movies/Some Film (2020)/Some.Film.2020.mkv", false, "Some.Film.2020", true},
		{"/library/Show/Season 1", true, "Season 1", true},
		{"/library/Show.v2", true, "Show.v2", true},
		{"/library/root/", true, "root", true},
		{"dir/.mkv", false, ".mkv", true},
		{"dir/ .mkv", false, " .mkv", true},
		{"", false, "", false},
		{"/", true, "", false},
		{".", true, "", false},
		{strings.Repeat("a", 1025) + ".mkv", false, "", false},
	} {
		got, ok := LocalFallbackTitle(tc.relative, tc.directory)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("%q directory=%t: got %q %t", tc.relative, tc.directory, got, ok)
		}
	}
}

func TestMetadataRemoveLockSemanticsAndInput(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	if !ExternalMetadataRemovable(ItemMetadataField{Field: "overview", Source: "tmdb"}) {
		t.Fatal("unlocked provider value kept")
	}
	for _, field := range []ItemMetadataField{
		{Field: "overview", Source: "tmdb", Locked: true},
		{Field: "overview", Source: "tmdb", NFOLockOrigin: &NFOFieldLockOrigin{Locked: true}},
		{Field: "overview", Source: "nfo"},
		{Field: "overview", Source: "manual"},
		{Field: "title", Source: "existing"},
	} {
		if ExternalMetadataRemovable(field) {
			t.Fatal("protected or local field removable", field)
		}
	}
	if !ValidMetadataRemoveInput(id, 1) || ValidMetadataRemoveInput(id, 0) || ValidMetadataRemoveInput(id, ItemMetadataRevisionMax) || ValidMetadataRemoveInput("bad", 1) {
		t.Fatal("remove input validation differs")
	}
	original := MetadataRemoveResult{Removed: []string{"title"}, Skipped: []MetadataFieldSkip{{Field: "date", Reason: "locked"}}, Metadata: ItemMetadata{Fields: []ItemMetadataField{{Field: "title"}}}}
	clone := CloneMetadataRemoveResult(original)
	clone.Removed[0], clone.Skipped[0].Reason, clone.Metadata.Fields[0].Value = "x", "x", "x"
	if original.Removed[0] != "title" || original.Skipped[0].Reason != "locked" || original.Metadata.Fields[0].Value != "" {
		t.Fatal("remove result clone shares state")
	}
}
