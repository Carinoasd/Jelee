package postgres

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

var _ app.ChapterImageRepository = (*Store)(nil)

// NextChapterImageCandidates pages, under a catalog sync lease, the items of
// the job's library whose single media file has a ready probe result with
// chapters and an attached picture stream, and whose last remembered chapter
// image attempt does not match the probed size, mtime and fingerprint. Rows
// whose metadata selects no usable stream are skipped but still advance the
// page cursor; Next is empty once fewer than limit rows were scanned.
func (s *Store) NextChapterImageCandidates(ctx context.Context, l domain.JobLease, after string, limit int) (domain.ChapterImagePage, error) {
	if ctx == nil || !validLease(l) || after != "" && !domain.ValidID(after) || limit < 1 || limit > domain.ChapterImageBatch {
		return domain.ChapterImagePage{}, domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return domain.ChapterImagePage{}, err
	}
	defer tx.Rollback(ctx)
	_, r, err := catalogSyncLease(ctx, tx, l)
	if err != nil {
		return domain.ChapterImagePage{}, err
	}
	var cursor *string
	if after != "" {
		cursor = &after
	}
	rows, err := tx.Query(ctx, `SELECT i.id::text,m.root_id::text,lr.path,m.relative_path,c.size,c.modified_unix_nano,c.fingerprint,c.metadata::text
 FROM items i
 JOIN media_sources m ON m.item_id=i.id AND m.library_id=i.library_id
 JOIN library_roots lr ON lr.id=m.root_id AND lr.library_id=m.library_id
 JOIN probe_cache c ON c.root_id=m.root_id AND c.relative_path=m.relative_path AND c.library_id=i.library_id AND c.state='ready'
 WHERE i.library_id=$1::uuid AND ($2::uuid IS NULL OR i.id>$2::uuid)
  AND NOT EXISTS(SELECT 1 FROM media_sources o WHERE o.item_id=i.id AND o.id<>m.id)
  AND c.metadata @> '{"streams":[{"attachedPic":true}]}'::jsonb
  AND jsonb_typeof(c.metadata->'chapters')='array' AND jsonb_array_length(c.metadata->'chapters')>0
  AND NOT EXISTS(SELECT 1 FROM item_chapter_image_attempts a WHERE a.item_id=i.id AND a.root_id=m.root_id
   AND a.relative_path=m.relative_path AND a.source_size=c.size AND a.source_mtime_unix_nano=c.modified_unix_nano AND a.fingerprint=c.fingerprint)
 ORDER BY i.id LIMIT $3`, r.library, cursor, limit)
	if err != nil {
		return domain.ChapterImagePage{}, storageError(err)
	}
	page := domain.ChapterImagePage{Candidates: make([]domain.ChapterImageCandidate, 0, limit)}
	scanned, last := 0, ""
	for rows.Next() {
		var v domain.ChapterImageCandidate
		var fingerprint []byte
		var metadata string
		if err = rows.Scan(&v.ItemID, &v.RootID, &v.RootPath, &v.RelativePath, &v.Stamp.Size, &v.Stamp.ModifiedUnixNano, &fingerprint, &metadata); err != nil {
			rows.Close()
			return domain.ChapterImagePage{}, storageError(err)
		}
		scanned, last = scanned+1, v.ItemID
		var meta domain.MediaMetadata
		if json.Unmarshal([]byte(metadata), &meta) != nil {
			continue
		}
		v.Streams = domain.ChapterImageStreams(meta)
		v.LibraryID = r.library
		v.Stamp.Fingerprint, v.Stamp.FingerprintVersion = hex.EncodeToString(fingerprint), domain.ProbeFingerprintVersion
		if domain.ValidChapterImageCandidate(v) {
			page.Candidates = append(page.Candidates, v)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return domain.ChapterImagePage{}, storageError(err)
	}
	if scanned == limit {
		page.Next = last
	}
	return page, storageError(tx.Commit(ctx))
}

// chapterImageBlockedSlot is true when one Chapter slot of the item already
// has a locked row or a usable row of a higher G40.10 priority than embedded.
const chapterImageBlockedSlot = `EXISTS(SELECT 1 FROM item_images g WHERE g.item_id=$1::uuid AND g.image_type='Chapter' AND g.image_index=$2
 AND (g.locked OR (g.source_kind IN ('local','nfo','remote') AND (g.root_id IS NOT NULL OR g.content_sha256 IS NOT NULL))))`

// RecordChapterImages applies one attempt under the catalog sync lease. It
// first re-reads, under the item lock, that the item still has exactly this
// media file and that the probe cache still holds the same stamp; otherwise
// nothing is written and the outcome is ChapterImageChanged. Each stored
// picture becomes the 'embedded' row of its Chapter slot unless the slot is
// locked, manual or filled by a higher-priority source (Skipped). Every
// other unlocked, non-manual embedded Chapter row of the item is removed
// (Removed): it holds a picture this file no longer provides for that slot,
// or one a higher-priority source now replaces; a refusal removes them all. Outcomes other than changed
// are remembered for the file's fingerprint.
func (s *Store) RecordChapterImages(ctx context.Context, l domain.JobLease, result domain.ChapterImageResult) (domain.ChapterImageRecord, error) {
	if ctx == nil || !validLease(l) || !domain.ValidChapterImageResult(result) {
		return domain.ChapterImageRecord{}, domain.ErrInvalid
	}
	c := result.Candidate
	fingerprint, ok := domain.EmbeddedCoverFingerprint(c.Stamp)
	if !ok {
		return domain.ChapterImageRecord{}, domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return domain.ChapterImageRecord{}, err
	}
	defer tx.Rollback(ctx)
	current, r, err := catalogSyncLease(ctx, tx, l)
	if err != nil {
		return domain.ChapterImageRecord{}, err
	}
	if r.library != c.LibraryID {
		return domain.ChapterImageRecord{}, domain.ErrInvalid
	}
	changed := domain.ChapterImageRecord{Outcome: domain.ChapterImageChanged}
	if err := lockImageItem(ctx, tx, c.ItemID); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return s.finishChapterImages(ctx, tx, current, l, changed)
		}
		return domain.ChapterImageRecord{}, err
	}
	var same bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM items i JOIN media_sources m ON m.item_id=i.id AND m.library_id=i.library_id
  JOIN probe_cache c ON c.root_id=m.root_id AND c.relative_path=m.relative_path AND c.state='ready'
  WHERE i.id=$1::uuid AND i.library_id=$2::uuid AND m.root_id=$3::uuid AND m.relative_path=$4
   AND c.size=$5 AND c.modified_unix_nano=$6 AND c.fingerprint=$7
   AND NOT EXISTS(SELECT 1 FROM media_sources o WHERE o.item_id=i.id AND o.id<>m.id))`,
		c.ItemID, c.LibraryID, c.RootID, c.RelativePath, c.Stamp.Size, c.Stamp.ModifiedUnixNano, fingerprint).Scan(&same)
	if err != nil {
		return domain.ChapterImageRecord{}, storageError(err)
	}
	if !same {
		return s.finishChapterImages(ctx, tx, current, l, changed)
	}
	record := domain.ChapterImageRecord{Outcome: result.Outcome}
	produced := make([]int, 0, len(result.Images))
	size, mtime := c.Stamp.Size, c.Stamp.ModifiedUnixNano
	for _, image := range result.Images {
		var blocked bool
		if err := tx.QueryRow(ctx, `SELECT `+chapterImageBlockedSlot, c.ItemID, image.Index).Scan(&blocked); err != nil {
			return domain.ChapterImageRecord{}, storageError(err)
		}
		if blocked {
			record.Skipped++
			continue
		}
		content := image.Content
		upsert, err := upsertItemImage(ctx, tx, domain.Actor{}, domain.ItemImageInput{ItemID: c.ItemID, Type: "Chapter", Index: image.Index, SourceKind: domain.ImageSourceEmbedded,
			RootID: c.RootID, RelativePath: c.RelativePath, SourceModifiedUnixNano: &mtime, SourceSize: &size, Content: &content})
		if err != nil {
			return domain.ChapterImageRecord{}, err
		}
		if upsert.Skipped {
			record.Skipped++
			continue
		}
		if upsert.Image.Content == nil || !bytes.Equal(upsert.Image.Content.SHA256, content.SHA256) {
			return domain.ChapterImageRecord{}, domain.ErrDatabase
		}
		record.Stored++
		produced = append(produced, image.Index)
	}
	tag, err := tx.Exec(ctx, `DELETE FROM item_images WHERE item_id=$1::uuid AND image_type='Chapter' AND source_kind='embedded'
 AND NOT locked AND NOT manual AND NOT (image_index=ANY($2::integer[]))`, c.ItemID, produced)
	if err != nil {
		return domain.ChapterImageRecord{}, storageError(err)
	}
	record.Removed = int(tag.RowsAffected())
	_, err = tx.Exec(ctx, `INSERT INTO item_chapter_image_attempts(item_id,library_id,root_id,relative_path,source_size,source_mtime_unix_nano,fingerprint,outcome,image_count)
 VALUES($1::uuid,$2::uuid,$3::uuid,$4,$5,$6,$7,$8,$9)
 ON CONFLICT(item_id) DO UPDATE SET library_id=EXCLUDED.library_id,root_id=EXCLUDED.root_id,relative_path=EXCLUDED.relative_path,
 source_size=EXCLUDED.source_size,source_mtime_unix_nano=EXCLUDED.source_mtime_unix_nano,fingerprint=EXCLUDED.fingerprint,
 outcome=EXCLUDED.outcome,image_count=EXCLUDED.image_count,attempted_at=clock_timestamp()`,
		c.ItemID, c.LibraryID, c.RootID, c.RelativePath, c.Stamp.Size, c.Stamp.ModifiedUnixNano, fingerprint, result.Outcome, len(result.Images))
	if err != nil {
		return domain.ChapterImageRecord{}, storageError(err)
	}
	return s.finishChapterImages(ctx, tx, current, l, record)
}

func (s *Store) finishChapterImages(ctx context.Context, tx pgx.Tx, current, l domain.JobLease, record domain.ChapterImageRecord) (domain.ChapterImageRecord, error) {
	if err := guardedJobUpdate(ctx, tx, current, `UPDATE jobs SET files=files WHERE id=$1::uuid`, l.Job.ID); err != nil {
		return domain.ChapterImageRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ChapterImageRecord{}, storageError(err)
	}
	return record, nil
}
