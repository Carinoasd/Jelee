package domain

import "testing"

func TestValidFavoriteQuery(t *testing.T) {
	library := "10000000-0000-4000-8000-00000000000a"
	for _, q := range []FavoriteQuery{
		{Limit: 1}, {Limit: BrowseLimitMax, Offset: BrowseOffsetMax}, {Limit: 5, Kinds: []string{"Movie", "Series", "Season", "Episode", "HomeVideo"}}, {Limit: 5, LibraryID: library},
	} {
		if !ValidFavoriteQuery(q) {
			t.Fatalf("rejected %+v", q)
		}
	}
	for _, q := range []FavoriteQuery{
		{}, {Limit: BrowseLimitMax + 1}, {Limit: 1, Offset: -1}, {Limit: 1, Offset: BrowseOffsetMax + 1}, {Limit: 1, Kinds: []string{"Audio"}},
		{Limit: 1, Kinds: []string{"Movie", "Movie", "Movie", "Movie", "Movie", "Movie"}}, {Limit: 1, LibraryID: "x"},
	} {
		if ValidFavoriteQuery(q) {
			t.Fatalf("accepted %+v", q)
		}
	}
}
