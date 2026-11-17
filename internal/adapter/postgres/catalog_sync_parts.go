package postgres

import (
	"context"
	"maps"
	"path"
	"slices"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

// Multi-part versions (G21.1; docs/catalog-sync.md). Files that differ only
// in a part marker are registered into the same item by the sources phase,
// because the marker is not part of the group key; this step, at the start
// of the versions phase, turns them into one version with ordered parts. It
// plans every item with more than one scan source (or with parts already)
// again on every run, from the file names alone, so a part that appears,
// disappears or is renamed is reflected on the next synchronisation. Items
// an administrator decided about are left exactly as they are.

const catalogSyncPartsLimit = 128 // items whose parts are planned per batch

type partSource struct {
	id, root, path string
	manual         bool
	reason         *string
	version        *string
	number         *int
}

// syncCatalogPartsBatch plans the parts of the next page of items. It sets
// partsDone once every candidate item was seen.
func syncCatalogPartsBatch(ctx context.Context, tx pgx.Tx, current domain.JobLease, r *catalogSyncRequest) error {
	rows, err := tx.Query(ctx, `SELECT item_id::text FROM catalog_scan_sources WHERE library_id=$1::uuid AND ($2::uuid IS NULL OR item_id>$2::uuid)
 GROUP BY item_id HAVING count(*)>1 OR bool_or(part_reason IS NOT NULL) ORDER BY item_id LIMIT $3`, r.library, r.partsCursor, catalogSyncPartsLimit)
	if err != nil {
		return storageError(err)
	}
	items, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return storageError(err)
	}
	if len(items) < catalogSyncPartsLimit {
		r.partsDone, r.partsCursor = true, nil
	} else {
		last := items[len(items)-1]
		r.partsCursor = &last
	}
	if len(items) == 0 {
		return nil
	}
	naming, err := readCatalogNaming(ctx, tx, r.library)
	if err != nil {
		return err
	}
	held, err := partsHeldItems(ctx, tx, items)
	if err != nil {
		return err
	}
	sources, err := readPartSources(ctx, tx, items)
	if err != nil {
		return err
	}
	assembled, changed := 0, 0
	for _, item := range items {
		n, written, err := planItemParts(ctx, tx, sources[item], held[item], naming)
		if err != nil {
			return err
		}
		assembled += n
		if written {
			changed++
		}
	}
	if changed > 0 {
		return auditAccount(ctx, tx, domain.Actor{}, "catalog_sync.batch", current.Job.ID, nil, map[string]any{"libraryId": r.library, "partItems": changed, "multiPartVersions": assembled})
	}
	return nil
}

// partsHeldItems lists the items whose versions an administrator decided
// about: any manual version operation (or any undone one), an exclusion or a
// manually placed source, the same as the versions phase holds (G20.3).
func partsHeldItems(ctx context.Context, tx pgx.Tx, items []string) (map[string]bool, error) {
	rows, err := tx.Query(ctx, `SELECT i::text FROM unnest($1::uuid[]) i WHERE
 EXISTS(SELECT 1 FROM item_version_operations o WHERE (o.item_id=i OR o.other_item_id=i) AND (NOT o.automatic OR o.undone_at IS NOT NULL))
 OR EXISTS(SELECT 1 FROM item_version_exclusions e WHERE e.item_id=i)
 OR EXISTS(SELECT 1 FROM catalog_scan_sources s WHERE s.item_id=i AND s.manual)`, items)
	if err != nil {
		return nil, storageError(err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, storageError(err)
	}
	held := map[string]bool{}
	for _, id := range ids {
		held[id] = true
	}
	return held, nil
}

func readPartSources(ctx context.Context, tx pgx.Tx, items []string) (map[string][]partSource, error) {
	rows, err := tx.Query(ctx, `SELECT s.item_id::text,s.source_id::text,s.root_id::text,s.relative_path,s.manual,s.part_reason,p.version_source_id::text,p.part_number
 FROM catalog_scan_sources s LEFT JOIN media_source_parts p ON p.source_id=s.source_id
 WHERE s.item_id=ANY($1::uuid[]) ORDER BY s.item_id,s.source_id FOR UPDATE OF s`, items)
	if err != nil {
		return nil, storageError(err)
	}
	defer rows.Close()
	out := map[string][]partSource{}
	for rows.Next() {
		var item string
		var s partSource
		if err = rows.Scan(&item, &s.id, &s.root, &s.path, &s.manual, &s.reason, &s.version, &s.number); err != nil {
			return nil, storageError(err)
		}
		out[item] = append(out[item], s)
	}
	return out, storageError(rows.Err())
}

// planItemParts applies the plan of one item. It returns the number of
// multi-part versions the item has afterwards and whether anything was
// written.
func planItemParts(ctx context.Context, tx pgx.Tx, sources []partSource, held bool, naming domain.CatalogNamingOptions) (int, bool, error) {
	candidates := make([]domain.SourcePartCandidate, 0, len(sources))
	marked := map[string]bool{}
	ids := make([]string, 0, len(sources))
	for _, s := range sources {
		ids = append(ids, s.id)
		if s.manual {
			continue
		}
		parsed := app.PlanCatalogScan(s.path, naming).Parsed
		if parsed.Part > 0 && parsed.PartStem != "" {
			marked[s.id] = true
			candidates = append(candidates, domain.SourcePartCandidate{SourceID: s.id, Directory: s.root + "/" + path.Dir(s.path), Stem: parsed.PartStem, Part: parsed.Part})
		}
	}
	type row struct {
		version string
		number  int
	}
	existing := map[string]row{}
	for _, s := range sources {
		if s.version != nil && s.number != nil {
			existing[s.id] = row{*s.version, *s.number}
		}
	}
	reasons := map[string]string{}
	wanted := existing
	if held {
		// An administrator's decision freezes the structure; only the
		// outcome of marked files is recorded.
		for id := range marked {
			reasons[id] = domain.SourcePartHeld
		}
	} else {
		plan := domain.PlanSourceParts(candidates)
		reasons = plan.Reasons
		wanted = map[string]row{}
		for _, version := range plan.Versions {
			for i, id := range version {
				wanted[id] = row{version[0], i + 1}
			}
		}
	}
	written := false
	if !maps.Equal(existing, wanted) {
		if _, err := tx.Exec(ctx, `DELETE FROM media_source_parts WHERE source_id=ANY($1::uuid[]) OR version_source_id=ANY($1::uuid[])`, ids); err != nil {
			return 0, false, storageError(err)
		}
		keys := make([]string, 0, len(wanted))
		for id := range wanted {
			keys = append(keys, id)
		}
		slices.Sort(keys)
		for _, id := range keys {
			w := wanted[id]
			if _, err := tx.Exec(ctx, `INSERT INTO media_source_parts(source_id,version_source_id,part_number) VALUES($1::uuid,$2::uuid,$3)`, id, w.version, w.number); err != nil {
				return 0, false, storageError(err)
			}
		}
		written = true
	}
	for _, s := range sources {
		var want *string
		if reason, ok := reasons[s.id]; ok {
			want = &reason
		}
		if sameText(s.reason, want) {
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE catalog_scan_sources SET part_reason=$2 WHERE source_id=$1::uuid`, s.id, want); err != nil {
			return 0, false, storageError(err)
		}
		written = true
	}
	versions := 0
	for _, w := range wanted {
		if w.number == 1 {
			versions++
		}
	}
	return versions, written, nil
}
