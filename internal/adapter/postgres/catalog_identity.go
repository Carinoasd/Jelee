package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

var _ app.CatalogIdentityRepository = (*Store)(nil)

// identityCorrection is the stored lock row of an item.
type identityCorrection struct {
	revision                   int64
	kindLocked, parentLocked   bool
	season, episode, episodeTo *int
	updatedAt                  *time.Time
}

func (c identityCorrection) anyLocked() bool {
	return c.kindLocked || c.parentLocked || c.season != nil || c.episode != nil
}

// readItemIdentity resolves the effective identification: a locked
// correction wins over NFO facts, which win over file-name values (G21.2).
func readItemIdentity(ctx context.Context, tx pgx.Tx, item string, lock bool) (domain.ItemIdentity, identityCorrection, error) {
	v := domain.ItemIdentity{ItemID: item}
	var c identityCorrection
	var revision *int64
	var kindLocked, parentLocked *bool
	var scanKind *string
	var scanSeason, scanEpisode, scanEnd, nfoSeason, nfoEpisode *int
	query := `SELECT i.library_id::text,i.kind,COALESCE(p.parent_id::text,''),COALESCE(p.parent_kind,''),
 k.revision,k.kind_locked,k.parent_locked,k.season,k.episode,k.episode_end,k.updated_at,
 s.kind,s.season,s.episode,s.episode_end,
 (SELECT CASE WHEN jsonb_typeof(f.value)='number' THEN (f.value#>>'{}')::integer END FROM item_metadata_facts f WHERE f.item_id=i.id AND f.field='seasonNumber'),
 (SELECT CASE WHEN jsonb_typeof(f.value)='number' THEN (f.value#>>'{}')::integer END FROM item_metadata_facts f WHERE f.item_id=i.id AND f.field='episodeNumber')
 FROM items i
 LEFT JOIN item_parent_links p ON p.item_id=i.id
 LEFT JOIN item_identity_corrections k ON k.item_id=i.id
 LEFT JOIN catalog_scan_items s ON s.item_id=i.id
 WHERE i.id=$1::uuid`
	if lock {
		query += ` FOR UPDATE OF i`
	}
	err := tx.QueryRow(ctx, query, item).Scan(&v.LibraryID, &v.Kind, &v.ParentID, &v.ParentKind,
		&revision, &kindLocked, &parentLocked, &c.season, &c.episode, &c.episodeTo, &c.updatedAt,
		&scanKind, &scanSeason, &scanEpisode, &scanEnd, &nfoSeason, &nfoEpisode)
	if err != nil {
		return v, c, storageError(err)
	}
	c.revision = 1
	if revision != nil {
		c.revision, c.kindLocked, c.parentLocked = *revision, *kindLocked, *parentLocked
	}
	v.Revision, v.KindLocked, v.ParentLocked = c.revision, c.kindLocked, c.parentLocked
	if c.updatedAt != nil {
		stamp := c.updatedAt.UTC()
		v.UpdatedAt = &stamp
	}
	if scanKind != nil {
		v.Scan = &domain.ItemIdentityScan{Kind: *scanKind, Season: scanSeason, Episode: scanEpisode, EpisodeEnd: scanEnd}
	}
	v.SeasonSource, v.EpisodeSource = domain.ItemIdentitySourceNone, domain.ItemIdentitySourceNone
	switch {
	case c.season != nil:
		v.Season, v.SeasonSource, v.SeasonLocked = c.season, domain.ItemIdentitySourceManual, true
	case nfoSeason != nil:
		v.Season, v.SeasonSource = nfoSeason, domain.ItemIdentitySourceNFO
	case scanSeason != nil:
		v.Season, v.SeasonSource = scanSeason, domain.ItemIdentitySourceScan
	}
	switch {
	case c.episode != nil:
		v.Episode, v.EpisodeEnd, v.EpisodeSource, v.EpisodeLocked = c.episode, c.episodeTo, domain.ItemIdentitySourceManual, true
	case nfoEpisode != nil:
		end := *nfoEpisode
		v.Episode, v.EpisodeEnd, v.EpisodeSource = nfoEpisode, &end, domain.ItemIdentitySourceNFO
	case scanEpisode != nil:
		v.Episode, v.EpisodeEnd, v.EpisodeSource = scanEpisode, scanEnd, domain.ItemIdentitySourceScan
	}
	return v, c, nil
}

