package postgres

import (
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func collectionMemberNames(f contentAccessFixture, view domain.CollectionView) []string {
	names := []string{}
	for _, m := range view.Items {
		names = append(names, f.names[m.ID])
	}
	slices.Sort(names)
	return names
}

func playlistEntryNames(f contentAccessFixture, view domain.PlaylistView) []string {
	names := []string{}
	for _, e := range view.Entries {
		names = append(names, f.names[e.Item.ID])
	}
	return names
}

func (f contentAccessFixture) hideFrom(t *testing.T, u contentAccessUser, names ...string) {
	t.Helper()
	for _, name := range names {
		if _, err := f.s.SetItemAccessRule(f.ctx, f.a, u.principal.UserID, f.items[name], domain.ItemAccessHide); err != nil {
			t.Fatal(err)
		}
	}
}

// setCollectionFact stores the collection metadata fact of an item at its
// current revision.
func (f contentAccessFixture) setCollectionFact(t *testing.T, name, value string) {
	t.Helper()
	revision := int64(1)
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT COALESCE((SELECT revision FROM item_metadata_state WHERE item_id=$1::uuid),1)`, f.items[name]).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.UpdateItemMetadataWithFacts(f.ctx, f.a, f.items[name], revision, nil, []domain.ItemMetadataFactPatch{{Field: "collection", Value: json.RawMessage(value)}}); err != nil {
		t.Fatal("collection fact", err)
	}
}

func (f contentAccessFixture) ids(names ...string) []string {
	ids := []string{}
	for _, name := range names {
		ids = append(ids, f.items[name])
	}
	return ids
}

// TestCollectionsVisibilityPostgres checks collection administration, NFO
// grouping and that every read goes through the unified filter (G02.1,
// G48.3): hidden members are neither listed nor counted, and a collection
// without a visible member does not exist for a viewer.
func TestCollectionsVisibilityPostgres(t *testing.T) {
	f := newContentAccessFixture(t)
	admin, viewer := f.admin.actor, f.viewer.actor
	if _, err := f.s.CreateCollection(f.ctx, viewer, domain.CollectionInput{Name: "Nope"}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("viewer created a collection: %v", err)
	}
	saga, err := f.s.CreateCollection(f.ctx, admin, domain.CollectionInput{Name: "Saga", Overview: "line\nbreak"})
	if err != nil || saga.Collection.ItemCount != 0 || len(saga.Items) != 0 {
		t.Fatalf("create: %+v %v", saga, err)
	}
	id := saga.Collection.ID
	if _, err = f.s.AddCollectionItems(f.ctx, admin, id, f.ids("season-pg")); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("season accepted: %v", err)
	}
	if _, err = f.s.AddCollectionItems(f.ctx, admin, id, []string{f.items["movie-g"], "00000000-0000-4000-8000-000000000009"}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing item accepted: %v", err)
	}
	if _, err = f.s.AddCollectionItems(f.ctx, viewer, id, f.ids("movie-g")); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("viewer added: %v", err)
	}
	view, err := f.s.AddCollectionItems(f.ctx, admin, id, f.ids("movie-g", "movie-r", "series-pg", "movie-other", "movie-g"))
	if err != nil || view.Collection.ItemCount != 4 || !slices.Equal(collectionMemberNames(f, view), []string{"movie-g", "movie-other", "movie-r", "series-pg"}) {
		t.Fatalf("admin view: %+v %v", view, err)
	}
	// movie-other is outside the viewer's libraries; movie-r is hidden.
	f.hideFrom(t, f.viewer, "movie-r")
	view, err = f.s.Collection(f.ctx, viewer, id)
	if err != nil || view.Collection.ItemCount != 2 || !slices.Equal(collectionMemberNames(f, view), []string{"movie-g", "series-pg"}) {
		t.Fatalf("viewer view: %+v %v", view, err)
	}
	if view.Collection.CoverItemID != f.items["movie-g"] || !view.Items[0].Manual || view.Items[0].FromNFO {
		t.Fatalf("viewer cover or flags: %+v", view)
	}
	hidden, err := f.s.CreateCollection(f.ctx, admin, domain.CollectionInput{Name: "Elsewhere"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.AddCollectionItems(f.ctx, admin, hidden.Collection.ID, f.ids("movie-other", "movie-r")); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Collection(f.ctx, viewer, hidden.Collection.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("viewer read a collection without visible members: %v", err)
	}
	page, err := f.s.ListCollections(f.ctx, viewer, "", 10)
	if err != nil || len(page.Collections) != 1 || page.Collections[0].ID != id || page.Collections[0].ItemCount != 2 {
		t.Fatalf("viewer list: %+v %v", page, err)
	}
	page, err = f.s.ListCollections(f.ctx, admin, "", 1)
	if err != nil || len(page.Collections) != 1 || page.Collections[0].Name != "Elsewhere" || page.NextCursor == "" {
		t.Fatalf("admin first page: %+v %v", page, err)
	}
	if page, err = f.s.ListCollections(f.ctx, admin, page.NextCursor, 1); err != nil || len(page.Collections) != 1 || page.Collections[0].ID != id || page.NextCursor != "" {
		t.Fatalf("admin second page: %+v %v", page, err)
	}

	// NFO grouping: items whose collection metadata carries a name join the
	// collection of that name, trimmed and case-insensitively.
	f.setCollectionFact(t, "movie-pg13", `{"name":"Star Saga","overview":"From NFO"}`)
	f.setCollectionFact(t, "movie-16", `{"name":"star saga","overview":""}`)
	sync, err := f.s.SyncNFOCollections(f.ctx, admin)
	if err != nil || sync.Created != 1 {
		t.Fatalf("sync: %+v %v", sync, err)
	}
	if sync, err = f.s.SyncNFOCollections(f.ctx, admin); err != nil || sync.Created != 0 {
		t.Fatalf("second sync: %+v %v", sync, err)
	}
	if _, err = f.s.SyncNFOCollections(f.ctx, viewer); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("viewer synced: %v", err)
	}
	var star domain.Collection
	if page, err = f.s.ListCollections(f.ctx, viewer, "", 10); err != nil {
		t.Fatal(err)
	}
	for _, c := range page.Collections {
		if c.NFOName != nil {
			star = c
		}
	}
	if star.ID == "" || star.Name != "Star Saga" || star.Overview != "From NFO" || star.ItemCount != 2 {
		t.Fatalf("nfo collection: %+v", page)
	}
	// A later item with the name joins without a sync.
	f.setCollectionFact(t, "movie-unrated", `{"name":" STAR SAGA ","overview":""}`)
	view, err = f.s.Collection(f.ctx, viewer, star.ID)
	if err != nil || !slices.Equal(collectionMemberNames(f, view), []string{"movie-16", "movie-pg13", "movie-unrated"}) || !view.Items[0].FromNFO || view.Items[0].Manual {
		t.Fatalf("nfo members: %+v %v", view, err)
	}
	if _, err = f.s.RemoveCollectionItem(f.ctx, admin, star.ID, f.items["movie-16"]); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("removed an NFO member: %v", err)
	}
	if _, err = f.s.CreateCollection(f.ctx, admin, domain.CollectionInput{Name: "Copy", NFOName: new("STAR saga")}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate NFO name: %v", err)
	}
	if _, err = f.s.RemoveCollectionItem(f.ctx, admin, id, f.items["movie-pg13"]); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("removed a non-member: %v", err)
	}
	if view, err = f.s.RemoveCollectionItem(f.ctx, admin, id, f.items["movie-g"]); err != nil || view.Collection.ItemCount != 3 {
		t.Fatalf("remove: %+v %v", view, err)
	}
	renamed, err := f.s.UpdateCollection(f.ctx, admin, id, domain.CollectionInput{Name: "Saga II", NFOName: new("Other")})
	if err != nil || renamed.Collection.Name != "Saga II" || renamed.Collection.NFOName == nil || *renamed.Collection.NFOName != "Other" {
		t.Fatalf("rename: %+v %v", renamed, err)
	}
	if err = f.s.DeleteCollection(f.ctx, viewer, id); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("viewer deleted: %v", err)
	}
	if err = f.s.DeleteCollection(f.ctx, admin, id); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Collection(f.ctx, admin, id); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("deleted collection read: %v", err)
	}
	if syncCount(t, f.jobFixture, `SELECT count(*) FROM items WHERE id=ANY($1::uuid[])`, f.ids("movie-g", "movie-r", "series-pg")) != 3 {
		t.Fatal("deleting a collection removed items")
	}
	if n := syncCount(t, f.jobFixture, `SELECT count(*) FROM audit_logs WHERE event LIKE 'collection.%'`); n != 8 {
		t.Fatalf("collection audit rows %d", n)
	}
}

// TestPlaylistsVisibilityAndOrderPostgres checks playlist ownership, public
// read access through the unified filter, stable ordering under concurrent
// changes and deletion with the owner (G02.1, G48.3, G07.7).
func TestPlaylistsVisibilityAndOrderPostgres(t *testing.T) {
	f := newContentAccessFixture(t)
	viewer, peer := f.viewer.actor, f.peer.actor
	mine, err := f.s.CreatePlaylist(f.ctx, viewer, domain.PlaylistInput{Name: "Mine"})
	if err != nil || !mine.Playlist.Owned || mine.Playlist.Public || mine.Playlist.OwnerID != viewer.UserID {
		t.Fatalf("create: %+v %v", mine, err)
	}
	id := mine.Playlist.ID
	if _, err = f.s.AddPlaylistItems(f.ctx, viewer, id, f.ids("series-pg")); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("series accepted: %v", err)
	}
	if _, err = f.s.AddPlaylistItems(f.ctx, viewer, id, f.ids("movie-g", "movie-other")); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("invisible item accepted: %v", err)
	}
	view, err := f.s.AddPlaylistItems(f.ctx, viewer, id, f.ids("movie-g", "episode-pg", "movie-g"))
	if err != nil || !slices.Equal(playlistEntryNames(f, view), []string{"movie-g", "episode-pg", "movie-g"}) || view.Playlist.CoverItemID != f.items["movie-g"] {
		t.Fatalf("append: %+v %v", view, err)
	}
	if syncCount(t, f.jobFixture, `SELECT count(*) FROM playlist_items WHERE playlist_id=$1::uuid`, id) != 3 {
		t.Fatal("refused append stored entries")
	}
	// Private: another user can neither list, read nor change it.
	if page, err := f.s.ListPlaylists(f.ctx, peer, "", 10); err != nil || len(page.Playlists) != 0 {
		t.Fatalf("peer listed a private playlist: %+v %v", page, err)
	}
	if _, err = f.s.Playlist(f.ctx, peer, id); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("peer read: %v", err)
	}
	if _, err = f.s.UpdatePlaylist(f.ctx, peer, id, domain.PlaylistInput{Name: "Taken", Public: true}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("peer update of a private playlist: %v", err)
	}
	if view, err = f.s.UpdatePlaylist(f.ctx, viewer, id, domain.PlaylistInput{Name: "Shared", Public: true}); err != nil || !view.Playlist.Public || view.Playlist.Name != "Shared" {
		t.Fatalf("publish: %+v %v", view, err)
	}
	// Public: readable, not changeable, and filtered by the reader's rules.
	if _, err = f.s.AddPlaylistItems(f.ctx, peer, id, f.ids("movie-g")); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("peer append: %v", err)
	}
	if err = f.s.DeletePlaylist(f.ctx, peer, id); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("peer delete: %v", err)
	}
	f.hideFrom(t, f.peer, "movie-g")
	view, err = f.s.Playlist(f.ctx, peer, id)
	if err != nil || view.Playlist.Owned || view.Playlist.ItemCount != 1 || !slices.Equal(playlistEntryNames(f, view), []string{"episode-pg"}) || view.Playlist.CoverItemID != f.items["episode-pg"] {
		t.Fatalf("peer view: %+v %v", view, err)
	}
	f.hideFrom(t, f.peer, "episode-pg")
	if page, err := f.s.ListPlaylists(f.ctx, peer, "", 10); err != nil || len(page.Playlists) != 0 {
		t.Fatalf("peer listed a playlist without visible entries: %+v %v", page, err)
	}
	if _, err = f.s.Playlist(f.ctx, peer, id); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("peer read a playlist without visible entries: %v", err)
	}
	// The owner's own hidden items drop out of the owner's view as well,
	// but keep their place.
	f.hideFrom(t, f.viewer, "episode-pg")
	view, err = f.s.Playlist(f.ctx, viewer, id)
	if err != nil || !slices.Equal(playlistEntryNames(f, view), []string{"movie-g", "movie-g"}) {
		t.Fatalf("owner view with a hidden entry: %+v %v", view, err)
	}
	first, last := view.Entries[0].EntryID, view.Entries[1].EntryID
	if view, err = f.s.MovePlaylistEntry(f.ctx, viewer, id, last, first); err != nil || view.Entries[0].EntryID != last || view.Entries[1].EntryID != first {
		t.Fatalf("move before: %+v %v", view, err)
	}
	if syncCount(t, f.jobFixture, `SELECT count(*) FROM playlist_items e JOIN items i ON i.id=e.item_id WHERE e.playlist_id=$1::uuid AND i.title='episode-pg' AND e.position=2`, id) != 1 {
		t.Fatal("moving visible entries changed the hidden entry's place")
	}
	if view, err = f.s.MovePlaylistEntry(f.ctx, viewer, id, last, ""); err != nil || view.Entries[1].EntryID != last {
		t.Fatalf("move to end: %+v %v", view, err)
	}
	if _, err = f.s.MovePlaylistEntry(f.ctx, viewer, id, first, "00000000-0000-4000-8000-000000000009"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("move before a foreign entry: %v", err)
	}
	if view, err = f.s.RemovePlaylistEntry(f.ctx, viewer, id, first); err != nil || len(view.Entries) != 1 || view.Entries[0].EntryID != last {
		t.Fatalf("remove: %+v %v", view, err)
	}
	if _, err = f.s.RemovePlaylistEntry(f.ctx, viewer, id, first); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("second remove: %v", err)
	}

	// Concurrent appends apply one after another: every batch stays
	// contiguous and positions stay unique.
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			_, err := f.s.AddPlaylistItems(f.ctx, viewer, id, f.ids("movie-g", "movie-pg13", "movie-16", "movie-unrated"))
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal("concurrent append", err)
		}
	}
	view, err = f.s.Playlist(f.ctx, viewer, id)
	if err != nil || len(view.Entries) != 33 {
		t.Fatalf("after concurrent appends: %d %v", len(view.Entries), err)
	}
	for i := 1; i < 33; i += 4 {
		if !slices.Equal(playlistEntryNames(f, domain.PlaylistView{Entries: view.Entries[i : i+4]}), []string{"movie-g", "movie-pg13", "movie-16", "movie-unrated"}) {
			t.Fatalf("batch at %d interleaved: %v", i, playlistEntryNames(f, view))
		}
	}
	if syncCount(t, f.jobFixture, `SELECT count(DISTINCT position) FROM playlist_items WHERE playlist_id=$1::uuid`, id) != 34 {
		t.Fatal("positions not unique")
	}

	// Deleting the owner deletes the playlists (G07.7).
	if _, err = f.s.CreatePlaylist(f.ctx, viewer, domain.PlaylistInput{Name: "Second"}); err != nil {
		t.Fatal(err)
	}
	if err = f.s.DeleteUser(f.ctx, f.a, viewer.UserID); err != nil {
		t.Fatal(err)
	}
	if syncCount(t, f.jobFixture, `SELECT count(*) FROM playlists WHERE owner_id=$1::uuid`, viewer.UserID) != 0 ||
		syncCount(t, f.jobFixture, `SELECT count(*) FROM playlist_items WHERE playlist_id=$1::uuid`, id) != 0 {
		t.Fatal("deleting the user kept playlists")
	}
	// A purged user row cascades as well.
	if syncCount(t, f.jobFixture, `SELECT count(*) FROM pg_constraint WHERE contype='f' AND conrelid='playlists'::regclass AND confrelid='users'::regclass AND confdeltype='c'`) != 1 {
		t.Fatal("playlists do not cascade from users")
	}
}

// TestCollectionsPlaylistsMigrationRoundTrip checks that the migration
// refuses to drop stored collections or playlists and round-trips empty.
func TestCollectionsPlaylistsMigrationRoundTrip(t *testing.T) {
	f := newContentAccessFixture(t)
	dsn := f.s.Pool.Config().ConnString()
	c, err := f.s.CreateCollection(f.ctx, f.a, domain.CollectionInput{Name: "Keep"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.s.CreatePlaylist(f.ctx, f.viewer.actor, domain.PlaylistInput{Name: "Keep"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.AddPlaylistItems(f.ctx, f.viewer.actor, p.Playlist.ID, f.ids("movie-g")); err != nil {
		t.Fatal(err)
	}
	want := downgradeAboveMigration(t, f.jobFixture, "collections_playlists")
	refuse := func(what string) {
		t.Helper()
		if _, _, err := Migrate(f.ctx, dsn, "down"); err == nil {
			t.Fatalf("downgraded with %s", what)
		}
		if _, err := f.s.Pool.Exec(f.ctx, `UPDATE schema_migrations SET version=$1,dirty=false`, want); err != nil {
			t.Fatal(err)
		}
	}
	refuse("a collection and a playlist")
	if err = f.s.DeleteCollection(f.ctx, f.a, c.Collection.ID); err != nil {
		t.Fatal(err)
	}
	refuse("a playlist")
	if err = f.s.DeletePlaylist(f.ctx, f.viewer.actor, p.Playlist.ID); err != nil {
		t.Fatal(err)
	}
	if version, dirty, err := Migrate(f.ctx, dsn, "down"); err != nil || dirty || version != want-1 {
		t.Fatalf("downgrade: %d %t %v", version, dirty, err)
	}
	if syncCount(t, f.jobFixture, `SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name IN ('collections','collection_items','playlists','playlist_items')`) != 0 ||
		syncCount(t, f.jobFixture, `SELECT count(*) FROM pg_indexes WHERE schemaname=current_schema() AND indexname='item_metadata_facts_collection_name_idx'`) != 0 {
		t.Fatal("downgrade left collection or playlist schema")
	}
	if syncCount(t, f.jobFixture, `SELECT count(*) FROM audit_logs WHERE event LIKE 'collection.%'`) != 2 {
		t.Fatal("downgrade removed audit history")
	}
	if version, dirty, err := Migrate(f.ctx, dsn, "up"); err != nil || dirty || version != SchemaVersion {
		t.Fatalf("upgrade: %d %t %v", version, dirty, err)
	}
	if _, err = f.s.CreatePlaylist(f.ctx, f.viewer.actor, domain.PlaylistInput{Name: "Again"}); err != nil {
		t.Fatal(err)
	}
}

// TestCollectionPlaylistMembershipSurvivesMergeUndo checks that undoing a
// version merge brings the absorbed item's memberships back (G20.5).
func TestCollectionPlaylistMembershipSurvivesMergeUndo(t *testing.T) {
	f := newContentAccessFixture(t)
	c, err := f.s.CreateCollection(f.ctx, f.a, domain.CollectionInput{Name: "Merged"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.AddCollectionItems(f.ctx, f.a, c.Collection.ID, f.ids("movie-g", "movie-pg13")); err != nil {
		t.Fatal(err)
	}
	p, err := f.s.CreatePlaylist(f.ctx, f.viewer.actor, domain.PlaylistInput{Name: "Merged"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.AddPlaylistItems(f.ctx, f.viewer.actor, p.Playlist.ID, f.ids("movie-pg13", "movie-g")); err != nil {
		t.Fatal(err)
	}
	op, err := f.s.MergeItems(f.ctx, f.a, f.items["movie-g"], f.items["movie-pg13"])
	if err != nil {
		t.Fatal(err)
	}
	if view, err := f.s.Playlist(f.ctx, f.viewer.actor, p.Playlist.ID); err != nil || !slices.Equal(playlistEntryNames(f, view), []string{"movie-g"}) {
		t.Fatalf("after merge: %+v %v", view, err)
	}
	if _, err = f.s.UndoVersionOperation(f.ctx, f.a, op.ID); err != nil {
		t.Fatal(err)
	}
	if view, err := f.s.Playlist(f.ctx, f.viewer.actor, p.Playlist.ID); err != nil || !slices.Equal(playlistEntryNames(f, view), []string{"movie-pg13", "movie-g"}) {
		t.Fatalf("after undo: %+v %v", view, err)
	}
	if view, err := f.s.Collection(f.ctx, f.a, c.Collection.ID); err != nil || !slices.Equal(collectionMemberNames(f, view), []string{"movie-g", "movie-pg13"}) {
		t.Fatalf("collection after undo: %+v %v", view, err)
	}
}
