package domain

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Collections and playlists (G02.1). Their member items are always read
// through the unified visibility filter (G48.3): a hidden item is never a
// visible member, and a collection without a visible member is listed to
// administrators only. docs/collections-playlists.md describes the model.

const (
	// CollectionNameMax bounds collection and playlist names in bytes; it
	// matches the NFO collection name limit.
	CollectionNameMax = 1024
	// CollectionOverviewMax bounds a collection overview in bytes.
	CollectionOverviewMax = 16384
	// CollectionsMax bounds the stored collections.
	CollectionsMax = 10000
	// CollectionManualItemsMax bounds the manual members of one collection.
	CollectionManualItemsMax = 10000
	// CollectionViewItemsMax bounds the members one collection read returns;
	// a larger collection is reported truncated.
	CollectionViewItemsMax = 2000
	// MembershipBatchMax bounds the items one add request names.
	MembershipBatchMax = 100
	// PlaylistsPerUserMax bounds the playlists of one user.
	PlaylistsPerUserMax = 1000
	// PlaylistEntriesMax bounds the entries of one playlist.
	PlaylistEntriesMax = 5000
	// CollectionPageMax bounds one page of a collection or playlist listing.
	CollectionPageMax = 200
)

// CollectionKinds are the item kinds a collection holds manually; NFO
// members are whatever items carry the collection name.
var CollectionKinds = []string{"Movie", "Series", "HomeVideo"}

// PlaylistKinds are the playable item kinds a playlist holds.
var PlaylistKinds = []string{"Movie", "Episode", "HomeVideo"}

// ValidCollectionName accepts a trimmed, nonblank name without control
// characters within CollectionNameMax bytes.
func ValidCollectionName(s string) bool {
	return s != "" && len(s) <= CollectionNameMax && utf8.ValidString(s) && s == strings.TrimSpace(s) && strings.IndexFunc(s, unicode.IsControl) < 0
}

func validOverview(s string) bool {
	return len(s) <= CollectionOverviewMax && utf8.ValidString(s) && strings.IndexFunc(s, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' }) < 0
}

// CollectionInput is what an administrator sets on a collection. NFOName,
// when set, adds every item whose collection metadata carries that name
// (trimmed, case-insensitive) as a member; at most one collection may use
// a name.
type CollectionInput struct {
	Name     string  `json:"name"`
	Overview string  `json:"overview"`
	NFOName  *string `json:"nfoName"`
}

// Normalize trims the names.
func (in CollectionInput) Normalize() CollectionInput {
	in.Name = strings.TrimSpace(in.Name)
	if in.NFOName != nil {
		name := strings.TrimSpace(*in.NFOName)
		in.NFOName = &name
	}
	return in
}

// Valid checks a normalized input.
func (in CollectionInput) Valid() bool {
	return ValidCollectionName(in.Name) && validOverview(in.Overview) && (in.NFOName == nil || ValidCollectionName(*in.NFOName))
}

// Collection is a stored collection as one user sees it: ItemCount and
// CoverItemID count only the members visible to that user.
type Collection struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Overview string  `json:"overview"`
	NFOName  *string `json:"nfoName"`
	// ItemCount counts the members visible to the caller.
	ItemCount int `json:"itemCount"`
	// CoverItemID is the first visible member in title order, if any.
	CoverItemID string    `json:"coverItemId,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// CollectionMember is one visible member of a collection.
type CollectionMember struct {
	Item
	// Manual reports a member added by an administrator; only those can be
	// removed. FromNFO reports a member through the NFO name.
	Manual  bool `json:"manual"`
	FromNFO bool `json:"fromNfo"`
}

// CollectionView is one collection with its visible members in title order.
type CollectionView struct {
	Collection Collection         `json:"collection"`
	Items      []CollectionMember `json:"items"`
	// Truncated reports more visible members than CollectionViewItemsMax.
	Truncated bool `json:"truncated"`
}

// CollectionPage is one page of collections in name order.
type CollectionPage struct {
	Collections []Collection `json:"collections"`
	NextCursor  string       `json:"nextCursor"`
}

// CollectionNFOSync reports the collections created from NFO collection
// names that had none.
type CollectionNFOSync struct {
	Created int `json:"created"`
}

// PlaylistInput is what the owner sets on a playlist. Public lets every
// other user read it; only the owner ever changes it.
type PlaylistInput struct {
	Name   string `json:"name"`
	Public bool   `json:"public"`
}

// Normalize trims the name.
func (in PlaylistInput) Normalize() PlaylistInput {
	in.Name = strings.TrimSpace(in.Name)
	return in
}

// Valid checks a normalized input.
func (in PlaylistInput) Valid() bool { return ValidCollectionName(in.Name) }

// Playlist is a stored playlist as one user sees it: ItemCount and
// CoverItemID count only the entries visible to that user.
type Playlist struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Public    bool   `json:"public"`
	OwnerID   string `json:"ownerId"`
	OwnerName string `json:"ownerName"`
	// Owned reports the caller's own playlist, which only they change.
	Owned       bool      `json:"owned"`
	ItemCount   int       `json:"itemCount"`
	CoverItemID string    `json:"coverItemId,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// PlaylistEntry is one visible entry; an item may appear more than once.
type PlaylistEntry struct {
	EntryID string `json:"entryId"`
	Item    Item   `json:"item"`
}

// PlaylistView is one playlist with its visible entries in order.
type PlaylistView struct {
	Playlist Playlist        `json:"playlist"`
	Entries  []PlaylistEntry `json:"entries"`
}

// PlaylistPage is one page of playlists in name order.
type PlaylistPage struct {
	Playlists  []Playlist `json:"playlists"`
	NextCursor string     `json:"nextCursor"`
}

// ValidMembershipBatch accepts 1..MembershipBatchMax well-formed IDs.
func ValidMembershipBatch(ids []string) bool {
	if len(ids) == 0 || len(ids) > MembershipBatchMax {
		return false
	}
	for _, id := range ids {
		if !ValidID(id) {
			return false
		}
	}
	return true
}
