package postgres

import (
	"bytes"
	"context"
	"errors"
	"slices"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

// The versions phase of catalog synchronisation (G20.1, G20.5, G21.2;
// docs/catalog-sync.md). Files are grouped by directory and parsed identity
// when they are registered; copies of one work kept in other directories
// become items of their own. Once their NFO provider IDs are applied, this
// phase folds them together as an automatic, undoable merge. It only adds
// aggregation: no item is ever split here, and items an administrator
// locked or decided about are left alone.

const (
	catalogSyncVersionsLimit = 128 // items with NFO IDs read per batch
	catalogSyncMergeLimit    = 8   // automatic merges per batch
	catalogSyncPeerLimit     = 16  // items sharing one provider ID compared
)

// factNumberSQL reads an integer fact (NFO or manual) of an item; any other
// JSON value, including an explicit null, counts as absent.
func factNumberSQL(item, field string) string {
	return `(SELECT CASE WHEN jsonb_typeof(f.value)='number' THEN (f.value#>>'{}')::integer END FROM item_metadata_facts f WHERE f.item_id=` + item + `::uuid AND f.field='` + field + `')`
}

// effectiveNumbersSQL selects season, episode and episode_end in the
// identification order of readItemIdentity: a locked correction (alias k),
// else NFO facts, else the file name (alias c) (G21.2).
func effectiveNumbersSQL(item, k, c string) string {
	nfoEpisode := factNumberSQL(item, "episodeNumber")
	return `COALESCE(` + k + `.season,` + factNumberSQL(item, "seasonNumber") + `,` + c + `.season) AS season,
 COALESCE(` + k + `.episode,` + nfoEpisode + `,` + c + `.episode) AS episode,
 CASE WHEN ` + k + `.episode IS NOT NULL THEN ` + k + `.episode_end WHEN ` + nfoEpisode + ` IS NOT NULL THEN ` + nfoEpisode + ` ELSE ` + c + `.episode_end END AS episode_end`
}

// versionCandidateSQL reads synchronised Movies and Episodes that carry NFO
// provider IDs, with everything a grouping decision depends on. A
// multi-part version (G21.1) contributes one duration, the sum of its parts.
// Callers add the remaining conditions and the order.
var versionCandidateSQL = `SELECT c.item_id::text,c.kind,f.value::text,c.grouping_evidence,` + effectiveNumbersSQL("c.item_id", "k", "c") + `,
 COALESCE(` + factNumberSQL("c.item_id", "seasonNumber") + `<>c.season OR ` + factNumberSQL("c.item_id", "episodeNumber") + `<>c.episode,false),
 CASE WHEN k.kind_locked OR k.parent_locked OR k.season IS NOT NULL OR k.episode IS NOT NULL THEN '` + domain.VersionGroupingHeldLocked + `'
  WHEN EXISTS(SELECT 1 FROM item_version_operations o WHERE o.item_id=c.item_id AND (NOT o.automatic OR o.undone_at IS NOT NULL))
   OR EXISTS(SELECT 1 FROM item_version_operations o WHERE o.other_item_id=c.item_id AND (NOT o.automatic OR o.undone_at IS NOT NULL))
   OR EXISTS(SELECT 1 FROM item_version_exclusions e WHERE e.item_id=c.item_id)
   OR EXISTS(SELECT 1 FROM catalog_scan_sources s WHERE s.item_id=c.item_id AND s.manual) THEN '` + domain.VersionGroupingHeldManual + `'
  WHEN EXISTS(SELECT 1 FROM media_sources m WHERE m.item_id=c.item_id AND NOT EXISTS(SELECT 1 FROM catalog_scan_sources s WHERE s.source_id=m.id)) THEN '` + domain.VersionGroupingHeldImported + `'
  ELSE '' END,
 ARRAY(SELECT CASE WHEN bool_and(v.duration IS NOT NULL) THEN sum(v.duration)::bigint END FROM (
  SELECT COALESCE(pp.version_source_id,m.id) AS version,(SELECT CASE WHEN jsonb_typeof(p.metadata#>'{format,durationMicros}')='number' THEN (p.metadata#>>'{format,durationMicros}')::bigint END
   FROM probe_cache p WHERE p.root_id=m.root_id AND p.relative_path=m.relative_path AND p.state='ready' AND p.size=s.size AND p.modified_unix_nano=s.modified_unix_nano) AS duration
  FROM media_sources m LEFT JOIN catalog_scan_sources s ON s.source_id=m.id LEFT JOIN media_source_parts pp ON pp.source_id=m.id WHERE m.item_id=c.item_id) v
  GROUP BY v.version ORDER BY v.version)
 FROM catalog_scan_items c
 JOIN item_metadata_facts f ON f.item_id=c.item_id AND f.field='uniqueIds' AND f.source='nfo'
 LEFT JOIN item_identity_corrections k ON k.item_id=c.item_id
 WHERE c.library_id=$1::uuid AND c.kind IN ('Movie','Episode')`

type versionCandidate struct {
	domain.VersionGroupingCandidate
	stored []byte
}

func readVersionCandidates(ctx context.Context, tx pgx.Tx, query string, args ...any) ([]versionCandidate, error) {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, storageError(err)
	}
	defer rows.Close()
	var out []versionCandidate
	for rows.Next() {
		var c versionCandidate
		var ids string
		if err = rows.Scan(&c.ItemID, &c.Kind, &ids, &c.stored, &c.Season, &c.Episode, &c.EpisodeEnd, &c.NFONumbers, &c.Held, &c.Durations); err != nil {
			return nil, storageError(err)
		}
		c.ExternalIDs = []byte(ids)
		if c.Kind != "Episode" {
			c.Season, c.Episode, c.EpisodeEnd, c.NFONumbers = nil, nil, nil, false
		}
		out = append(out, c)
	}
	return out, storageError(rows.Err())
}

