package postgres

import (
	"context"
	"path/filepath"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

// Local operator methods use the same database authority as ImportVideo.
// They must not be exposed directly as unauthenticated HTTP handlers.
func (s *Store) ResolveInventoryImport(ctx context.Context, job, entry string) (domain.InventoryImportSource, error) {
	if ctx == nil || !domain.ValidID(job) || !domain.ValidID(entry) {
		return domain.InventoryImportSource{}, domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return domain.InventoryImportSource{}, err
	}
	defer tx.Rollback(ctx)
	source, err := readInventoryImport(ctx, tx, job, entry)
	if err != nil {
		return domain.InventoryImportSource{}, err
	}
	return source, storageError(tx.Commit(ctx))
}

func readInventoryImport(ctx context.Context, tx pgx.Tx, job, entry string) (domain.InventoryImportSource, error) {
	var value domain.InventoryImportSource
	err := tx.QueryRow(ctx, `SELECT j.id::text,i.id::text,j.library_id::text,i.root_id::text,r.path,i.path,i.size,i.modified_unix_nano,l.inventory_generation,l.inventory_baseline_revision
 FROM jobs j JOIN job_inventory i ON i.job_id=j.id
 JOIN libraries l ON l.id=j.library_id
 JOIN library_roots r ON r.id=i.root_id AND r.library_id=j.library_id
 JOIN library_inventory_baseline b ON b.library_id=j.library_id AND b.root_id=i.root_id AND b.path=i.path
 WHERE j.id=$1::uuid AND i.id=$2::uuid AND j.kind='inventory_scan' AND j.state='succeeded' AND NOT j.review_required AND NOT j.cancel_requested
 AND j.inventory_generation=l.inventory_generation AND i.kind='video' AND b.attributes_known AND b.kind=i.kind
 AND b.size=i.size AND b.modified_unix_nano=i.modified_unix_nano AND b.inventory_generation=l.inventory_generation
 AND b.observed_revision=l.inventory_baseline_revision
 AND NOT EXISTS(SELECT 1 FROM jobs newer WHERE newer.library_id=j.library_id AND newer.kind='inventory_scan' AND (newer.created_at,newer.id)>(j.created_at,j.id))
 FOR UPDATE OF j,i,l,r,b`, job, entry).Scan(&value.JobID, &value.EntryID, &value.LibraryID, &value.RootID, &value.RootPath, &value.Path, &value.Size, &value.ModifiedUnixNano, &value.Generation, &value.BaselineRevision)
	if err != nil {
		return domain.InventoryImportSource{}, storageError(err)
	}
	if !filepath.IsAbs(value.RootPath) || domain.ImportVideoContentType(value.Path) == "" {
		return domain.InventoryImportSource{}, domain.ErrInvalid
	}
	return value, nil
}

// ImportInventoryVideo rechecks the captured scope after local file validation.
// Duplicate locations fail atomically and do not overwrite existing metadata.
func (s *Store) ImportInventoryVideo(ctx context.Context, expected domain.InventoryImportSource, title, kind, parent string) (string, error) {
	if ctx == nil || !domain.ValidID(expected.JobID) || !domain.ValidID(expected.EntryID) || title == "" || len(title) > 1024 || !domain.ValidVideoItemKind(kind) || parent != "" && (kind != "Episode" || !domain.ValidID(parent)) {
		return "", domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	current, err := readInventoryImport(ctx, tx, expected.JobID, expected.EntryID)
	if err != nil {
		return "", err
	}
	if current != expected {
		return "", domain.ErrConflict
	}
	result, err := writeInventoryImport(ctx, tx, current, domain.InventoryImportInput{Title: title, Kind: kind, ParentID: parent}, domain.Actor{})
	if err != nil {
		return "", err
	}
	return result.SourceID, storageError(tx.Commit(ctx))
}

func writeInventoryImport(ctx context.Context, tx pgx.Tx, current domain.InventoryImportSource, input domain.InventoryImportInput, actor domain.Actor) (domain.InventoryImportResult, error) {
	var result domain.InventoryImportResult
	if err := tx.QueryRow(ctx, `INSERT INTO items(library_id,title,kind) VALUES($1,$2,$3) RETURNING id::text`, current.LibraryID, input.Title, input.Kind).Scan(&result.ItemID); err != nil {
		return domain.InventoryImportResult{}, storageError(err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO media_sources(item_id,library_id,root_id,relative_path,content_type) VALUES($1,$2,$3,$4,$5) RETURNING id::text`, result.ItemID, current.LibraryID, current.RootID, current.Path, domain.ImportVideoContentType(current.Path)).Scan(&result.SourceID); err != nil {
		return domain.InventoryImportResult{}, storageError(err)
	}
	if err := writeImportedParent(ctx, tx, result.ItemID, current.LibraryID, current.RootID, current.Path, input.Kind, input.ParentID); err != nil {
		return domain.InventoryImportResult{}, err
	}
	if err := auditAccount(ctx, tx, actor, "inventory.imported", result.SourceID, nil, map[string]any{"jobId": current.JobID, "entryId": current.EntryID}); err != nil {
		return domain.InventoryImportResult{}, err
	}
	return result, nil
}
