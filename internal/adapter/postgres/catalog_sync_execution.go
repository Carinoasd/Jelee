package postgres

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/domain/medianame"
	"github.com/jackc/pgx/v5"
)

const (
	// Each call is one transaction under the worker's database deadline.
	catalogSyncReadLimit    = 256 // baseline rows examined per sources batch
	catalogSyncWriteLimit   = 32  // files that change the catalog per batch
	catalogSyncMissingLimit = 128 // tracked sources compared per missing batch
	catalogSyncPendingLimit = 512 // pending rows compared per cleanup batch
	catalogSyncPublishLimit = 128 // baseline rows copied per publish batch
	catalogSyncCleanupLimit = 512 // stale snapshot rows removed per batch
)

// errCatalogConflict marks a file whose plan contradicts existing structure.
// The file is kept as a pending entry instead of being forced in.
var errCatalogConflict = errors.New("catalog structure conflict")

type catalogSyncRequest struct {
	library, mode, phase                       string
	source                                     *string
	expectedSnapshot, expectedRevision, epoch  int64
	publishSnapshot                            *int64
	publishTotal, publishCopied                int64
	publishCursor                              *string
	publishCleaned                             bool
	targetSnapshot, targetRevision             *int64
	cursorRoot, cursorPath, missingCursor      *string
	examined, created, updated, unchanged      int64
	protected, pending, markedMissing, removed int64
}

func readCatalogSyncRequest(ctx context.Context, tx pgx.Tx, job string) (catalogSyncRequest, error) {
	var r catalogSyncRequest
	err := tx.QueryRow(ctx, `SELECT library_id::text,mode,phase,source_job_id::text,expected_snapshot,expected_revision,inventory_generation,publish_snapshot,publish_total,publish_copied,publish_cursor::text,publish_cleaned,target_snapshot,target_revision,cursor_root::text,cursor_path,missing_cursor::text,examined,created,updated,unchanged,protected,pending,marked_missing,removed FROM catalog_sync_requests WHERE job_id=$1::uuid FOR UPDATE`, job).Scan(
		&r.library, &r.mode, &r.phase, &r.source, &r.expectedSnapshot, &r.expectedRevision, &r.epoch, &r.publishSnapshot, &r.publishTotal, &r.publishCopied, &r.publishCursor, &r.publishCleaned, &r.targetSnapshot, &r.targetRevision, &r.cursorRoot, &r.cursorPath, &r.missingCursor,
		&r.examined, &r.created, &r.updated, &r.unchanged, &r.protected, &r.pending, &r.markedMissing, &r.removed)
	return r, storageError(err)
}

func writeCatalogSyncRequest(ctx context.Context, tx pgx.Tx, job string, r catalogSyncRequest) error {
	_, err := tx.Exec(ctx, `UPDATE catalog_sync_requests SET phase=$2,publish_snapshot=$3,publish_copied=$4,publish_cursor=$5::uuid,publish_cleaned=$6,target_snapshot=$7,target_revision=$8,cursor_root=$9::uuid,cursor_path=$10,missing_cursor=$11::uuid,examined=$12,created=$13,updated=$14,unchanged=$15,protected=$16,pending=$17,marked_missing=$18,removed=$19 WHERE job_id=$1::uuid`,
		job, r.phase, r.publishSnapshot, r.publishCopied, r.publishCursor, r.publishCleaned, r.targetSnapshot, r.targetRevision, r.cursorRoot, r.cursorPath, r.missingCursor, r.examined, r.created, r.updated, r.unchanged, r.protected, r.pending, r.markedMissing, r.removed)
	return storageError(err)
}

func catalogSyncLease(ctx context.Context, tx pgx.Tx, l domain.JobLease) (domain.JobLease, catalogSyncRequest, error) {
	current, err := fencedJob(ctx, tx, l)
	if err != nil {
		return current, catalogSyncRequest{}, err
	}
	if current.Job.Kind != domain.JobCatalogSync {
		return current, catalogSyncRequest{}, domain.ErrInvalid
	}
	if current.Job.CancelRequested {
		return current, catalogSyncRequest{}, context.Canceled
	}
	r, err := readCatalogSyncRequest(ctx, tx, l.Job.ID)
	if err != nil {
		return current, r, err
	}
	if r.library != current.Job.LibraryID {
		return current, r, domain.ErrConflict
	}
	return current, r, nil
}

