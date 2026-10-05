package postgres

import (
	"context"
	"slices"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

// Playlists (G02.1). This file administers playlists and playlist_items
// (TestVisibilityPredicateHasOneSource keeps every other file from reading
// them, except the account deletion statement). A playlist belongs to one
// user, who alone changes it; a public one is readable by every other user.
// Entries are bound to the reader through itemVisibleSQL in the same
// statement (G48.3): each reader sees, counts and gets a cover from only
// the entries visible to them, and another user's public playlist without
// a visible entry is neither listed nor readable. Changes lock the
// playlist row first, so concurrent edits of one playlist apply one after
// another; entries keep their order in position, ties broken by id.

// playlistStatsSQL counts the entries of p visible to u and picks the first
// in playlist order as the cover.
func playlistStatsSQL(rq string) string {
	return `CROSS JOIN LATERAL (SELECT count(*)::int AS n,(array_agg(ps_i.id::text ORDER BY ps_e.position,ps_e.id))[1] AS cover
  FROM playlist_items ps_e JOIN items ps_i ON ps_i.id=ps_e.item_id WHERE ps_e.playlist_id=p.id AND ` + itemVisibleSQL(rq, "ps_i.library_id", "ps_i.id") + `) v`
}

const playlistColumns = `p.id::text,p.name,p.public,p.owner_id::text,COALESCE(NULLIF(o.display_name,''),o.name),p.owner_id=u.id,v.n,COALESCE(v.cover,''),p.created_at,p.updated_at`

// playlistReadableSQL holds for the own playlists of u and for the public
// playlists of other live users that have a visible entry.
const playlistReadableSQL = `(p.owner_id=u.id OR p.public AND v.n>0)`

// playlistListSQL pages the readable playlists after the cursor ($3) in
// name order; $4 is the page size and $5 the request scope.
var playlistListSQL = `SELECT ` + playlistColumns + ` FROM ` + liveUserSQL + `
 JOIN playlists p ON p.owner_id=u.id OR p.public
 JOIN users o ON o.id=p.owner_id AND o.deleted_at IS NULL ` + playlistStatsSQL("$5") + `
 WHERE ` + liveUserWhereSQL + ` AND ` + playlistReadableSQL + `
 AND ($3::uuid IS NULL OR (lower(p.name),p.id)>(SELECT lower(k.name),k.id FROM playlists k WHERE k.id=$3::uuid))
 ORDER BY lower(p.name),p.id LIMIT $4`

// playlistOneSQL reads one playlist ($2) as the user $1 whether readable or
// not; the caller decides. $3 is the request scope.
var playlistOneSQL = `SELECT ` + playlistColumns + `,` + playlistReadableSQL + ` FROM users u
 JOIN playlists p ON p.id=$2::uuid
 JOIN users o ON o.id=p.owner_id AND o.deleted_at IS NULL ` + playlistStatsSQL("$3") + `
 WHERE u.id=$1::uuid`

// playlistEntriesSQL lists the entries of one playlist ($2) visible to the
// user $1 in order; $3 is the request scope.
var playlistEntriesSQL = `SELECT e.id::text,i.id::text,i.library_id::text,i.title,i.kind,COALESCE(pl.parent_id::text,'')
 FROM playlist_items e JOIN items i ON i.id=e.item_id JOIN users u ON u.id=$1::uuid
 LEFT JOIN item_parent_links pl ON pl.item_id=i.id
 WHERE e.playlist_id=$2::uuid AND ` + itemVisibleSQL("$3", "i.library_id", "i.id") + `
 ORDER BY e.position,e.id`

func scanPlaylist(row pgx.Row, extra ...any) (domain.Playlist, error) {
	var p domain.Playlist
	err := row.Scan(append([]any{&p.ID, &p.Name, &p.Public, &p.OwnerID, &p.OwnerName, &p.Owned, &p.ItemCount, &p.CoverItemID, &p.CreatedAt, &p.UpdatedAt}, extra...)...)
	return p, err
}

// ListPlaylists pages the caller's own and other users' public playlists.
func (s *Store) ListPlaylists(ctx context.Context, actor domain.Actor, cursor string, limit int) (domain.PlaylistPage, error) {
	page := domain.PlaylistPage{Playlists: []domain.Playlist{}}
	if limit < 1 || limit > domain.CollectionPageMax || cursor != "" && !domain.ValidID(cursor) {
		return page, domain.ErrInvalid
	}
	tx, err := s.readTransaction(ctx, actor)
	if err != nil {
		return page, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, playlistListSQL, actor.UserID, actor.SessionID, nullableID(cursor), limit+1, requestScopeArg(ctx))
	if err != nil {
		return page, storageError(err)
	}
	defer rows.Close()
	for rows.Next() {
		p, err := scanPlaylist(rows)
		if err != nil {
			return page, storageError(err)
		}
		page.Playlists = append(page.Playlists, p)
	}
	if err = rows.Err(); err != nil {
		return page, storageError(err)
	}
	if len(page.Playlists) > limit {
		page.Playlists = page.Playlists[:limit]
		page.NextCursor = page.Playlists[limit-1].ID
	}
	return page, nil
}

// readPlaylist reads one readable playlist with its visible entries.
func readPlaylist(ctx context.Context, q queryer, actor domain.Actor, id string) (domain.PlaylistView, error) {
	var readable bool
	p, err := scanPlaylist(q.QueryRow(ctx, playlistOneSQL, actor.UserID, id, requestScopeArg(ctx)), &readable)
	if err != nil {
		return domain.PlaylistView{}, storageError(err)
	}
	if !readable {
		return domain.PlaylistView{}, domain.ErrNotFound
	}
	view := domain.PlaylistView{Playlist: p, Entries: []domain.PlaylistEntry{}}
	rows, err := q.Query(ctx, playlistEntriesSQL, actor.UserID, id, requestScopeArg(ctx))
	if err != nil {
		return view, storageError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var e domain.PlaylistEntry
		if err = rows.Scan(&e.EntryID, &e.Item.ID, &e.Item.LibraryID, &e.Item.Title, &e.Item.Kind, &e.Item.ParentID); err != nil {
			return view, storageError(err)
		}
		view.Entries = append(view.Entries, e)
	}
	return view, storageError(rows.Err())
}

// Playlist reads one own or public playlist with its visible entries.
func (s *Store) Playlist(ctx context.Context, actor domain.Actor, id string) (domain.PlaylistView, error) {
	if !domain.ValidID(id) {
		return domain.PlaylistView{}, domain.ErrNotFound
	}
	tx, err := s.readTransaction(ctx, actor)
	if err != nil {
		return domain.PlaylistView{}, err
	}
	defer tx.Rollback(ctx)
	return readPlaylist(ctx, tx, actor, id)
}

// CreatePlaylist creates a playlist of the actor; at most
// PlaylistsPerUserMax per user. The actor's locked user row serializes the
// count.
func (s *Store) CreatePlaylist(ctx context.Context, actor domain.Actor, in domain.PlaylistInput) (domain.PlaylistView, error) {
	if ctx == nil || !in.Valid() {
		return domain.PlaylistView{}, domain.ErrInvalid
	}
	tx, err := s.memberTransaction(ctx, actor)
	if err != nil {
		return domain.PlaylistView{}, err
	}
	defer tx.Rollback(ctx)
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM playlists WHERE owner_id=$1::uuid`, actor.UserID).Scan(&count); err != nil {
		return domain.PlaylistView{}, storageError(err)
	}
	if count >= domain.PlaylistsPerUserMax {
		return domain.PlaylistView{}, domain.ErrConflict
	}
	var id string
	if err = tx.QueryRow(ctx, `INSERT INTO playlists(owner_id,name,public) VALUES($1::uuid,$2,$3) RETURNING id::text`, actor.UserID, in.Name, in.Public).Scan(&id); err != nil {
		return domain.PlaylistView{}, storageError(err)
	}
	view, err := readPlaylist(ctx, tx, actor, id)
	if err != nil {
		return view, err
	}
	return view, storageError(tx.Commit(ctx))
}

// lockOwnPlaylist locks a playlist of the actor for a change. Another
// user's playlist is ErrForbidden when the actor may read it and
// ErrNotFound otherwise, like a missing one.
func lockOwnPlaylist(ctx context.Context, tx pgx.Tx, actor domain.Actor, id string) error {
	var owner string
	if err := tx.QueryRow(ctx, `SELECT owner_id::text FROM playlists WHERE id=$1::uuid FOR UPDATE`, id).Scan(&owner); err != nil {
		return storageError(err)
	}
	if owner == actor.UserID {
		return nil
	}
	if _, err := readPlaylist(ctx, tx, actor, id); err != nil {
		return err
	}
	return domain.ErrForbidden
}

// UpdatePlaylist renames a playlist of the actor or changes whether it is
// public.
func (s *Store) UpdatePlaylist(ctx context.Context, actor domain.Actor, id string, in domain.PlaylistInput) (domain.PlaylistView, error) {
	if ctx == nil || !in.Valid() {
		return domain.PlaylistView{}, domain.ErrInvalid
	}
	return s.changePlaylist(ctx, actor, id, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE playlists SET name=$2,public=$3,updated_at=clock_timestamp() WHERE id=$1::uuid`, id, in.Name, in.Public)
		return storageError(err)
	})
}

