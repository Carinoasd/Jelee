package postgres

import (
	"context"
	"path/filepath"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

var _ app.NFOItemScopeRepository = (*Store)(nil)

func readItemNFOScope(ctx context.Context, tx pgx.Tx, item string, expected int64) (domain.NFOItemScope, error) {
	metadata, err := readItemMetadata(ctx, tx, item, true)
	if err != nil {
		return domain.NFOItemScope{}, err
	}
	if metadata.Revision != expected {
		return domain.NFOItemScope{}, domain.ErrConflict
	}
	policy, err := readNFOLibraryPolicy(ctx, tx, metadata.LibraryID)
	if err != nil {
		return domain.NFOItemScope{}, err
	}
	if policy.Mode != domain.NFOModeReadOnly {
		return domain.NFOItemScope{}, domain.ErrMetadataUnavailable
	}
	if metadata.Kind == "Series" || metadata.Kind == "Season" {
		value := domain.NFOItemScope{ItemID: item, LibraryID: metadata.LibraryID, Kind: metadata.Kind, Revision: metadata.Revision, Generation: policy.Generation}
		err := tx.QueryRow(ctx, `SELECT d.id::text,d.root_id::text,d.relative_path,r.path FROM item_directory_sources d JOIN library_roots r ON r.id=d.root_id AND r.library_id=d.library_id WHERE d.item_id=$1::uuid AND d.library_id=$2::uuid AND NOT EXISTS(SELECT 1 FROM media_sources s WHERE s.item_id=d.item_id) FOR UPDATE OF d,r`, item, metadata.LibraryID).Scan(&value.SourceID, &value.RootID, &value.DirectoryPath, &value.Source.RootPath)
		if err == nil {
			value.Source.RelativePath, _ = domain.DirectoryNFOPath(value.DirectoryPath, value.Kind)
			if !filepath.IsAbs(value.Source.RootPath) || !domain.ValidNFOItemScope(value) {
				return domain.NFOItemScope{}, domain.ErrMetadataUnavailable
			}
			return value, nil
		}
		if err != pgx.ErrNoRows {
			return domain.NFOItemScope{}, storageError(err)
		}
		var directoryCount int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM item_directory_sources WHERE item_id=$1::uuid`, item).Scan(&directoryCount); err != nil {
			return domain.NFOItemScope{}, storageError(err)
		}
		if directoryCount != 0 || metadata.Kind == "Season" {
			return domain.NFOItemScope{}, domain.ErrMetadataUnavailable
		}
	}
	// Two rows suffice to detect ambiguity. The foreign keys bind both source
	// and root to this item's library; no path is supplied by the request.
	rows, err := tx.Query(ctx, `SELECT s.id::text,s.root_id::text,s.relative_path,r.path FROM media_sources s JOIN library_roots r ON r.id=s.root_id AND r.library_id=s.library_id WHERE s.item_id=$1::uuid AND s.library_id=$2::uuid ORDER BY s.id LIMIT 2 FOR UPDATE OF s,r`, item, metadata.LibraryID)
	if err != nil {
		return domain.NFOItemScope{}, storageError(err)
	}
	defer rows.Close()
	value := domain.NFOItemScope{ItemID: item, LibraryID: metadata.LibraryID, Kind: metadata.Kind, Revision: metadata.Revision, Generation: policy.Generation}
	count := 0
	for rows.Next() {
		count++
		if err := rows.Scan(&value.SourceID, &value.RootID, &value.MediaPath, &value.Source.RootPath); err != nil {
			return domain.NFOItemScope{}, storageError(err)
		}
	}
	if err := rows.Err(); err != nil {
		return domain.NFOItemScope{}, storageError(err)
	}
	if count != 1 || !filepath.IsAbs(value.Source.RootPath) {
		return domain.NFOItemScope{}, domain.ErrMetadataUnavailable
	}
	value.Source.RelativePath, _ = domain.AdjacentNFOPath(value.MediaPath)
	if !domain.ValidNFOItemScope(value) {
		return domain.NFOItemScope{}, domain.ErrMetadataUnavailable
	}
	return value, nil
}

func (s *Store) ResolveItemNFO(ctx context.Context, actor domain.Actor, item string, expected int64) (domain.NFOItemScope, error) {
	if !domain.ValidID(item) || expected < 1 || expected >= domain.ItemMetadataRevisionMax {
		return domain.NFOItemScope{}, domain.ErrInvalid
	}
	tx, err := s.authorizedJobs(ctx, actor)
	if err != nil {
		return domain.NFOItemScope{}, err
	}
	defer tx.Rollback(ctx)
	value, err := readItemNFOScope(ctx, tx, item, expected)
	if err != nil {
		return domain.NFOItemScope{}, err
	}
	return value, storageError(tx.Commit(ctx))
}