// AdvanceCatalogSync commits exactly one bounded batch of the current phase
// together with its checkpoint and the final lease fence. An expired or
// replaced owner rolls back everything the batch wrote.
func (s *Store) AdvanceCatalogSync(ctx context.Context, l domain.JobLease) (bool, error) {
	if ctx == nil || !validLease(l) {
		return false, domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	current, r, err := catalogSyncLease(ctx, tx, l)
	if err != nil {
		return false, err
	}
	switch r.phase {
	case "publish":
		err = publishAcceptedBaselineBatch(ctx, tx, current, &r)
	case "sources":
		err = syncCatalogSourcesBatch(ctx, tx, current, &r, s.webhooksOn())
	case "missing":
		err = syncCatalogMissingBatch(ctx, tx, current, &r, s.webhooksOn())
	case "pending":
		err = syncCatalogPendingBatch(ctx, tx, current, &r)
	case "done":
		return true, storageError(tx.Commit(ctx))
	default:
		return false, domain.ErrDatabase
	}
	if err != nil {
		return false, err
	}
	if err = writeCatalogSyncRequest(ctx, tx, l.Job.ID, r); err != nil {
		return false, err
	}
	if err = guardedJobUpdate(ctx, tx, current, `UPDATE jobs SET files=$2 WHERE id=$1::uuid`, l.Job.ID, r.examined); err != nil {
		return false, err
	}
	return r.phase == "done", storageError(tx.Commit(ctx))
}

// checkSyncTarget keeps every batch on the baseline it was created for.
func checkSyncTarget(ctx context.Context, tx pgx.Tx, r catalogSyncRequest) error {
	if r.targetSnapshot == nil || r.targetRevision == nil {
		return domain.ErrConflict
	}
	var valid bool
	if err := tx.QueryRow(ctx, `SELECT active_inventory_snapshot=$2 AND inventory_baseline_revision=$3 AND inventory_generation=$4 FROM libraries WHERE id=$1::uuid FOR UPDATE`, r.library, *r.targetSnapshot, *r.targetRevision, r.epoch).Scan(&valid); err != nil {
		return storageError(err)
	}
	if !valid {
		return domain.ErrInventoryInvalidated
	}
	return nil
}

// publishAcceptedBaselineBatch copies the reviewed observation into an
// invisible snapshot, then switches the library pointer in the batch that
// copies the last row. The previous baseline stays visible until then.
func publishAcceptedBaselineBatch(ctx context.Context, tx pgx.Tx, current domain.JobLease, r *catalogSyncRequest) error {
	if r.mode != domain.CatalogSyncModeAccept || r.source == nil {
		return domain.ErrConflict
	}
	var live, source, accepted bool
	if err := tx.QueryRow(ctx, `SELECT
 EXISTS(SELECT 1 FROM jobs j JOIN users u ON u.id=j.actor_id WHERE j.id=$1::uuid AND u.is_admin AND NOT u.disabled AND u.deleted_at IS NULL),
 EXISTS(SELECT 1 FROM jobs WHERE id=$2::uuid AND library_id=$3::uuid AND kind='inventory_scan' AND state='succeeded' AND review_required AND files=$4 AND inventory_generation=$5),
 EXISTS(SELECT 1 FROM inventory_missing_acceptances WHERE job_id=$2::uuid AND sync_job_id=$1::uuid AND published_at IS NULL)`, current.Job.ID, *r.source, r.library, r.publishTotal, r.epoch).Scan(&live, &source, &accepted); err != nil {
		return storageError(err)
	}
	if !live {
		return domain.ErrForbidden
	}
	if !source || !accepted {
		return domain.ErrConflict
	}
	var unchanged bool
	if err := tx.QueryRow(ctx, `SELECT active_inventory_snapshot=$2 AND inventory_baseline_revision=$3 AND inventory_generation=$4 FROM libraries WHERE id=$1::uuid FOR UPDATE`, r.library, r.expectedSnapshot, r.expectedRevision, r.epoch).Scan(&unchanged); err != nil {
		return storageError(err)
	}
	if !unchanged {
		return domain.ErrInventoryInvalidated
	}
	if r.publishSnapshot == nil {
		var snapshot int64
		if err := tx.QueryRow(ctx, `SELECT nextval('inventory_snapshot_sequence')`).Scan(&snapshot); err != nil {
			return storageError(err)
		}
		r.publishSnapshot = &snapshot
	}
	if !r.publishCleaned {
		tag, err := tx.Exec(ctx, `WITH stale AS (SELECT ctid FROM library_inventory_baseline_data WHERE library_id=$1::uuid AND snapshot_id<>$2 AND snapshot_id<>$3 LIMIT $4) DELETE FROM library_inventory_baseline_data WHERE ctid IN (SELECT ctid FROM stale)`, r.library, r.expectedSnapshot, *r.publishSnapshot, catalogSyncCleanupLimit)
		if err != nil {
			return storageError(err)
		}
		if tag.RowsAffected() > 0 {
			return nil
		}
		r.publishCleaned = true
	}
	if r.publishCopied < r.publishTotal {
		var count int64
		var cursor *string
		err := tx.QueryRow(ctx, `WITH entries AS MATERIALIZED (SELECT id,root_id,path,kind,size,modified_unix_nano FROM job_inventory WHERE job_id=$1::uuid AND id>COALESCE($2::uuid,'00000000-0000-0000-0000-000000000000'::uuid) ORDER BY id LIMIT $7), inserted AS (INSERT INTO library_inventory_baseline_data(library_id,snapshot_id,root_id,path,attributes_known,kind,size,modified_unix_nano,inventory_generation,observed_revision) SELECT $3::uuid,$4,root_id,path,true,kind,size,modified_unix_nano,$5,$6 FROM entries RETURNING 1) SELECT count(*),(SELECT id::text FROM entries ORDER BY id DESC LIMIT 1) FROM inserted`, *r.source, r.publishCursor, r.library, *r.publishSnapshot, r.epoch, r.expectedRevision+1, catalogSyncPublishLimit).Scan(&count, &cursor)
		if err != nil {
			return storageError(err)
		}
		r.publishCopied += count
		if count == 0 || r.publishCopied > r.publishTotal {
			return domain.ErrInventoryInvalidated
		}
		r.publishCursor = cursor
		if r.publishCopied < r.publishTotal {
			return nil
		}
	}
	var more bool
	var rows int64
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM job_inventory WHERE job_id=$1::uuid AND id>COALESCE($2::uuid,'00000000-0000-0000-0000-000000000000'::uuid)),(SELECT count(*) FROM library_inventory_baseline_data WHERE library_id=$3::uuid AND snapshot_id=$4)`, *r.source, r.publishCursor, r.library, *r.publishSnapshot).Scan(&more, &rows); err != nil {
		return storageError(err)
	}
	if more || rows != r.publishTotal {
		return domain.ErrInventoryInvalidated
	}
	var revision int64
	if err := tx.QueryRow(ctx, `UPDATE libraries SET active_inventory_snapshot=$2,inventory_baseline_revision=inventory_baseline_revision+1 WHERE id=$1::uuid RETURNING inventory_baseline_revision`, r.library, *r.publishSnapshot).Scan(&revision); err != nil {
		return storageError(err)
	}
	if revision != r.expectedRevision+1 {
		return domain.ErrInventoryInvalidated
	}
	if _, err := tx.Exec(ctx, `UPDATE inventory_missing_acceptances SET published_at=clock_timestamp() WHERE job_id=$1::uuid AND sync_job_id=$2::uuid`, *r.source, current.Job.ID); err != nil {
		return storageError(err)
	}
	r.targetSnapshot, r.targetRevision = r.publishSnapshot, &revision
	r.phase = "sources"
	return auditAccount(ctx, tx, domain.Actor{}, "inventory.baseline_accepted", *r.source, map[string]any{"baselineRevision": r.expectedRevision}, map[string]any{"baselineRevision": revision, "entries": rows, "catalogSyncJobId": current.Job.ID})
}

type syncCandidate struct {
	root, path                   string
	size, modified               int64
	trackedSource, trackedItem   *string
	trackedSize, trackedModified *int64
	trackedVersion               *string
	missing, external            bool
	pendingSize, pendingModified *int64
	pendingVersion               *string
}

func syncCatalogSourcesBatch(ctx context.Context, tx pgx.Tx, current domain.JobLease, r *catalogSyncRequest, events bool) error {
	if err := checkSyncTarget(ctx, tx, *r); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT b.root_id::text,b.path,b.size,b.modified_unix_nano,
 s.source_id::text,s.item_id::text,s.size,s.modified_unix_nano,s.parser_version,COALESCE(s.missing_since IS NOT NULL,false),
 EXISTS(SELECT 1 FROM media_sources m WHERE m.root_id=b.root_id AND m.relative_path=b.path),
 p.size,p.modified_unix_nano,p.parser_version
 FROM library_inventory_baseline_data b
 JOIN library_roots r ON r.id=b.root_id AND r.library_id=b.library_id
 LEFT JOIN catalog_scan_sources s ON s.root_id=b.root_id AND s.relative_path=b.path
 LEFT JOIN catalog_scan_pending p ON p.root_id=b.root_id AND p.relative_path=b.path
 WHERE b.library_id=$1::uuid AND b.snapshot_id=$2 AND b.kind='video' AND b.attributes_known AND b.inventory_generation=$3
 AND ($4::uuid IS NULL OR (b.root_id,b.path COLLATE "C")>($4::uuid,$5::text COLLATE "C"))
 ORDER BY b.root_id,b.path COLLATE "C" LIMIT $6`, r.library, *r.targetSnapshot, r.epoch, r.cursorRoot, r.cursorPath, catalogSyncReadLimit)
	if err != nil {
		return storageError(err)
	}
	candidates := make([]syncCandidate, 0, catalogSyncReadLimit)
	for rows.Next() {
		var c syncCandidate
		if err = rows.Scan(&c.root, &c.path, &c.size, &c.modified, &c.trackedSource, &c.trackedItem, &c.trackedSize, &c.trackedModified, &c.trackedVersion, &c.missing, &c.external, &c.pendingSize, &c.pendingModified, &c.pendingVersion); err != nil {
			rows.Close()
			return storageError(err)
		}
		candidates = append(candidates, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return storageError(err)
	}
	sidecars, err := readSidecarBatch(ctx, tx, *r, candidates)
	if err != nil {
		return err
	}
	w := catalogWriter{tx: tx, library: r.library, snapshot: *r.targetSnapshot, now: time.Now().UTC(), events: events}
	writes, processed, sidecarSources := 0, 0, 0
	for _, c := range candidates {
		if writes >= catalogSyncWriteLimit {
			break
		}
		changed, source, err := w.syncCandidate(ctx, c, r)
		if err != nil {
			return err
		}
		// Sidecars follow every scan source, including one whose video is
		// unchanged: a subtitle added, removed or renamed next to it is a
		// write of this batch as well.
		if source != "" {
			written, err := syncSourceSidecars(ctx, tx, source, sidecars.wanted[sidecarKey{c.root, c.path}], sidecars.existing[source])
			if err != nil {
				return err
			}
			if written {
				sidecarSources++
				changed = true
			}
		}
		if changed {
			writes++
		}
		processed++
		r.examined++
		root, p := c.root, c.path
		r.cursorRoot, r.cursorPath = &root, &p
	}
	if processed == len(candidates) && len(candidates) < catalogSyncReadLimit {
		r.phase, r.cursorRoot, r.cursorPath, r.missingCursor = "missing", nil, nil, nil
	}
	if writes > 0 {
		details := map[string]any{"libraryId": r.library, "written": writes}
		if sidecarSources > 0 {
			details["sidecarSources"] = sidecarSources
		}
		return auditAccount(ctx, tx, domain.Actor{}, "catalog_sync.batch", current.Job.ID, nil, details)
	}
	return nil
}

