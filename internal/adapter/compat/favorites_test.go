package compat

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestParseFavoriteFilter checks the favourite filter of IsFavorite and
// Filters (G24.2): containers never pass a request for favourites, a
// contradiction matches nothing and the other filters stay ignored.
func TestParseFavoriteFilter(t *testing.T) {
	parse := func(raw string) (itemsRequest, error) {
		t.Helper()
		return parseItemsRequest(readQuery(httptest.NewRequest(http.MethodGet, "/Items?"+raw, nil)))
	}
	for _, c := range []struct {
		raw                 string
		favorite, nothing   bool
		set, views, folders bool
	}{
		{raw: "", views: true, folders: true},
		{raw: "Filters=IsPlayed,IsResumable", views: true, folders: true},
		{raw: "Filters=IsFavorite", set: true, favorite: true},
		{raw: "filters=isfavoriteorlikes", set: true, favorite: true},
		{raw: "IsFavorite=TRUE", set: true, favorite: true},
		{raw: "isFavorite=false", set: true, views: true, folders: true},
		{raw: "IsFavorite=false&Filters=IsFavorite", set: true, favorite: true, nothing: true},
		{raw: "IsFavorite=true&Filters=IsFavorite", set: true, favorite: true},
	} {
		req, err := parse(c.raw)
		if err != nil {
			t.Fatalf("%q: %v", c.raw, err)
		}
		if (req.favorite != nil) != c.set || req.favorite != nil && *req.favorite != c.favorite || req.nothing != c.nothing ||
			req.views != c.views || req.boxsets != c.folders || req.playlists != c.folders {
			t.Fatalf("%q: favorite=%v nothing=%t views=%t boxsets=%t playlists=%t", c.raw, req.favorite, req.nothing, req.views, req.boxsets, req.playlists)
		}
		for _, mark := range []bool{true, false} {
			want := !c.nothing && (!c.set || c.favorite == mark)
			if got := req.keepFavorite(mark); got != want {
				t.Fatalf("%q keep(%t)=%t", c.raw, mark, got)
			}
		}
	}
	if _, err := parse("IsFavorite=yes"); err == nil {
		t.Fatal("malformed IsFavorite accepted")
	}
}
