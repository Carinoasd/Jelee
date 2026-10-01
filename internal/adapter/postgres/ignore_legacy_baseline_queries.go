package postgres

import (
	"context"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

func (s *Store) ReadLegacyIgnoreBaselinePage(ctx context.Context, l domain.JobLease, after domain.IgnoreProofCursor) ([]domain.LegacyIgnoreBaselineObservation, error) {
	if after != (domain.IgnoreProofCursor{}) && (!domain.ValidID(after.RootID) || !validScanPath(after.Directory, true)) {
		return nil, domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	current, epoch, err := legacyVerificationFence(ctx, tx, l)
	if err != nil {
		return nil, err
	}
	result, err := legacyBaselineObservationPage(ctx, tx, l.Job.ID, after)
	if err != nil {
		return nil, err
	}
	if err = commitIgnoreManifest(ctx, tx, current, epoch); err != nil {
		return nil, err
	}
	return result, nil
}

func legacyBaselineObservationPage(ctx context.Context, tx pgx.Tx, id string, after domain.IgnoreProofCursor) ([]domain.LegacyIgnoreBaselineObservation, error) {
	rows, err := tx.Query(ctx, `SELECT b.root_id::text,b.lookup_directory,b.source_directory,b.missing_directory,b.missing_parent_identity,b.proof_version,q.selected_directory FROM job_ignore_legacy_baseline_queries b JOIN job_ignore_legacy_queries q ON q.job_id=b.job_id AND q.root_id=b.root_id AND q.directory=b.source_directory WHERE b.job_id=$1::uuid AND (b.root_id,b.lookup_directory)>(COALESCE(NULLIF($2,'')::uuid,'00000000-0000-0000-0000-000000000000'::uuid),$3 COLLATE "C") ORDER BY b.root_id,b.lookup_directory LIMIT $4`, id, after.RootID, after.Directory, legacyObservationPageSize)
	if err != nil {
		return nil, storageError(err)
	}
	var result []domain.LegacyIgnoreBaselineObservation
	var queries []legacyQuery
	for rows.Next() {
		var o domain.LegacyIgnoreBaselineObservation
		var q legacyQuery
		var missing *string
		var parent []byte
		if err = rows.Scan(&q.key.RootID, &o.LookupDirectory, &q.key.Directory, &missing, &parent, &o.Version, &q.selected); err != nil {
			rows.Close()
			return nil, storageError(err)
		}
		if o.Version != domain.LegacyIgnoreBaselineProofVersion || !domain.ValidID(q.key.RootID) || !validScanPath(q.key.Directory, true) {
			rows.Close()
			return nil, domain.ErrDatabase
		}
		if missing != nil {
			if len(parent) != 32 {
				rows.Close()
				return nil, domain.ErrDatabase
			}
			o.MissingDirectory = domain.IgnoreDirectoryProof{RootID: q.key.RootID, Directory: *missing, MissingDirectory: true}
			copy(o.MissingDirectory.ParentIdentity[:], parent)
		} else if len(parent) != 0 {
			rows.Close()
			return nil, domain.ErrDatabase
		}
		result = append(result, o)
		queries = append(queries, q)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, storageError(err)
	}
	sources, err := restoreLegacyQueries(ctx, tx, id, queries)
	if err != nil {
		return nil, err
	}
	for i := range result {
		result[i].Source = sources[i]
		if domain.ValidateLegacyIgnoreBaselineObservation(result[i]) != nil {
			return nil, domain.ErrDatabase
		}
	}
	return result, nil
}