// syncCatalogVersionsBatch judges the next page of items whose grouping
// evidence changed since they were last judged.
func syncCatalogVersionsBatch(ctx context.Context, tx pgx.Tx, current domain.JobLease, r *catalogSyncRequest, events bool) error {
	if err := checkSyncTarget(ctx, tx, *r); err != nil {
		return err
	}
	if !r.partsDone {
		// Parts first: a multi-part version counts as one version with
		// the combined duration when copies are compared below.
		return syncCatalogPartsBatch(ctx, tx, current, r)
	}
	batch, err := readVersionCandidates(ctx, tx, versionCandidateSQL+` AND ($2::uuid IS NULL OR c.item_id>$2::uuid) ORDER BY c.item_id LIMIT $3`, r.library, r.versionsCursor, catalogSyncVersionsLimit)
	if err != nil {
		return err
	}
	absorbed, targets := map[string]bool{}, map[string]bool{}
	processed, merged := 0, 0
	for _, c := range batch {
		if merged >= catalogSyncMergeLimit {
			break
		}
		processed++
		id := c.ItemID
		r.versionsCursor = &id
		if absorbed[c.ItemID] {
			continue
		}
		if targets[c.ItemID] {
			// Gained versions earlier in this batch: judge what it is now.
			fresh, err := readVersionCandidates(ctx, tx, versionCandidateSQL+` AND c.item_id=$2::uuid`, r.library, c.ItemID)
			if err != nil {
				return err
			}
			if len(fresh) == 0 {
				continue
			}
			c = fresh[0]
		}
		evidence := domain.VersionGroupingEvidence(c.VersionGroupingCandidate)
		if bytes.Equal(evidence, c.stored) {
			continue
		}
		gone, kept, err := judgeVersionCandidate(ctx, tx, r.library, c, evidence, events)
		if err != nil {
			return err
		}
		if gone != "" {
			absorbed[gone], targets[kept] = true, true
			merged++
			r.aggregated++
		}
	}
	if processed == len(batch) && len(batch) < catalogSyncVersionsLimit {
		r.phase, r.versionsCursor = "done", nil
	}
	if merged > 0 {
		return auditAccount(ctx, tx, domain.Actor{}, "catalog_sync.batch", current.Job.ID, nil, map[string]any{"libraryId": r.library, "aggregated": merged})
	}
	return nil
}

