package postgres

import (
	"context"
	"errors"
	"slices"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

var _ app.ItemImageIngestRepository = (*Store)(nil)

// IngestItemImages runs one page of the catalog sync image pass (G40.2,
// G40.3) for the items of the job's library after the given item ID. It
// runs once every sync phase has committed, in one transaction fenced by the
// catalog sync lease, and reads only the baseline the sync published and the
// catalog: image file names of each item folder (images.RecognizeImageNames)
// and the NFO art facts. URLs are recorded, never fetched. Locked and manual
// rows are never changed; a scan-written local or NFO row whose file or
// reference is gone is removed. When the published baseline is no longer
// the library's active one, nothing is written and the page is Stale.
func (s *Store) IngestItemImages(ctx context.Context, l domain.JobLease, after string, limit int) (domain.ItemImageIngestPage, error) {
	if ctx == nil || !validLease(l) || after != "" && !domain.ValidID(after) || limit < 1 || limit > domain.ItemImageIngestBatch {
		return domain.ItemImageIngestPage{}, domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return domain.ItemImageIngestPage{}, err
	}
	defer tx.Rollback(ctx)
	current, r, err := catalogSyncLease(ctx, tx, l)
	if err != nil {
		return domain.ItemImageIngestPage{}, err
	}
	if r.phase != "done" || r.targetSnapshot == nil || r.targetRevision == nil {
		return domain.ItemImageIngestPage{}, domain.ErrConflict
	}
	var page domain.ItemImageIngestPage
	// A share lock keeps the published baseline in place while the page
	// reads it; publishing a newer one waits for this transaction.
	var active bool
	if err = tx.QueryRow(ctx, `SELECT active_inventory_snapshot=$2 AND inventory_baseline_revision=$3 AND inventory_generation=$4 FROM libraries WHERE id=$1::uuid FOR SHARE`,
		r.library, *r.targetSnapshot, *r.targetRevision, r.epoch).Scan(&active); err != nil {
		return domain.ItemImageIngestPage{}, storageError(err)
	}
	if !active {
		page.Stale = true
	} else if err = ingestItemImagePage(ctx, tx, r, after, limit, &page); err != nil {
		return domain.ItemImageIngestPage{}, err
	}
	if err = guardedJobUpdate(ctx, tx, current, `UPDATE jobs SET files=files WHERE id=$1::uuid`, l.Job.ID); err != nil {
		return domain.ItemImageIngestPage{}, err
	}
	return page, storageError(tx.Commit(ctx))
}

func ingestItemImagePage(ctx context.Context, tx pgx.Tx, r catalogSyncRequest, after string, limit int, page *domain.ItemImageIngestPage) error {
	var cursor *string
	if after != "" {
		cursor = &after
	}
	in := ingestInputs{parents: map[string]*ingestDir{}, listings: map[ingestDir]ingestListing{}, videos: map[ingestDir]int{},
		art: map[string]ingestArt{}, nfoFiles: map[ingestDir]ingestFile{}}
	rows, err := tx.Query(ctx, `SELECT i.id::text,i.kind,COALESCE(p.parent_id::text,''),d.root_id::text,d.relative_path,
 CASE WHEN i.kind<>'Season' THEN NULL WHEN c.season IS NOT NULL THEN c.season
  WHEN jsonb_typeof(f.value)='number' AND f.value::text ~ '^[0-9]{1,4}$' THEN f.value::text::integer END
 FROM items i
 LEFT JOIN item_parent_links p ON p.item_id=i.id
 LEFT JOIN item_directory_sources d ON d.item_id=i.id AND d.library_id=i.library_id
 LEFT JOIN catalog_scan_items c ON c.item_id=i.id
 LEFT JOIN item_metadata_facts f ON f.item_id=i.id AND f.field='seasonNumber'
 WHERE i.library_id=$1::uuid AND ($2::uuid IS NULL OR i.id>$2::uuid)
 ORDER BY i.id LIMIT $3`, r.library, cursor, limit)
	if err != nil {
		return storageError(err)
	}
	var ids, parents []string
	for rows.Next() {
		var item ingestItem
		var root, dir *string
		if err = rows.Scan(&item.id, &item.kind, &item.parent, &root, &dir, &item.season); err != nil {
			rows.Close()
			return storageError(err)
		}
		if root != nil && dir != nil {
			item.dir = &ingestDir{*root, registeredDirectory(*dir)}
		}
		if item.kind == "Season" && item.parent != "" {
			parents = append(parents, item.parent)
		}
		ids = append(ids, item.id)
		in.items = append(in.items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return storageError(err)
	}
	page.Items = len(in.items)
	if len(in.items) == limit {
		page.Next = in.items[len(in.items)-1].id
	}
	if len(in.items) == 0 {
		return nil
	}
	if err = readIngestSources(ctx, tx, ids, in.items); err != nil {
		return err
	}
	if err = readIngestParents(ctx, tx, parents, in.parents); err != nil {
		return err
	}
	all, movies := ingestDirectories(in.items, in.parents)
	if err = readIngestListings(ctx, tx, r, all, in.listings); err != nil {
		return err
	}
	if err = readIngestVideoCounts(ctx, tx, r, movies, in.videos); err != nil {
		return err
	}
	if err = readIngestArt(ctx, tx, append(ids, parents...), in.art); err != nil {
		return err
	}
	if err = readIngestNFOFiles(ctx, tx, r, nfoReferences(in), in.nfoFiles); err != nil {
		return err
	}
	existing, err := readIngestExisting(ctx, tx, ids)
	if err != nil {
		return err
	}
	return applyIngestPlan(ctx, tx, in.items, planItemImages(in), existing, page)
}

func readIngestSources(ctx context.Context, tx pgx.Tx, ids []string, items []ingestItem) error {
	rows, err := tx.Query(ctx, `SELECT item_id::text,id::text,root_id::text,relative_path FROM media_sources
 WHERE item_id=ANY($1::uuid[]) ORDER BY item_id,root_id,relative_path COLLATE "C"`, ids)
	if err != nil {
		return storageError(err)
	}
	defer rows.Close()
	index := make(map[string]int, len(items))
	for i, item := range items {
		index[item.id] = i
	}
	for rows.Next() {
		var item string
		var source ingestSource
		if err = rows.Scan(&item, &source.id, &source.root, &source.path); err != nil {
			return storageError(err)
		}
		if i, ok := index[item]; ok {
			items[i].sources = append(items[i].sources, source)
		}
	}
	return storageError(rows.Err())
}

func readIngestParents(ctx context.Context, tx pgx.Tx, parents []string, out map[string]*ingestDir) error {
	if len(parents) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT item_id::text,root_id::text,relative_path FROM item_directory_sources WHERE item_id=ANY($1::uuid[]) AND kind='Series'`, parents)
	if err != nil {
		return storageError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var item, root, dir string
		if err = rows.Scan(&item, &root, &dir); err != nil {
			return storageError(err)
		}
		out[item] = &ingestDir{root, registeredDirectory(dir)}
	}
	return storageError(rows.Err())
}

// baselineImageFilter keeps only image rows observed by the scan that
// published the target baseline, like sidecar pairing: a file an ignore
// rule excluded keeps its old observation and is not an image any more.
const baselineImageFilter = `b.kind='image' AND b.attributes_known AND b.inventory_generation=$5 AND b.observed_revision=$6`

func readIngestListings(ctx context.Context, tx pgx.Tx, r catalogSyncRequest, dirs []ingestDir, out map[ingestDir]ingestListing) error {
	if len(dirs) == 0 {
		return nil
	}
	roots, names := make([]string, len(dirs)), make([]string, len(dirs))
	for i, d := range dirs {
		roots[i], names[i] = d.root, d.dir
	}
	rows, err := tx.Query(ctx, `SELECT o.root_id::text,o.dir,b.path,b.size,b.modified_unix_nano
 FROM unnest($3::uuid[],$4::text[]) AS o(root_id,dir)
 JOIN library_inventory_baseline_data b ON b.library_id=$1::uuid AND b.snapshot_id=$2 AND b.root_id=o.root_id
  AND inventory_parent_directory(b.path)=o.dir AND b.kind='image'
 WHERE `+baselineImageFilter+` ORDER BY o.root_id,o.dir,b.path COLLATE "C"`,
		r.library, *r.targetSnapshot, roots, names, r.epoch, *r.targetRevision)
	if err != nil {
		return storageError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var d ingestDir
		var f ingestFile
		if err = rows.Scan(&d.root, &d.dir, &f.path, &f.size, &f.modified); err != nil {
			return storageError(err)
		}
		listing := out[d]
		listing.files = append(listing.files, f)
		out[d] = listing
	}
	return storageError(rows.Err())
}

// readIngestVideoCounts counts, per movie folder, the distinct catalog items
// (or uncatalogued videos) directly inside it.
func readIngestVideoCounts(ctx context.Context, tx pgx.Tx, r catalogSyncRequest, dirs []ingestDir, out map[ingestDir]int) error {
	if len(dirs) == 0 {
		return nil
	}
	roots, names := make([]string, len(dirs)), make([]string, len(dirs))
	for i, d := range dirs {
		roots[i], names[i] = d.root, d.dir
	}
	rows, err := tx.Query(ctx, `SELECT o.root_id::text,o.dir,count(DISTINCT COALESCE(m.item_id::text,b.path))
 FROM unnest($3::uuid[],$4::text[]) AS o(root_id,dir)
 JOIN library_inventory_baseline_data b ON b.library_id=$1::uuid AND b.snapshot_id=$2 AND b.root_id=o.root_id
  AND inventory_sidecar_owner(b.path)=o.dir AND b.kind='video'
 LEFT JOIN media_sources m ON m.root_id=b.root_id AND m.relative_path=b.path
 WHERE b.attributes_known AND b.inventory_generation=$5 AND inventory_parent_directory(b.path)=o.dir
 GROUP BY o.root_id,o.dir`, r.library, *r.targetSnapshot, roots, names, r.epoch)
	if err != nil {
		return storageError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var d ingestDir
		var n int
		if err = rows.Scan(&d.root, &d.dir, &n); err != nil {
			return storageError(err)
		}
		out[d] = n
	}
	return storageError(rows.Err())
}

// readIngestArt reads the NFO art facts with the folder of the NFO they came
// from: the media file's folder or the registered series or season folder
// named by the fact's NFO origin.
func readIngestArt(ctx context.Context, tx pgx.Tx, ids []string, out map[string]ingestArt) error {
	rows, err := tx.Query(ctx, `SELECT f.item_id::text,f.value::text,COALESCE(m.root_id,d.root_id)::text,m.relative_path,d.relative_path
 FROM item_metadata_facts f
 LEFT JOIN media_sources m ON m.id=(f.nfo_origin->>'sourceId')::uuid AND m.item_id=f.item_id AND m.root_id=(f.nfo_origin->>'rootId')::uuid
 LEFT JOIN item_directory_sources d ON d.id=(f.nfo_origin->>'sourceId')::uuid AND d.item_id=f.item_id AND d.root_id=(f.nfo_origin->>'rootId')::uuid
 WHERE f.item_id=ANY($1::uuid[]) AND f.field='art' AND f.source='nfo' AND jsonb_typeof(f.value)='array'`, ids)
	if err != nil {
		return storageError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var item, value string
		var root, media, dir *string
		if err = rows.Scan(&item, &value, &root, &media, &dir); err != nil {
			return storageError(err)
		}
		art := ingestArt{entries: decodeArtFact([]byte(value))}
		switch {
		case root != nil && media != nil:
			art.dir = &ingestDir{*root, parentDirectory(*media)}
		case root != nil && dir != nil:
			art.dir = &ingestDir{*root, registeredDirectory(*dir)}
		}
		out[item] = art
	}
	return storageError(rows.Err())
}

// readIngestNFOFiles looks up, in the published baseline, the root files NFO
// art points at. refs carry the file path in the dir field.
func readIngestNFOFiles(ctx context.Context, tx pgx.Tx, r catalogSyncRequest, refs []ingestDir, out map[ingestDir]ingestFile) error {
	if len(refs) == 0 {
		return nil
	}
	roots, paths := make([]string, len(refs)), make([]string, len(refs))
	for i, ref := range refs {
		roots[i], paths[i] = ref.root, ref.dir
	}
	rows, err := tx.Query(ctx, `SELECT b.root_id::text,b.path,b.size,b.modified_unix_nano
 FROM unnest($3::uuid[],$4::text[]) AS o(root_id,path)
 JOIN library_inventory_baseline_data b ON b.library_id=$1::uuid AND b.snapshot_id=$2 AND b.root_id=o.root_id AND b.path=o.path
 WHERE `+baselineImageFilter, r.library, *r.targetSnapshot, roots, paths, r.epoch, *r.targetRevision)
	if err != nil {
		return storageError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var root string
		var f ingestFile
		if err = rows.Scan(&root, &f.path, &f.size, &f.modified); err != nil {
			return storageError(err)
		}
		out[ingestDir{root, f.path}] = f
	}
	return storageError(rows.Err())
}

type ingestRow struct {
	id              string
	slot            ingestSlot
	root, path, url string
	modified, size  *int64
	locked, manual  bool
}

func readIngestExisting(ctx context.Context, tx pgx.Tx, ids []string) (map[string]map[ingestSlot]ingestRow, error) {
	rows, err := tx.Query(ctx, `SELECT g.id::text,g.item_id::text,g.image_type,g.image_index,g.source_kind,COALESCE(g.root_id::text,''),
 COALESCE(g.relative_path,''),COALESCE(g.remote_url,''),g.source_mtime_unix_nano,g.source_size,g.locked,g.manual
 FROM item_images g WHERE g.item_id=ANY($1::uuid[]) AND g.source_kind IN ('local','nfo','remote')`, ids)
	if err != nil {
		return nil, storageError(err)
	}
	defer rows.Close()
	out := map[string]map[ingestSlot]ingestRow{}
	for rows.Next() {
		var item string
		var v ingestRow
		if err = rows.Scan(&v.id, &item, &v.slot.typ, &v.slot.index, &v.slot.kind, &v.root, &v.path, &v.url, &v.modified, &v.size, &v.locked, &v.manual); err != nil {
			return nil, storageError(err)
		}
		if out[item] == nil {
			out[item] = map[ingestSlot]ingestRow{}
		}
		out[item][v.slot] = v
	}
	return out, storageError(rows.Err())
}

func sameIngestSource(v ingestRow, in domain.ItemImageInput) bool {
	return v.root == in.RootID && v.path == in.RelativePath && v.url == in.RemoteURL &&
		equalInt64(v.modified, in.SourceModifiedUnixNano) && equalInt64(v.size, in.SourceSize)
}

// applyIngestPlan writes the planned rows item by item. Unchanged rows are
// not written; locked and manual rows are skipped; a scan-written local or
// NFO row the plan no longer names is removed unless its kind could not be
// fully observed or its slot was ambiguous. Remote rows are only added or
// updated: other writers own remote references too.
func applyIngestPlan(ctx context.Context, tx pgx.Tx, items []ingestItem, plan map[string]*ingestWant, existing map[string]map[ingestSlot]ingestRow, page *domain.ItemImageIngestPage) error {
	for _, item := range items {
		want, rows := plan[item.id], existing[item.id]
		slots := make([]ingestSlot, 0, len(want.inputs))
		for slot := range want.inputs {
			slots = append(slots, slot)
		}
		slices.SortFunc(slots, compareIngestSlots)
		gone := false
		for _, slot := range slots {
			in := want.inputs[slot]
			if old, found := rows[slot]; found {
				if old.locked || old.manual {
					page.Skipped++
					continue
				}
				if sameIngestSource(old, in) {
					continue
				}
			}
			result, err := upsertItemImage(ctx, tx, domain.Actor{}, in)
			if errors.Is(err, domain.ErrNotFound) {
				// The item was deleted meanwhile.
				gone = true
				break
			}
			if err != nil {
				return err
			}
			switch {
			case result.Skipped:
				page.Skipped++
			case result.Created:
				page.Created++
			default:
				page.Updated++
			}
		}
		if gone {
			continue
		}
		stale := make([]ingestRow, 0)
		for slot, old := range rows {
			_, wanted := want.inputs[slot]
			if wanted || old.locked || old.manual || want.protected[slot] || slot.kind == domain.ImageSourceRemote ||
				slot.kind == domain.ImageSourceLocal && !want.localComplete {
				continue
			}
			stale = append(stale, old)
		}
		slices.SortFunc(stale, func(a, b ingestRow) int { return compareIngestSlots(a.slot, b.slot) })
		for _, old := range stale {
			// The item lock serializes with manual writers; the row is
			// re-checked so a lock or manual write that won the race stays.
			if err := lockImageItem(ctx, tx, item.id); errors.Is(err, domain.ErrNotFound) {
				break
			} else if err != nil {
				return err
			}
			tag, err := tx.Exec(ctx, `DELETE FROM item_images WHERE id=$1::uuid AND NOT locked AND NOT manual AND source_kind IN ('local','nfo')`, old.id)
			if err != nil {
				return storageError(err)
			}
			page.Removed += int(tag.RowsAffected())
		}
	}
	return nil
}

func compareIngestSlots(a, b ingestSlot) int {
	if a.typ != b.typ {
		if a.typ < b.typ {
			return -1
		}
		return 1
	}
	if a.index != b.index {
		return a.index - b.index
	}
	return domain.ItemImageSourcePriority(a.kind) - domain.ItemImageSourcePriority(b.kind)
}