// lockItemLibrary takes the library row before any item row. Synchronisation
// batches hold the same lock for their whole transaction, so a correction
// never interleaves with a batch that links or renames items.
func lockItemLibrary(ctx context.Context, tx pgx.Tx, item string) error {
	var library string
	if err := tx.QueryRow(ctx, `SELECT library_id::text FROM items WHERE id=$1::uuid`, item).Scan(&library); err != nil {
		return storageError(err)
	}
	_, err := lockLibraryBaseline(ctx, tx, library)
	return err
}

// ItemIdentity reads the effective identification of an item.
func (s *Store) ItemIdentity(ctx context.Context, actor domain.Actor, item string) (domain.ItemIdentity, error) {
	if ctx == nil || !domain.ValidID(item) {
		return domain.ItemIdentity{}, domain.ErrInvalid
	}
	tx, err := s.authorizedJobs(ctx, actor)
	if err != nil {
		return domain.ItemIdentity{}, err
	}
	defer tx.Rollback(ctx)
	value, _, err := readItemIdentity(ctx, tx, item, false)
	if err != nil {
		return domain.ItemIdentity{}, err
	}
	return value, storageError(tx.Commit(ctx))
}

// UpdateItemIdentity applies a revision-checked correction. Kind changes
// first, so the other fields are checked against the new kind. The audit
// row carries the effective identity before and after.
func (s *Store) UpdateItemIdentity(ctx context.Context, actor domain.Actor, item string, expected int64, patches []domain.ItemIdentityPatch) (domain.ItemIdentity, error) {
	if ctx == nil || !domain.ValidItemIdentityPatches(item, expected, patches) {
		return domain.ItemIdentity{}, domain.ErrInvalid
	}
	tx, err := s.authorizedJobs(ctx, actor)
	if err != nil {
		return domain.ItemIdentity{}, err
	}
	defer tx.Rollback(ctx)
	if err = lockItemLibrary(ctx, tx, item); err != nil {
		return domain.ItemIdentity{}, err
	}
	before, c, err := readItemIdentity(ctx, tx, item, true)
	if err != nil {
		return domain.ItemIdentity{}, err
	}
	if c.revision != expected {
		return domain.ItemIdentity{}, domain.ErrConflict
	}
	kind := before.Kind
	for _, p := range domain.OrderItemIdentityPatches(patches) {
		// Numbers belong to seasons and episodes; unlocking one that no
		// longer applies is harmless. Parent values are checked by kind below.
		if (p.Field == "season" || p.Field == "episode") && !domain.ItemIdentityFieldApplies(kind, p.Field) && (p.HasValue || *p.Locked) {
			return domain.ItemIdentity{}, domain.ErrConflict
		}
		switch p.Field {
		case "kind":
			if p.HasValue && p.Kind != kind {
				if err = changeItemKind(ctx, tx, item, before.LibraryID, kind, p.Kind); err != nil {
					return domain.ItemIdentity{}, err
				}
				kind = p.Kind
				// Numbers and parent of the former kind no longer apply.
				c.parentLocked = false
				if !domain.ItemIdentityFieldApplies(kind, "season") {
					c.season = nil
				}
				if !domain.ItemIdentityFieldApplies(kind, "episode") {
					c.episode, c.episodeTo = nil, nil
				}
			}
			c.kindLocked = p.HasValue || *p.Locked
		case "parent":
			if p.HasValue {
				if err = setItemParent(ctx, tx, item, before.LibraryID, kind, p.Parent); err != nil {
					return domain.ItemIdentity{}, err
				}
			}
			c.parentLocked = p.HasValue || *p.Locked
		case "season":
			switch {
			case p.HasValue:
				n := p.Number
				c.season = &n
			case *p.Locked && c.season == nil:
				if before.Season == nil {
					return domain.ItemIdentity{}, domain.ErrConflict
				}
				n := *before.Season
				c.season = &n
			case !*p.Locked:
				c.season = nil
			}
		case "episode":
			switch {
			case p.HasValue:
				n, end := p.Number, p.NumberEnd
				c.episode, c.episodeTo = &n, &end
			case *p.Locked && c.episode == nil:
				if before.Episode == nil {
					return domain.ItemIdentity{}, domain.ErrConflict
				}
				n, end := *before.Episode, *before.Episode
				if before.EpisodeEnd != nil {
					end = *before.EpisodeEnd
				}
				c.episode, c.episodeTo = &n, &end
			case !*p.Locked:
				c.episode, c.episodeTo = nil, nil
			}
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO item_identity_corrections(item_id,library_id,revision,kind_locked,parent_locked,season,episode,episode_end,updated_at) VALUES($1::uuid,$2::uuid,$3,$4,$5,$6,$7,$8,clock_timestamp())
 ON CONFLICT(item_id) DO UPDATE SET revision=EXCLUDED.revision,kind_locked=EXCLUDED.kind_locked,parent_locked=EXCLUDED.parent_locked,season=EXCLUDED.season,episode=EXCLUDED.episode,episode_end=EXCLUDED.episode_end,updated_at=EXCLUDED.updated_at`,
		item, before.LibraryID, expected+1, c.kindLocked, c.parentLocked, c.season, c.episode, c.episodeTo); err != nil {
		return domain.ItemIdentity{}, storageError(err)
	}
	after, _, err := readItemIdentity(ctx, tx, item, false)
	if err != nil {
		return domain.ItemIdentity{}, err
	}
	if err = auditAccount(ctx, tx, actor, "item.identity_changed", item, before, after); err != nil {
		return domain.ItemIdentity{}, err
	}
	return after, storageError(tx.Commit(ctx))
}

// changeItemKind reclassifies a leaf item. Rows that carry the kind in their
// foreign key are taken out and written back with the new kind; a parent
// link does not survive because the old kind chose it.
func changeItemKind(ctx context.Context, tx pgx.Tx, item, library, from, to string) error {
	if !domain.ItemIdentityLeafKind(from) || !domain.ItemIdentityLeafKind(to) {
		return domain.ErrConflict
	}
	var blocked, scanned bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM item_parent_links WHERE parent_id=$1::uuid) OR EXISTS(SELECT 1 FROM item_directory_sources WHERE item_id=$1::uuid) OR EXISTS(SELECT 1 FROM catalog_scan_item_aliases WHERE item_id=$1::uuid),
 EXISTS(SELECT 1 FROM catalog_scan_items WHERE item_id=$1::uuid)`, item).Scan(&blocked, &scanned); err != nil {
		return storageError(err)
	}
	// Scan structure knows only Movie, Series, Season and Episode, and a
	// merged group names its item by kind: undo the merge first.
	if blocked || scanned && to == "HomeVideo" {
		return domain.ErrConflict
	}
	if _, err := tx.Exec(ctx, `DELETE FROM item_parent_links WHERE item_id=$1::uuid`, item); err != nil {
		return storageError(err)
	}
	// The scan row names the kind in its foreign key: take it out, change
	// the kind and write it back with the same grouping key.
	var digest []byte
	var version, title string
	var year, season, episode, episodeEnd *int
	if scanned {
		if err := tx.QueryRow(ctx, `DELETE FROM catalog_scan_items WHERE item_id=$1::uuid RETURNING group_digest,parser_version,scan_title,year,season,episode,episode_end`, item).Scan(&digest, &version, &title, &year, &season, &episode, &episodeEnd); err != nil {
			return storageError(err)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE items SET kind=$3 WHERE id=$1::uuid AND library_id=$2::uuid`, item, library, to); err != nil {
		return storageError(err)
	}
	if !scanned {
		return nil
	}
	_, err := tx.Exec(ctx, `INSERT INTO catalog_scan_items(item_id,library_id,kind,group_digest,parser_version,scan_title,year,season,episode,episode_end) VALUES($1::uuid,$2::uuid,$3,$4,$5,$6,$7,$8,$9,$10)`, item, library, to, digest, version, title, year, season, episode, episodeEnd)
	return storageError(err)
}

// setItemParent links a Season under a Series or an Episode under a Season or
// Series of the same library. An explicit correction is not bound to the
// parent's folder: moving a misfiled episode to its real season is the point.
func setItemParent(ctx context.Context, tx pgx.Tx, item, library, kind, parent string) error {
	if parent == "" {
		if kind == "Season" {
			return domain.ErrConflict
		}
		_, err := tx.Exec(ctx, `DELETE FROM item_parent_links WHERE item_id=$1::uuid`, item)
		return storageError(err)
	}
	if parent == item {
		return domain.ErrConflict
	}
	var parentKind string
	err := tx.QueryRow(ctx, `SELECT kind FROM items WHERE id=$1::uuid AND library_id=$2::uuid FOR UPDATE`, parent, library).Scan(&parentKind)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrConflict
	}
	if err != nil {
		return storageError(err)
	}
	if !domain.ItemIdentityParentAllowed(kind, parentKind) {
		return domain.ErrConflict
	}
	if _, err = tx.Exec(ctx, `DELETE FROM item_parent_links WHERE item_id=$1::uuid`, item); err != nil {
		return storageError(err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO item_parent_links(item_id,library_id,item_kind,parent_id,parent_kind) VALUES($1::uuid,$2::uuid,$3,$4::uuid,$5)`, item, library, kind, parent, parentKind)
	return storageError(err)
}

const catalogPendingColumns = `id::text,root_id::text,relative_path,reason,kind,confidence,title,COALESCE(year,0),season,episode,episode_end,COALESCE(absolute,0),special,CASE WHEN rejected_at IS NULL THEN 'pending' ELSE 'rejected' END`

func scanCatalogPending(row pgx.Row, e *domain.CatalogPendingEntry, extra ...any) error {
	return row.Scan(append([]any{&e.ID, &e.RootID, &e.Path, &e.Reason, &e.Kind, &e.Confidence, &e.Title, &e.Year, &e.Season, &e.Episode, &e.EpisodeEnd, &e.Absolute, &e.Special, &e.Decision}, extra...)...)
}

// lockCatalogPending reads one pending entry of a library under the library
// lock synchronisation batches take. An entry of another library is missing.
func lockCatalogPending(ctx context.Context, tx pgx.Tx, library, entry string) (libraryBaseline, domain.CatalogPendingEntry, int64, int64, error) {
	var e domain.CatalogPendingEntry
	var size, modified int64
	b, err := lockLibraryBaseline(ctx, tx, library)
	if err != nil {
		return b, e, 0, 0, err
	}
	err = scanCatalogPending(tx.QueryRow(ctx, `SELECT `+catalogPendingColumns+`,size,modified_unix_nano FROM catalog_scan_pending WHERE id=$1::uuid AND library_id=$2::uuid FOR UPDATE`, entry, library), &e, &size, &modified)
	return b, e, size, modified, storageError(err)
}

// AcceptCatalogPending registers a pending file the way synchronisation
// would have with a confident name, using the administrator's values. The
// file must still be the one the accepted baseline observed. A new item
// takes the decision as a locked identity so no later scan undoes it.
func (s *Store) AcceptCatalogPending(ctx context.Context, actor domain.Actor, library, entry string, d domain.CatalogPendingDecision) (domain.CatalogPendingAcceptance, error) {
	if ctx == nil || !domain.ValidID(library) || !domain.ValidID(entry) || !domain.ValidCatalogPendingDecision(d) {
		return domain.CatalogPendingAcceptance{}, domain.ErrInvalid
	}
	tx, err := s.authorizedJobs(ctx, actor)
	if err != nil {
		return domain.CatalogPendingAcceptance{}, err
	}
	defer tx.Rollback(ctx)
	b, e, size, modified, err := lockCatalogPending(ctx, tx, library, entry)
	if err != nil {
		return domain.CatalogPendingAcceptance{}, err
	}
	var current, registered bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM library_inventory_baseline_data WHERE library_id=$1::uuid AND snapshot_id=$2 AND root_id=$3::uuid AND path=$4 AND kind='video' AND attributes_known AND inventory_generation=$5 AND size=$6 AND modified_unix_nano=$7),
 EXISTS(SELECT 1 FROM media_sources WHERE root_id=$3::uuid AND relative_path=$4)`, library, b.snapshot, e.RootID, e.Path, b.generation, size, modified).Scan(&current, &registered); err != nil {
		return domain.CatalogPendingAcceptance{}, storageError(err)
	}
	// A changed or vanished file is decided again after the next
	// synchronisation refreshed the entry.
	if !current || registered {
		return domain.CatalogPendingAcceptance{}, domain.ErrConflict
	}
	plan, err := app.PlanCatalogPendingAccept(e.Path, e, d)
	if err != nil {
		return domain.CatalogPendingAcceptance{}, err
	}
	now := time.Now().UTC()
	savepoint, err := tx.Begin(ctx)
	if err != nil {
		return domain.CatalogPendingAcceptance{}, storageError(err)
	}
	w := catalogWriter{tx: savepoint, library: library, snapshot: b.snapshot, now: now, events: s.webhooksOn()}
	source, err := w.create(ctx, syncCandidate{root: e.RootID, path: e.Path, size: size, modified: modified}, plan)
	if err != nil {
		_ = savepoint.Rollback(ctx)
		if errors.Is(err, errCatalogConflict) {
			return domain.CatalogPendingAcceptance{}, domain.ErrConflict
		}
		return domain.CatalogPendingAcceptance{}, err
	}
	if err = savepoint.Commit(ctx); err != nil {
		return domain.CatalogPendingAcceptance{}, storageError(err)
	}
	result := domain.CatalogPendingAcceptance{EntryID: entry, SourceID: source, Kind: plan.Kind}
	if err = tx.QueryRow(ctx, `SELECT item_id::text,NOT EXISTS(SELECT 1 FROM media_sources o WHERE o.item_id=m.item_id AND o.id<>m.id) FROM media_sources m WHERE m.id=$1::uuid`, source).Scan(&result.ItemID, &result.Created); err != nil {
		return domain.CatalogPendingAcceptance{}, storageError(err)
	}
	if result.Created {
		var season, episode, end *int
		if plan.Kind == "Episode" {
			season, episode, end = &plan.Season, &plan.Episode, &plan.EpisodeEnd
		}
		if _, err = tx.Exec(ctx, `INSERT INTO item_identity_corrections(item_id,library_id,revision,kind_locked,season,episode,episode_end) VALUES($1::uuid,$2::uuid,2,true,$3,$4,$5)`, result.ItemID, library, season, episode, end); err != nil {
			return domain.CatalogPendingAcceptance{}, storageError(err)
		}
		if d.Title != nil {
			if err = writeItemMetadataField(ctx, tx, result.ItemID, domain.ItemMetadataField{Field: "title", Value: plan.Title, Source: "manual"}, now); err != nil {
				return domain.CatalogPendingAcceptance{}, err
			}
		}
	}
	// Paths stay out of the audit trail; the entry ID and the result name the decision.
	if err = auditAccount(ctx, tx, actor, "catalog_sync.pending_accepted", entry, map[string]any{"libraryId": library, "reason": e.Reason, "kind": e.Kind, "decision": e.Decision}, result); err != nil {
		return domain.CatalogPendingAcceptance{}, err
	}
	return result, storageError(tx.Commit(ctx))
}

// RejectCatalogPending keeps a pending file out of the catalog until the
// file changes. Repeating a rejection is a conflict, not a replay.
func (s *Store) RejectCatalogPending(ctx context.Context, actor domain.Actor, library, entry string) (domain.CatalogPendingEntry, error) {
	if ctx == nil || !domain.ValidID(library) || !domain.ValidID(entry) {
		return domain.CatalogPendingEntry{}, domain.ErrInvalid
	}
	tx, err := s.authorizedJobs(ctx, actor)
	if err != nil {
		return domain.CatalogPendingEntry{}, err
	}
	defer tx.Rollback(ctx)
	_, e, _, _, err := lockCatalogPending(ctx, tx, library, entry)
	if err != nil {
		return domain.CatalogPendingEntry{}, err
	}
	if e.Decision != domain.CatalogPendingOpen {
		return domain.CatalogPendingEntry{}, domain.ErrConflict
	}
	if _, err = tx.Exec(ctx, `UPDATE catalog_scan_pending SET rejected_at=clock_timestamp(),rejected_by=$2::uuid WHERE id=$1::uuid`, entry, actor.UserID); err != nil {
		return domain.CatalogPendingEntry{}, storageError(err)
	}
	e.Decision = domain.CatalogPendingRejected
	if err = auditAccount(ctx, tx, actor, "catalog_sync.pending_rejected", entry, map[string]any{"libraryId": library, "decision": domain.CatalogPendingOpen}, map[string]any{"libraryId": library, "decision": e.Decision, "reason": e.Reason, "kind": e.Kind}); err != nil {
		return domain.CatalogPendingEntry{}, err
	}
	return e, storageError(tx.Commit(ctx))
}
