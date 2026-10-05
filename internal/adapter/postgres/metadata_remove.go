package postgres

import (
	"context"
	"errors"
	"path"
	"path/filepath"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

var _ app.MetadataExternalRemovalRepository = (*Store)(nil)

// RemoveExternalMetadata deletes unlocked provider-sourced field values in one
// transaction (G14.7). Providers never replace manual, NFO or nonempty local
// values (TMDBMetadataSkip), so the state before a provider write was an
// absent field or, for the required title, the imported local name. Removal
// therefore deletes the field rows; the title falls back to the local media
// filename or directory name. NFO values reappear through the next NFO apply,
// where NFO keeps its priority. Facts never carry provider values.
func (s *Store) RemoveExternalMetadata(ctx context.Context, actor domain.Actor, item string, expected int64) (domain.MetadataRemoveResult, error) {
	if !domain.ValidMetadataRemoveInput(item, expected) {
		return domain.MetadataRemoveResult{}, domain.ErrInvalid
	}
	tx, err := s.authorizedJobs(ctx, actor)
	if err != nil {
		return domain.MetadataRemoveResult{}, err
	}
	defer tx.Rollback(ctx)
	before, err := readItemMetadata(ctx, tx, item, true)
	if err != nil {
		return domain.MetadataRemoveResult{}, err
	}
	if before.Revision != expected {
		return domain.MetadataRemoveResult{}, domain.ErrConflict
	}
	if _, err = tx.Exec(ctx, `INSERT INTO item_metadata_state(item_id,revision) VALUES($1::uuid,$2) ON CONFLICT(item_id) DO UPDATE SET revision=EXCLUDED.revision`, item, expected+1); err != nil {
		return domain.MetadataRemoveResult{}, storageError(err)
	}
	result := domain.MetadataRemoveResult{Removed: []string{}, Skipped: []domain.MetadataFieldSkip{}}
	// The audit keeps which provider records were removed and when they were
	// fetched, but not the removed provider text itself.
	removedOrigins := []map[string]any{}
	for _, field := range before.Fields {
		if field.Source != domain.ExternalMetadataSource {
			continue
		}
		if !domain.ExternalMetadataRemovable(field) {
			result.Skipped = append(result.Skipped, domain.MetadataFieldSkip{Field: field.Field, Reason: "locked"})
			continue
		}
		if field.Field == "title" {
			title, found, err := localFallbackTitle(ctx, tx, item)
			if err != nil {
				return domain.MetadataRemoveResult{}, err
			}
			if !found {
				result.Skipped = append(result.Skipped, domain.MetadataFieldSkip{Field: field.Field, Reason: "required"})
				continue
			}
			if _, err = tx.Exec(ctx, `UPDATE items SET title=$2 WHERE id=$1::uuid`, item, title); err != nil {
				return domain.MetadataRemoveResult{}, storageError(err)
			}
		}
		if _, err = tx.Exec(ctx, `DELETE FROM item_metadata_fields WHERE item_id=$1::uuid AND field=$2 AND source=$3`, item, field.Field, domain.ExternalMetadataSource); err != nil {
			return domain.MetadataRemoveResult{}, storageError(err)
		}
		result.Removed = append(result.Removed, field.Field)
		removedOrigins = append(removedOrigins, map[string]any{"field": field.Field, "providerOrigin": field.ProviderOrigin})
	}
	result.Metadata, err = readItemMetadata(ctx, tx, item, false)
	if err != nil {
		return domain.MetadataRemoveResult{}, err
	}
	auditBefore := map[string]any{"revision": before.Revision, "provider": domain.ExternalMetadataSource, "removedOrigins": removedOrigins}
	auditAfter := map[string]any{"metadata": result.Metadata, "removed": result.Removed, "skipped": result.Skipped}
	if err = auditAccount(ctx, tx, actor, "item.external_metadata_removed", item, auditBefore, auditAfter); err != nil {
		return domain.MetadataRemoveResult{}, err
	}
	return result, storageError(tx.Commit(ctx))
}

// localFallbackTitle picks the first local source in a stable order: media
// files before directories, then by root and relative path.
func localFallbackTitle(ctx context.Context, tx pgx.Tx, item string) (string, bool, error) {
	var root, relative string
	var directory bool
	err := tx.QueryRow(ctx, `SELECT r.path,s.relative_path,s.directory FROM (
 SELECT root_id,relative_path,false AS directory FROM media_sources WHERE item_id=$1::uuid
 UNION ALL SELECT root_id,relative_path,true FROM item_directory_sources WHERE item_id=$1::uuid
) s JOIN library_roots r ON r.id=s.root_id ORDER BY s.directory,r.path,s.relative_path LIMIT 1`, item).Scan(&root, &relative, &directory)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, storageError(err)
	}
	// A directory source of "." names the library root itself.
	title, ok := domain.LocalFallbackTitle(path.Join(filepath.ToSlash(root), relative), directory)
	return title, ok, nil
}