type catalogWriter struct {
	tx       pgx.Tx
	library  string
	snapshot int64
	now      time.Time
	// events raises media.added for items this writer creates (G12.1).
	events bool
}

// syncCandidate applies one baseline video. It reports whether the catalog
// or the pending list changed; untouched files only advance the cursor. The
// returned source is the scan source the file is registered as, or empty
// for an explicit import or a pending file, whose tracks sync leaves alone.
func (w catalogWriter) syncCandidate(ctx context.Context, c syncCandidate, r *catalogSyncRequest) (bool, string, error) {
	created := ""
	changed, err := w.applyCandidate(ctx, c, r, &created)
	switch {
	case err != nil:
		return false, "", err
	case c.trackedSource != nil:
		return changed, *c.trackedSource, nil
	default:
		return changed, created, nil
	}
}

func (w catalogWriter) applyCandidate(ctx context.Context, c syncCandidate, r *catalogSyncRequest, created *string) (bool, error) {
	version := domain.CatalogSyncParserVersion
	switch {
	case c.trackedSource != nil:
		if *c.trackedSize == c.size && *c.trackedModified == c.modified && *c.trackedVersion == version {
			if !c.missing {
				r.unchanged++
				return false, nil
			}
			if _, err := w.tx.Exec(ctx, `UPDATE catalog_scan_sources SET missing_since=NULL WHERE source_id=$1::uuid`, *c.trackedSource); err != nil {
				return false, storageError(err)
			}
			r.updated++
			return true, nil
		}
		protected, err := w.refreshTracked(ctx, c)
		if err != nil {
			return false, err
		}
		if protected {
			r.protected++
		}
		r.updated++
		return true, nil
	case c.external:
		// Explicitly imported sources belong to their importer.
		if c.pendingSize != nil {
			if _, err := w.tx.Exec(ctx, `DELETE FROM catalog_scan_pending WHERE root_id=$1::uuid AND relative_path=$2`, c.root, c.path); err != nil {
				return false, storageError(err)
			}
			r.unchanged++
			return true, nil
		}
		r.unchanged++
		return false, nil
	case c.pendingSize != nil && *c.pendingSize == c.size && *c.pendingModified == c.modified && *c.pendingVersion == version:
		r.unchanged++
		return false, nil
	}
	plan := app.PlanCatalogScan(c.path)
	if !plan.Auto {
		r.pending++
		return true, w.putPending(ctx, c, plan, plan.Reason)
	}
	savepoint, err := w.tx.Begin(ctx)
	if err != nil {
		return false, storageError(err)
	}
	inner := catalogWriter{tx: savepoint, library: w.library, snapshot: w.snapshot, now: w.now, events: w.events}
	source, err := inner.create(ctx, c, plan)
	if errors.Is(err, errCatalogConflict) {
		if rollback := savepoint.Rollback(ctx); rollback != nil {
			return false, storageError(rollback)
		}
		r.pending++
		return true, w.putPending(ctx, c, plan, "conflict")
	}
	if err != nil {
		_ = savepoint.Rollback(ctx)
		return false, err
	}
	if err = savepoint.Commit(ctx); err != nil {
		return false, storageError(err)
	}
	r.created++
	*created = source
	return true, nil
}

