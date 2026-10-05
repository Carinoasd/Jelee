package postgres

import (
	"context"
	"slices"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

// Collections (G02.1). This file administers collections and
// collection_items (TestVisibilityPredicateHasOneSource keeps every other
// file from reading them); the playlist tables belong to their own file. Member items
// are always bound to the caller through itemVisibleSQL in the same
// statement (G48.3), so a hidden member is never listed or counted, and a
// collection without a visible member is listed to administrators only.
// Changes need a live administrator and are audited.

// queryer is what reads need from a pool or a transaction.
type queryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// collectionMembersSQL selects the item IDs of the collection alias c: its
// manual members and, for an NFO-linked collection, every item whose
// collection metadata fact carries the name (trimmed, case-insensitive).
func collectionMembersSQL(c string) string {
	return `SELECT cm_i.item_id FROM collection_items cm_i WHERE cm_i.collection_id=` + c + `.id
 UNION SELECT cm_f.item_id FROM item_metadata_facts cm_f WHERE ` + c + `.nfo_name IS NOT NULL AND cm_f.field='collection' AND lower(btrim(cm_f.value->>'name'))=lower(btrim(` + c + `.nfo_name))`
}

// collectionStatsSQL counts the visible members of c for the user u and
// picks the first in title order as the cover.
func collectionStatsSQL(rq, c string) string {
	return `CROSS JOIN LATERAL (SELECT count(*)::int AS n,(array_agg(cs_i.id::text ORDER BY lower(cs_i.title),cs_i.id))[1] AS cover
  FROM (` + collectionMembersSQL(c) + `) cs_m JOIN items cs_i ON cs_i.id=cs_m.item_id WHERE ` + itemVisibleSQL(rq, "cs_i.library_id", "cs_i.id") + `) v`
}

const collectionColumns = `c.id::text,c.name,c.overview,c.nfo_name,v.n,COALESCE(v.cover,''),c.created_at,c.updated_at`

// liveUserSQL joins the actor's live user and session as u; $1 and $2 are
// the user and session IDs.
const liveUserSQL = `users u JOIN sessions cs_s ON cs_s.id=$2::uuid AND cs_s.user_id=u.id AND cs_s.revoked_at IS NULL AND cs_s.expires_at>clock_timestamp()`

const liveUserWhereSQL = `u.id=$1::uuid AND NOT u.disabled AND u.deleted_at IS NULL`

// collectionListSQL pages the collections after the cursor ($3) in name
// order; $4 is the page size and $5 the request scope.
var collectionListSQL = `SELECT ` + collectionColumns + ` FROM ` + liveUserSQL + ` CROSS JOIN collections c ` + collectionStatsSQL("$5", "c") + `
 WHERE ` + liveUserWhereSQL + ` AND (u.is_admin OR v.n>0)
 AND ($3::uuid IS NULL OR (lower(c.name),c.id)>(SELECT lower(k.name),k.id FROM collections k WHERE k.id=$3::uuid))
 ORDER BY lower(c.name),c.id LIMIT $4`

// collectionOneSQL reads one collection ($3) with the caller's counts; $4
// is the request scope.
var collectionOneSQL = `SELECT ` + collectionColumns + `,u.is_admin FROM ` + liveUserSQL + ` CROSS JOIN collections c ` + collectionStatsSQL("$4", "c") + `
 WHERE ` + liveUserWhereSQL + ` AND c.id=$3::uuid`

// collectionMembersQuery lists the visible members of one collection ($2)
// to the user $1 in title order; $3 is the limit and $4 the request scope.
var collectionMembersQuery = `SELECT i.id::text,i.library_id::text,i.title,i.kind,COALESCE(pl.parent_id::text,''),
 EXISTS(SELECT 1 FROM collection_items ci WHERE ci.collection_id=c.id AND ci.item_id=i.id),
 c.nfo_name IS NOT NULL AND EXISTS(SELECT 1 FROM item_metadata_facts f WHERE f.item_id=i.id AND f.field='collection' AND lower(btrim(f.value->>'name'))=lower(btrim(c.nfo_name)))
 FROM collections c JOIN users u ON u.id=$1::uuid
 JOIN items i ON i.id IN (` + collectionMembersSQL("c") + `)
 LEFT JOIN item_parent_links pl ON pl.item_id=i.id
 WHERE c.id=$2::uuid AND ` + itemVisibleSQL("$4", "i.library_id", "i.id") + `
 ORDER BY lower(i.title),i.id LIMIT $3`

func scanCollection(row pgx.Row, extra ...any) (domain.Collection, error) {
	var c domain.Collection
	err := row.Scan(append([]any{&c.ID, &c.Name, &c.Overview, &c.NFOName, &c.ItemCount, &c.CoverItemID, &c.CreatedAt, &c.UpdatedAt}, extra...)...)
	return c, err
}

// readTransaction opens a read-only transaction for a live actor, so a
// revoked session is told apart from an empty result.
func (s *Store) readTransaction(ctx context.Context, actor domain.Actor) (pgx.Tx, error) {
	if ctx == nil {
		return nil, domain.ErrInvalid
	}
	if !domain.ValidID(actor.UserID) || !domain.ValidID(actor.SessionID) {
		return nil, domain.ErrUnauthenticated
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, storageError(err)
	}
	var live bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM `+liveUserSQL+` WHERE `+liveUserWhereSQL+`)`, actor.UserID, actor.SessionID).Scan(&live); err != nil || !live {
		_ = tx.Rollback(ctx)
		if err != nil {
			return nil, storageError(err)
		}
		return nil, domain.ErrUnauthenticated
	}
	return tx, nil
}

// ListCollections pages the collections the caller may see.
func (s *Store) ListCollections(ctx context.Context, actor domain.Actor, cursor string, limit int) (domain.CollectionPage, error) {
	page := domain.CollectionPage{Collections: []domain.Collection{}}
	if limit < 1 || limit > domain.CollectionPageMax || cursor != "" && !domain.ValidID(cursor) {
		return page, domain.ErrInvalid
	}
	tx, err := s.readTransaction(ctx, actor)
	if err != nil {
		return page, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, collectionListSQL, actor.UserID, actor.SessionID, nullableID(cursor), limit+1, requestScopeArg(ctx))
	if err != nil {
		return page, storageError(err)
	}
	defer rows.Close()
	for rows.Next() {
		c, err := scanCollection(rows)
		if err != nil {
			return page, storageError(err)
		}
		page.Collections = append(page.Collections, c)
	}
	if err = rows.Err(); err != nil {
		return page, storageError(err)
	}
	if len(page.Collections) > limit {
		page.Collections = page.Collections[:limit]
		page.NextCursor = page.Collections[limit-1].ID
	}
	return page, nil
}

// readCollection reads one collection with its visible members as the
// actor; ErrNotFound when missing or, for a non-administrator, without a
// visible member.
func readCollection(ctx context.Context, q queryer, actor domain.Actor, id string) (domain.CollectionView, error) {
	var admin bool
	c, err := scanCollection(q.QueryRow(ctx, collectionOneSQL, actor.UserID, actor.SessionID, id, requestScopeArg(ctx)), &admin)
	if err != nil {
		return domain.CollectionView{}, storageError(err)
	}
	if !admin && c.ItemCount == 0 {
		return domain.CollectionView{}, domain.ErrNotFound
	}
	view := domain.CollectionView{Collection: c, Items: []domain.CollectionMember{}}
	rows, err := q.Query(ctx, collectionMembersQuery, actor.UserID, id, domain.CollectionViewItemsMax+1, requestScopeArg(ctx))
	if err != nil {
		return view, storageError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var m domain.CollectionMember
		if err = rows.Scan(&m.ID, &m.LibraryID, &m.Title, &m.Kind, &m.ParentID, &m.Manual, &m.FromNFO); err != nil {
			return view, storageError(err)
		}
		view.Items = append(view.Items, m)
	}
	if err = rows.Err(); err != nil {
		return view, storageError(err)
	}
	if len(view.Items) > domain.CollectionViewItemsMax {
		view.Items, view.Truncated = view.Items[:domain.CollectionViewItemsMax], true
	}
	return view, nil
}

// Collection reads one collection with its visible members.
func (s *Store) Collection(ctx context.Context, actor domain.Actor, id string) (domain.CollectionView, error) {
	if !domain.ValidID(id) {
		return domain.CollectionView{}, domain.ErrNotFound
	}
	tx, err := s.readTransaction(ctx, actor)
	if err != nil {
		return domain.CollectionView{}, err
	}
	defer tx.Rollback(ctx)
	return readCollection(ctx, tx, actor, id)
}

// collectionAudit is the audited state of a collection.
type collectionAudit struct {
	Name     string  `json:"name"`
	Overview string  `json:"overview"`
	NFOName  *string `json:"nfoName"`
}

// lockCollection locks one collection row for a change.
func lockCollection(ctx context.Context, tx pgx.Tx, id string) (collectionAudit, error) {
	var a collectionAudit
	err := tx.QueryRow(ctx, `SELECT name,overview,nfo_name FROM collections WHERE id=$1::uuid FOR UPDATE`, id).Scan(&a.Name, &a.Overview, &a.NFOName)
	return a, storageError(err)
}

// CreateCollection creates a collection; at most CollectionsMax exist.
func (s *Store) CreateCollection(ctx context.Context, actor domain.Actor, in domain.CollectionInput) (domain.CollectionView, error) {
	if ctx == nil || !in.Valid() {
		return domain.CollectionView{}, domain.ErrInvalid
	}
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return domain.CollectionView{}, err
	}
	defer tx.Rollback(ctx)
	// The account transaction serializes administrators, so the count
	// cannot be passed by concurrent creations.
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM collections`).Scan(&count); err != nil {
		return domain.CollectionView{}, storageError(err)
	}
	if count >= domain.CollectionsMax {
		return domain.CollectionView{}, domain.ErrConflict
	}
	var id string
	if err = tx.QueryRow(ctx, `INSERT INTO collections(name,overview,nfo_name,created_by) VALUES($1,$2,$3,$4::uuid) RETURNING id::text`,
		in.Name, in.Overview, in.NFOName, actor.UserID).Scan(&id); err != nil {
		return domain.CollectionView{}, storageError(err)
	}
	if err = auditAccount(ctx, tx, actor, "collection.created", id, nil, collectionAudit(in)); err != nil {
		return domain.CollectionView{}, err
	}
	view, err := readCollection(ctx, tx, actor, id)
	if err != nil {
		return view, err
	}
	return view, storageError(tx.Commit(ctx))
}

