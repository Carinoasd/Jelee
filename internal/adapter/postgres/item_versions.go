package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"path"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

var _ app.VersionRepository = (*Store)(nil)

// Manual version decisions (G20.3, G20.5); docs/item-versions.md.
//
// Every decision runs in one transaction under the jobs lock, the lock
// every catalog writer (synchronisation, imports, probes) holds, with a live
// administrator whose visibility filter must show every item involved. The
// operation row keeps what undo needs; undo of a split or merge is only
// possible newest first among the operations touching the same items.

// versionUndoMaxBytes bounds the undo document of one operation.
const versionUndoMaxBytes = 16 << 20

type versionItem struct {
	id, library, kind, title string
}

// versionTransaction is an administrator transaction with the jobs lock and
// a statement budget large enough for an item's history.
func (s *Store) versionTransaction(ctx context.Context, actor domain.Actor) (pgx.Tx, error) {
	if ctx == nil {
		return nil, domain.ErrInvalid
	}
	tx, err := s.authorizedJobs(ctx, actor)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `SET LOCAL statement_timeout='15s'`); err != nil {
		_ = tx.Rollback(ctx)
		return nil, storageError(err)
	}
	return tx, nil
}

// lockVersionItem locks an item the administrator may see. A missing and an
// invisible item are the same ErrNotFound.
func lockVersionItem(ctx context.Context, tx pgx.Tx, actor domain.Actor, id string) (versionItem, error) {
	var it versionItem
	err := tx.QueryRow(ctx, `SELECT i.id::text,i.library_id::text,i.kind,i.title FROM items i
 JOIN users u ON u.id=$2::uuid AND NOT u.disabled AND u.deleted_at IS NULL
 WHERE i.id=$1::uuid AND `+itemVisibleSQL("$3", "i.library_id", "i.id")+` FOR UPDATE OF i`, id, actor.UserID, requestScopeArg(ctx)).Scan(&it.id, &it.library, &it.kind, &it.title)
	return it, storageError(err)
}

// versionBusy refuses a change while a client plays one of the items, a
// probe job targets one, or an NFO write job is about to write one.
func versionBusy(ctx context.Context, tx pgx.Tx, items ...string) error {
	var busy bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM playback_sessions WHERE item_id=ANY($1::uuid[]) AND state='active')
 OR EXISTS(SELECT 1 FROM probe_requests WHERE target_item_id=ANY($1::uuid[]))
 OR EXISTS(SELECT 1 FROM probe_job_state WHERE target_item_id=ANY($1::uuid[]))
 OR EXISTS(SELECT 1 FROM nfo_write_entries e JOIN jobs j ON j.id=e.job_id WHERE e.item_id=ANY($1::uuid[]) AND j.state IN ('queued','running'))`, items).Scan(&busy)
	if err != nil {
		return storageError(err)
	}
	if busy {
		return domain.ErrVersionItemBusy
	}
	return nil
}

// laterStructuralOperation reports a newer split or merge, not undone, that
// involves any of the items.
func laterStructuralOperation(ctx context.Context, tx pgx.Tx, after time.Time, id string, items ...string) (bool, error) {
	var later bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM item_version_operations o WHERE o.kind IN ('split','merge') AND o.undone_at IS NULL AND o.id<>$3::uuid
 AND (o.created_at,o.id)>($2,$3::uuid) AND (o.item_id=ANY($1::uuid[]) OR o.other_item_id=ANY($1::uuid[])))`, items, after, id).Scan(&later)
	return later, storageError(err)
}

const versionOperationColumns = `o.id::text,o.library_id::text,o.kind,o.item_id::text,COALESCE(o.other_item_id::text,''),o.source_ids::text[],COALESCE(o.actor_id::text,''),o.created_at,o.undo_until,o.undone_at`

func scanVersionOperation(row pgx.Row) (domain.VersionOperation, error) {
	var op domain.VersionOperation
	err := row.Scan(&op.ID, &op.LibraryID, &op.Kind, &op.ItemID, &op.OtherItemID, &op.SourceIDs, &op.ActorID, &op.CreatedAt, &op.UndoUntil, &op.UndoneAt)
	if op.SourceIDs == nil {
		op.SourceIDs = []string{}
	}
	return op, storageError(err)
}

// describeVersionOperation fills the computed undo state.
func describeVersionOperation(ctx context.Context, tx pgx.Tx, op domain.VersionOperation) (domain.VersionOperation, error) {
	items := []string{op.ItemID}
	if op.OtherItemID != "" {
		items = append(items, op.OtherItemID)
	}
	later, err := laterStructuralOperation(ctx, tx, op.CreatedAt, op.ID, items...)
	if err != nil {
		return op, err
	}
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return op, storageError(err)
	}
	op.Undoable, op.UndoBlocked = domain.VersionUndoState(op, now, later)
	return op, nil
}

