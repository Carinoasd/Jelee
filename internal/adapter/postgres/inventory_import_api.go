package postgres

import (
	"context"
	"errors"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

func (s *Store) ResolveAuthorizedInventoryImport(ctx context.Context, actor domain.Actor, job, entry string) (domain.InventoryImportSource, error) {
	if ctx == nil || !domain.ValidID(job) || !domain.ValidID(entry) {
		return domain.InventoryImportSource{}, domain.ErrInvalid
	}
	tx, err := s.authorizedJobs(ctx, actor)
	if err != nil {
		return domain.InventoryImportSource{}, err
	}
	defer tx.Rollback(ctx)
	source, err := readInventoryImport(ctx, tx, job, entry)
	if err != nil {
		return domain.InventoryImportSource{}, err
	}
	if err = probeAdminStillLive(ctx, tx, actor); err != nil {
		return domain.InventoryImportSource{}, err
	}
	return source, storageError(tx.Commit(ctx))
}

func (s *Store) PutInventoryVideo(ctx context.Context, actor domain.Actor, expected domain.InventoryImportSource, input domain.InventoryImportInput) (domain.InventoryImportResult, error) {
	if ctx == nil || !domain.ValidID(expected.JobID) || !domain.ValidID(expected.EntryID) || !domain.ValidInventoryImportInput(input) {
		return domain.InventoryImportResult{}, domain.ErrInvalid
	}
	tx, err := s.authorizedJobs(ctx, actor)
	if err != nil {
		return domain.InventoryImportResult{}, err
	}
	defer tx.Rollback(ctx)
	current, err := readInventoryImport(ctx, tx, expected.JobID, expected.EntryID)
	if err != nil {
		return domain.InventoryImportResult{}, err
	}
	if current != expected {
		return domain.InventoryImportResult{}, domain.ErrConflict
	}
	var result domain.InventoryImportResult
	var existing domain.InventoryImportInput
	err = tx.QueryRow(ctx, `SELECT i.id::text,s.id::text,i.title,i.kind,COALESCE(p.parent_id::text,'') FROM media_sources s JOIN items i ON i.id=s.item_id AND i.library_id=s.library_id LEFT JOIN item_parent_links p ON p.item_id=i.id WHERE s.root_id=$1::uuid AND s.relative_path=$2 FOR UPDATE OF i,s`, current.RootID, current.Path).Scan(&result.ItemID, &result.SourceID, &existing.Title, &existing.Kind, &existing.ParentID)
	if err == nil {
		if existing != input {
			return domain.InventoryImportResult{}, domain.ErrConflict
		}
	} else if errors.Is(err, pgx.ErrNoRows) {
		result, err = writeInventoryImport(ctx, tx, current, input, actor)
		if err != nil {
			return domain.InventoryImportResult{}, err
		}
	} else {
		return domain.InventoryImportResult{}, storageError(err)
	}
	if err = probeAdminStillLive(ctx, tx, actor); err != nil {
		return domain.InventoryImportResult{}, err
	}
	return result, storageError(tx.Commit(ctx))
}
