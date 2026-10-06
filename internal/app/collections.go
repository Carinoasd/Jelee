package app

import (
	"context"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// CollectionRepository stores collections and playlists (G02.1). Every
// method binds the caller's live session, and every read of member items
// applies the unified visibility filter in the same statement (G48.3): a
// hidden item is answered like a missing one and never listed. Collection
// changes need a live administrator and are audited; playlist changes need
// the owner.
type CollectionRepository interface {
	// ListCollections pages the collections in name order after the cursor
	// collection. Non-administrators only get collections with a visible
	// member.
	ListCollections(ctx context.Context, actor domain.Actor, cursor string, limit int) (domain.CollectionPage, error)
	// Collection reads one collection with its visible members; one without
	// a visible member is ErrNotFound to a non-administrator.
	Collection(ctx context.Context, actor domain.Actor, id string) (domain.CollectionView, error)
	CreateCollection(ctx context.Context, actor domain.Actor, in domain.CollectionInput) (domain.CollectionView, error)
	UpdateCollection(ctx context.Context, actor domain.Actor, id string, in domain.CollectionInput) (domain.CollectionView, error)
	DeleteCollection(ctx context.Context, actor domain.Actor, id string) error
	// AddCollectionItems adds visible items of CollectionKinds as manual
	// members; an invisible or missing item fails the whole request.
	AddCollectionItems(ctx context.Context, actor domain.Actor, id string, itemIDs []string) (domain.CollectionView, error)
	// RemoveCollectionItem removes a manual member; a member only through
	// the NFO name is ErrConflict.
	RemoveCollectionItem(ctx context.Context, actor domain.Actor, id, itemID string) (domain.CollectionView, error)
	// SyncNFOCollections creates a collection for every NFO collection name
	// that has none.
	SyncNFOCollections(ctx context.Context, actor domain.Actor) (domain.CollectionNFOSync, error)

	// ListPlaylists pages the caller's own and other users' public
	// playlists in name order.
	ListPlaylists(ctx context.Context, actor domain.Actor, cursor string, limit int) (domain.PlaylistPage, error)
	// Playlist reads one own or public playlist with its visible entries.
	Playlist(ctx context.Context, actor domain.Actor, id string) (domain.PlaylistView, error)
	CreatePlaylist(ctx context.Context, actor domain.Actor, in domain.PlaylistInput) (domain.PlaylistView, error)
	// UpdatePlaylist, DeletePlaylist and the entry changes act on the
	// caller's own playlists only; any other is ErrNotFound when not
	// public and ErrForbidden when public.
	UpdatePlaylist(ctx context.Context, actor domain.Actor, id string, in domain.PlaylistInput) (domain.PlaylistView, error)
	DeletePlaylist(ctx context.Context, actor domain.Actor, id string) error
	// AddPlaylistItems appends visible items of PlaylistKinds in order.
	AddPlaylistItems(ctx context.Context, actor domain.Actor, id string, itemIDs []string) (domain.PlaylistView, error)
	RemovePlaylistEntry(ctx context.Context, actor domain.Actor, id, entryID string) (domain.PlaylistView, error)
	// MovePlaylistEntry moves an entry before another one, or to the end
	// when beforeEntryID is empty.
	MovePlaylistEntry(ctx context.Context, actor domain.Actor, id, entryID, beforeEntryID string) (domain.PlaylistView, error)
}

// WithCollections enables collections and playlists.
func (c *Catalog) WithCollections(repository CollectionRepository) (*Catalog, error) {
	if c == nil || repository == nil {
		return nil, domain.ErrInvalid
	}
	next := *c
	next.collections = repository
	return &next, nil
}

func (c *Catalog) collectionsReady(ctx context.Context, actor domain.Actor) error {
	if c == nil || ctx == nil {
		return domain.ErrInvalid
	}
	if c.collections == nil {
		return domain.ErrDatabase
	}
	if !domain.ValidID(actor.UserID) || !domain.ValidID(actor.SessionID) {
		return domain.ErrUnauthenticated
	}
	return ctx.Err()
}

func validPage(cursor string, limit int) bool {
	return (cursor == "" || domain.ValidID(cursor)) && limit >= 1 && limit <= domain.CollectionPageMax
}

// ListCollections pages the collections the caller may see.
func (c *Catalog) ListCollections(ctx context.Context, actor domain.Actor, cursor string, limit int) (domain.CollectionPage, error) {
	if err := c.collectionsReady(ctx, actor); err != nil {
		return domain.CollectionPage{}, err
	}
	if !validPage(cursor, limit) {
		return domain.CollectionPage{}, domain.ErrInvalid
	}
	return c.collections.ListCollections(ctx, actor, cursor, limit)
}

// Collection reads one collection.
func (c *Catalog) Collection(ctx context.Context, actor domain.Actor, id string) (domain.CollectionView, error) {
	if err := c.collectionsReady(ctx, actor); err != nil {
		return domain.CollectionView{}, err
	}
	if !domain.ValidID(id) {
		return domain.CollectionView{}, domain.ErrNotFound
	}
	return c.collections.Collection(ctx, actor, id)
}

// CreateCollection validates and creates a collection.
func (c *Catalog) CreateCollection(ctx context.Context, actor domain.Actor, in domain.CollectionInput) (domain.CollectionView, error) {
	if err := c.collectionsReady(ctx, actor); err != nil {
		return domain.CollectionView{}, err
	}
	in = in.Normalize()
	if !in.Valid() {
		return domain.CollectionView{}, domain.ErrInvalid
	}
	return c.collections.CreateCollection(ctx, actor, in)
}

// UpdateCollection validates and replaces a collection's name, overview
// and NFO name.
func (c *Catalog) UpdateCollection(ctx context.Context, actor domain.Actor, id string, in domain.CollectionInput) (domain.CollectionView, error) {
	if err := c.collectionsReady(ctx, actor); err != nil {
		return domain.CollectionView{}, err
	}
	if !domain.ValidID(id) {
		return domain.CollectionView{}, domain.ErrNotFound
	}
	in = in.Normalize()
	if !in.Valid() {
		return domain.CollectionView{}, domain.ErrInvalid
	}
	return c.collections.UpdateCollection(ctx, actor, id, in)
}

// DeleteCollection removes a collection; its items stay.
func (c *Catalog) DeleteCollection(ctx context.Context, actor domain.Actor, id string) error {
	if err := c.collectionsReady(ctx, actor); err != nil {
		return err
	}
	if !domain.ValidID(id) {
		return domain.ErrNotFound
	}
	return c.collections.DeleteCollection(ctx, actor, id)
}

// AddCollectionItems adds manual members.
func (c *Catalog) AddCollectionItems(ctx context.Context, actor domain.Actor, id string, itemIDs []string) (domain.CollectionView, error) {
	if err := c.collectionsReady(ctx, actor); err != nil {
		return domain.CollectionView{}, err
	}
	if !domain.ValidID(id) {
		return domain.CollectionView{}, domain.ErrNotFound
	}
	if !domain.ValidMembershipBatch(itemIDs) {
		return domain.CollectionView{}, domain.ErrInvalid
	}
	return c.collections.AddCollectionItems(ctx, actor, id, itemIDs)
}

// RemoveCollectionItem removes a manual member.
func (c *Catalog) RemoveCollectionItem(ctx context.Context, actor domain.Actor, id, itemID string) (domain.CollectionView, error) {
	if err := c.collectionsReady(ctx, actor); err != nil {
		return domain.CollectionView{}, err
	}
	if !domain.ValidID(id) || !domain.ValidID(itemID) {
		return domain.CollectionView{}, domain.ErrNotFound
	}
	return c.collections.RemoveCollectionItem(ctx, actor, id, itemID)
}

// SyncNFOCollections creates collections from NFO collection names.
func (c *Catalog) SyncNFOCollections(ctx context.Context, actor domain.Actor) (domain.CollectionNFOSync, error) {
	if err := c.collectionsReady(ctx, actor); err != nil {
		return domain.CollectionNFOSync{}, err
	}
	return c.collections.SyncNFOCollections(ctx, actor)
}

// ListPlaylists pages the caller's own and other users' public playlists.
func (c *Catalog) ListPlaylists(ctx context.Context, actor domain.Actor, cursor string, limit int) (domain.PlaylistPage, error) {
	if err := c.collectionsReady(ctx, actor); err != nil {
		return domain.PlaylistPage{}, err
	}
	if !validPage(cursor, limit) {
		return domain.PlaylistPage{}, domain.ErrInvalid
	}
	return c.collections.ListPlaylists(ctx, actor, cursor, limit)
}

// Playlist reads one own or public playlist.
func (c *Catalog) Playlist(ctx context.Context, actor domain.Actor, id string) (domain.PlaylistView, error) {
	if err := c.collectionsReady(ctx, actor); err != nil {
		return domain.PlaylistView{}, err
	}
	if !domain.ValidID(id) {
		return domain.PlaylistView{}, domain.ErrNotFound
	}
	return c.collections.Playlist(ctx, actor, id)
}

// CreatePlaylist validates and creates a playlist of the caller.
func (c *Catalog) CreatePlaylist(ctx context.Context, actor domain.Actor, in domain.PlaylistInput) (domain.PlaylistView, error) {
	if err := c.collectionsReady(ctx, actor); err != nil {
		return domain.PlaylistView{}, err
	}
	in = in.Normalize()
	if !in.Valid() {
		return domain.PlaylistView{}, domain.ErrInvalid
	}
	return c.collections.CreatePlaylist(ctx, actor, in)
}

// UpdatePlaylist renames a playlist or changes whether it is public.
func (c *Catalog) UpdatePlaylist(ctx context.Context, actor domain.Actor, id string, in domain.PlaylistInput) (domain.PlaylistView, error) {
	if err := c.collectionsReady(ctx, actor); err != nil {
		return domain.PlaylistView{}, err
	}
	if !domain.ValidID(id) {
		return domain.PlaylistView{}, domain.ErrNotFound
	}
	in = in.Normalize()
	if !in.Valid() {
		return domain.PlaylistView{}, domain.ErrInvalid
	}
	return c.collections.UpdatePlaylist(ctx, actor, id, in)
}

// DeletePlaylist removes a playlist of the caller.
func (c *Catalog) DeletePlaylist(ctx context.Context, actor domain.Actor, id string) error {
	if err := c.collectionsReady(ctx, actor); err != nil {
		return err
	}
	if !domain.ValidID(id) {
		return domain.ErrNotFound
	}
	return c.collections.DeletePlaylist(ctx, actor, id)
}

// AddPlaylistItems appends items to a playlist of the caller.
func (c *Catalog) AddPlaylistItems(ctx context.Context, actor domain.Actor, id string, itemIDs []string) (domain.PlaylistView, error) {
	if err := c.collectionsReady(ctx, actor); err != nil {
		return domain.PlaylistView{}, err
	}
	if !domain.ValidID(id) {
		return domain.PlaylistView{}, domain.ErrNotFound
	}
	if !domain.ValidMembershipBatch(itemIDs) {
		return domain.PlaylistView{}, domain.ErrInvalid
	}
	return c.collections.AddPlaylistItems(ctx, actor, id, itemIDs)
}

// RemovePlaylistEntry removes one entry of a playlist of the caller.
func (c *Catalog) RemovePlaylistEntry(ctx context.Context, actor domain.Actor, id, entryID string) (domain.PlaylistView, error) {
	if err := c.collectionsReady(ctx, actor); err != nil {
		return domain.PlaylistView{}, err
	}
	if !domain.ValidID(id) || !domain.ValidID(entryID) {
		return domain.PlaylistView{}, domain.ErrNotFound
	}
	return c.collections.RemovePlaylistEntry(ctx, actor, id, entryID)
}

// MovePlaylistEntry reorders one entry of a playlist of the caller.
func (c *Catalog) MovePlaylistEntry(ctx context.Context, actor domain.Actor, id, entryID, beforeEntryID string) (domain.PlaylistView, error) {
	if err := c.collectionsReady(ctx, actor); err != nil {
		return domain.PlaylistView{}, err
	}
	if !domain.ValidID(id) || !domain.ValidID(entryID) {
		return domain.PlaylistView{}, domain.ErrNotFound
	}
	if beforeEntryID != "" && (!domain.ValidID(beforeEntryID) || beforeEntryID == entryID) {
		return domain.PlaylistView{}, domain.ErrInvalid
	}
	return c.collections.MovePlaylistEntry(ctx, actor, id, entryID, beforeEntryID)
}