// recordVersionOperation stores an operation with its undo document and
// writes its audit row.
func recordVersionOperation(ctx context.Context, tx pgx.Tx, actor domain.Actor, kind string, item versionItem, other string, sources []string, undo any, event string, details map[string]any) (domain.VersionOperation, error) {
	doc, err := json.Marshal(undo)
	if err != nil {
		return domain.VersionOperation{}, domain.ErrDatabase
	}
	if len(doc) > versionUndoMaxBytes {
		// The decision could not be undone; refuse it rather than keep a
		// partial record.
		return domain.VersionOperation{}, domain.ErrVersionMergeIncompatible
	}
	if sources == nil {
		sources = []string{}
	}
	var otherID *string
	if other != "" {
		otherID = &other
	}
	op, err := scanVersionOperation(tx.QueryRow(ctx, `INSERT INTO item_version_operations AS o(library_id,kind,actor_id,item_id,other_item_id,source_ids,undo,undo_until)
 VALUES($1::uuid,$2,$3::uuid,$4::uuid,$5::uuid,$6::uuid[],$7::jsonb,clock_timestamp()+make_interval(secs=>$8)) RETURNING `+versionOperationColumns,
		item.library, kind, actor.UserID, item.id, otherID, sources, doc, domain.VersionUndoWindow.Seconds()))
	if err != nil {
		return op, err
	}
	details["operationId"] = op.ID
	details["libraryId"] = item.library
	if err = auditAccount(ctx, tx, actor, event, item.id, nil, details); err != nil {
		return op, err
	}
	op.Undoable = true
	return op, nil
}