// DeletePlaylist removes a playlist of the actor with its entries.
func (s *Store) DeletePlaylist(ctx context.Context, actor domain.Actor, id string) error {
	if !domain.ValidID(id) {
		return domain.ErrNotFound
	}
	tx, err := s.memberTransaction(ctx, actor)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockOwnPlaylist(ctx, tx, actor, id); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM playlists WHERE id=$1::uuid`, id); err != nil {
		return storageError(err)
	}
	return storageError(tx.Commit(ctx))
}

// changePlaylist applies one change to a locked playlist of the actor and
// returns the playlist as the actor now sees it.
func (s *Store) changePlaylist(ctx context.Context, actor domain.Actor, id string, change func(pgx.Tx) error) (domain.PlaylistView, error) {
	if !domain.ValidID(id) {
		return domain.PlaylistView{}, domain.ErrNotFound
	}
	tx, err := s.memberTransaction(ctx, actor)
	if err != nil {
		return domain.PlaylistView{}, err
	}
	defer tx.Rollback(ctx)
	if err = lockOwnPlaylist(ctx, tx, actor, id); err != nil {
		return domain.PlaylistView{}, err
	}
	if err = change(tx); err != nil {
		return domain.PlaylistView{}, err
	}
	view, err := readPlaylist(ctx, tx, actor, id)
	if err != nil {
		return view, err
	}
	return view, storageError(tx.Commit(ctx))
}

// playlistPositionMax bounds stored positions; appends renumber the
// playlist before passing it.
const playlistPositionMax = 1 << 30

// renumberPlaylist stores the entries in order as positions 0..n-1.
func renumberPlaylist(ctx context.Context, tx pgx.Tx, id string, entries []string) error {
	_, err := tx.Exec(ctx, `UPDATE playlist_items e SET position=o.ord-1 FROM unnest($2::uuid[]) WITH ORDINALITY o(id,ord) WHERE e.id=o.id AND e.playlist_id=$1::uuid`, id, entries)
	return storageError(err)
}

// playlistOrder reads every entry ID of a locked playlist in order,
// including entries the actor may not see.
func playlistOrder(ctx context.Context, tx pgx.Tx, id string) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT id::text FROM playlist_items WHERE playlist_id=$1::uuid ORDER BY position,id`, id)
	if err != nil {
		return nil, storageError(err)
	}
	entries, err := pgx.CollectRows(rows, pgx.RowTo[string])
	return entries, storageError(err)
}