// UpdateCollection replaces the name, overview and NFO name.
func (s *Store) UpdateCollection(ctx context.Context, actor domain.Actor, id string, in domain.CollectionInput) (domain.CollectionView, error) {
	if ctx == nil || !in.Valid() {
		return domain.CollectionView{}, domain.ErrInvalid
	}
	if !domain.ValidID(id) {
		return domain.CollectionView{}, domain.ErrNotFound
	}
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return domain.CollectionView{}, err
	}
	defer tx.Rollback(ctx)
	before, err := lockCollection(ctx, tx, id)
	if err != nil {
		return domain.CollectionView{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE collections SET name=$2,overview=$3,nfo_name=$4,updated_at=clock_timestamp() WHERE id=$1::uuid`, id, in.Name, in.Overview, in.NFOName); err != nil {
		return domain.CollectionView{}, storageError(err)
	}
	if err = auditAccount(ctx, tx, actor, "collection.updated", id, before, collectionAudit(in)); err != nil {
		return domain.CollectionView{}, err
	}
	view, err := readCollection(ctx, tx, actor, id)
	if err != nil {
		return view, err
	}
	return view, storageError(tx.Commit(ctx))
}

// DeleteCollection removes a collection; its items stay.
func (s *Store) DeleteCollection(ctx context.Context, actor domain.Actor, id string) error {
	if ctx == nil {
		return domain.ErrInvalid
	}
	if !domain.ValidID(id) {
		return domain.ErrNotFound
	}
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	before, err := lockCollection(ctx, tx, id)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM collections WHERE id=$1::uuid`, id); err != nil {
		return storageError(err)
	}
	if err = auditAccount(ctx, tx, actor, "collection.deleted", id, before, nil); err != nil {
		return err
	}
	return storageError(tx.Commit(ctx))
}