func (w catalogWriter) putPending(ctx context.Context, c syncCandidate, plan app.CatalogScanPlan, reason string) error {
	p := plan.Parsed
	kind := "unknown"
	switch p.Kind {
	case medianame.KindMovie:
		kind = "movie"
	case medianame.KindEpisode:
		kind = "episode"
	}
	var year, season, episode, episodeEnd, absolute *int
	if p.Year > 0 {
		year = &p.Year
	}
	if p.HasSeason {
		season = &p.Season
	}
	if p.HasEpisode {
		episode, episodeEnd = &p.Episode, &p.EpisodeEnd
	}
	if p.Absolute > 0 {
		absolute = &p.Absolute
	}
	title := p.Title
	if len(title) > 1024 {
		title = ""
	}
	_, err := w.tx.Exec(ctx, `INSERT INTO catalog_scan_pending(library_id,root_id,relative_path,size,modified_unix_nano,parser_version,reason,kind,confidence,title,year,season,episode,episode_end,absolute,special) VALUES($1::uuid,$2::uuid,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
 ON CONFLICT(root_id,relative_path) DO UPDATE SET size=EXCLUDED.size,modified_unix_nano=EXCLUDED.modified_unix_nano,parser_version=EXCLUDED.parser_version,reason=EXCLUDED.reason,kind=EXCLUDED.kind,confidence=EXCLUDED.confidence,title=EXCLUDED.title,year=EXCLUDED.year,season=EXCLUDED.season,episode=EXCLUDED.episode,episode_end=EXCLUDED.episode_end,absolute=EXCLUDED.absolute,special=EXCLUDED.special,updated_at=clock_timestamp()`,
		w.library, c.root, c.path, c.size, c.modified, domain.CatalogSyncParserVersion, reason, kind, p.Confidence.String(), title, year, season, episode, episodeEnd, absolute, p.Special.String())
	return storageError(err)
}

// refreshTracked re-plans a tracked file whose stamp or parser changed. It
// never moves the source to another item; a newer file-name title replaces
// only an unlocked scan value. It reports whether a higher-priority value
// protected the field.
func (w catalogWriter) refreshTracked(ctx context.Context, c syncCandidate) (bool, error) {
	if _, err := w.tx.Exec(ctx, `UPDATE catalog_scan_sources SET size=$2,modified_unix_nano=$3,parser_version=$4,missing_since=NULL WHERE source_id=$1::uuid`, *c.trackedSource, c.size, c.modified, domain.CatalogSyncParserVersion); err != nil {
		return false, storageError(err)
	}
	plan := app.PlanCatalogScan(c.path)
	if !plan.Auto {
		return false, nil
	}
	var kind, title string
	err := w.tx.QueryRow(ctx, `SELECT kind,scan_title FROM catalog_scan_items WHERE item_id=$1::uuid AND library_id=$2::uuid FOR UPDATE`, *c.trackedItem, w.library).Scan(&kind, &title)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, storageError(err)
	}
	if kind != plan.Kind || title == plan.Title {
		return false, nil
	}
	if _, err = w.tx.Exec(ctx, `UPDATE catalog_scan_items SET scan_title=$2,parser_version=$3 WHERE item_id=$1::uuid`, *c.trackedItem, plan.Title, domain.CatalogSyncParserVersion); err != nil {
		return false, storageError(err)
	}
	return w.applyScanTitle(ctx, *c.trackedItem, plan.Title)
}

