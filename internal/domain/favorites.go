package domain

import "time"

// Favourites (G24.2): each user marks items as favourites. A favourite
// belongs to the logical item, so every version shares it, and it is user
// data like the resume point: it never widens what the user may see. An item
// the user can no longer see keeps its row but is never listed, counted or
// reported as a favourite, and marking or unmarking it is answered like a
// missing item (G48.3).

// FavoriteQuery pages the caller's favourites, most recently marked first.
type FavoriteQuery struct {
	Offset int
	Limit  int
	// Kinds restricts the item kinds; empty means every kind.
	Kinds []string
	// LibraryID, when set, restricts the list to one library.
	LibraryID string
}

// FavoriteEntry is one visible favourite.
type FavoriteEntry struct {
	Item BrowseItem
	// FavoritedAt is when the user marked the item.
	FavoritedAt time.Time
}

// FavoritePage is one page of favourites; Total counts every visible
// favourite matching the query.
type FavoritePage struct {
	Items []FavoriteEntry
	Total int
}

// ValidFavoriteQuery checks a favourites request.
func ValidFavoriteQuery(q FavoriteQuery) bool {
	if q.Offset < 0 || q.Offset > BrowseOffsetMax || q.Limit < 1 || q.Limit > BrowseLimitMax || len(q.Kinds) > len(browseKinds) {
		return false
	}
	if q.LibraryID != "" && !ValidID(q.LibraryID) {
		return false
	}
	for _, kind := range q.Kinds {
		if !browseKinds[kind] {
			return false
		}
	}
	return true
}
