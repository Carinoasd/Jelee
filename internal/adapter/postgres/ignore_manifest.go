package postgres

import (
	"context"
	"errors"
	"path"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

const ignoreProofColumns = `root_id::text,directory,parent_identity,identity,missing_directory,rule_present,rule_identity,rule_size,rule_modified_nano,rule_sha256`

// Qualify the UUID ordering: the projected root_id is text. Ordering by that
// output alias would sort the remaining suffix instead of using the index.
const listIgnoreProofsSQL = `SELECT ` + ignoreProofColumns + ` FROM job_ignore_proofs WHERE job_id=$1::uuid AND (root_id,directory)>(COALESCE(NULLIF($2,'')::uuid,'00000000-0000-0000-0000-000000000000'::uuid),$3 COLLATE "C") ORDER BY job_ignore_proofs.root_id,directory LIMIT $4`

func scanIgnoreProof(row pgx.Row) (domain.IgnoreDirectoryProof, error) {
	var p domain.IgnoreDirectoryProof
	var parent, identity, rule, digest []byte
	if err := row.Scan(&p.RootID, &p.Directory, &parent, &identity, &p.MissingDirectory, &p.RulePresent, &rule, &p.RuleSize, &p.RuleModifiedNano, &digest); err != nil {
		return domain.IgnoreDirectoryProof{}, storageError(err)
	}
	if len(parent) != 32 || len(identity) != 32 || len(rule) != 32 || len(digest) != 32 {
		return domain.IgnoreDirectoryProof{}, domain.ErrDatabase
	}
	copy(p.ParentIdentity[:], parent)
	copy(p.Identity[:], identity)
	copy(p.RuleIdentity[:], rule)
	copy(p.RuleSHA256[:], digest)
	if domain.ValidateIgnoreDirectoryProof(p) != nil {
		return domain.IgnoreDirectoryProof{}, domain.ErrDatabase
	}
	return p, nil
}

// Unlike inventory execution, recording evidence does not enumerate files or
// publish a baseline. Claims and all existing enabled execution guards remain
// closed until the filtered executor and final verification are implemented.
func ignoreManifestFence(ctx context.Context, tx pgx.Tx, l domain.JobLease) (domain.JobLease, int64, error) {
	current, err := fencedJob(ctx, tx, l)
	if err != nil {
		return current, 0, err
	}
	if current.Job.CancelRequested {
		return current, 0, context.Canceled
	}
	r, err := loadIgnoreRequest(ctx, tx, l.Job.ID)
	if err != nil {
		return current, 0, err
	}
	if r == nil {
		return current, 0, domain.ErrConflict
	}
	epoch, live, err := inventoryEpoch(ctx, tx, current)
	if err != nil {
		return current, 0, err
	}
	if epoch == nil || *epoch != live {
		return current, 0, domain.ErrInventoryInvalidated
	}
	return current, live, nil
}

func commitIgnoreManifest(ctx context.Context, tx pgx.Tx, l domain.JobLease, epoch int64) error {
	if err := guardedJobUpdate(ctx, tx, l, `UPDATE jobs SET generation=generation WHERE id=$1::uuid`, l.Job.ID); err != nil {
		return err
	}
	if err := guardInventoryFinish(ctx, tx, l, &epoch, true); err != nil {
		return err
	}
	return storageError(tx.Commit(ctx))
}

// RecordIgnoreProofs atomically appends a bounded parent-first batch. Identical
// replay is free; conflicting evidence invalidates the entire retained manifest
// and returns ErrInventoryInvalidated after committing that marker. It never
// refreshes an existing proof or continues with an old sibling's source state.
func (s *Store) RecordIgnoreProofs(ctx context.Context, l domain.JobLease, proofs []domain.IgnoreDirectoryProof) error {
	if len(proofs) == 0 || len(proofs) > domain.IgnoreProofPageSize {
		return domain.ErrInvalid
	}
	for _, p := range proofs {
		if err := domain.ValidateIgnoreDirectoryProof(p); err != nil {
			return err
		}
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
	err = recordIgnoreProofs(ctx, tx, current, epoch, proofs)
	if err != nil && !errors.Is(err, domain.ErrInventoryInvalidated) {
		return err
	}
	if commitErr := commitIgnoreManifest(ctx, tx, current, epoch); commitErr != nil {
		return commitErr
	}
	return err
}

// The caller owns the transaction and commits an invalidation marker even when
// this helper returns ErrInventoryInvalidated. Other errors require rollback.
func recordIgnoreProofs(ctx context.Context, tx pgx.Tx, current domain.JobLease, epoch int64, proofs []domain.IgnoreDirectoryProof) error {
	l := current
	_, err := tx.Exec(ctx, `INSERT INTO job_ignore_manifests(job_id,inventory_generation) VALUES($1::uuid,$2) ON CONFLICT DO NOTHING`, l.Job.ID, epoch)
	if err != nil {
		return storageError(err)
	}
	var frozen, invalid bool
	var recordedEpoch, rows, sourceBytes, chargeBytes int64
	err = tx.QueryRow(ctx, `SELECT frozen,invalidated,inventory_generation,rows,source_bytes,charge_bytes FROM job_ignore_manifests WHERE job_id=$1::uuid FOR UPDATE`, l.Job.ID).Scan(&frozen, &invalid, &recordedEpoch, &rows, &sourceBytes, &chargeBytes)
	if err != nil {
		return storageError(err)
	}
	if invalid || recordedEpoch != epoch {
		return domain.ErrInventoryInvalidated
	}
	// A savepoint allows invalidation to retain exactly the previously accepted
	// ledger, without preserving a new prefix from the conflicting batch.
	if _, err = tx.Exec(ctx, `SAVEPOINT ignore_batch`); err != nil {
		return storageError(err)
	}
	for _, p := range proofs {
		old, readErr := scanIgnoreProof(tx.QueryRow(ctx, `SELECT `+ignoreProofColumns+` FROM job_ignore_proofs WHERE job_id=$1::uuid AND root_id=$2::uuid AND directory=$3`, l.Job.ID, p.RootID, p.Directory))
		if readErr == nil {
			if old == p {
				continue
			}
			if _, err = tx.Exec(ctx, `ROLLBACK TO SAVEPOINT ignore_batch`); err != nil {
				return storageError(err)
			}
			if _, err = tx.Exec(ctx, `UPDATE job_ignore_manifests SET invalidated=true WHERE job_id=$1::uuid`, l.Job.ID); err != nil {
				return storageError(err)
			}
			return domain.ErrInventoryInvalidated
		}
		if !errors.Is(readErr, domain.ErrNotFound) {
			return readErr
		}
		if frozen {
			return domain.ErrConflict
		}
		var rootOwned bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM library_roots WHERE id=$1::uuid AND library_id=$2::uuid)`, p.RootID, current.Job.LibraryID).Scan(&rootOwned); err != nil {
			return storageError(err)
		}
		if !rootOwned {
			return domain.ErrConflict
		}
		var parent *string
		if p.Directory != "." {
			name := path.Dir(p.Directory)
			parent = &name
			prior, err := scanIgnoreProof(tx.QueryRow(ctx, `SELECT `+ignoreProofColumns+` FROM job_ignore_proofs WHERE job_id=$1::uuid AND root_id=$2::uuid AND directory=$3`, l.Job.ID, p.RootID, name))
			if err != nil && !errors.Is(err, domain.ErrNotFound) {
				return err
			}
			if err != nil || !domain.IgnoreProofParentMatches(p, prior) {
				return domain.ErrConflict
			}
		}
		rows++
		sourceBytes += p.RuleSize
		chargeBytes += p.Charge()
		if rows > domain.IgnoreManifestMaxRows || rows > int64(current.Policy.MaxDirectories) || sourceBytes > domain.IgnoreManifestMaxBytes || chargeBytes > domain.IgnoreManifestMaxBytes {
			return domain.ErrScanLimit
		}
		_, err = tx.Exec(ctx, `INSERT INTO job_ignore_proofs(job_id,root_id,directory,parent_path,parent_identity,identity,missing_directory,rule_present,rule_identity,rule_size,rule_modified_nano,rule_sha256) VALUES($1::uuid,$2::uuid,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, l.Job.ID, p.RootID, p.Directory, parent, p.ParentIdentity[:], p.Identity[:], p.MissingDirectory, p.RulePresent, p.RuleIdentity[:], p.RuleSize, p.RuleModifiedNano, p.RuleSHA256[:])
		if err != nil {
			return storageError(err)
		}
	}
	_, err = tx.Exec(ctx, `UPDATE job_ignore_manifests SET rows=$2,source_bytes=$3,charge_bytes=$4 WHERE job_id=$1::uuid`, l.Job.ID, rows, sourceBytes, chargeBytes)
	if err != nil {
		return storageError(err)
	}
	return nil
}

// FreezeIgnoreManifest freezes only the source ledger. It does not declare
// inventory complete, verify sources under the current lease, or seal a scan.
func (s *Store) FreezeIgnoreManifest(ctx context.Context, l domain.JobLease) error {
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	current, epoch, err := ignoreManifestFence(ctx, tx, l)
	if err != nil {
		return err
	}
	var covered bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM library_roots WHERE library_id=$2::uuid) AND NOT EXISTS(SELECT 1 FROM library_roots r WHERE r.library_id=$2::uuid AND NOT EXISTS(SELECT 1 FROM job_ignore_proofs p WHERE p.job_id=$1::uuid AND p.root_id=r.id AND p.directory='.' AND NOT p.missing_directory))`, l.Job.ID, current.Job.LibraryID).Scan(&covered)
	if err != nil {
		return storageError(err)
	}
	if !covered {
		return domain.ErrConflict
	}
	tag, err := tx.Exec(ctx, `UPDATE job_ignore_manifests SET frozen=true WHERE job_id=$1::uuid AND inventory_generation=$2 AND NOT invalidated`, l.Job.ID, epoch)
	if err != nil {
		return storageError(err)
	}
	if tag.RowsAffected() != 1 {
		return domain.ErrInventoryInvalidated
	}
	return commitIgnoreManifest(ctx, tx, current, epoch)
}

// ReadIgnoreProofPage uses the frozen ledger's byte-ordered keyset. An empty
// page is EOF. The last returned row is the next cursor; no caller supplied
// offset or unbounded result size is accepted. This read is not verification.
func (s *Store) ReadIgnoreProofPage(ctx context.Context, l domain.JobLease, after domain.IgnoreProofCursor) ([]domain.IgnoreDirectoryProof, error) {
	if after != (domain.IgnoreProofCursor{}) && (!domain.ValidID(after.RootID) || !validScanPath(after.Directory, true)) {
		return nil, domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	current, epoch, err := ignoreManifestFence(ctx, tx, l)
	if err != nil {
		return nil, err
	}
	var ready bool
	err = tx.QueryRow(ctx, `SELECT frozen AND NOT invalidated AND inventory_generation=$2 FROM job_ignore_manifests WHERE job_id=$1::uuid`, l.Job.ID, epoch).Scan(&ready)
	if err != nil {
		return nil, storageError(err)
	}
	if !ready {
		return nil, domain.ErrConflict
	}
	rows, err := tx.Query(ctx, listIgnoreProofsSQL, l.Job.ID, after.RootID, after.Directory, domain.IgnoreProofPageSize)
	if err != nil {
		return nil, storageError(err)
	}
	var result []domain.IgnoreDirectoryProof
	for rows.Next() {
		p, readErr := scanIgnoreProof(rows)
		if readErr != nil {
			rows.Close()
			return nil, readErr
		}
		result = append(result, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, storageError(err)
	}
	if err = commitIgnoreManifest(ctx, tx, current, epoch); err != nil {
		return nil, err
	}
	return result, nil
}