// AddPlaylistItems appends visible items of PlaylistKinds in the given
// order, repeats included.
func (s *Store) AddPlaylistItems(ctx context.Context, actor domain.Actor, id string, itemIDs []string) (domain.PlaylistView, error) {
	if ctx == nil || !domain.ValidMembershipBatch(itemIDs) {
		return domain.PlaylistView{}, domain.ErrInvalid
	}
	return s.changePlaylist(ctx, actor, id, func(tx pgx.Tx) error {
		if err := checkMemberItems(ctx, tx, actor, distinctIDs(itemIDs), domain.PlaylistKinds); err != nil {
			return err
		}
		var count, next int
		if err := tx.QueryRow(ctx, `SELECT count(*),COALESCE(max(position)+1,0) FROM playlist_items WHERE playlist_id=$1::uuid`, id).Scan(&count, &next); err != nil {
			return storageError(err)
		}
		if count+len(itemIDs) > domain.PlaylistEntriesMax {
			return domain.ErrConflict
		}
		if next+len(itemIDs) > playlistPositionMax {
			entries, err := playlistOrder(ctx, tx, id)
			if err != nil {
				return err
			}
			if err = renumberPlaylist(ctx, tx, id, entries); err != nil {
				return err
			}
			next = len(entries)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO playlist_items(playlist_id,item_id,position) SELECT $1::uuid,x.id,$3::int+x.ord-1 FROM unnest($2::uuid[]) WITH ORDINALITY x(id,ord)`, id, itemIDs, next); err != nil {
			return storageError(err)
		}
		_, err := tx.Exec(ctx, `UPDATE playlists SET updated_at=clock_timestamp() WHERE id=$1::uuid`, id)
		return storageError(err)
	})
}

// RemovePlaylistEntry removes one entry of a playlist of the actor.
func (s *Store) RemovePlaylistEntry(ctx context.Context, actor domain.Actor, id, entryID string) (domain.PlaylistView, error) {
	if ctx == nil {
		return domain.PlaylistView{}, domain.ErrInvalid
	}
	if !domain.ValidID(entryID) {
		return domain.PlaylistView{}, domain.ErrNotFound
	}
	return s.changePlaylist(ctx, actor, id, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM playlist_items WHERE id=$2::uuid AND playlist_id=$1::uuid`, id, entryID)
		if err != nil {
			return storageError(err)
		}
		if tag.RowsAffected() == 0 {
			return domain.ErrNotFound
		}
		_, err = tx.Exec(ctx, `UPDATE playlists SET updated_at=clock_timestamp() WHERE id=$1::uuid`, id)
		return storageError(err)
	})
}