// applyScanTitle writes a file-name title with source scan unless a lock,
// manual edit, NFO, TMDB or pre-existing value holds the field.
func (w catalogWriter) applyScanTitle(ctx context.Context, item, title string) (bool, error) {
	before, err := readItemMetadata(ctx, w.tx, item, true)
	if err != nil {
		return false, err
	}
	old := before.Fields[0]
	if domain.ScanMetadataSkip(old) != "" {
		return true, nil
	}
	if old.Value == title {
		return false, nil
	}
	if _, err = w.tx.Exec(ctx, `INSERT INTO item_metadata_state(item_id,revision) VALUES($1::uuid,$2) ON CONFLICT(item_id) DO UPDATE SET revision=EXCLUDED.revision`, item, before.Revision+1); err != nil {
		return false, storageError(err)
	}
	return false, writeItemMetadataField(ctx, w.tx, item, domain.ItemMetadataField{Field: "title", Value: title, Source: "scan"}, w.now)
}

func (w catalogWriter) create(ctx context.Context, c syncCandidate, plan app.CatalogScanPlan) (string, error) {
	var item string
	var err error
	switch plan.Kind {
	case "Movie":
		item, err = w.groupItem(ctx, c, "Movie", domain.CatalogGroupDigest("movie", c.root, plan.Directory, domain.NormalizeCatalogTitle(plan.Title), strconv.Itoa(plan.Year)), scanItem{title: plan.Title, year: plan.Year})
	case "Episode":
		var season string
		season, err = w.season(ctx, c, plan)
		if err != nil {
			return "", err
		}
		item, err = w.groupItem(ctx, c, "Episode", domain.CatalogGroupDigest("episode", season, plan.Directory, strconv.Itoa(plan.Episode), strconv.Itoa(plan.EpisodeEnd)), scanItem{title: plan.Title, season: &plan.Season, episode: &plan.Episode, episodeEnd: &plan.EpisodeEnd, parent: season, parentKind: "Season", childRoot: c.root, childPath: c.path})
	default:
		return "", errCatalogConflict
	}
	if err != nil {
		return "", err
	}
	var source string
	if err = w.tx.QueryRow(ctx, `INSERT INTO media_sources(item_id,library_id,root_id,relative_path,content_type) VALUES($1::uuid,$2::uuid,$3::uuid,$4,$5) RETURNING id::text`, item, w.library, c.root, c.path, plan.ContentType).Scan(&source); err != nil {
		return "", storageError(err)
	}
	if _, err = w.tx.Exec(ctx, `INSERT INTO catalog_scan_sources(source_id,library_id,item_id,root_id,relative_path,size,modified_unix_nano,parser_version) VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5,$6,$7,$8)`, source, w.library, item, c.root, c.path, c.size, c.modified, domain.CatalogSyncParserVersion); err != nil {
		return "", storageError(err)
	}
	_, err = w.tx.Exec(ctx, `DELETE FROM catalog_scan_pending WHERE root_id=$1::uuid AND relative_path=$2`, c.root, c.path)
	return source, storageError(err)
}

type scanItem struct {
	title                       string
	year                        int
	season, episode, episodeEnd *int
	// Optional folder registered for the item, and the parent link with the
	// child location the link legality rules check.
	directory, directoryRoot string
	parent, parentKind       string
	childRoot, childPath     string
}

// groupItem returns the item a file version belongs to: same directory and
// same parsed identity (G20.1 first step). NFO identity wins over the file
// name: a file with its own NFO is not merged into an item that already
// carries NFO provider IDs, because its NFO may name a different title.
func (w catalogWriter) groupItem(ctx context.Context, c syncCandidate, kind string, digest []byte, spec scanItem) (string, error) {
	item, err := w.findScanItem(ctx, kind, digest)
	if err != nil {
		return "", err
	}
	if item != "" {
		separate, err := w.nfoIdentityConflict(ctx, item, c)
		if err != nil {
			return "", err
		}
		if !separate {
			return item, nil
		}
		digest = domain.CatalogGroupDigest(kind+"-file", c.root, c.path)
		if item, err = w.findScanItem(ctx, kind, digest); err != nil || item != "" {
			return item, err
		}
	}
	return w.createScanItem(ctx, kind, digest, spec)
}

