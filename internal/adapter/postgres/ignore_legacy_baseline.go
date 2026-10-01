package postgres

import (
	"context"
	"errors"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

type legacyBaselineQuery struct {
	lookup, source string
	missing        domain.IgnoreDirectoryProof
}

func (s *Store) RecordLegacyIgnoreBaselineObservations(ctx context.Context, l domain.JobLease, observations []domain.LegacyIgnoreBaselineObservation) error {
	if len(observations) == 0 || len(observations) > domain.ScanBatchMaxEntries {
		return domain.ErrInvalid
	}
	for _, o := range observations {
		if err := domain.ValidateLegacyIgnoreBaselineObservation(o); err != nil {
			return err
		}
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	current, epoch, err := legacyManifestFence(ctx, tx, l)
	if err != nil {
		return err
	}
	err = recordLegacyBaselineObservations(ctx, tx, current, epoch, observations)
	if err != nil && !errors.Is(err, domain.ErrInventoryInvalidated) {
		return err
	}
	if e := commitIgnoreManifest(ctx, tx, current, epoch); e != nil {
		return e
	}
	return err
}

func recordLegacyBaselineObservations(ctx context.Context, tx pgx.Tx, l domain.JobLease, epoch int64, observations []domain.LegacyIgnoreBaselineObservation) error {
	root := observations[0].Source.Proofs[0].RootID
	sources := make([]domain.LegacyIgnoreObservation, 0, len(observations))
	queries := map[string]legacyBaselineQuery{}
	var lookups, missingNames []string
	conflict := false
	for _, o := range observations {
		if o.Source.Proofs[0].RootID != root {
			return domain.ErrInvalid
		}
		sources = append(sources, o.Source)
		q := legacyBaselineQuery{lookup: o.LookupDirectory, source: o.Source.Directory, missing: o.MissingDirectory}
		if old, exists := queries[q.lookup]; exists {
			if old != q {
				conflict = true
			}
		} else {
			queries[q.lookup] = q
			lookups = append(lookups, q.lookup)
		}
		if q.missing.MissingDirectory {
			missingNames = append(missingNames, q.missing.Directory)
		}
	}
	if _, err := tx.Exec(ctx, `SAVEPOINT legacy_baseline_batch`); err != nil {
		return storageError(err)
	}
	invalidate := func() error {
		if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT legacy_baseline_batch`); err != nil {
			return storageError(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO job_ignore_legacy_manifests(job_id,inventory_generation,invalidated) VALUES($1::uuid,$2,true) ON CONFLICT(job_id) DO UPDATE SET invalidated=true`, l.Job.ID, epoch); err != nil {
			return storageError(err)
		}
		return domain.ErrInventoryInvalidated
	}
	if err := recordLegacyObservations(ctx, tx, l, epoch, sources); err != nil {
		if errors.Is(err, domain.ErrInventoryInvalidated) {
			return invalidate()
		}
		return err
	}
	var frozen bool
	var count, charge int64
	err := tx.QueryRow(ctx, `SELECT frozen,baseline_queries,charge_bytes FROM job_ignore_legacy_manifests WHERE job_id=$1::uuid FOR UPDATE`, l.Job.ID).Scan(&frozen, &count, &charge)
	if err != nil {
		return storageError(err)
	}
	var observed bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM job_ignore_legacy_proofs WHERE job_id=$1::uuid AND root_id=$2::uuid AND directory=ANY($3::text[]))`, l.Job.ID, root, missingNames).Scan(&observed)
	if err != nil {
		return storageError(err)
	}
	conflict = conflict || observed
	rows, err := tx.Query(ctx, `SELECT lookup_directory,source_directory,missing_directory,missing_parent_identity FROM job_ignore_legacy_baseline_queries WHERE job_id=$1::uuid AND root_id=$2::uuid AND lookup_directory=ANY($3::text[])`, l.Job.ID, root, lookups)
	if err != nil {
		return storageError(err)
	}
	previous := map[string]legacyBaselineQuery{}
	for rows.Next() {
		var q legacyBaselineQuery
		var missing *string
		var parent []byte
		if err = rows.Scan(&q.lookup, &q.source, &missing, &parent); err != nil {
			rows.Close()
			return storageError(err)
		}
		if missing != nil {
			if len(parent) != 32 {
				rows.Close()
				return domain.ErrDatabase
			}
			q.missing = domain.IgnoreDirectoryProof{RootID: root, Directory: *missing, MissingDirectory: true}
			copy(q.missing.ParentIdentity[:], parent)
		}
		previous[q.lookup] = q
		if queries[q.lookup] != q {
			conflict = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return storageError(err)
	}
	if conflict {
		return invalidate()
	}
	batch := &pgx.Batch{}
	for _, name := range lookups {
		if _, exists := previous[name]; exists {
			continue
		}
		if frozen {
			return domain.ErrConflict
		}
		q := queries[name]
		count++
		charge += int64(192 + len(q.lookup) + len(q.source) + len(q.missing.Directory))
		var missing *string
		var parent []byte
		if q.missing.MissingDirectory {
			missing = &q.missing.Directory
			parent = q.missing.ParentIdentity[:]
		}
		batch.Queue(`INSERT INTO job_ignore_legacy_baseline_queries(job_id,root_id,lookup_directory,source_directory,missing_directory,missing_parent_identity,proof_version) VALUES($1::uuid,$2::uuid,$3,$4,$5,$6,$7)`, l.Job.ID, root, q.lookup, q.source, missing, parent, domain.LegacyIgnoreBaselineProofVersion)
	}
	if count > domain.IgnoreManifestMaxRows || charge > domain.IgnoreManifestMaxBytes {
		return domain.ErrScanLimit
	}
	batch.Queue(`UPDATE job_ignore_legacy_manifests SET baseline_queries=$2,charge_bytes=$3 WHERE job_id=$1::uuid`, l.Job.ID, count, charge)
	if err = tx.SendBatch(ctx, batch).Close(); err != nil {
		return storageError(err)
	}
	_, err = tx.Exec(ctx, `RELEASE SAVEPOINT legacy_baseline_batch`)
	return storageError(err)
}
