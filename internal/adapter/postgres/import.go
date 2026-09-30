package postgres

import (
	"context"
	"fmt"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// ImportVideo registers a single validated existing file without changing it.
// File safety checks belong to the local CLI and again to the delivery adapter.
func (s *Store) ImportVideo(ctx context.Context, library, root, relative, title, contentType string) (string, error) {
	if library == "" || len(library) > 128 || title == "" || len(title) > 1024 {
		return "", domain.ErrInvalid
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin catalog import: %w", err)
	}
	defer tx.Rollback(ctx)
	var libraryID, rootID, itemID, sourceID string
	if err = tx.QueryRow(ctx, `INSERT INTO libraries(name) VALUES($1) ON CONFLICT(name) DO UPDATE SET name=EXCLUDED.name RETURNING id::text`, library).Scan(&libraryID); err != nil {
		return "", fmt.Errorf("import library: %w", err)
	}
	if err = tx.QueryRow(ctx, `INSERT INTO library_roots(library_id,path) VALUES($1,$2) ON CONFLICT(path) DO UPDATE SET path=EXCLUDED.path WHERE library_roots.library_id=EXCLUDED.library_id RETURNING id::text`, libraryID, root).Scan(&rootID); err != nil {
		return "", fmt.Errorf("import root: %w", err)
	}
	// Duplicate files fail atomically rather than producing orphan items.
	if err = tx.QueryRow(ctx, `INSERT INTO items(library_id,title,kind) VALUES($1,$2,'HomeVideo') RETURNING id::text`, libraryID, title).Scan(&itemID); err != nil {
		return "", fmt.Errorf("import item: %w", err)
	}
	if err = tx.QueryRow(ctx, `INSERT INTO media_sources(item_id,library_id,root_id,relative_path,content_type) VALUES($1,$2,$3,$4,$5) RETURNING id::text`, itemID, libraryID, rootID, relative, contentType).Scan(&sourceID); err != nil {
		return "", fmt.Errorf("import source: %w", err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_logs(event,target_id) VALUES('media.registered',$1)`, sourceID); err != nil {
		return "", fmt.Errorf("audit import: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit import: %w", err)
	}
	return sourceID, nil
}
