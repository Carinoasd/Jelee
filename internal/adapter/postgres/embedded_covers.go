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

var _ app.EmbeddedCoverRepository = (*Store)(nil)

// embeddedCoverBlockedSlot is true when the item's Primary slot already has
// a locked row or a usable row of a higher G40.10 priority than embedded
// (local, NFO, or a fetched remote image).
const embeddedCoverBlockedSlot = `EXISTS(SELECT 1 FROM item_images g WHERE g.item_id=i.id AND g.library_id=i.library_id
 AND g.image_type='Primary' AND g.image_index=0 AND (g.locked OR (g.source_kind IN ('local','nfo','remote')
 AND (g.root_id IS NOT NULL OR g.content_sha256 IS NOT NULL))))`

// NextEmbeddedCoverCandidates pages, under a catalog sync lease, the items
// of the job's library whose single media file has a ready probe result with
// an attached picture, whose Primary slot is not blocked, and whose last
// remembered attempt does not match the probed size, mtime and fingerprint.
// Rows whose metadata selects no usable stream are skipped but still advance
// the page cursor; Next is empty once fewer than limit rows were scanned.
func (s *Store) NextEmbeddedCoverCandidates(ctx context.Context, l domain.JobLease, after string, limit int) (domain.EmbeddedCoverPage, error) {
	if ctx == nil || !validLease(l) || after != "" && !domain.ValidID(after) || limit < 1 || limit > domain.EmbeddedCoverBatch {
		return domain.EmbeddedCoverPage{}, domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return domain.EmbeddedCoverPage{}, err
	}
	defer tx.Rollback(ctx)
	_, r, err := catalogSyncLease(ctx, tx, l)
	if err != nil {
		return domain.EmbeddedCoverPage{}, err
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
  AND NOT `+embeddedCoverBlockedSlot+`
  AND NOT EXISTS(SELECT 1 FROM item_embedded_cover_attempts a WHERE a.item_id=i.id AND a.root_id=m.root_id
   AND a.relative_path=m.relative_path AND a.source_size=c.size AND a.source_mtime_unix_nano=c.modified_unix_nano AND a.fingerprint=c.fingerprint)
 ORDER BY i.id LIMIT $3`, r.library, cursor, limit)
	if err != nil {
		return domain.EmbeddedCoverPage{}, storageError(err)
	}
	page := domain.EmbeddedCoverPage{Candidates: make([]domain.EmbeddedCoverCandidate, 0, limit)}
	scanned, last := 0, ""
	for rows.Next() {
		var v domain.EmbeddedCoverCandidate
		var fingerprint []byte
		var metadata string
		if err = rows.Scan(&v.ItemID, &v.RootID, &v.RootPath, &v.RelativePath, &v.Stamp.Size, &v.Stamp.ModifiedUnixNano, &fingerprint, &metadata); err != nil {
			rows.Close()
			return domain.EmbeddedCoverPage{}, storageError(err)
		}
		scanned, last = scanned+1, v.ItemID
		var meta domain.MediaMetadata
		if json.Unmarshal([]byte(metadata), &meta) != nil {
			continue
		}
		stream, video, ok := domain.EmbeddedCoverStream(meta)
		if !ok {
			continue
		}
		v.LibraryID, v.StreamIndex, v.VideoIndex = r.library, stream, video
		v.Stamp.Fingerprint, v.Stamp.FingerprintVersion = hex.EncodeToString(fingerprint), domain.ProbeFingerprintVersion
		if domain.ValidEmbeddedCoverCandidate(v) {
			page.Candidates = append(page.Candidates, v)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return domain.EmbeddedCoverPage{}, storageError(err)
	}
	if scanned == limit {
		page.Next = last
	}
	return page, storageError(tx.Commit(ctx))
}

// RecordEmbeddedCover applies one attempt under the catalog sync lease. It
// first re-reads, under the item lock, that the item still has exactly this
// media file and that the probe cache still holds the same stamp; otherwise
// nothing is written and the outcome is EmbeddedCoverChanged. A locked or
// higher-priority Primary row wins (skipped_locked / skipped_priority). A
// stored picture becomes the item's 'embedded' Primary row; a deterministic
// refusal is remembered without a row.
func (s *Store) RecordEmbeddedCover(ctx context.Context, l domain.JobLease, result domain.EmbeddedCoverResult) (string, error) {
	if ctx == nil || !validLease(l) || !domain.ValidEmbeddedCoverResult(result) {
		return "", domain.ErrInvalid
	}
	c := result.Candidate
	fingerprint, ok := domain.EmbeddedCoverFingerprint(c.Stamp)
	if !ok {
		return "", domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	current, r, err := catalogSyncLease(ctx, tx, l)
	if err != nil {
		return "", err
	}
	if r.library != c.LibraryID {
		return "", domain.ErrInvalid
	}
	if err := lockImageItem(ctx, tx, c.ItemID); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return s.finishEmbeddedCover(ctx, tx, current, l, domain.EmbeddedCoverChanged)
		}
		return "", err
	}
	var same, blocked, locked bool
	err = tx.QueryRow(ctx, `SELECT
 EXISTS(SELECT 1 FROM items i JOIN media_sources m ON m.item_id=i.id AND m.library_id=i.library_id
  JOIN probe_cache c ON c.root_id=m.root_id AND c.relative_path=m.relative_path AND c.state='ready'
  WHERE i.id=$1::uuid AND i.library_id=$2::uuid AND m.root_id=$3::uuid AND m.relative_path=$4
   AND c.size=$5 AND c.modified_unix_nano=$6 AND c.fingerprint=$7
   AND NOT EXISTS(SELECT 1 FROM media_sources o WHERE o.item_id=i.id AND o.id<>m.id)),
 EXISTS(SELECT 1 FROM items i WHERE i.id=$1::uuid AND `+embeddedCoverBlockedSlot+`),
 EXISTS(SELECT 1 FROM item_images g WHERE g.item_id=$1::uuid AND g.image_type='Primary' AND g.image_index=0 AND g.locked)`,
		c.ItemID, c.LibraryID, c.RootID, c.RelativePath, c.Stamp.Size, c.Stamp.ModifiedUnixNano, fingerprint).Scan(&same, &blocked, &locked)
	if err != nil {
		return "", storageError(err)
	}
	switch {
	case !same:
		return s.finishEmbeddedCover(ctx, tx, current, l, domain.EmbeddedCoverChanged)
	case locked:
		return s.finishEmbeddedCover(ctx, tx, current, l, domain.EmbeddedCoverSkippedLocked)
	case blocked:
		return s.finishEmbeddedCover(ctx, tx, current, l, domain.EmbeddedCoverSkippedPriority)
	}
	var digest []byte
	if result.Outcome == domain.EmbeddedCoverStored {
		size, mtime := c.Stamp.Size, c.Stamp.ModifiedUnixNano
		upsert, err := upsertItemImage(ctx, tx, domain.Actor{}, domain.ItemImageInput{ItemID: c.ItemID, Type: "Primary", SourceKind: domain.ImageSourceEmbedded,
			RootID: c.RootID, RelativePath: c.RelativePath, SourceModifiedUnixNano: &mtime, SourceSize: &size, Content: result.Content})
		if err != nil {
			return "", err
		}
		if upsert.Skipped {
			return s.finishEmbeddedCover(ctx, tx, current, l, domain.EmbeddedCoverSkippedLocked)
		}
		if upsert.Image.Content == nil || !bytes.Equal(upsert.Image.Content.SHA256, result.Content.SHA256) {
			return "", domain.ErrDatabase
		}
		digest = result.Content.SHA256
	}
	_, err = tx.Exec(ctx, `INSERT INTO item_embedded_cover_attempts(item_id,library_id,root_id,relative_path,source_size,source_mtime_unix_nano,fingerprint,outcome,content_sha256)
 VALUES($1::uuid,$2::uuid,$3::uuid,$4,$5,$6,$7,$8,$9)
 ON CONFLICT(item_id) DO UPDATE SET library_id=EXCLUDED.library_id,root_id=EXCLUDED.root_id,relative_path=EXCLUDED.relative_path,
 source_size=EXCLUDED.source_size,source_mtime_unix_nano=EXCLUDED.source_mtime_unix_nano,fingerprint=EXCLUDED.fingerprint,
 outcome=EXCLUDED.outcome,content_sha256=EXCLUDED.content_sha256,attempted_at=clock_timestamp()`,
		c.ItemID, c.LibraryID, c.RootID, c.RelativePath, c.Stamp.Size, c.Stamp.ModifiedUnixNano, fingerprint, result.Outcome, digest)
	if err != nil {
		return "", storageError(err)
	}
	return s.finishEmbeddedCover(ctx, tx, current, l, result.Outcome)
}

func (s *Store) finishEmbeddedCover(ctx context.Context, tx pgx.Tx, current, l domain.JobLease, outcome string) (string, error) {
	if err := guardedJobUpdate(ctx, tx, current, `UPDATE jobs SET files=files WHERE id=$1::uuid`, l.Job.ID); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", storageError(err)
	}
	return outcome, nil
}
