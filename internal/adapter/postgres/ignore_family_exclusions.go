package postgres

import (
	"context"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

func saveFamilyExclusions(ctx context.Context, tx pgx.Tx, id string, d domain.ScanDirectory, excluded []domain.FamilyIgnoreExclusion) (int64, int64, error) {
	if len(excluded) == 0 {
		return 0, 0, nil
	}
	paths := make([]string, len(excluded))
	for i, e := range excluded {
		paths[i] = e.Path
	}
	var conflict bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM job_inventory WHERE job_id=$1::uuid AND root_id=$2::uuid AND path=ANY($3::text[])) OR EXISTS(SELECT 1 FROM job_directories WHERE job_id=$1::uuid AND root_id=$2::uuid AND path=ANY($3::text[]))`, id, d.RootID, paths).Scan(&conflict)
	if err != nil {
		return 0, 0, storageError(err)
	}
	if conflict {
		return 0, 0, domain.ErrConflict
	}
	rows, err := tx.Query(ctx, `SELECT path,kind,family,reason,rule_directory,rule_line,matched_path FROM job_ignore_family_exclusions WHERE job_id=$1::uuid AND root_id=$2::uuid AND path=ANY($3::text[])`, id, d.RootID, paths)
	if err != nil {
		return 0, 0, storageError(err)
	}
	previous := map[string]domain.FamilyIgnoreExclusion{}
	for rows.Next() {
		var e domain.FamilyIgnoreExclusion
		if err = rows.Scan(&e.Path, &e.Kind, &e.Family, &e.Reason, &e.RuleDirectory, &e.RuleLine, &e.MatchedPath); err != nil {
			rows.Close()
			return 0, 0, storageError(err)
		}
		previous[e.Path] = e
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, 0, storageError(err)
	}
	var files, dirs int64
	batch := &pgx.Batch{}
	for _, e := range excluded {
		if old, exists := previous[e.Path]; exists {
			if old != e {
				return 0, 0, domain.ErrConflict
			}
			continue
		}
		if e.Kind == "directory" {
			dirs++
		} else {
			files++
		}
		batch.Queue(`INSERT INTO job_ignore_family_exclusions(job_id,root_id,parent_path,path,kind,family,reason,rule_directory,rule_line,matched_path) VALUES($1::uuid,$2::uuid,$3,$4,$5,$6,$7,$8,$9,$10)`, id, d.RootID, d.Path, e.Path, e.Kind, e.Family, e.Reason, e.RuleDirectory, e.RuleLine, e.MatchedPath)
	}
	if batch.Len() > 0 {
		err = tx.SendBatch(ctx, batch).Close()
	}
	return files, dirs, storageError(err)
}
