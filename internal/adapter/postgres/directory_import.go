package postgres

import (
	"context"
	"errors"
	"strings"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

// ImportDirectory records a locally validated directory without inventing media.
func (s *Store) ImportDirectory(ctx context.Context, library, root, relative, title, kind, parent string) (string, error) {
	if ctx == nil || library == "" || len(library) > 128 || title == "" || len(title) > 1024 || !domain.ValidDirectorySourcePath(relative) || kind != "Series" && kind != "Season" || kind == "Series" && parent != "" || kind == "Season" && !domain.ValidID(parent) {
		return "", domain.ErrInvalid
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return "", storageError(err)
	}
	defer tx.Rollback(ctx)
	if err := lockJobs(ctx, tx); err != nil {
		return "", err
	}
	var libraryID, rootID, itemID string
	if err := tx.QueryRow(ctx, `INSERT INTO libraries(name) VALUES($1) ON CONFLICT(name) DO UPDATE SET name=EXCLUDED.name RETURNING id::text`, library).Scan(&libraryID); err != nil {
		return "", storageError(err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO library_roots(library_id,path) VALUES($1,$2) ON CONFLICT(path) DO UPDATE SET path=EXCLUDED.path WHERE library_roots.library_id=EXCLUDED.library_id RETURNING id::text`, libraryID, root).Scan(&rootID); err != nil {
		return "", storageError(err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO items(library_id,title,kind) VALUES($1,$2,$3) RETURNING id::text`, libraryID, title, kind).Scan(&itemID); err != nil {
		return "", storageError(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO item_directory_sources(item_id,library_id,kind,root_id,relative_path) VALUES($1,$2,$3,$4,$5)`, itemID, libraryID, kind, rootID, relative); err != nil {
		return "", storageError(err)
	}
	if err := writeImportedParent(ctx, tx, itemID, libraryID, rootID, relative, kind, parent); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO audit_logs(event,target_id) VALUES('directory.registered',$1)`, itemID); err != nil {
		return "", storageError(err)
	}
	return itemID, storageError(tx.Commit(ctx))
}

func writeImportedParent(ctx context.Context, tx pgx.Tx, item, library, root, relative, kind, parent string) error {
	if parent == "" {
		if kind == "Season" {
			return domain.ErrInvalid
		}
		return nil
	}
	if !domain.ValidID(parent) || kind != "Season" && kind != "Episode" {
		return domain.ErrInvalid
	}
	var parentKind, parentRoot, parentPath string
	err := tx.QueryRow(ctx, `SELECT i.kind,d.root_id::text,d.relative_path FROM items i JOIN item_directory_sources d ON d.item_id=i.id AND d.library_id=i.library_id WHERE i.id=$1::uuid AND i.library_id=$2::uuid FOR UPDATE OF i,d`, parent, library).Scan(&parentKind, &parentRoot, &parentPath)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrInvalid
		}
		return storageError(err)
	}
	if parentRoot != root || relative == parentPath || parentPath != "." && !strings.HasPrefix(relative, parentPath+"/") || parentKind != "Series" && parentKind != "Season" || kind == "Season" && parentKind != "Series" {
		return domain.ErrInvalid
	}
	_, err = tx.Exec(ctx, `INSERT INTO item_parent_links(item_id,library_id,item_kind,parent_id,parent_kind) VALUES($1,$2,$3,$4,$5)`, item, library, kind, parent, parentKind)
	return storageError(err)
}