func (w catalogWriter) nfoIdentityConflict(ctx context.Context, item string, c syncCandidate) (bool, error) {
	nfo, ok := domain.AdjacentNFOPath(c.path)
	if !ok {
		return false, nil
	}
	var conflict bool
	err := w.tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM library_inventory_baseline_data WHERE library_id=$1::uuid AND snapshot_id=$2 AND root_id=$3::uuid AND path=$4 AND kind='nfo') AND EXISTS(SELECT 1 FROM item_metadata_facts WHERE item_id=$5::uuid AND field='uniqueIds' AND source='nfo')`, w.library, w.snapshot, c.root, nfo, item).Scan(&conflict)
	return conflict, storageError(err)
}

func (w catalogWriter) findScanItem(ctx context.Context, kind string, digest []byte) (string, error) {
	var item string
	err := w.tx.QueryRow(ctx, `SELECT item_id::text FROM catalog_scan_items WHERE library_id=$1::uuid AND kind=$2 AND group_digest=$3`, w.library, kind, digest).Scan(&item)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return item, storageError(err)
}

func (w catalogWriter) createScanItem(ctx context.Context, kind string, digest []byte, spec scanItem) (string, error) {
	var item string
	if err := w.tx.QueryRow(ctx, `INSERT INTO items(library_id,title,kind) VALUES($1::uuid,$2,$3) RETURNING id::text`, w.library, spec.title, kind).Scan(&item); err != nil {
		return "", storageError(err)
	}
	var year *int
	if spec.year > 0 {
		year = &spec.year
	}
	if _, err := w.tx.Exec(ctx, `INSERT INTO catalog_scan_items(item_id,library_id,kind,group_digest,parser_version,scan_title,year,season,episode,episode_end) VALUES($1::uuid,$2::uuid,$3,$4,$5,$6,$7,$8,$9,$10)`, item, w.library, kind, digest, domain.CatalogSyncParserVersion, spec.title, year, spec.season, spec.episode, spec.episodeEnd); err != nil {
		return "", storageError(err)
	}
	if _, err := w.tx.Exec(ctx, `INSERT INTO item_metadata_state(item_id,revision) VALUES($1::uuid,2)`, item); err != nil {
		return "", storageError(err)
	}
	if err := writeItemMetadataField(ctx, w.tx, item, domain.ItemMetadataField{Field: "title", Value: spec.title, Source: "scan"}, w.now); err != nil {
		return "", err
	}
	if spec.directory != "" {
		if _, err := w.tx.Exec(ctx, `INSERT INTO item_directory_sources(item_id,library_id,kind,root_id,relative_path) VALUES($1::uuid,$2::uuid,$3,$4::uuid,$5)`, item, w.library, kind, spec.directoryRoot, spec.directory); err != nil {
			return "", storageError(err)
		}
	}
	if spec.parent != "" {
		if err := w.link(ctx, item, kind, spec.parent, spec.parentKind, spec.childRoot, spec.childPath); err != nil {
			return "", err
		}
	}
	// Raised inside the savepoint of the file that created the item, so a
	// conflict that rolls the item back rolls the event back too.
	if err := appendWebhook(ctx, w.tx, w.events, domain.WebhookMediaAdded, w.now, domain.WebhookSubject{Kind: domain.WebhookSubjectItem, ID: item},
		map[string]any{"libraryId": w.library, "kind": kind, "title": spec.title}); err != nil {
		return "", err
	}
	return item, nil
}

// link applies the same legality rules as writeImportedParent: the parent is
// in the same library, the kinds nest, and a parent with a registered folder
// only receives children located under that folder on the same root.
func (w catalogWriter) link(ctx context.Context, item, kind, parent, parentKind, root, childPath string) error {
	var actualKind string
	var parentRoot, parentPath *string
	err := w.tx.QueryRow(ctx, `SELECT i.kind,d.root_id::text,d.relative_path FROM items i LEFT JOIN item_directory_sources d ON d.item_id=i.id AND d.library_id=i.library_id WHERE i.id=$1::uuid AND i.library_id=$2::uuid FOR UPDATE OF i`, parent, w.library).Scan(&actualKind, &parentRoot, &parentPath)
	if errors.Is(err, pgx.ErrNoRows) {
		return errCatalogConflict
	}
	if err != nil {
		return storageError(err)
	}
	if actualKind != parentKind || !(kind == "Season" && parentKind == "Series" || kind == "Episode" && (parentKind == "Season" || parentKind == "Series")) {
		return errCatalogConflict
	}
	if parentRoot != nil && (*parentRoot != root || childPath == *parentPath || *parentPath != "." && !pathUnder(childPath, *parentPath)) {
		return errCatalogConflict
	}
	_, err = w.tx.Exec(ctx, `INSERT INTO item_parent_links(item_id,library_id,item_kind,parent_id,parent_kind) VALUES($1::uuid,$2::uuid,$3,$4::uuid,$5)`, item, w.library, kind, parent, parentKind)
	return storageError(err)
}

func pathUnder(child, parent string) bool {
	return len(child) > len(parent)+1 && child[:len(parent)] == parent && child[len(parent)] == '/'
}

// directoryItem returns the item registered for a folder, if any.
func (w catalogWriter) directoryItem(ctx context.Context, root, relative string) (string, string, error) {
	var item, kind string
	err := w.tx.QueryRow(ctx, `SELECT i.id::text,i.kind FROM item_directory_sources d JOIN items i ON i.id=d.item_id AND i.library_id=d.library_id WHERE d.root_id=$1::uuid AND d.relative_path=$2 AND d.library_id=$3::uuid FOR UPDATE OF d`, root, relative, w.library).Scan(&item, &kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", nil
	}
	return item, kind, storageError(err)
}

func (w catalogWriter) series(ctx context.Context, c syncCandidate, plan app.CatalogScanPlan) (string, error) {
	spec := scanItem{title: plan.SeriesTitle, year: plan.SeriesYear}
	if plan.SeriesDirectory == "" {
		digest := domain.CatalogGroupDigest("series", domain.NormalizeCatalogTitle(plan.SeriesTitle), strconv.Itoa(plan.SeriesYear))
		item, err := w.findScanItem(ctx, "Series", digest)
		if err != nil || item != "" {
			return item, err
		}
		return w.createScanItem(ctx, "Series", digest, spec)
	}
	item, kind, err := w.directoryItem(ctx, c.root, plan.SeriesDirectory)
	if err != nil {
		return "", err
	}
	if item != "" {
		if kind != "Series" {
			return "", errCatalogConflict
		}
		return item, nil
	}
	digest := domain.CatalogGroupDigest("series-dir", c.root, plan.SeriesDirectory)
	if item, err = w.findScanItem(ctx, "Series", digest); err != nil || item != "" {
		return item, err
	}
	spec.directory, spec.directoryRoot = plan.SeriesDirectory, c.root
	return w.createScanItem(ctx, "Series", digest, spec)
}

func (w catalogWriter) season(ctx context.Context, c syncCandidate, plan app.CatalogScanPlan) (string, error) {
	series, err := w.series(ctx, c, plan)
	if err != nil {
		return "", err
	}
	if plan.SeasonDirectory != "" {
		item, kind, err := w.directoryItem(ctx, c.root, plan.SeasonDirectory)
		if err != nil {
			return "", err
		}
		if item != "" {
			var parent string
			err = w.tx.QueryRow(ctx, `SELECT parent_id::text FROM item_parent_links WHERE item_id=$1::uuid AND library_id=$2::uuid`, item, w.library).Scan(&parent)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return "", storageError(err)
			}
			if kind != "Season" || parent != series {
				return "", errCatalogConflict
			}
			return item, nil
		}
	}
	digest := domain.CatalogGroupDigest("season", series, strconv.Itoa(plan.Season))
	item, err := w.findScanItem(ctx, "Season", digest)
	if err != nil {
		return "", err
	}
	if item != "" {
		return item, nil
	}
	season := plan.Season
	spec := scanItem{title: domain.SeasonTitle(season), season: &season, parent: series, parentKind: "Series"}
	if plan.SeasonDirectory != "" {
		spec.directory, spec.directoryRoot = plan.SeasonDirectory, c.root
		spec.childRoot, spec.childPath = c.root, plan.SeasonDirectory
	} else {
		spec.childRoot, spec.childPath = c.root, c.path
	}
	return w.createScanItem(ctx, "Season", digest, spec)
}

func syncCatalogMissingBatch(ctx context.Context, tx pgx.Tx, current domain.JobLease, r *catalogSyncRequest, events bool) error {
	if err := checkSyncTarget(ctx, tx, *r); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT s.source_id::text,s.item_id::text,s.missing_since IS NOT NULL,EXISTS(SELECT 1 FROM library_inventory_baseline_data b WHERE b.library_id=s.library_id AND b.snapshot_id=$2 AND b.root_id=s.root_id AND b.path=s.relative_path AND b.kind='video' AND b.attributes_known AND b.inventory_generation=$3) FROM catalog_scan_sources s WHERE s.library_id=$1::uuid AND ($4::uuid IS NULL OR s.source_id>$4::uuid) ORDER BY s.source_id LIMIT $5`, r.library, *r.targetSnapshot, r.epoch, r.missingCursor, catalogSyncMissingLimit)
	if err != nil {
		return storageError(err)
	}
	type tracked struct {
		source, item     string
		marked, observed bool
	}
	var batch []tracked
	for rows.Next() {
		var t tracked
		if err = rows.Scan(&t.source, &t.item, &t.marked, &t.observed); err != nil {
			rows.Close()
			return storageError(err)
		}
		batch = append(batch, t)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return storageError(err)
	}
	withTracks, err := sourcesWithSidecars(ctx, tx, batch, func(t tracked) (string, bool) {
		return t.source, !t.observed && !t.marked && r.mode != domain.CatalogSyncModeAccept
	})
	if err != nil {
		return err
	}
	removed := 0
	for _, t := range batch {
		if t.observed {
			continue
		}
		// Without an administrator acceptance a vanished file is only
		// marked. Accepted baselines remove the scan-registered source and
		// any scan-created item left without media or children.
		if r.mode != domain.CatalogSyncModeAccept {
			if !t.marked {
				if _, err = tx.Exec(ctx, `UPDATE catalog_scan_sources SET missing_since=clock_timestamp() WHERE source_id=$1::uuid`, t.source); err != nil {
					return storageError(err)
				}
				r.markedMissing++
				// The video's folder no longer yields it, so no file can be
				// paired with it either. Accepted removals cascade instead.
				if withTracks[t.source] {
					if _, err = UpsertSidecarTracks(ctx, tx, t.source, nil); err != nil {
						return err
					}
				}
			}
			continue
		}
		if _, err = tx.Exec(ctx, `DELETE FROM media_sources WHERE id=$1::uuid AND library_id=$2::uuid`, t.source, r.library); err != nil {
			return storageError(err)
		}
		if err = pruneScanItem(ctx, tx, r.library, t.item, events); err != nil {
			return err
		}
		r.removed++
		removed++
	}
	if len(batch) > 0 {
		last := batch[len(batch)-1].source
		r.missingCursor = &last
	}
	if len(batch) < catalogSyncMissingLimit {
		r.phase, r.missingCursor = "pending", nil
	}
	if removed > 0 {
		return auditAccount(ctx, tx, domain.Actor{}, "catalog_sync.removed", current.Job.ID, nil, map[string]any{"libraryId": r.library, "removed": removed})
	}
	return nil
}

