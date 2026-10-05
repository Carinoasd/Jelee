package domain

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestNFOItemCandidatePathsAndSelectedAuthority(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	scope := NFOItemScope{ItemID: id, LibraryID: id, SourceID: id, RootID: id, Kind: "Movie", Revision: 1, Generation: 1, MediaPath: "dir/Film.mkv", Source: NFOSource{RootPath: "/trusted", RelativePath: "dir/Film.nfo"}}
	if !reflect.DeepEqual(NFOItemCandidatePaths(scope), []string{"dir/Film.nfo", "dir/movie.nfo"}) {
		t.Fatal("movie names differ")
	}
	scope.Kind = "Series"
	if !reflect.DeepEqual(NFOItemCandidatePaths(scope), []string{"dir/Film.nfo", "dir/tvshow.nfo"}) {
		t.Fatal("series names differ")
	}
	selected := NFOItemSelection{RelativePath: "dir/TVSHOW.NFO", CandidateDigest: NFOCandidateDigest([]string{"dir/TVSHOW.NFO"}), Fields: NFOItemFields{Version: NFOItemFieldsVersion, Kind: "Series", Identity: DefaultNFOIdentity(), Stamp: NFOStamp{Size: 10, SHA256: strings.Repeat("a", 64), FingerprintVersion: NFOFingerprintVersion}, ReadAt: time.Now().UTC(), Fields: []NFOTextField{{Field: "title", Value: "Local"}}}}
	if !ValidNFOItemSelection(scope, selected) {
		t.Fatal("owned conventional name rejected")
	}
	for _, name := range []string{"../TVSHOW.NFO", "other/TVSHOW.NFO", "dir/../dir/TVSHOW.NFO", "dir/season.nfo", "dir/movie.nfo", "dir/TVSHOW.NFO/"} {
		selected.RelativePath = name
		if ValidNFOItemSelection(scope, selected) {
			t.Fatalf("unauthorized selected path accepted: %q", name)
		}
	}
	if NFOCandidateDigest([]string{"b", "a"}) != NFOCandidateDigest([]string{"a", "b"}) {
		t.Fatal("digest depends on directory enumeration order")
	}
	scope.Kind = "Movie"
	scope.MediaPath = "dir/MOVIE.mkv"
	scope.Source.RelativePath = "dir/MOVIE.nfo"
	if len(NFOItemCandidatePaths(scope)) != 1 {
		t.Fatal("case-equivalent candidates duplicated")
	}
}
