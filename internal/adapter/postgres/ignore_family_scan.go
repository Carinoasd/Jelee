package postgres

import (
	"context"
	"errors"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

// NextFamilyIgnoreScanDirectory restarts only the incomplete directory's inventory
// and exclusion observations. Previously accepted source proofs remain pinned.
func (s *Store) NextFamilyIgnoreScanDirectory(ctx context.Context, l domain.JobLease) (domain.ScanDirectory, error) {
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return domain.ScanDirectory{}, err
	}
	defer tx.Rollback(ctx)
	current, epoch, err := legacyManifestFence(ctx, tx, l)
	if err != nil {
		return domain.ScanDirectory{}, err
	}
	if err = requireFamilyIgnoreScanPhase(ctx, tx, l.Job.ID); err != nil {
		return domain.ScanDirectory{}, err
	}
	d, err := nextScanDirectory(ctx, tx, current)
	if err != nil {
		return domain.ScanDirectory{}, err
	}
	_, err = tx.Exec(ctx, `WITH removed AS (DELETE FROM job_ignore_family_exclusions WHERE job_id=$1::uuid AND root_id=$2::uuid AND parent_path=$3 RETURNING kind),counts AS(SELECT count(*) FILTER(WHERE kind<>'directory') f,count(*) FILTER(WHERE kind='directory') d FROM removed) UPDATE job_ignore_scan_state SET excluded_files=excluded_files-counts.f,excluded_directories=excluded_directories-counts.d FROM counts WHERE job_id=$1::uuid`, l.Job.ID, d.RootID, d.Path)
	if err != nil {
		return domain.ScanDirectory{}, storageError(err)
	}
	if err = commitIgnoreManifest(ctx, tx, current, epoch); err != nil {
		return domain.ScanDirectory{}, err
	}
	return d, nil
}

