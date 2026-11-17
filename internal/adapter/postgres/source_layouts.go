package postgres

import (
	"context"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

var _ app.SourceLayoutRepository = (*Store)(nil)

// NextSourceLayoutInspections returns up to limit present scan sources of
// the catalog sync's library, after the given source ID, whose container
// layout is missing, was read against another stamp or by another reader
// version. Only inspected containers are listed. The lease is fenced like
// every other sync step.
func (s *Store) NextSourceLayoutInspections(ctx context.Context, l domain.JobLease, after string, limit int) ([]domain.SourceLayoutTarget, error) {
	if ctx == nil || !validLease(l) || after != "" && !domain.ValidID(after) || limit < 1 || limit > domain.SourceLayoutInspectionBatch {
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
	rows, err := tx.Query(ctx, `SELECT m.id::text,m.content_type,lr.path,m.relative_path,c.size,c.modified_unix_nano
 FROM catalog_scan_sources c
 JOIN media_sources m ON m.id=c.source_id AND m.library_id=c.library_id
 JOIN library_roots lr ON lr.id=m.root_id AND lr.library_id=m.library_id
 LEFT JOIN media_source_layouts ml ON ml.source_id=c.source_id
 WHERE c.library_id=$1::uuid AND c.missing_since IS NULL AND m.content_type=ANY($4::text[])
  AND (ml.source_id IS NULL OR ml.size<>c.size OR ml.modified_unix_nano<>c.modified_unix_nano OR ml.reader_version<>$5)
  AND ($2::uuid IS NULL OR c.source_id>$2::uuid)
 ORDER BY c.source_id LIMIT $3`, r.library, cursor, limit, domain.LayoutContentTypes, domain.SourceLayoutVersion)
	if err != nil {
		return nil, storageError(err)
	}
	targets := make([]domain.SourceLayoutTarget, 0, limit)
	for rows.Next() {
		var v domain.SourceLayoutTarget
		if err = rows.Scan(&v.SourceID, &v.ContentType, &v.RootPath, &v.RelativePath, &v.Size, &v.ModifiedUnixNano); err != nil {
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

// RecordSourceLayouts stores bounded-read results in one statement. A result
// is only stored while the source's scan stamp still equals the stamp that
// was read; a later sync reads a changed file again. It returns how many
// rows were written.
func (s *Store) RecordSourceLayouts(ctx context.Context, l domain.JobLease, results []domain.SourceLayoutInspection) (int, error) {
	if ctx == nil || !validLease(l) || len(results) > domain.SourceLayoutInspectionBatch {
		return 0, domain.ErrInvalid
	}
	ids, layouts := make([]string, 0, len(results)), make([]string, 0, len(results))
	sizes, stamps, offsets, lengths := make([]int64, 0, len(results)), make([]int64, 0, len(results)), make([]int64, 0, len(results)), make([]int64, 0, len(results))
	for _, v := range results {
		if !domain.ValidSourceLayoutInspection(v) {
			return 0, domain.ErrInvalid
		}
		ids, layouts = append(ids, v.SourceID), append(layouts, string(v.Layout))
		sizes, stamps = append(sizes, v.Size), append(stamps, v.ModifiedUnixNano)
		offsets, lengths = append(offsets, v.IndexOffset), append(lengths, v.IndexLength)
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
	written := int64(0)
	if len(results) > 0 {
		tag, err := tx.Exec(ctx, `INSERT INTO media_source_layouts(source_id,size,modified_unix_nano,reader_version,layout,index_offset,index_length)
 SELECT v.id,v.size,v.stamp,$8,v.layout,v.index_offset,v.index_length
 FROM unnest($2::uuid[],$3::bigint[],$4::bigint[],$5::text[],$6::bigint[],$7::bigint[]) AS v(id,size,stamp,layout,index_offset,index_length)
 JOIN catalog_scan_sources c ON c.source_id=v.id AND c.library_id=$1::uuid AND c.size=v.size AND c.modified_unix_nano=v.stamp
 ON CONFLICT (source_id) DO UPDATE SET size=EXCLUDED.size,modified_unix_nano=EXCLUDED.modified_unix_nano,reader_version=EXCLUDED.reader_version,
  layout=EXCLUDED.layout,index_offset=EXCLUDED.index_offset,index_length=EXCLUDED.index_length,checked_at=clock_timestamp()`,
			r.library, ids, sizes, stamps, layouts, offsets, lengths, domain.SourceLayoutVersion)
		if err != nil {
			return 0, storageError(err)
		}
		written = tag.RowsAffected()
	}
	if err = guardedJobUpdate(ctx, tx, current, `UPDATE jobs SET files=files WHERE id=$1::uuid`, l.Job.ID); err != nil {
		return 0, err
	}
	return int(written), storageError(tx.Commit(ctx))
}
