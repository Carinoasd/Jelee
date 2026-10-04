package domain

import (
	"strings"
	"testing"
)

func TestValidBrowseQuery(t *testing.T) {
	const parent = "12345678-1234-4234-8234-123456789abc"
	good := BrowseQuery{Scope: BrowseParentRecursive, ParentID: parent, Kinds: []string{"Movie", "Episode"}, SearchTerm: "名字",
		Sort: []BrowseSort{{Key: BrowseSortName}, {Key: BrowseSortPremiereDate, Descending: true}, {Key: BrowseSortProductionYear}}, Offset: BrowseOffsetMax, Limit: BrowseLimitMax}
	if !ValidBrowseQuery(good) || !ValidBrowseQuery(BrowseQuery{Limit: 1}) {
		t.Fatal("valid query refused")
	}
	for name, change := range map[string]func(*BrowseQuery){
		"all with parent":  func(q *BrowseQuery) { q.Scope = BrowseAll },
		"parent missing":   func(q *BrowseQuery) { q.ParentID = "" },
		"parent malformed": func(q *BrowseQuery) { q.ParentID = strings.ToUpper(parent) },
		"unknown scope":    func(q *BrowseQuery) { q.Scope = 7 },
		"negative offset":  func(q *BrowseQuery) { q.Offset = -1 },
		"offset too large": func(q *BrowseQuery) { q.Offset = BrowseOffsetMax + 1 },
		"zero limit":       func(q *BrowseQuery) { q.Limit = 0 },
		"limit too large":  func(q *BrowseQuery) { q.Limit = BrowseLimitMax + 1 },
		"unknown kind":     func(q *BrowseQuery) { q.Kinds = []string{"Audio"} },
		"lower-case kind":  func(q *BrowseQuery) { q.Kinds = []string{"movie"} },
		"too many kinds":   func(q *BrowseQuery) { q.Kinds = []string{"Movie", "Movie", "Movie", "Movie", "Movie", "Movie"} },
		"unknown sort":     func(q *BrowseQuery) { q.Sort = []BrowseSort{{Key: "id; DROP TABLE items"}} },
		"too many sorts": func(q *BrowseQuery) {
			q.Sort = append(q.Sort, BrowseSort{Key: BrowseSortName}, BrowseSort{Key: BrowseSortName})
		},
		"long search":   func(q *BrowseQuery) { q.SearchTerm = strings.Repeat("字", BrowseSearchMax+1) },
		"invalid utf8":  func(q *BrowseQuery) { q.SearchTerm = "\xff" },
		"nul in search": func(q *BrowseQuery) { q.SearchTerm = "a\x00" },
	} {
		q := good
		q.Sort = append([]BrowseSort(nil), good.Sort...)
		change(&q)
		if ValidBrowseQuery(q) {
			t.Errorf("%s accepted", name)
		}
	}
}
