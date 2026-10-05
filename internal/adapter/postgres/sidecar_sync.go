package postgres

import (
	"context"
	"path"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

var _ app.SidecarInspectionRepository = (*Store)(nil)

// sidecarBatch holds the sidecar view of one catalog sync sources batch: the
// tracks each baseline video should have and the tracks its scan source has.
// Both are read with one statement each for the whole batch.
type sidecarBatch struct {
	wanted   map[sidecarKey][]domain.SidecarTrackInput // keyed by video root and path
	existing map[string][]domain.SidecarTrackRecord    // keyed by source ID
}

// readSidecarBatch pairs the batch's videos with the sidecar files of the
// same baseline. Only files observed by the scan that published the target
// baseline count, so a file an ignore rule excluded (its row is kept with
// its old observation) never becomes a track.
func readSidecarBatch(ctx context.Context, tx pgx.Tx, r catalogSyncRequest, candidates []syncCandidate) (sidecarBatch, error) {
	batch := sidecarBatch{wanted: map[sidecarKey][]domain.SidecarTrackInput{}, existing: map[string][]domain.SidecarTrackRecord{}}
	if len(candidates) == 0 {
		return batch, nil
	}
	type owner struct{ root, dir string }
	seen := map[owner]bool{}
	var roots, owners []string
	sources := make([]string, 0, len(candidates))
	for _, c := range candidates {
		for _, dir := range domain.SidecarLookupDirectories(path.Dir(c.path)) {
			key := owner{c.root, dir}
			if !seen[key] {
				seen[key] = true
				roots, owners = append(roots, c.root), append(owners, dir)
			}
		}
		if c.trackedSource != nil {
			sources = append(sources, *c.trackedSource)
		}
	}
	rows, err := tx.Query(ctx, `SELECT b.root_id::text,b.path,b.kind,b.size,b.modified_unix_nano
 FROM unnest($3::uuid[],$4::text[]) AS o(root_id,owner)
 JOIN library_inventory_baseline_data b ON b.library_id=$1::uuid AND b.snapshot_id=$2 AND b.root_id=o.root_id
  AND inventory_sidecar_owner(b.path)=o.owner AND b.kind IN ('video','other')
 WHERE b.attributes_known AND b.inventory_generation=$5 AND (b.kind='video' OR b.observed_revision=$6)`,
		r.library, *r.targetSnapshot, roots, owners, r.epoch, *r.targetRevision)
	if err != nil {
		return batch, storageError(err)
	}
	files := map[string][]domain.SidecarScanFile{}
	for rows.Next() {
		var root string
		var f domain.SidecarScanFile
		if err = rows.Scan(&root, &f.Path, &f.Kind, &f.Size, &f.ModifiedUnixNano); err != nil {
			rows.Close()
			return batch, storageError(err)
		}
		files[root] = append(files[root], f)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return batch, storageError(err)
	}
	paired := map[owner]map[string][]domain.SidecarScanMatch{}
	for _, c := range candidates {
		key := owner{c.root, path.Dir(c.path)}
		matches, done := paired[key]
		if !done {
			matches = domain.PairSidecarFiles(key.dir, files[c.root])
			paired[key] = matches
		}
		var inputs []domain.SidecarTrackInput
		for _, m := range matches[c.path] {
			in := domain.SidecarTrackInput{Track: m.Track, RootID: c.root, RelativePath: m.File.Path, Size: m.File.Size, ModifiedUnixNano: m.File.ModifiedUnixNano}
			in.Track.Subdir = ""
			// A name the table cannot hold (an over-long title, say) is
			// skipped alone instead of failing the whole source.
			if domain.ValidSidecarTrackInput(in) {
				inputs = append(inputs, in)
			}
		}
		batch.wanted[sidecarKey{c.root, c.path}] = inputs
	}
	if len(sources) == 0 {
		return batch, nil
	}
	rows, err = tx.Query(ctx, `SELECT `+sidecarColumns+` FROM media_sidecar_tracks t WHERE t.source_id=ANY($1::uuid[])`, sources)
	if err != nil {
		return batch, storageError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var row sidecarRow
		if err = rows.Scan(row.targets()...); err != nil {
			return batch, storageError(err)
		}
		value, err := row.value()
		if err != nil {
			return batch, err
		}
		batch.existing[value.SourceID] = append(batch.existing[value.SourceID], value)
	}
	return batch, storageError(rows.Err())
}

// syncSourceSidecars brings one scan source's tracks in line with the files
// paired with its video. Charset and fingerprint of a file whose stamp did
// not change are carried over, so an unchanged directory compares equal and
// writes nothing. It reports whether anything was written.
func syncSourceSidecars(ctx context.Context, tx pgx.Tx, source string, wanted []domain.SidecarTrackInput, existing []domain.SidecarTrackRecord) (bool, error) {
	old := make(map[sidecarKey]domain.SidecarTrackRecord, len(existing))
	for _, record := range existing {
		old[sidecarKey{record.RootID, record.RelativePath}] = record
	}
	same := len(wanted) == len(existing)
	for i, in := range wanted {
		record, found := old[sidecarKey{in.RootID, in.RelativePath}]
		if found && record.Size == in.Size && record.ModifiedUnixNano == in.ModifiedUnixNano && record.Track.Kind == in.Track.Kind && record.Track.Format == in.Track.Format {
			wanted[i].Charset, wanted[i].Fingerprint = record.Charset, record.Fingerprint
		}
		if !found || !sameSidecarTrack(record, wanted[i]) {
			same = false
		}
	}
	if same {
		return false, nil
	}
	changes, err := UpsertSidecarTracks(ctx, tx, source, wanted)
	if err != nil {
		return false, err
	}
	return changes.Total() > 0, nil
}

// NextSidecarInspections returns up to limit tracks of the catalog sync's
// library, after the given track ID, whose charset and fingerprint have not
// been read yet. The lease is fenced like every other sync step.
func (s *Store) NextSidecarInspections(ctx context.Context, l domain.JobLease, after string, limit int) ([]domain.SidecarInspectionTarget, error) {
	if ctx == nil || !validLease(l) || after != "" && !domain.ValidID(after) || limit < 1 || limit > domain.SidecarInspectionBatch {
		return nil, domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	_, r, err := catalogSyncLease(ctx, tx, l)
	if err != nil {
		return nil, err
	}
	var cursor *string
	if after != "" {
		cursor = &after
	}
	rows, err := tx.Query(ctx, `SELECT t.id::text,t.kind,t.format,lr.path,t.relative_path,t.size,t.modified_unix_nano
 FROM media_sidecar_tracks t JOIN library_roots lr ON lr.id=t.root_id AND lr.library_id=t.library_id
 WHERE t.library_id=$1::uuid AND t.fingerprint IS NULL AND ($2::uuid IS NULL OR t.id>$2::uuid)
 ORDER BY t.id LIMIT $3`, r.library, cursor, limit)
	if err != nil {
		return nil, storageError(err)
	}
	targets := make([]domain.SidecarInspectionTarget, 0, limit)
	for rows.Next() {
		var v domain.SidecarInspectionTarget
		if err = rows.Scan(&v.ID, &v.Kind, &v.Format, &v.RootPath, &v.RelativePath, &v.Size, &v.ModifiedUnixNano); err != nil {
			rows.Close()
			return nil, storageError(err)
		}
		targets = append(targets, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, storageError(err)
	}
	return targets, storageError(tx.Commit(ctx))
}

// RecordSidecarInspections stores bounded-read results in one statement. A
// row is only filled while its stamp still equals the stamp that was read and
// it has no fingerprint yet; a later scan replaces changed files. It returns
// how many rows were filled.
func (s *Store) RecordSidecarInspections(ctx context.Context, l domain.JobLease, results []domain.SidecarInspection) (int, error) {
	if ctx == nil || !validLease(l) || len(results) > domain.SidecarInspectionBatch {
		return 0, domain.ErrInvalid
	}
	ids, sizes, stamps := make([]string, 0, len(results)), make([]int64, 0, len(results)), make([]int64, 0, len(results))
	charsets, fingerprints := make([]string, 0, len(results)), make([][]byte, 0, len(results))
	for _, v := range results {
		if !domain.ValidSidecarInspection(v) {
			return 0, domain.ErrInvalid
		}
		ids, sizes, stamps = append(ids, v.ID), append(sizes, v.Size), append(stamps, v.ModifiedUnixNano)
		charsets, fingerprints = append(charsets, v.Charset), append(fingerprints, v.Fingerprint)
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	current, r, err := catalogSyncLease(ctx, tx, l)
	if err != nil {
		return 0, err
	}
	filled := int64(0)
	if len(results) > 0 {
		tag, err := tx.Exec(ctx, `UPDATE media_sidecar_tracks t SET charset=CASE WHEN t.kind='subtitle' THEN NULLIF(v.charset,'') END,
 fingerprint=v.fingerprint,updated_at=greatest(clock_timestamp(),t.created_at)
 FROM unnest($2::uuid[],$3::bigint[],$4::bigint[],$5::text[],$6::bytea[]) AS v(id,size,stamp,charset,fingerprint)
 WHERE t.id=v.id AND t.library_id=$1::uuid AND t.size=v.size AND t.modified_unix_nano=v.stamp AND t.fingerprint IS NULL`,
			r.library, ids, sizes, stamps, charsets, fingerprints)
		if err != nil {
			return 0, storageError(err)
		}
		filled = tag.RowsAffected()
	}
	if err = guardedJobUpdate(ctx, tx, current, `UPDATE jobs SET files=files WHERE id=$1::uuid`, l.Job.ID); err != nil {
		return 0, err
	}
	return int(filled), storageError(tx.Commit(ctx))
}