// MovePlaylistEntry moves an entry before another one of the same
// playlist, or to the end when beforeEntryID is empty, and renumbers the
// playlist.
func (s *Store) MovePlaylistEntry(ctx context.Context, actor domain.Actor, id, entryID, beforeEntryID string) (domain.PlaylistView, error) {
	if ctx == nil || beforeEntryID != "" && (!domain.ValidID(beforeEntryID) || beforeEntryID == entryID) {
		return domain.PlaylistView{}, domain.ErrInvalid
	}
	if !domain.ValidID(entryID) {
		return domain.PlaylistView{}, domain.ErrNotFound
	}
	return s.changePlaylist(ctx, actor, id, func(tx pgx.Tx) error {
		entries, err := playlistOrder(ctx, tx, id)
		if err != nil {
			return err
		}
		from := slices.Index(entries, entryID)
		if from < 0 || beforeEntryID != "" && !slices.Contains(entries, beforeEntryID) {
			return domain.ErrNotFound
		}
		entries = slices.Delete(entries, from, from+1)
		to := len(entries)
		if beforeEntryID != "" {
			to = slices.Index(entries, beforeEntryID)
		}
		entries = slices.Insert(entries, to, entryID)
		if err = renumberPlaylist(ctx, tx, id, entries); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE playlists SET updated_at=clock_timestamp() WHERE id=$1::uuid`, id)
		return storageError(err)
	})
}
