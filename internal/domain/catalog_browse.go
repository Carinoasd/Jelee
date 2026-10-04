package domain

import (
	"strings"
	"unicode/utf8"
)

// Catalog browsing (G24.2): the read model behind library views, filtered and
// sorted item listings and item details. Every read is evaluated for one user
// and applies that user's library grants in storage, so an invisible library
// or item is indistinguishable from a missing one.

const (
	// BrowseLimitMax bounds one page of a listing.
	BrowseLimitMax = 500
	// BrowseOffsetMax bounds how far a listing may skip.
	BrowseOffsetMax = 1_000_000
	// BrowseSearchMax bounds a search term in characters.
	BrowseSearchMax = 128
	// BrowseSortMax bounds the number of sort keys.
	BrowseSortMax = 4
	// BrowseViewsMax bounds the library views one user can be shown.
	BrowseViewsMax = 1000
)

// BrowseKindLibrary is the kind reported for a library returned by a detail
// lookup; items keep their catalog kind.
const BrowseKindLibrary = "Library"

// BrowseSortKey is one supported sort order.
type BrowseSortKey string

const (
	// BrowseSortName orders by the sort title, falling back to the title,
	// compared case-insensitively.
	BrowseSortName BrowseSortKey = "name"
	// BrowseSortPremiereDate orders by the release date; items without one
	// come first in ascending order.
	BrowseSortPremiereDate BrowseSortKey = "premiereDate"
	// BrowseSortProductionYear orders by the release year (the year fact,
	// else the year of the release date).
	BrowseSortProductionYear BrowseSortKey = "productionYear"
)

type BrowseSort struct {
	Key        BrowseSortKey
	Descending bool
}

// BrowseScope selects which items a listing covers.
type BrowseScope int

const (
	// BrowseAll lists every visible item.
	BrowseAll BrowseScope = iota
	// BrowseParent lists the children of ParentID: the top-level items of a
	// library, or the seasons and episodes linked to a series or season.
	BrowseParent
	// BrowseParentRecursive lists every item below ParentID.
	BrowseParentRecursive
)

// BrowseQuery is a validated listing request.
type BrowseQuery struct {
	Scope    BrowseScope
	ParentID string
	// LibraryID, when set, additionally restricts the listing to one
	// library. It narrows the scope; it never widens the grants.
	LibraryID string
	// Kinds restricts the item kinds; empty means every kind.
	Kinds      []string
	SearchTerm string
	// Sort is applied in order; the item ID always breaks remaining ties.
	Sort   []BrowseSort
	Offset int
	Limit  int
	// WithOverview asks for the overview text, which is otherwise left empty.
	WithOverview bool
}

// BrowseItem is one visible item or, from a detail lookup, one visible
// library (Kind BrowseKindLibrary, no parent).
type BrowseItem struct {
	ID        string
	LibraryID string
	// ParentID is the linked series or season, else the library.
	ParentID  string
	Kind      string
	Title     string
	SortTitle string
	Overview  string
	// PremiereDate is YYYY-MM-DD or empty.
	PremiereDate string
	// Year is zero when unknown.
	Year int
	// ContentKinds lists, for a library only, which of Movie, Series,
	// Episode and HomeVideo it contains.
	ContentKinds []string
}

type BrowsePage struct {
	Items []BrowseItem
	// Total counts every item matching the query, ignoring Offset and Limit.
	Total int
}

// LibraryView is a library the user may browse.
type LibraryView struct {
	ID   string
	Name string
	// ContentKinds lists which of Movie, Series, Episode and HomeVideo the
	// library contains, in that order.
	ContentKinds []string
}

var browseKinds = map[string]bool{"Movie": true, "Series": true, "Season": true, "Episode": true, "HomeVideo": true}

// BrowseItemKind reports whether kind is a catalog item kind.
func BrowseItemKind(kind string) bool { return browseKinds[kind] }

// ValidBrowseQuery checks a listing request before it reaches storage.
func ValidBrowseQuery(q BrowseQuery) bool {
	switch q.Scope {
	case BrowseAll:
		if q.ParentID != "" {
			return false
		}
	case BrowseParent, BrowseParentRecursive:
		if !ValidID(q.ParentID) {
			return false
		}
	default:
		return false
	}
	if q.LibraryID != "" && !ValidID(q.LibraryID) {
		return false
	}
	if q.Offset < 0 || q.Offset > BrowseOffsetMax || q.Limit < 1 || q.Limit > BrowseLimitMax {
		return false
	}
	if len(q.Kinds) > len(browseKinds) || len(q.Sort) > BrowseSortMax {
		return false
	}
	for _, kind := range q.Kinds {
		if !browseKinds[kind] {
			return false
		}
	}
	for _, s := range q.Sort {
		switch s.Key {
		case BrowseSortName, BrowseSortPremiereDate, BrowseSortProductionYear:
		default:
			return false
		}
	}
	if !utf8.ValidString(q.SearchTerm) || utf8.RuneCountInString(q.SearchTerm) > BrowseSearchMax || strings.ContainsRune(q.SearchTerm, 0) {
		return false
	}
	return true
}