// sourcesWithSidecars reports, with one statement, which of the selected
// sources hold sidecar rows.
func sourcesWithSidecars[T any](ctx context.Context, tx pgx.Tx, batch []T, selected func(T) (string, bool)) (map[string]bool, error) {
	var ids []string
	for _, v := range batch {
		if id, ok := selected(v); ok {
			ids = append(ids, id)
		}
	}
	found := map[string]bool{}
	if len(ids) == 0 {
		return found, nil
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT source_id::text FROM media_sidecar_tracks WHERE source_id=ANY($1::uuid[])`, ids)
	if err != nil {
		return nil, storageError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, storageError(err)
		}
		found[id] = true
	}
	return found, storageError(rows.Err())
}

// pruneScanItem deletes scan-created items that no longer hold media or
// children, walking up to the season and series. Items carrying any value
// from a person, NFO or TMDB, any lock, or any fact are kept.
func pruneScanItem(ctx context.Context, tx pgx.Tx, library, item string, events bool) error {
	for depth := 0; depth < 3 && item != ""; depth++ {
		var removable bool
		var parent *string
		err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM catalog_scan_items WHERE item_id=$1::uuid AND library_id=$2::uuid)
 AND NOT EXISTS(SELECT 1 FROM media_sources WHERE item_id=$1::uuid)
 AND NOT EXISTS(SELECT 1 FROM item_parent_links WHERE parent_id=$1::uuid)
 AND NOT EXISTS(SELECT 1 FROM item_metadata_fields WHERE item_id=$1::uuid AND (source<>'scan' OR locked))
 AND NOT EXISTS(SELECT 1 FROM item_metadata_facts WHERE item_id=$1::uuid)
 AND NOT EXISTS(SELECT 1 FROM item_nfo_field_locks WHERE item_id=$1::uuid),
 (SELECT parent_id::text FROM item_parent_links WHERE item_id=$1::uuid)`, item, library).Scan(&removable, &parent)
		if err != nil {
			return storageError(err)
		}
		if !removable {
			return nil
		}
		if _, err = tx.Exec(ctx, `DELETE FROM items WHERE id=$1::uuid AND library_id=$2::uuid`, item, library); err != nil {
			return storageError(err)
		}
		if err = appendWebhook(ctx, tx, events, domain.WebhookMediaDeleted, time.Now(), domain.WebhookSubject{Kind: domain.WebhookSubjectItem, ID: item}, map[string]any{"libraryId": library}); err != nil {
			return err
		}
		item = ""
		if parent != nil {
			item = *parent
		}
	}
	return nil
}