// moveSources assigns sources to another item with everything that follows
// a version: the scan registration (marked as manually placed) and the
// version-level track preferences. It returns the previous manual marks.
func moveSources(ctx context.Context, tx pgx.Tx, sources []string, from, to string) (map[string]bool, error) {
	manual := map[string]bool{}
	if len(sources) == 0 {
		return manual, nil
	}
	tag, err := tx.Exec(ctx, `UPDATE media_sources SET item_id=$3::uuid WHERE id=ANY($1::uuid[]) AND item_id=$2::uuid`, sources, from, to)
	if err != nil {
		return nil, storageError(err)
	}
	if tag.RowsAffected() != int64(len(sources)) {
		return nil, domain.ErrConflict
	}
	rows, err := tx.Query(ctx, `UPDATE catalog_scan_sources c SET item_id=$2::uuid,manual=true FROM catalog_scan_sources old
 WHERE c.source_id=old.source_id AND c.source_id=ANY($1::uuid[]) RETURNING c.source_id::text,old.manual`, sources, to)
	if err != nil {
		return nil, storageError(err)
	}
	for rows.Next() {
		var id string
		var was bool
		if err = rows.Scan(&id, &was); err != nil {
			rows.Close()
			return nil, storageError(err)
		}
		manual[id] = was
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, storageError(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE user_track_preferences SET item_id=$2::uuid WHERE source_id=ANY($1::uuid[])`, sources, to); err != nil {
		return nil, storageError(err)
	}
	// A probe result keeps describing the same file; only its link to the
	// item is dropped, so the next probe of the new item re-checks it.
	if _, err = tx.Exec(ctx, `UPDATE probe_cache p SET item_id=NULL,item_generation=NULL FROM media_sources m
 WHERE m.id=ANY($1::uuid[]) AND p.root_id=m.root_id AND p.relative_path=m.relative_path AND p.item_id IS NOT NULL`, sources); err != nil {
		return nil, storageError(err)
	}
	return manual, nil
}

// restoreSources moves sources back after an undo. Sources that no longer
// exist or meanwhile belong to a third item stay where they are.
func restoreSources(ctx context.Context, tx pgx.Tx, sources []string, from, to string, manual map[string]bool) ([]string, error) {
	rows, err := tx.Query(ctx, `UPDATE media_sources SET item_id=$3::uuid WHERE id=ANY($1::uuid[]) AND item_id=$2::uuid RETURNING id::text`, sources, from, to)
	if err != nil {
		return nil, storageError(err)
	}
	moved, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, storageError(err)
	}
	if len(moved) == 0 {
		return moved, nil
	}
	keep := make([]string, 0, len(moved))
	for _, id := range moved {
		if manual[id] {
			keep = append(keep, id)
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE catalog_scan_sources SET item_id=$2::uuid,manual=(source_id=ANY($3::uuid[])) WHERE source_id=ANY($1::uuid[])`, moved, to, keep); err != nil {
		return nil, storageError(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE user_track_preferences SET item_id=$2::uuid WHERE source_id=ANY($1::uuid[])`, moved, to); err != nil {
		return nil, storageError(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE probe_cache p SET item_id=NULL,item_generation=NULL FROM media_sources m
 WHERE m.id=ANY($1::uuid[]) AND p.root_id=m.root_id AND p.relative_path=m.relative_path AND p.item_id IS NOT NULL`, moved); err != nil {
		return nil, storageError(err)
	}
	// A main version that left the item is no longer its main version.
	if _, err = tx.Exec(ctx, `DELETE FROM item_primary_versions WHERE item_id=$1::uuid AND source_id=ANY($2::uuid[])`, from, moved); err != nil {
		return nil, storageError(err)
	}
	return moved, nil
}

// itemWebhook raises media.added or media.deleted for an item a decision
// created, removed or restored, in the decision's transaction (G12.1).
func itemWebhook(ctx context.Context, tx pgx.Tx, events bool, kind domain.WebhookEventType, item versionItem) error {
	data := map[string]any{"libraryId": item.library}
	if kind == domain.WebhookMediaAdded {
		data["kind"], data["title"] = item.kind, item.title
	}
	return appendWebhook(ctx, tx, events, kind, time.Now().UTC(), domain.WebhookSubject{Kind: domain.WebhookSubjectItem, ID: item.id}, data)
}

// --- split ----------------------------------------------------------------

type splitUndo struct {
	Manual          map[string]bool `json:"manual"`
	PrimaryRemoved  bool            `json:"primaryRemoved"`
	ExclusionID     string          `json:"exclusionId,omitempty"`
	ScanItemCreated bool            `json:"scanItemCreated"`
}

// SplitVersion moves one version of an item into a new item of the same
// kind, title and parent. With Exclude, synchronisation never groups the
// file into the original item again.
func (s *Store) SplitVersion(ctx context.Context, actor domain.Actor, itemID string, in domain.SplitVersionInput) (domain.VersionOperation, error) {
	if !in.Valid() {
		return domain.VersionOperation{}, domain.ErrInvalid
	}
	tx, err := s.versionTransaction(ctx, actor)
	if err != nil {
		return domain.VersionOperation{}, err
	}
	defer tx.Rollback(ctx)
	item, err := lockVersionItem(ctx, tx, actor, itemID)
	if err != nil {
		return domain.VersionOperation{}, err
	}
	var root, relative string
	var count int
	err = tx.QueryRow(ctx, `SELECT m.root_id::text,m.relative_path,(SELECT count(*) FROM media_sources WHERE item_id=$2::uuid) FROM media_sources m WHERE m.id=$1::uuid AND m.item_id=$2::uuid FOR UPDATE`, in.SourceID, item.id).Scan(&root, &relative, &count)
	if err != nil {
		return domain.VersionOperation{}, storageError(err)
	}
	if !domain.VersionMergeKind(item.kind) || count < 2 {
		return domain.VersionOperation{}, domain.ErrVersionMergeIncompatible
	}
	if err = versionBusy(ctx, tx, item.id); err != nil {
		return domain.VersionOperation{}, err
	}
	title := item.title
	if in.Title != "" {
		title = in.Title
	}
	var created string
	if err = tx.QueryRow(ctx, `INSERT INTO items(library_id,title,kind) VALUES($1::uuid,$2,$3) RETURNING id::text`, item.library, title, item.kind).Scan(&created); err != nil {
		return domain.VersionOperation{}, storageError(err)
	}
	now := time.Now().UTC()
	if err = itemWebhook(ctx, tx, s.webhooksOn(), domain.WebhookMediaAdded, versionItem{id: created, library: item.library, kind: item.kind, title: title}); err != nil {
		return domain.VersionOperation{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO item_metadata_state(item_id,revision) VALUES($1::uuid,2)`, created); err != nil {
		return domain.VersionOperation{}, storageError(err)
	}
	// The administrator named this item; synchronisation never renames it.
	if err = writeItemMetadataField(ctx, tx, created, domain.ItemMetadataField{Field: "title", Value: title, Source: "manual"}, now); err != nil {
		return domain.VersionOperation{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO item_parent_links(item_id,library_id,item_kind,parent_id,parent_kind) SELECT $1::uuid,library_id,item_kind,parent_id,parent_kind FROM item_parent_links WHERE item_id=$2::uuid`, created, item.id); err != nil {
		return domain.VersionOperation{}, storageError(err)
	}
	undo := splitUndo{}
	// A scan-registered file gets the per-file scan group that
	// synchronisation uses for a file kept apart, so the file finds this
	// item again if it disappears and comes back.
	var tracked bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM catalog_scan_sources WHERE source_id=$1::uuid)`, in.SourceID).Scan(&tracked); err != nil {
		return domain.VersionOperation{}, storageError(err)
	}
	if tracked && (item.kind == "Movie" || item.kind == "Episode") {
		digest := domain.CatalogGroupDigest(item.kind+"-file", root, relative)
		tag, err := tx.Exec(ctx, `INSERT INTO catalog_scan_items(item_id,library_id,kind,group_digest,parser_version,scan_title,year,season,episode,episode_end)
 SELECT $1::uuid,$2::uuid,$3,$4,$5,$6,c.year,c.season,c.episode,c.episode_end FROM (SELECT 1) one LEFT JOIN catalog_scan_items c ON c.item_id=$7::uuid
 WHERE NOT EXISTS(SELECT 1 FROM catalog_scan_items WHERE library_id=$2::uuid AND kind=$3 AND group_digest=$4)
 AND NOT EXISTS(SELECT 1 FROM catalog_scan_item_aliases WHERE library_id=$2::uuid AND kind=$3 AND group_digest=$4)`,
			created, item.library, item.kind, digest, domain.CatalogSyncParserVersion, truncateScanTitle(title), item.id)
		if err != nil {
			return domain.VersionOperation{}, storageError(err)
		}
		undo.ScanItemCreated = tag.RowsAffected() == 1
	}
	if undo.Manual, err = moveSources(ctx, tx, []string{in.SourceID}, item.id, created); err != nil {
		return domain.VersionOperation{}, err
	}
	tag, err := tx.Exec(ctx, `DELETE FROM item_primary_versions WHERE item_id=$1::uuid AND source_id=$2::uuid`, item.id, in.SourceID)
	if err != nil {
		return domain.VersionOperation{}, storageError(err)
	}
	undo.PrimaryRemoved = tag.RowsAffected() == 1
	if in.Exclude {
		if err = tx.QueryRow(ctx, `INSERT INTO item_version_exclusions(item_id,library_id,root_id,relative_path) VALUES($1::uuid,$2::uuid,$3::uuid,$4)
 ON CONFLICT(item_id,root_id,relative_path) DO UPDATE SET created_at=item_version_exclusions.created_at RETURNING id::text`, item.id, item.library, root, relative).Scan(&undo.ExclusionID); err != nil {
			return domain.VersionOperation{}, storageError(err)
		}
	}
	op, err := recordVersionOperation(ctx, tx, actor, domain.VersionOpSplit, item, created, []string{in.SourceID}, undo, "item.version_split",
		map[string]any{"sourceId": in.SourceID, "newItemId": created, "exclude": in.Exclude, "kind": item.kind})
	if err != nil {
		return domain.VersionOperation{}, err
	}
	if undo.ExclusionID != "" {
		if _, err = tx.Exec(ctx, `UPDATE item_version_exclusions SET operation_id=$2::uuid WHERE id=$1::uuid`, undo.ExclusionID, op.ID); err != nil {
			return domain.VersionOperation{}, storageError(err)
		}
	}
	return op, storageError(tx.Commit(ctx))
}

func truncateScanTitle(title string) string {
	if len(title) <= 1024 {
		return title
	}
	end := 1024
	for end > 0 && title[end]&0xC0 == 0x80 {
		end--
	}
	return title[:end]
}

// --- merge ----------------------------------------------------------------

type mergeUndo struct {
	// Absorbed holds the rows of the removed item, per table.
	Absorbed map[string]json.RawMessage `json:"absorbed"`
	Manual   map[string]bool            `json:"manual"`
	Transfer versionTransfer            `json:"transfer"`
	// Aliases are the scan groups that pointed at the absorbed item and
	// now point at the target; AliasCreated is its own group.
	Aliases      []string `json:"aliases"`
	AliasCreated bool     `json:"aliasCreated"`
}

// MergeItems moves every version of sourceItemID into targetID, transfers
// playback history, user data, access rules and track preferences by the
// merge policy (docs/item-versions.md) and removes the source item. Items
// of another kind, library or series, container items, items with children
// and items whose external IDs or episode numbers disagree are refused;
// there is no override.
func (s *Store) MergeItems(ctx context.Context, actor domain.Actor, targetID, sourceItemID string) (domain.VersionOperation, error) {
	if !domain.ValidID(targetID) || !domain.ValidID(sourceItemID) || targetID == sourceItemID {
		return domain.VersionOperation{}, domain.ErrInvalid
	}
	tx, err := s.versionTransaction(ctx, actor)
	if err != nil {
		return domain.VersionOperation{}, err
	}
	defer tx.Rollback(ctx)
	// Lock in a fixed order; both must be visible to the administrator.
	first, second := targetID, sourceItemID
	if second < first {
		first, second = second, first
	}
	locked := map[string]versionItem{}
	for _, id := range []string{first, second} {
		it, err := lockVersionItem(ctx, tx, actor, id)
		if err != nil {
			return domain.VersionOperation{}, err
		}
		locked[id] = it
	}
	target, source := locked[targetID], locked[sourceItemID]
	if err = checkMergeable(ctx, tx, target, source); err != nil {
		return domain.VersionOperation{}, err
	}
	if err = versionBusy(ctx, tx, target.id, source.id); err != nil {
		return domain.VersionOperation{}, err
	}
	undo := mergeUndo{Aliases: []string{}}
	if undo.Absorbed, err = snapshotVersionItem(ctx, tx, source.id); err != nil {
		return domain.VersionOperation{}, err
	}
	rows, err := tx.Query(ctx, `SELECT id::text FROM media_sources WHERE item_id=$1::uuid ORDER BY id FOR UPDATE`, source.id)
	if err != nil {
		return domain.VersionOperation{}, storageError(err)
	}
	sources, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return domain.VersionOperation{}, storageError(err)
	}
	if undo.Manual, err = moveSources(ctx, tx, sources, source.id, target.id); err != nil {
		return domain.VersionOperation{}, err
	}
	if undo.Transfer, err = transferItemData(ctx, tx, source.id, target.id); err != nil {
		return domain.VersionOperation{}, err
	}
	// Scan groups: aliases of the absorbed item follow it, and its own group
	// now names the target.
	rows, err = tx.Query(ctx, `UPDATE catalog_scan_item_aliases SET item_id=$2::uuid WHERE item_id=$1::uuid RETURNING encode(group_digest,'hex')`, source.id, target.id)
	if err != nil {
		return domain.VersionOperation{}, storageError(err)
	}
	if undo.Aliases, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		return domain.VersionOperation{}, storageError(err)
	}
	tag, err := tx.Exec(ctx, `INSERT INTO catalog_scan_item_aliases(library_id,kind,group_digest,item_id) SELECT library_id,kind,group_digest,$2::uuid FROM catalog_scan_items WHERE item_id=$1::uuid`, source.id, target.id)
	if err != nil {
		return domain.VersionOperation{}, storageError(err)
	}
	undo.AliasCreated = tag.RowsAffected() == 1
	if _, err = tx.Exec(ctx, `UPDATE probe_cache SET item_id=NULL,item_generation=NULL WHERE item_id=$1::uuid`, source.id); err != nil {
		return domain.VersionOperation{}, storageError(err)
	}
	if _, err = tx.Exec(ctx, `DELETE FROM items WHERE id=$1::uuid AND library_id=$2::uuid`, source.id, source.library); err != nil {
		return domain.VersionOperation{}, storageError(err)
	}
	if err = itemWebhook(ctx, tx, s.webhooksOn(), domain.WebhookMediaDeleted, source); err != nil {
		return domain.VersionOperation{}, err
	}
	op, err := recordVersionOperation(ctx, tx, actor, domain.VersionOpMerge, target, source.id, sources, undo, "item.versions_merged",
		map[string]any{"absorbedItemId": source.id, "absorbedTitle": source.title, "sources": len(sources), "kind": target.kind,
			"userData": len(undo.Transfer.UserDataUsers), "sessions": len(undo.Transfer.Sessions), "rules": len(undo.Transfer.Rules)})
	if err != nil {
		return domain.VersionOperation{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE catalog_scan_item_aliases SET operation_id=$2::uuid WHERE item_id=$1::uuid AND operation_id IS NULL`, target.id, op.ID); err != nil {
		return domain.VersionOperation{}, storageError(err)
	}
	return op, storageError(tx.Commit(ctx))
}

// checkMergeable enforces the G20.5 boundaries.
func checkMergeable(ctx context.Context, tx pgx.Tx, target, source versionItem) error {
	if target.library != source.library || target.kind != source.kind || !domain.VersionMergeKind(target.kind) {
		return domain.ErrVersionMergeIncompatible
	}
	var children bool
	var targetParent, sourceParent, targetSeries, sourceSeries *string
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM item_parent_links WHERE parent_id=$2::uuid),
 (SELECT parent_id::text FROM item_parent_links WHERE item_id=$1::uuid),(SELECT parent_id::text FROM item_parent_links WHERE item_id=$2::uuid),
 (SELECT COALESCE(p2.parent_id,p1.parent_id)::text FROM item_parent_links p1 LEFT JOIN item_parent_links p2 ON p2.item_id=p1.parent_id WHERE p1.item_id=$1::uuid),
 (SELECT COALESCE(p2.parent_id,p1.parent_id)::text FROM item_parent_links p1 LEFT JOIN item_parent_links p2 ON p2.item_id=p1.parent_id WHERE p1.item_id=$2::uuid)`,
		target.id, source.id).Scan(&children, &targetParent, &sourceParent, &targetSeries, &sourceSeries)
	if err != nil {
		return storageError(err)
	}
	if children || (targetSeries == nil) != (sourceSeries == nil) || targetSeries != nil && *targetSeries != *sourceSeries {
		// Episodes of different series never become one item.
		return domain.ErrVersionMergeIncompatible
	}
	var targetIDs, sourceIDs []byte
	var numbersDiffer bool
	err = tx.QueryRow(ctx, `SELECT (SELECT value::text FROM item_metadata_facts WHERE item_id=$1::uuid AND field='uniqueIds'),
 (SELECT value::text FROM item_metadata_facts WHERE item_id=$2::uuid AND field='uniqueIds'),
 EXISTS(SELECT 1 FROM item_metadata_facts a JOIN item_metadata_facts b ON b.field=a.field AND b.item_id=$2::uuid
  WHERE a.item_id=$1::uuid AND a.field IN ('seasonNumber','episodeNumber') AND a.value<>b.value)
 OR EXISTS(SELECT 1 FROM catalog_scan_items a JOIN catalog_scan_items b ON b.item_id=$2::uuid WHERE a.item_id=$1::uuid AND a.kind='Episode'
  AND (a.season IS DISTINCT FROM b.season AND a.season IS NOT NULL AND b.season IS NOT NULL
   OR a.episode IS DISTINCT FROM b.episode AND a.episode IS NOT NULL AND b.episode IS NOT NULL))`, target.id, source.id).Scan(&targetIDs, &sourceIDs, &numbersDiffer)
	if err != nil {
		return storageError(err)
	}
	if numbersDiffer || len(domain.ExternalIDConflicts(targetIDs, sourceIDs)) > 0 {
		return domain.ErrVersionIdentityConflict
	}
	return nil
}

// --- main version -----------------------------------------------------------

type primaryUndo struct {
	Previous string `json:"previous,omitempty"`
}

// SetPrimaryVersion records the main version of an item; an empty source
// clears it.
func (s *Store) SetPrimaryVersion(ctx context.Context, actor domain.Actor, itemID, sourceID string) (domain.VersionOperation, error) {
	tx, err := s.versionTransaction(ctx, actor)
	if err != nil {
		return domain.VersionOperation{}, err
	}
	defer tx.Rollback(ctx)
	item, err := lockVersionItem(ctx, tx, actor, itemID)
	if err != nil {
		return domain.VersionOperation{}, err
	}
	var previous *string
	if err = tx.QueryRow(ctx, `SELECT (SELECT source_id::text FROM item_primary_versions WHERE item_id=$1::uuid FOR UPDATE)`, item.id).Scan(&previous); err != nil {
		return domain.VersionOperation{}, storageError(err)
	}
	undo := primaryUndo{}
	if previous != nil {
		undo.Previous = *previous
	}
	if undo.Previous == sourceID {
		return domain.VersionOperation{}, domain.ErrConflict
	}
	if err = applyPrimaryVersion(ctx, tx, item, sourceID); err != nil {
		return domain.VersionOperation{}, err
	}
	sources := []string{}
	if sourceID != "" {
		sources = append(sources, sourceID)
	}
	op, err := recordVersionOperation(ctx, tx, actor, domain.VersionOpPrimary, item, "", sources, undo, "item.primary_version_changed",
		map[string]any{"previousSourceId": undo.Previous, "sourceId": sourceID})
	if err != nil {
		return domain.VersionOperation{}, err
	}
	return op, storageError(tx.Commit(ctx))
}

func applyPrimaryVersion(ctx context.Context, tx pgx.Tx, item versionItem, sourceID string) error {
	if sourceID == "" {
		_, err := tx.Exec(ctx, `DELETE FROM item_primary_versions WHERE item_id=$1::uuid`, item.id)
		return storageError(err)
	}
	tag, err := tx.Exec(ctx, `INSERT INTO item_primary_versions(item_id,library_id,source_id) SELECT $1::uuid,$2::uuid,id FROM media_sources WHERE id=$3::uuid AND item_id=$1::uuid
 ON CONFLICT(item_id) DO UPDATE SET source_id=EXCLUDED.source_id,updated_at=clock_timestamp()`, item.id, item.library, sourceID)
	if err != nil {
		return storageError(err)
	}
	if tag.RowsAffected() != 1 {
		// A source of another item is answered like a missing one.
		return domain.ErrNotFound
	}
	return nil
}

// --- exclusions -------------------------------------------------------------

type unexcludeUndo struct {
	RootID       string `json:"rootId"`
	RelativePath string `json:"relativePath"`
}

// RemoveVersionExclusion lets synchronisation group a file into the item
// again.
func (s *Store) RemoveVersionExclusion(ctx context.Context, actor domain.Actor, itemID, exclusionID string) (domain.VersionOperation, error) {
	tx, err := s.versionTransaction(ctx, actor)
	if err != nil {
		return domain.VersionOperation{}, err
	}
	defer tx.Rollback(ctx)
	item, err := lockVersionItem(ctx, tx, actor, itemID)
	if err != nil {
		return domain.VersionOperation{}, err
	}
	var undo unexcludeUndo
	if err = tx.QueryRow(ctx, `DELETE FROM item_version_exclusions WHERE id=$1::uuid AND item_id=$2::uuid RETURNING root_id::text,relative_path`, exclusionID, item.id).Scan(&undo.RootID, &undo.RelativePath); err != nil {
		return domain.VersionOperation{}, storageError(err)
	}
	op, err := recordVersionOperation(ctx, tx, actor, domain.VersionOpUnexclude, item, "", nil, undo, "item.version_exclusion_removed",
		map[string]any{"exclusionId": exclusionID, "fileName": path.Base(undo.RelativePath)})
	if err != nil {
		return domain.VersionOperation{}, err
	}
	return op, storageError(tx.Commit(ctx))
}

// --- undo -------------------------------------------------------------------

// UndoVersionOperation reverses one operation. The administrator must see
// the item the operation kept; a split or merge waits for every later split
// or merge of the same items to be undone first.
func (s *Store) UndoVersionOperation(ctx context.Context, actor domain.Actor, operationID string) (domain.VersionOperation, error) {
	tx, err := s.versionTransaction(ctx, actor)
	if err != nil {
		return domain.VersionOperation{}, err
	}
	defer tx.Rollback(ctx)
	var raw []byte
	var op domain.VersionOperation
	err = tx.QueryRow(ctx, `SELECT `+versionOperationColumns+`,o.undo::text FROM item_version_operations o WHERE o.id=$1::uuid FOR UPDATE`, operationID).Scan(
		&op.ID, &op.LibraryID, &op.Kind, &op.ItemID, &op.OtherItemID, &op.SourceIDs, &op.ActorID, &op.CreatedAt, &op.UndoUntil, &op.UndoneAt, &raw)
	if err != nil {
		return domain.VersionOperation{}, storageError(err)
	}
	item, err := lockVersionItem(ctx, tx, actor, op.ItemID)
	if err != nil {
		return domain.VersionOperation{}, err
	}
	if op, err = describeVersionOperation(ctx, tx, op); err != nil {
		return domain.VersionOperation{}, err
	}
	if !op.Undoable {
		return op, domain.ErrVersionUndoUnavailable
	}
	details := map[string]any{"operationId": op.ID, "kind": op.Kind}
	switch op.Kind {
	case domain.VersionOpSplit:
		err = undoSplit(ctx, tx, actor, item, op, raw, details, s.webhooksOn())
	case domain.VersionOpMerge:
		err = undoMerge(ctx, tx, item, op, raw, details, s.webhooksOn())
	case domain.VersionOpPrimary:
		var undo primaryUndo
		if err = json.Unmarshal(raw, &undo); err != nil {
			return domain.VersionOperation{}, domain.ErrDatabase
		}
		// A previous main version that left the item meanwhile is not
		// restored; the item then has none.
		err = applyPrimaryVersion(ctx, tx, item, undo.Previous)
		if errors.Is(err, domain.ErrNotFound) {
			err = applyPrimaryVersion(ctx, tx, item, "")
		}
	case domain.VersionOpUnexclude:
		var undo unexcludeUndo
		if err = json.Unmarshal(raw, &undo); err != nil {
			return domain.VersionOperation{}, domain.ErrDatabase
		}
		_, err = tx.Exec(ctx, `INSERT INTO item_version_exclusions(item_id,library_id,root_id,relative_path,operation_id)
 SELECT $1::uuid,$2::uuid,r.id,$4,$5::uuid FROM library_roots r WHERE r.id=$3::uuid AND r.library_id=$2::uuid ON CONFLICT DO NOTHING`, item.id, item.library, undo.RootID, undo.RelativePath, op.ID)
		err = storageError(err)
	default:
		return domain.VersionOperation{}, domain.ErrDatabase
	}
	if err != nil {
		return domain.VersionOperation{}, err
	}
	if op, err = scanVersionOperation(tx.QueryRow(ctx, `UPDATE item_version_operations o SET undone_at=clock_timestamp(),undone_by=$2::uuid WHERE o.id=$1::uuid RETURNING `+versionOperationColumns, op.ID, actor.UserID)); err != nil {
		return domain.VersionOperation{}, err
	}
	op.Undoable, op.UndoBlocked = false, domain.VersionUndoBlockedUndone
	if err = auditAccount(ctx, tx, actor, "item.version_operation_undone", item.id, nil, details); err != nil {
		return domain.VersionOperation{}, err
	}
	return op, storageError(tx.Commit(ctx))
}

// undoSplit moves the version back, folds whatever the new item gathered
// since (playback history, user data, rules) back into the original by the
// merge policy, and removes the new item and the exclusion.
func undoSplit(ctx context.Context, tx pgx.Tx, actor domain.Actor, item versionItem, op domain.VersionOperation, raw []byte, details map[string]any, events bool) error {
	var undo splitUndo
	if err := json.Unmarshal(raw, &undo); err != nil {
		return domain.ErrDatabase
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM items WHERE id=$1::uuid)`, op.OtherItemID).Scan(&exists); err != nil {
		return storageError(err)
	}
	// The new item may be gone (an administrator removed it with its file):
	// only the exclusion is then left to lift.
	if exists {
		created, err := lockVersionItem(ctx, tx, actor, op.OtherItemID)
		if err != nil {
			return err
		}
		if err = versionBusy(ctx, tx, item.id, created.id); err != nil {
			return err
		}
		var others bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM media_sources WHERE item_id=$1::uuid AND NOT id=ANY($2::uuid[])) OR EXISTS(SELECT 1 FROM item_parent_links WHERE parent_id=$1::uuid)`, created.id, op.SourceIDs).Scan(&others); err != nil {
			return storageError(err)
		}
		if others {
			// Versions placed there by someone else would be lost.
			return domain.ErrVersionUndoUnavailable
		}
		moved, err := restoreSources(ctx, tx, op.SourceIDs, created.id, item.id, undo.Manual)
		if err != nil {
			return err
		}
		if _, err = transferItemData(ctx, tx, created.id, item.id); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE catalog_scan_item_aliases SET item_id=$2::uuid WHERE item_id=$1::uuid`, created.id, item.id); err != nil {
			return storageError(err)
		}
		if _, err = tx.Exec(ctx, `UPDATE probe_cache SET item_id=NULL,item_generation=NULL WHERE item_id=$1::uuid`, created.id); err != nil {
			return storageError(err)
		}
		if _, err = tx.Exec(ctx, `DELETE FROM items WHERE id=$1::uuid AND library_id=$2::uuid`, created.id, created.library); err != nil {
			return storageError(err)
		}
		if err = itemWebhook(ctx, tx, events, domain.WebhookMediaDeleted, created); err != nil {
			return err
		}
		if undo.PrimaryRemoved && len(moved) == 1 {
			if _, err = tx.Exec(ctx, `INSERT INTO item_primary_versions(item_id,library_id,source_id) VALUES($1::uuid,$2::uuid,$3::uuid) ON CONFLICT DO NOTHING`, item.id, item.library, moved[0]); err != nil {
				return storageError(err)
			}
		}
		details["restoredSources"] = len(moved)
		details["removedItemId"] = created.id
	}
	if undo.ExclusionID != "" {
		if _, err := tx.Exec(ctx, `DELETE FROM item_version_exclusions WHERE id=$1::uuid`, undo.ExclusionID); err != nil {
			return storageError(err)
		}
	}
	return nil
}

