package postgres

import (
	"context"
	"sort"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

func libraryAccess(ctx context.Context, tx pgx.Tx, userID string) ([]domain.LibraryGrant, error) {
	rows, err := tx.Query(ctx, `SELECT l.id::text,l.name FROM library_acl a JOIN libraries l ON l.id=a.library_id WHERE a.user_id=$1::uuid ORDER BY l.id LIMIT 1001`, userID)
	if err != nil {
		return nil, storageError(err)
	}
	defer rows.Close()
	result := make([]domain.LibraryGrant, 0)
	for rows.Next() {
		var g domain.LibraryGrant
		if err = rows.Scan(&g.LibraryID, &g.Name); err != nil {
			return nil, storageError(err)
		}
		result = append(result, g)
	}
	if err = rows.Err(); err != nil {
		return nil, storageError(err)
	}
	if len(result) > 1000 {
		return nil, domain.ErrConflict
	}
	return result, nil
}

func (s *Store) GetLibraryAccess(ctx context.Context, actor domain.Actor, userID string) ([]domain.LibraryGrant, error) {
	tx, admin, err := s.authorizedTransaction(ctx, actor, false)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if !admin && actor.UserID != userID {
		return nil, domain.ErrForbidden
	}
	if _, err = userInTransaction(ctx, tx, userID); err != nil {
		return nil, err
	}
	grants, err := libraryAccess(ctx, tx, userID)
	if err != nil {
		return nil, err
	}
	return grants, storageError(tx.Commit(ctx))
}

func (s *Store) ReplaceLibraryAccess(ctx context.Context, actor domain.Actor, userID string, libraryIDs []string) error {
	if len(libraryIDs) > 1000 {
		return domain.ErrInvalid
	}
	ids := make([]string, 0, len(libraryIDs))
	seen := make(map[string]bool, len(libraryIDs))
	for _, id := range libraryIDs {
		if !domain.ValidID(id) || seen[id] {
			return domain.ErrInvalid
		}
		seen[id] = true
		ids = append(ids, id)
	}
	sort.Strings(ids)
	tx, _, err := s.authorizedTransaction(ctx, actor, true)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	u, err := userInTransaction(ctx, tx, userID)
	if err != nil {
		return err
	}
	if u.DeletedAt != nil {
		return domain.ErrNotFound
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM libraries WHERE id=ANY($1::uuid[])`, ids).Scan(&count); err != nil {
		return storageError(err)
	}
	if count != len(ids) {
		return domain.ErrNotFound
	}
	before, err := libraryAccess(ctx, tx, userID)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM library_acl WHERE user_id=$1::uuid`, userID); err != nil {
		return storageError(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO library_acl(user_id,library_id) SELECT $1::uuid,unnest($2::uuid[])`, userID, ids); err != nil {
		return storageError(err)
	}
	oldIDs := make([]string, 0, len(before))
	for _, g := range before {
		oldIDs = append(oldIDs, g.LibraryID)
	}
	if err = auditAccount(ctx, tx, actor, "user.library_access_replaced", userID, map[string]any{"libraryIds": oldIDs}, map[string]any{"libraryIds": ids}); err != nil {
		return err
	}
	return storageError(tx.Commit(ctx))
}
