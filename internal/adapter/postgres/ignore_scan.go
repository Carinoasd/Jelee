package postgres

import (
	"context"
	"errors"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

func requireIgnoreScanPhase(ctx context.Context, tx pgx.Tx, id string) error {
	if err := requireInventoryWork(ctx, tx, id); err != nil {
		return err
	}
	var frozen, invalid bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM job_ignore_comparisons WHERE job_id=$1::uuid) OR EXISTS(SELECT 1 FROM job_ignore_manifests WHERE job_id=$1::uuid AND frozen),EXISTS(SELECT 1 FROM job_ignore_manifests WHERE job_id=$1::uuid AND invalidated)`, id).Scan(&frozen, &invalid)
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

func validIgnoreScanBatch(d domain.ScanDirectory, b domain.IgnoreScanBatch) bool {
	if !validScanBatch(d, b.Inventory) || len(b.Proofs) == 0 || len(b.Proofs) > 128 || len(b.Inventory.Entries)+len(b.Inventory.Directories)+len(b.Excluded) > 128 {
		return false
	}
	for i, p := range b.Proofs {
		if domain.ValidateIgnoreDirectoryProof(p) != nil || p.RootID != d.RootID || p.MissingDirectory {
			return false
		}
		if i == 0 {
			if p.Directory != "." {
				return false
			}
		} else if !domain.IgnoreProofParentMatches(p, b.Proofs[i-1]) {
			return false
		}
	}
	tail := b.Proofs[len(b.Proofs)-1]
	if tail.Directory != d.Path || tail.Identity != b.HeldDirectoryIdentity {
		return false
	}
	seen := map[string]bool{}
	for _, e := range b.Inventory.Entries {
		seen[e.Path] = true
	}
	for _, p := range b.Inventory.Directories {
		seen[p] = true
	}
	for _, e := range b.Excluded {
		if !scanChild(d.Path, e.Path) || seen[e.Path] || e.Kind != "directory" && !validInventoryKind(e.Kind) || e.MatchedPath != e.Path {
			return false
		}
		decision := domain.IgnoreBaselineDecision{RootID: d.RootID, Path: e.Path, Outcome: domain.IgnoreBaselineExcluded, RuleDirectory: e.RuleDirectory, RuleLine: e.RuleLine, MatchedPath: e.MatchedPath}
		if domain.ValidateIgnoreBaselineDecision(decision) != nil {
			return false
		}
		found := false
		for _, p := range b.Proofs {
			if p.Directory == e.RuleDirectory && p.RulePresent {
				found = true
			}
		}
		if !found {
			return false
		}
		seen[e.Path] = true
	}
	return true
}

// NextIgnoreScanDirectory restarts only the incomplete directory's inventory
// and exclusion observations. Previously accepted source proofs remain pinned.
func (s *Store) NextIgnoreScanDirectory(ctx context.Context, l domain.JobLease) (domain.ScanDirectory, error) {
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return domain.ScanDirectory{}, err
	}
	defer tx.Rollback(ctx)
	current, epoch, err := ignoreManifestFence(ctx, tx, l)
	if err != nil {
		return domain.ScanDirectory{}, err
	}
	if err = requireIgnoreScanPhase(ctx, tx, l.Job.ID); err != nil {
		return domain.ScanDirectory{}, err
	}
	d, err := nextScanDirectory(ctx, tx, current)
	if err != nil {
		return domain.ScanDirectory{}, err
	}
	_, err = tx.Exec(ctx, `WITH removed AS (DELETE FROM job_ignore_exclusions WHERE job_id=$1::uuid AND root_id=$2::uuid AND parent_path=$3 RETURNING kind),counts AS(SELECT count(*) FILTER(WHERE kind<>'directory') f,count(*) FILTER(WHERE kind='directory') d FROM removed) UPDATE job_ignore_scan_state SET excluded_files=excluded_files-counts.f,excluded_directories=excluded_directories-counts.d FROM counts WHERE job_id=$1::uuid`, l.Job.ID, d.RootID, d.Path)
	if err != nil {
		return domain.ScanDirectory{}, storageError(err)
	}
	if err = commitIgnoreManifest(ctx, tx, current, epoch); err != nil {
		return domain.ScanDirectory{}, err
	}
	return d, nil
}

// SaveIgnoreScanBatch atomically binds the native held-directory identity and
// full ancestor chain to included inventory and excluded-child provenance.
func (s *Store) SaveIgnoreScanBatch(ctx context.Context, l domain.JobLease, d domain.ScanDirectory, b domain.IgnoreScanBatch) error {
	if !validIgnoreScanBatch(d, b) {
		return domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	current, epoch, err := ignoreManifestFence(ctx, tx, l)
	if err != nil {
		return err
	}
	if err = requireIgnoreScanPhase(ctx, tx, l.Job.ID); err != nil {
		return err
	}
	if err = recordIgnoreProofs(ctx, tx, current, epoch, b.Proofs); err != nil {
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
	var addedFiles, addedDirs int64
	for _, e := range b.Excluded {
		var conflict bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM job_inventory WHERE job_id=$1::uuid AND root_id=$2::uuid AND path=$3) OR EXISTS(SELECT 1 FROM job_directories WHERE job_id=$1::uuid AND root_id=$2::uuid AND path=$3)`, l.Job.ID, d.RootID, e.Path).Scan(&conflict)
		if err != nil {
			return storageError(err)
		}
		if conflict {
			return domain.ErrConflict
		}
		var old domain.IgnoreScanExclusion
		old.Path = e.Path
		err = tx.QueryRow(ctx, `SELECT kind,rule_directory,rule_line,matched_path FROM job_ignore_exclusions WHERE job_id=$1::uuid AND root_id=$2::uuid AND path=$3`, l.Job.ID, d.RootID, e.Path).Scan(&old.Kind, &old.RuleDirectory, &old.RuleLine, &old.MatchedPath)
		if err == nil {
			if old != e {
				return domain.ErrConflict
			}
			continue
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return storageError(err)
		}
		_, err = tx.Exec(ctx, `INSERT INTO job_ignore_exclusions(job_id,root_id,parent_path,path,kind,rule_directory,rule_line,matched_path) VALUES($1::uuid,$2::uuid,$3,$4,$5,$6,$7,$8)`, l.Job.ID, d.RootID, d.Path, e.Path, e.Kind, e.RuleDirectory, e.RuleLine, e.MatchedPath)
		if err != nil {
			return storageError(err)
		}
		if e.Kind == "directory" {
			addedDirs++
		} else {
			addedFiles++
		}
	}
	paths := append([]string(nil), b.Inventory.Directories...)
	for _, e := range b.Inventory.Entries {
		paths = append(paths, e.Path)
	}
	var conflict bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM job_ignore_exclusions WHERE job_id=$1::uuid AND root_id=$2::uuid AND path=ANY($3::text[]))`, l.Job.ID, d.RootID, paths).Scan(&conflict)
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