// undoMerge recreates the absorbed item with its rows, moves its versions
// and playback history back and reverses the transfer where the target's
// rows were not changed since.
func undoMerge(ctx context.Context, tx pgx.Tx, item versionItem, op domain.VersionOperation, raw []byte, details map[string]any, events bool) error {
	var undo mergeUndo
	if err := json.Unmarshal(raw, &undo); err != nil {
		return domain.ErrDatabase
	}
	if err := versionBusy(ctx, tx, item.id); err != nil {
		return err
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM items WHERE id=$1::uuid)`, op.OtherItemID).Scan(&exists); err != nil {
		return storageError(err)
	}
	if exists {
		return domain.ErrVersionUndoUnavailable
	}
	if err := restoreVersionItemRows(ctx, tx, undo.Absorbed, versionRowsItem); err != nil {
		return err
	}
	moved, err := restoreSources(ctx, tx, op.SourceIDs, item.id, op.OtherItemID, undo.Manual)
	if err != nil {
		return err
	}
	if err = restoreVersionItemRows(ctx, tx, undo.Absorbed, versionRowsRest); err != nil {
		return err
	}
	if err = undoTransfer(ctx, tx, item.id, op.OtherItemID, undo.Transfer, undo.Absorbed); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM catalog_scan_item_aliases WHERE item_id=$1::uuid AND operation_id=$2::uuid AND group_digest IN (SELECT group_digest FROM catalog_scan_items WHERE item_id=$3::uuid)`, item.id, op.ID, op.OtherItemID); err != nil {
		return storageError(err)
	}
	if len(undo.Aliases) > 0 {
		if _, err = tx.Exec(ctx, `UPDATE catalog_scan_item_aliases SET item_id=$2::uuid WHERE item_id=$1::uuid AND encode(group_digest,'hex')=ANY($3::text[])`, item.id, op.OtherItemID, undo.Aliases); err != nil {
			return storageError(err)
		}
	}
	details["restoredItemId"] = op.OtherItemID
	details["restoredSources"] = len(moved)
	restored := versionItem{id: op.OtherItemID, library: item.library}
	if err = tx.QueryRow(ctx, `SELECT kind,title FROM items WHERE id=$1::uuid`, restored.id).Scan(&restored.kind, &restored.title); err != nil {
		return storageError(err)
	}
	return itemWebhook(ctx, tx, events, domain.WebhookMediaAdded, restored)
}