// checkMemberItems checks that every distinct item is visible to the actor
// (ErrNotFound otherwise, like a missing item) and of one of kinds
// (ErrInvalid otherwise).
func checkMemberItems(ctx context.Context, tx pgx.Tx, actor domain.Actor, ids, kinds []string) error {
	var visible, fitting int
	if err := tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER (WHERE i.kind=ANY($3::text[])) FROM items i JOIN users u ON u.id=$1::uuid
 WHERE i.id=ANY($2::uuid[]) AND `+itemVisibleSQL("$4", "i.library_id", "i.id"), actor.UserID, ids, kinds, requestScopeArg(ctx)).Scan(&visible, &fitting); err != nil {
		return storageError(err)
	}
	if visible != len(ids) {
		return domain.ErrNotFound
	}
	if fitting != visible {
		return domain.ErrInvalid
	}
	return nil
}

func distinctIDs(ids []string) []string {
	out := slices.Clone(ids)
	slices.Sort(out)
	return slices.Compact(out)
}

// AddCollectionItems adds visible items as manual members; items already
// members stay as they are.
func (s *Store) AddCollectionItems(ctx context.Context, actor domain.Actor, id string, itemIDs []string) (domain.CollectionView, error) {
	if ctx == nil || !domain.ValidMembershipBatch(itemIDs) {
		return domain.CollectionView{}, domain.ErrInvalid
	}
	if !domain.ValidID(id) {
		return domain.CollectionView{}, domain.ErrNotFound
	}
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return domain.CollectionView{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = lockCollection(ctx, tx, id); err != nil {
		return domain.CollectionView{}, err
	}
	ids := distinctIDs(itemIDs)
	if err = checkMemberItems(ctx, tx, actor, ids, domain.CollectionKinds); err != nil {
		return domain.CollectionView{}, err
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM collection_items WHERE collection_id=$1::uuid AND NOT item_id=ANY($2::uuid[])`, id, ids).Scan(&count); err != nil {
		return domain.CollectionView{}, storageError(err)
	}
	if count+len(ids) > domain.CollectionManualItemsMax {
		return domain.CollectionView{}, domain.ErrConflict
	}
	tag, err := tx.Exec(ctx, `INSERT INTO collection_items(collection_id,item_id) SELECT $1::uuid,x FROM unnest($2::uuid[]) x ON CONFLICT DO NOTHING`, id, ids)
	if err != nil {
		return domain.CollectionView{}, storageError(err)
	}
	if tag.RowsAffected() > 0 {
		if _, err = tx.Exec(ctx, `UPDATE collections SET updated_at=clock_timestamp() WHERE id=$1::uuid`, id); err != nil {
			return domain.CollectionView{}, storageError(err)
		}
		if err = auditAccount(ctx, tx, actor, "collection.items_added", id, nil, map[string]any{"itemIds": ids}); err != nil {
			return domain.CollectionView{}, err
		}
	}
	view, err := readCollection(ctx, tx, actor, id)
	if err != nil {
		return view, err
	}
	return view, storageError(tx.Commit(ctx))
}

