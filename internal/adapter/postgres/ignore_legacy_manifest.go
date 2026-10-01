package postgres

import (
	"context"
	"errors"
	"path"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

// This fence permits evidence storage for the reserved tuple only. Public
// admission, claims and inventory execution retain their existing guards.
func legacyManifestFence(ctx context.Context, tx pgx.Tx, l domain.JobLease) (domain.JobLease, int64, error) {
	current, err := fencedJob(ctx, tx, l)
	if err != nil {
		return current, 0, err
	}
	if current.Job.CancelRequested {
		return current, 0, context.Canceled
	}
	var valid bool
	err = tx.QueryRow(ctx, `SELECT j.ignore_requested AND r.library_id=j.library_id AND r.mode='jeleeignore-legacy-v1' AND r.program_version='jeleeignore-legacy-v1' AND r.proof_version='jeleeignore-legacy-proof-v1' FROM jobs j JOIN job_ignore_requests r ON r.job_id=j.id WHERE j.id=$1::uuid FOR UPDATE OF r`, l.Job.ID).Scan(&valid)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !valid {
		return current, 0, domain.ErrConflict
	}
	if err != nil {
		return current, 0, storageError(err)
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

func scanLegacyProof(row pgx.Row) (domain.LegacyIgnoreDirectoryProof, error) {
	var p domain.LegacyIgnoreDirectoryProof
	var parent, identity, rule, digest []byte
	err := row.Scan(&p.RootID, &p.Directory, &parent, &identity, &p.Checked, &p.RulePresent, &rule, &p.RuleSize, &p.RuleModifiedNano, &digest)
	if err != nil {
		return p, storageError(err)
	}
	if len(parent) != 32 || len(identity) != 32 || len(rule) != 32 || len(digest) != 32 {
		return p, domain.ErrDatabase
	}
	copy(p.ParentIdentity[:], parent)
	copy(p.Identity[:], identity)
	copy(p.RuleIdentity[:], rule)
	copy(p.RuleSHA256[:], digest)
	if !domain.LegacyIgnoreProofsCompatible(p, p) {
		return p, domain.ErrDatabase
	}
	return p, nil
}

const legacyProofColumns = `root_id::text,directory,parent_identity,identity,checked,rule_present,rule_identity,rule_size,rule_modified_nano,rule_sha256`

// FreezeLegacyIgnoreManifest requires an explicit root query for every root;
// an unchecked ancestor of a deeper query cannot establish root coverage.
// Freezing evidence alone does not authorize inventory publication.
func (s *Store) FreezeLegacyIgnoreManifest(ctx context.Context, l domain.JobLease) error {
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	current, epoch, err := legacyManifestFence(ctx, tx, l)
	if err != nil {
		return err
	}
	var covered bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM library_roots WHERE library_id=$2::uuid) AND NOT EXISTS(SELECT 1 FROM library_roots r WHERE r.library_id=$2::uuid AND NOT EXISTS(SELECT 1 FROM job_ignore_legacy_queries q WHERE q.job_id=$1::uuid AND q.root_id=r.id AND q.directory='.'))`, l.Job.ID, current.Job.LibraryID).Scan(&covered)
	if err != nil {
		return storageError(err)
	}
	if !covered {
		return domain.ErrConflict
	}
	tag, err := tx.Exec(ctx, `UPDATE job_ignore_legacy_manifests SET frozen=true WHERE job_id=$1::uuid AND inventory_generation=$2 AND NOT invalidated`, l.Job.ID, epoch)
	if err != nil {
		return storageError(err)
	}
	if tag.RowsAffected() != 1 {
		return domain.ErrInventoryInvalidated
	}
	return commitIgnoreManifest(ctx, tx, current, epoch)
}

// ReadLegacyIgnoreProofPage returns a bounded frozen ledger page, including
// whether each rule was actually checked. This is not source revalidation.
func (s *Store) ReadLegacyIgnoreProofPage(ctx context.Context, l domain.JobLease, after domain.IgnoreProofCursor) ([]domain.LegacyIgnoreDirectoryProof, error) {
	if after != (domain.IgnoreProofCursor{}) && (!domain.ValidID(after.RootID) || !validScanPath(after.Directory, true)) {
		return nil, domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	current, epoch, err := legacyManifestFence(ctx, tx, l)
	if err != nil {
		return nil, err
	}
	var ready bool
	err = tx.QueryRow(ctx, `SELECT frozen AND NOT invalidated AND inventory_generation=$2 FROM job_ignore_legacy_manifests WHERE job_id=$1::uuid`, l.Job.ID, epoch).Scan(&ready)
	if err != nil {
		return nil, storageError(err)
	}
	if !ready {
		return nil, domain.ErrConflict
	}
	rows, err := tx.Query(ctx, `SELECT `+legacyProofColumns+` FROM job_ignore_legacy_proofs WHERE job_id=$1::uuid AND (root_id,directory)>(COALESCE(NULLIF($2,'')::uuid,'00000000-0000-0000-0000-000000000000'::uuid),$3 COLLATE "C") ORDER BY job_ignore_legacy_proofs.root_id,directory LIMIT $4`, l.Job.ID, after.RootID, after.Directory, domain.IgnoreProofPageSize)
	if err != nil {
		return nil, storageError(err)
	}
	var result []domain.LegacyIgnoreDirectoryProof
	for rows.Next() {
		p, e := scanLegacyProof(rows)
		if e != nil {
			rows.Close()
			return nil, e
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

// RecordLegacyIgnoreObservation retains one complete bounded nearest-source
// query. It reads existing chain rows together and pipelines mutations. No
// accepted prefix survives a conflict; only the invalidation marker commits.
func (s *Store) RecordLegacyIgnoreObservation(ctx context.Context, l domain.JobLease, o domain.LegacyIgnoreObservation) error {
	if err := domain.ValidateLegacyIgnoreObservation(o); err != nil {
		return err
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
	err = recordLegacyObservation(ctx, tx, current, epoch, o)
	if err != nil && !errors.Is(err, domain.ErrInventoryInvalidated) {
		return err
	}
	if commitErr := commitIgnoreManifest(ctx, tx, current, epoch); commitErr != nil {
		return commitErr
	}
	return err
}

func recordLegacyObservation(ctx context.Context, tx pgx.Tx, l domain.JobLease, epoch int64, o domain.LegacyIgnoreObservation) error {
	_, err := tx.Exec(ctx, `INSERT INTO job_ignore_legacy_manifests(job_id,inventory_generation) VALUES($1::uuid,$2) ON CONFLICT DO NOTHING`, l.Job.ID, epoch)
	if err != nil {
		return storageError(err)
	}
	var frozen, invalid bool
	var generation, count, queries, source, charge int64
	err = tx.QueryRow(ctx, `SELECT frozen,invalidated,inventory_generation,rows,queries,source_bytes,charge_bytes FROM job_ignore_legacy_manifests WHERE job_id=$1::uuid FOR UPDATE`, l.Job.ID).Scan(&frozen, &invalid, &generation, &count, &queries, &source, &charge)
	if err != nil {
		return storageError(err)
	}
	if invalid || generation != epoch {
		return domain.ErrInventoryInvalidated
	}
	root := o.Proofs[0].RootID
	var owned bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM library_roots WHERE id=$1::uuid AND library_id=$2::uuid)`, root, l.Job.LibraryID).Scan(&owned)
	if err != nil {
		return storageError(err)
	}
	if !owned {
		return domain.ErrConflict
	}
	names := make([]string, len(o.Proofs))
	var selected *string
	for i, p := range o.Proofs {
		names[i] = p.Directory
		if p.Checked && p.RulePresent {
			name := p.Directory
			selected = &name
		}
	}
	rows, err := tx.Query(ctx, `SELECT `+legacyProofColumns+` FROM job_ignore_legacy_proofs WHERE job_id=$1::uuid AND root_id=$2::uuid AND directory=ANY($3::text[])`, l.Job.ID, root, names)
	if err != nil {
		return storageError(err)
	}
	previous := make(map[string]domain.LegacyIgnoreDirectoryProof, len(o.Proofs))
	for rows.Next() {
		p, e := scanLegacyProof(rows)
		if e != nil {
			rows.Close()
			return e
		}
		previous[p.Directory] = p
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return storageError(err)
	}
	var priorSelected *string
	err = tx.QueryRow(ctx, `SELECT selected_directory FROM job_ignore_legacy_queries WHERE job_id=$1::uuid AND root_id=$2::uuid AND directory=$3`, l.Job.ID, root, o.Directory).Scan(&priorSelected)
	exists := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return storageError(err)
	}
	conflict := exists && (selected == nil) != (priorSelected == nil)
	if exists && selected != nil && priorSelected != nil && *selected != *priorSelected {
		conflict = true
	}
	for _, p := range o.Proofs {
		if old, ok := previous[p.Directory]; ok && !domain.LegacyIgnoreProofsCompatible(old, p) {
			conflict = true
		}
	}
	if conflict {
		_, err = tx.Exec(ctx, `UPDATE job_ignore_legacy_manifests SET invalidated=true WHERE job_id=$1::uuid`, l.Job.ID)
		if err != nil {
			return storageError(err)
		}
		return domain.ErrInventoryInvalidated
	}
	batch := &pgx.Batch{}
	for _, p := range o.Proofs {
		old, ok := previous[p.Directory]
		if ok && (old.Checked || !p.Checked) {
			continue
		}
		if frozen {
			return domain.ErrConflict
		}
		if !ok {
			count++
			charge += p.Charge() + 1
		}
		source += p.RuleSize
		var parent *string
		if p.Directory != "." {
			name := path.Dir(p.Directory)
			parent = &name
		}
		batch.Queue(`INSERT INTO job_ignore_legacy_proofs(job_id,root_id,directory,parent_path,parent_identity,identity,checked,rule_present,rule_identity,rule_size,rule_modified_nano,rule_sha256) VALUES($1::uuid,$2::uuid,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) ON CONFLICT(job_id,root_id,directory) DO UPDATE SET checked=EXCLUDED.checked,rule_present=EXCLUDED.rule_present,rule_identity=EXCLUDED.rule_identity,rule_size=EXCLUDED.rule_size,rule_modified_nano=EXCLUDED.rule_modified_nano,rule_sha256=EXCLUDED.rule_sha256`, l.Job.ID, root, p.Directory, parent, p.ParentIdentity[:], p.Identity[:], p.Checked, p.RulePresent, p.RuleIdentity[:], p.RuleSize, p.RuleModifiedNano, p.RuleSHA256[:])
	}
	if !exists {
		if frozen {
			return domain.ErrConflict
		}
		queries++
		charge += int64(128 + len(o.Directory))
		if selected != nil {
			charge += int64(len(*selected))
		}
		batch.Queue(`INSERT INTO job_ignore_legacy_queries(job_id,root_id,directory,selected_directory,proof_version) VALUES($1::uuid,$2::uuid,$3,$4,$5)`, l.Job.ID, root, o.Directory, selected, o.Version)
	}
	if count > domain.IgnoreManifestMaxRows || count > int64(l.Policy.MaxDirectories) || queries > domain.IgnoreManifestMaxRows || source > domain.IgnoreManifestMaxBytes || charge > domain.IgnoreManifestMaxBytes {
		return domain.ErrScanLimit
	}
	batch.Queue(`UPDATE job_ignore_legacy_manifests SET rows=$2,queries=$3,source_bytes=$4,charge_bytes=$5 WHERE job_id=$1::uuid`, l.Job.ID, count, queries, source, charge)
	return storageError(tx.SendBatch(ctx, batch).Close())
}