// syncCatalogPendingBatch drops pending entries whose file left the baseline.
func syncCatalogPendingBatch(ctx context.Context, tx pgx.Tx, _ domain.JobLease, r *catalogSyncRequest) error {
	if err := checkSyncTarget(ctx, tx, *r); err != nil {
		return err
	}
	var count int64
	var last *string
	err := tx.QueryRow(ctx, `WITH page AS MATERIALIZED (SELECT id,root_id,relative_path FROM catalog_scan_pending WHERE library_id=$1::uuid AND id>COALESCE($4::uuid,'00000000-0000-0000-0000-000000000000'::uuid) ORDER BY id LIMIT $5),
 gone AS (DELETE FROM catalog_scan_pending p USING page WHERE p.id=page.id AND NOT EXISTS(SELECT 1 FROM library_inventory_baseline_data b WHERE b.library_id=$1::uuid AND b.snapshot_id=$2 AND b.root_id=page.root_id AND b.path=page.relative_path AND b.kind='video' AND b.attributes_known AND b.inventory_generation=$3) RETURNING 1)
 SELECT (SELECT count(*) FROM page),(SELECT id::text FROM page ORDER BY id DESC LIMIT 1),(SELECT count(*) FROM gone)`, r.library, *r.targetSnapshot, r.epoch, r.missingCursor, catalogSyncPendingLimit).Scan(&count, &last, new(int64))
	if err != nil {
		return storageError(err)
	}
	r.missingCursor = last
	if count < catalogSyncPendingLimit {
		r.phase, r.missingCursor = "done", nil
	}
	return nil
}

// FinishCatalogSync records the terminal state. Success requires every phase
// to have committed; cancellation or failure keeps completed batches.
func (s *Store) FinishCatalogSync(ctx context.Context, l domain.JobLease, state, code string) error {
	if !validJobError(state, code) && !(state == domain.JobFailed && code == "catalog_sync_failed") {
		return domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	current, err := fencedJob(ctx, tx, l)
	if err != nil {
		return err
	}
	if current.Job.Kind != domain.JobCatalogSync {
		return domain.ErrInvalid
	}
	r, err := readCatalogSyncRequest(ctx, tx, l.Job.ID)
	if err != nil {
		return err
	}
	if state == domain.JobSucceeded && (current.Job.CancelRequested || r.phase != "done") {
		return domain.ErrConflict
	}
	if err = guardedJobUpdate(ctx, tx, current, `UPDATE jobs SET state=$2,error_code=$3,finished_at=clock_timestamp(),owner=NULL,lease_until=NULL WHERE id=$1::uuid`, l.Job.ID, state, code); err != nil {
		return err
	}
	if err = auditAccount(ctx, tx, domain.Actor{}, "catalog_sync.finished", l.Job.ID, nil, map[string]any{"state": state, "errorCode": code, "phase": r.phase, "created": r.created, "updated": r.updated, "pending": r.pending, "markedMissing": r.markedMissing, "removed": r.removed, "protected": r.protected}); err != nil {
		return err
	}
	if err = trimJobs(ctx, tx, current.Policy.HistoryLimit); err != nil {
		return err
	}
	return storageError(tx.Commit(ctx))
}