// RemoveCollectionItem removes a manual member. A member only through the
// NFO name is ErrConflict: its metadata, not the collection, decides.
func (s *Store) RemoveCollectionItem(ctx context.Context, actor domain.Actor, id, itemID string) (domain.CollectionView, error) {
	if ctx == nil {
		return domain.CollectionView{}, domain.ErrInvalid
	}
	if !domain.ValidID(id) || !domain.ValidID(itemID) {
		return domain.CollectionView{}, domain.ErrNotFound
	}
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return domain.CollectionView{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = lockCollection(ctx, tx, id); err != nil {
		return domain.CollectionView{}, err
	}
	// Only a visible item can be named, like everywhere else.
	if err = checkMemberItems(ctx, tx, actor, []string{itemID}, []string{"Movie", "Series", "Season", "Episode", "HomeVideo"}); err != nil {
		return domain.CollectionView{}, err
	}
	tag, err := tx.Exec(ctx, `DELETE FROM collection_items WHERE collection_id=$1::uuid AND item_id=$2::uuid`, id, itemID)
	if err != nil {
		return domain.CollectionView{}, storageError(err)
	}
	if tag.RowsAffected() == 0 {
		var nfo bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM collections c WHERE c.id=$1::uuid AND $2::uuid IN (`+collectionMembersSQL("c")+`))`, id, itemID).Scan(&nfo); err != nil {
			return domain.CollectionView{}, storageError(err)
		}
		if nfo {
			return domain.CollectionView{}, domain.ErrConflict
		}
		return domain.CollectionView{}, domain.ErrNotFound
	}
	if _, err = tx.Exec(ctx, `UPDATE collections SET updated_at=clock_timestamp() WHERE id=$1::uuid`, id); err != nil {
		return domain.CollectionView{}, storageError(err)
	}
	if err = auditAccount(ctx, tx, actor, "collection.item_removed", id, map[string]any{"itemId": itemID}, nil); err != nil {
		return domain.CollectionView{}, err
	}
	view, err := readCollection(ctx, tx, actor, id)
	if err != nil {
		return view, err
	}
	return view, storageError(tx.Commit(ctx))
}

// SyncNFOCollections creates an NFO-linked collection for every collection
// name in the metadata that no collection uses yet, up to CollectionsMax.
// The name and overview come from the alphabetically first spelling.
func (s *Store) SyncNFOCollections(ctx context.Context, actor domain.Actor) (domain.CollectionNFOSync, error) {
	if ctx == nil {
		return domain.CollectionNFOSync{}, domain.ErrInvalid
	}
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return domain.CollectionNFOSync{}, err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `INSERT INTO collections(name,overview,nfo_name,created_by)
 SELECT n.name,n.overview,n.name,$1::uuid FROM (
  SELECT DISTINCT ON (lower(btrim(f.value->>'name'))) btrim(f.value->>'name') AS name,left(COALESCE(f.value->>'overview',''),4096) AS overview
  FROM item_metadata_facts f WHERE f.field='collection' AND jsonb_typeof(f.value)='object' AND btrim(COALESCE(f.value->>'name',''))<>''
   AND octet_length(btrim(f.value->>'name'))<=1024
   AND NOT EXISTS(SELECT 1 FROM collections c WHERE lower(btrim(c.nfo_name))=lower(btrim(f.value->>'name')))
  ORDER BY lower(btrim(f.value->>'name')),btrim(f.value->>'name'),f.item_id) n
 ORDER BY lower(n.name) LIMIT GREATEST(0,$2::int-(SELECT count(*) FROM collections)::int)
 ON CONFLICT DO NOTHING`, actor.UserID, domain.CollectionsMax)
	if err != nil {
		return domain.CollectionNFOSync{}, storageError(err)
	}
	result := domain.CollectionNFOSync{Created: int(tag.RowsAffected())}
	if result.Created > 0 {
		if err = appendAudit(ctx, tx, AuditEntry{Event: "collection.nfo_synced", Actor: actor, TargetRef: "collections", After: result}); err != nil {
			return domain.CollectionNFOSync{}, err
		}
	}
	return result, storageError(tx.Commit(ctx))
}

// memberTransaction opens a change transaction for a live actor without the
// account lock: the actor's user row lock serializes one user's changes.
func (s *Store) memberTransaction(ctx context.Context, actor domain.Actor) (pgx.Tx, error) {
	if ctx == nil {
		return nil, domain.ErrInvalid
	}
	if !domain.ValidID(actor.UserID) || !domain.ValidID(actor.SessionID) {
		return nil, domain.ErrUnauthenticated
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, storageError(err)
	}
	if _, err = tx.Exec(ctx, `SET LOCAL lock_timeout='1500ms'; SET LOCAL statement_timeout='5000ms'`); err != nil {
		_ = tx.Rollback(ctx)
		return nil, storageError(err)
	}
	if _, err = authorizeActorInTransaction(ctx, tx, actor, false); err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}