// SaveFamilyIgnoreScanBatch atomically binds the native held-directory identity and
// full ancestor chain to included inventory and excluded-child provenance.
func (s *Store) SaveFamilyIgnoreScanBatch(ctx context.Context, l domain.JobLease, d domain.ScanDirectory, b domain.FamilyIgnoreScanBatch) error {
	if !validFamilyIgnoreScanBatch(d, b) {
		return domain.ErrInvalid
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
	if err = requireFamilyIgnoreScanPhase(ctx, tx, l.Job.ID); err != nil {
		return err
	}
	if err = recordFamilyScanEvidence(ctx, tx, current, epoch, b); err != nil {
		if errors.Is(err, domain.ErrInventoryInvalidated) {
			if e := commitIgnoreManifest(ctx, tx, current, epoch); e != nil {
				return e
			}
		}
		return err
	}
	var done bool
	var rootPath string
	err = tx.QueryRow(ctx, `SELECT d.done,r.path FROM job_directories d JOIN library_roots r ON r.id=d.root_id WHERE d.job_id=$1::uuid AND d.root_id=$2::uuid AND d.path=$3 AND (d.parent_path IS NULL OR EXISTS(SELECT 1 FROM job_directories p WHERE p.job_id=d.job_id AND p.root_id=d.root_id AND p.path=d.parent_path AND p.done)) FOR UPDATE OF d`, l.Job.ID, d.RootID, d.Path).Scan(&done, &rootPath)
	if err != nil {
		return storageError(err)
	}
	if rootPath != d.RootPath {
		return domain.ErrInvalid
	}
	if done {
		return commitIgnoreManifest(ctx, tx, current, epoch)
	}
	_, err = tx.Exec(ctx, `INSERT INTO job_ignore_scan_state(job_id) VALUES($1::uuid) ON CONFLICT DO NOTHING`, l.Job.ID)
	if err != nil {
		return storageError(err)
	}
	addedFiles, addedDirs, err := saveFamilyExclusions(ctx, tx, l.Job.ID, d, b.Excluded)
	if err != nil {
		return err
	}
	paths := append([]string(nil), b.Inventory.Directories...)
	for _, e := range b.Inventory.Entries {
		paths = append(paths, e.Path)
	}
	var conflict bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM job_ignore_family_exclusions WHERE job_id=$1::uuid AND root_id=$2::uuid AND path=ANY($3::text[]))`, l.Job.ID, d.RootID, paths).Scan(&conflict)
	if err != nil {
		return storageError(err)
	}
	if conflict {
		return domain.ErrConflict
	}
	if err = saveScanBatch(ctx, tx, current, d, b.Inventory); err != nil {
		return err
	}
	var excludedFiles, excludedDirs int64
	err = tx.QueryRow(ctx, `SELECT excluded_files,excluded_directories FROM job_ignore_scan_state WHERE job_id=$1::uuid FOR UPDATE`, l.Job.ID).Scan(&excludedFiles, &excludedDirs)
	if err != nil {
		return storageError(err)
	}
	excludedFiles += addedFiles
	excludedDirs += addedDirs
	var files, dirs int64
	if err = tx.QueryRow(ctx, `SELECT files,directory_total FROM jobs WHERE id=$1::uuid`, l.Job.ID).Scan(&files, &dirs); err != nil {
		return storageError(err)
	}
	if files+excludedFiles > int64(current.Policy.MaxEntries) || dirs+excludedDirs > int64(current.Policy.MaxDirectories) {
		return domain.ErrScanLimit
	}
	if _, err = tx.Exec(ctx, `UPDATE job_ignore_scan_state SET excluded_files=$2,excluded_directories=$3 WHERE job_id=$1::uuid`, l.Job.ID, excludedFiles, excludedDirs); err != nil {
		return storageError(err)
	}
	return commitIgnoreManifest(ctx, tx, current, epoch)
}

func requireFamilyIgnoreScanPhase(ctx context.Context, tx pgx.Tx, id string) error {
	if err := requireIgnoreScanPhase(ctx, tx, id); err != nil {
		return err
	}
	var frozen, invalid bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM job_ignore_legacy_manifests WHERE job_id=$1::uuid AND frozen),EXISTS(SELECT 1 FROM job_ignore_legacy_manifests WHERE job_id=$1::uuid AND invalidated)`, id).Scan(&frozen, &invalid)
	if err != nil {
		return storageError(err)
	}
	if invalid {
		return domain.ErrInventoryInvalidated
	}
	if frozen {
		return domain.ErrConflict
	}
	return nil
}

func recordFamilyScanEvidence(ctx context.Context, tx pgx.Tx, l domain.JobLease, epoch int64, b domain.FamilyIgnoreScanBatch) error {
	if _, err := tx.Exec(ctx, `SAVEPOINT family_scan_evidence`); err != nil {
		return storageError(err)
	}
	err := recordIgnoreProofs(ctx, tx, l, epoch, b.CustomProofs)
	if err == nil {
		err = recordLegacyObservations(ctx, tx, l, epoch, b.LegacyObservations)
	}
	if errors.Is(err, domain.ErrInventoryInvalidated) {
		if _, e := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT family_scan_evidence`); e != nil {
			return storageError(e)
		}
		// Roll back both families' provisional writes before retaining invalidation.
		for _, query := range []string{
			`INSERT INTO job_ignore_manifests(job_id,inventory_generation,invalidated) VALUES($1::uuid,$2,true) ON CONFLICT(job_id) DO UPDATE SET invalidated=true`,
			`INSERT INTO job_ignore_legacy_manifests(job_id,inventory_generation,invalidated) VALUES($1::uuid,$2,true) ON CONFLICT(job_id) DO UPDATE SET invalidated=true`,
		} {
			if _, e := tx.Exec(ctx, query, l.Job.ID, epoch); e != nil {
				return storageError(e)
			}
		}
	}
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `RELEASE SAVEPOINT family_scan_evidence`)
	return storageError(err)
}