// judgeVersionCandidate compares one item with the items sharing one of its
// strong provider IDs and merges it with the first one the evidence
// supports. It returns the absorbed and the kept item, or empty values when
// nothing merged.
func judgeVersionCandidate(ctx context.Context, tx pgx.Tx, library string, c versionCandidate, evidence []byte, events bool) (string, string, error) {
	record := func(evidence []byte, reason string) error {
		_, err := tx.Exec(ctx, `UPDATE catalog_scan_items SET grouping_evidence=$2,grouping_reason=$3 WHERE item_id=$1::uuid`, c.ItemID, evidence, reason)
		return storageError(err)
	}
	if c.Held != "" {
		return "", "", record(evidence, c.Held)
	}
	var peers []string
	for _, probe := range domain.VersionGroupingProbes(c.ExternalIDs) {
		rows, err := tx.Query(ctx, `SELECT f.item_id::text FROM item_metadata_facts f JOIN catalog_scan_items o ON o.item_id=f.item_id
 WHERE f.field='uniqueIds' AND f.source='nfo' AND f.value @> $1::jsonb AND o.library_id=$2::uuid AND o.kind=$3 AND f.item_id<>$4::uuid
 ORDER BY f.item_id LIMIT $5`, probe, library, c.Kind, c.ItemID, catalogSyncPeerLimit)
		if err != nil {
			return "", "", storageError(err)
		}
		found, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return "", "", storageError(err)
		}
		for _, id := range found {
			if !slices.Contains(peers, id) {
				peers = append(peers, id)
			}
		}
	}
	if len(peers) == 0 {
		return "", "", record(evidence, domain.VersionGroupingAlone)
	}
	candidates, err := readVersionCandidates(ctx, tx, versionCandidateSQL+` AND c.item_id=ANY($2::uuid[]) ORDER BY c.item_id`, library, peers)
	if err != nil {
		return "", "", err
	}
	reason := domain.VersionGroupingAlone
	for i, peer := range candidates {
		decision := domain.DecideVersionGrouping(c.VersionGroupingCandidate, peer.VersionGroupingCandidate)
		if !decision.Merge {
			if i == 0 {
				reason = decision.Reason
			}
			continue
		}
		gone, kept, outcome, err := mergeVersionCandidates(ctx, tx, c.VersionGroupingCandidate, peer.VersionGroupingCandidate, decision, events)
		if err != nil {
			return "", "", err
		}
		switch outcome {
		case "":
			return gone, kept, nil
		case domain.VersionGroupingBusy:
			// Played, probed, written or shared right now: judged again on
			// the next run, so no evidence is stored.
			return "", "", record(nil, outcome)
		default:
			reason = outcome
		}
	}
	return "", "", record(evidence, reason)
}

// mergeVersionCandidates folds the item with fewer versions (on a tie the
// greater ID) into the other in a savepoint. It returns the absorbed and the
// kept item, or an outcome when the merge boundaries refused: "busy" for a passing
// state, domain.VersionGroupingIncompatible otherwise.
func mergeVersionCandidates(ctx context.Context, tx pgx.Tx, a, b domain.VersionGroupingCandidate, decision domain.VersionGroupingDecision, events bool) (string, string, string, error) {
	target, source := a, b
	if len(b.Durations) > len(a.Durations) || len(b.Durations) == len(a.Durations) && b.ItemID < a.ItemID {
		target, source = b, a
	}
	savepoint, err := tx.Begin(ctx)
	if err != nil {
		return "", "", "", storageError(err)
	}
	rows, err := savepoint.Query(ctx, `SELECT id::text,library_id::text,kind,title FROM items WHERE id=ANY($1::uuid[]) ORDER BY id FOR UPDATE`, []string{target.ItemID, source.ItemID})
	if err != nil {
		_ = savepoint.Rollback(ctx)
		return "", "", "", storageError(err)
	}
	locked := map[string]versionItem{}
	for rows.Next() {
		var it versionItem
		if err = rows.Scan(&it.id, &it.library, &it.kind, &it.title); err != nil {
			rows.Close()
			_ = savepoint.Rollback(ctx)
			return "", "", "", storageError(err)
		}
		locked[it.id] = it
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		_ = savepoint.Rollback(ctx)
		return "", "", "", storageError(err)
	}
	if len(locked) != 2 {
		_ = savepoint.Rollback(ctx)
		return "", "", "", domain.ErrConflict
	}
	_, err = mergeVersionItems(ctx, savepoint, domain.Actor{}, locked[target.ItemID], locked[source.ItemID], events,
		map[string]any{"automatic": true, "evidence": decision.Evidence})
	outcome := ""
	switch {
	case errors.Is(err, domain.ErrVersionItemBusy):
		outcome = domain.VersionGroupingBusy
	case errors.Is(err, domain.ErrVersionIdentityConflict), errors.Is(err, domain.ErrVersionMergeIncompatible):
		outcome = domain.VersionGroupingIncompatible
	case err != nil:
		_ = savepoint.Rollback(ctx)
		return "", "", "", err
	}
	if outcome != "" {
		if err = savepoint.Rollback(ctx); err != nil {
			return "", "", "", storageError(err)
		}
		return "", "", outcome, nil
	}
	if err = savepoint.Commit(ctx); err != nil {
		return "", "", "", storageError(err)
	}
	// The target's versions changed, so its evidence did: it is judged
	// again, with the remaining copies, on this or the next run.
	_, err = tx.Exec(ctx, `UPDATE catalog_scan_items SET grouping_evidence=NULL,grouping_reason=$2 WHERE item_id=$1::uuid`, target.ItemID, domain.VersionGroupingMerged)
	return source.ItemID, target.ItemID, "", storageError(err)
}
