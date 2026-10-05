package domain

import "testing"

func TestScanMetadataSkipLowestPriority(t *testing.T) {
	if ScanMetadataSkip(ItemMetadataField{Source: "scan"}) != "" {
		t.Fatal("scan value not replaceable by a newer scan")
	}
	for _, f := range []ItemMetadataField{{Source: "scan", Locked: true}, {Source: "manual"}, {Source: "nfo"}, {Source: "tmdb"}, {Source: "existing", Value: "x"}} {
		if ScanMetadataSkip(f) == "" {
			t.Fatal("scan overwrote higher priority", f)
		}
	}
	if TMDBMetadataSkip(ItemMetadataField{Field: "title", Source: "scan", Value: "x"}, false) != "" {
		t.Fatal("TMDB must replace file-name values")
	}
	if NormalizeCatalogTitle("Breaking.Bad") != NormalizeCatalogTitle("breaking  bad") || string(CatalogGroupDigest("a", "bc")) == string(CatalogGroupDigest("ab", "c")) {
		t.Fatal("grouping key normalisation")
	}
}
