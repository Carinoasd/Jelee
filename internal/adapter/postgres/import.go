package postgres

import (
	"context"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// ImportVideo registers a single validated existing file without changing it.
// File safety checks belong to the local CLI and again to the delivery adapter.
func (s *Store) ImportVideo(ctx context.Context, library, root, relative, title, contentType string) (string, error) {
	return s.ImportVideoKind(ctx, library, root, relative, title, contentType, "HomeVideo")
}

// ImportVideoKind registers an explicitly classified, validated existing video.
func (s *Store) ImportVideoKind(ctx context.Context, library, root, relative, title, contentType, kind string) (string, error) {
	return s.ImportVideoWithParent(ctx, library, root, relative, title, contentType, kind, "")
}

func (s *Store) ImportVideoWithParent(ctx context.Context, library, root, relative, title, contentType, kind, parent string) (string, error) {
	if ctx == nil || !domain.ValidVideoItemKind(kind) || library == "" || len(library) > 128 || title == "" || len(title) > 1024 {
		return "", domain.ErrInvalid
	}
	if parent != "" {
		if _, valid := domain.AdjacentNFOPath(relative); !valid || kind != "Episode" || !domain.ValidID(parent) {
			return "", domain.ErrInvalid
		}
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return "", storageError(err)
	}
	defer tx.Rollback(ctx)
	if err = lockJobs(ctx, tx); err != nil {
		return "", err
	}
	var libraryID, rootID, itemID, sourceID string
	if err = tx.QueryRow(ctx, `INSERT INTO libraries(name) VALUES($1) ON CONFLICT(name) DO UPDATE SET name=EXCLUDED.name RETURNING id::text`, library).Scan(&libraryID); err != nil {
		return "", storageError(err)
	}
	if err = tx.QueryRow(ctx, `INSERT INTO library_roots(library_id,path) VALUES($1,$2) ON CONFLICT(path) DO UPDATE SET path=EXCLUDED.path WHERE library_roots.library_id=EXCLUDED.library_id RETURNING id::text`, libraryID, root).Scan(&rootID); err != nil {
		return "", storageError(err)
	}
	// Duplicate files fail atomically rather than producing orphan items.
	if err = tx.QueryRow(ctx, `INSERT INTO items(library_id,title,kind) VALUES($1,$2,$3) RETURNING id::text`, libraryID, title, kind).Scan(&itemID); err != nil {
		return "", storageError(err)
	}
	if err = tx.QueryRow(ctx, `INSERT INTO media_sources(item_id,library_id,root_id,relative_path,content_type) VALUES($1,$2,$3,$4,$5) RETURNING id::text`, itemID, libraryID, rootID, relative, contentType).Scan(&sourceID); err != nil {
		return "", storageError(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_logs(event,target_id) VALUES('media.registered',$1)`, sourceID); err != nil {
		return "", storageError(err)
	}
	if err := writeImportedParent(ctx, tx, itemID, libraryID, rootID, relative, kind, parent); err != nil {
		return "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", storageError(err)
	}
	return sourceID, nil
}