// --- overview ---------------------------------------------------------------

// VersionOverview lists the main version, exclusions and the newest
// operations of an item the administrator may see.
func (s *Store) VersionOverview(ctx context.Context, actor domain.Actor, itemID string) (domain.VersionOverview, error) {
	if ctx == nil {
		return domain.VersionOverview{}, domain.ErrInvalid
	}
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return domain.VersionOverview{}, err
	}
	defer tx.Rollback(ctx)
	var item string
	err = tx.QueryRow(ctx, `SELECT i.id::text FROM items i JOIN users u ON u.id=$2::uuid AND NOT u.disabled AND u.deleted_at IS NULL WHERE i.id=$1::uuid AND `+itemVisibleSQL("$3", "i.library_id", "i.id"), itemID, actor.UserID, requestScopeArg(ctx)).Scan(&item)
	if err != nil {
		return domain.VersionOverview{}, storageError(err)
	}
	out := domain.VersionOverview{ItemID: item, Exclusions: []domain.VersionExclusion{}, Operations: []domain.VersionOperation{}}
	var primary *string
	if err = tx.QueryRow(ctx, `SELECT (SELECT pv.source_id::text FROM item_primary_versions pv JOIN media_sources m ON m.id=pv.source_id AND m.item_id=pv.item_id WHERE pv.item_id=$1::uuid)`, item).Scan(&primary); err != nil {
		return domain.VersionOverview{}, storageError(err)
	}
	if primary != nil {
		out.PrimarySourceID = *primary
	}
	rows, err := tx.Query(ctx, `SELECT id::text,item_id::text,relative_path,COALESCE(operation_id::text,''),created_at FROM item_version_exclusions WHERE item_id=$1::uuid ORDER BY created_at DESC,id LIMIT $2`, item, domain.VersionOperationsPage)
	if err != nil {
		return domain.VersionOverview{}, storageError(err)
	}
	for rows.Next() {
		var e domain.VersionExclusion
		var relative string
		if err = rows.Scan(&e.ID, &e.ItemID, &relative, &e.OperationID, &e.CreatedAt); err != nil {
			rows.Close()
			return domain.VersionOverview{}, storageError(err)
		}
		e.FileName = path.Base(relative)
		out.Exclusions = append(out.Exclusions, e)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return domain.VersionOverview{}, storageError(err)
	}
	rows, err = tx.Query(ctx, `SELECT `+versionOperationColumns+` FROM item_version_operations o WHERE o.item_id=$1::uuid OR o.other_item_id=$1::uuid ORDER BY o.created_at DESC,o.id DESC LIMIT $2`, item, domain.VersionOperationsPage)
	if err != nil {
		return domain.VersionOverview{}, storageError(err)
	}
	var ops []domain.VersionOperation
	for rows.Next() {
		op, err := scanVersionOperation(rows)
		if err != nil {
			rows.Close()
			return domain.VersionOverview{}, err
		}
		ops = append(ops, op)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return domain.VersionOverview{}, storageError(err)
	}
	for _, op := range ops {
		if op, err = describeVersionOperation(ctx, tx, op); err != nil {
			return domain.VersionOverview{}, err
		}
		out.Operations = append(out.Operations, op)
	}
	return out, storageError(tx.Commit(ctx))
}

// versionExcluded reports whether the administrator excluded a file from
// an item (used by catalog synchronisation).
func versionExcluded(ctx context.Context, tx pgx.Tx, item, root, relative string) (bool, error) {
	var excluded bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM item_version_exclusions WHERE item_id=$1::uuid AND root_id=$2::uuid AND relative_path=$3)`, item, root, relative).Scan(&excluded)
	return excluded, storageError(err)
}
