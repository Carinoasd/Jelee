package postgres

import (
	"context"
	"crypto/sha256"
	"errors"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

func (s *Store) NextFamilyIgnoreVerificationPage(ctx context.Context, l domain.JobLease) (domain.IgnoreVerificationPage, error) {
	return s.nextIgnoreVerificationPage(ctx, l, true)
}

func (s *Store) CommitFamilyIgnoreVerificationPage(ctx context.Context, l domain.JobLease, token domain.IgnoreVerificationToken, observed []domain.IgnoreDirectoryProof) error {
	return s.commitIgnoreVerificationPage(ctx, l, token, observed, true)
}

func verificationModeComparison(ctx context.Context, tx pgx.Tx, l domain.JobLease, family bool) (domain.JobLease, int64, error) {
	if !family {
		return verificationComparison(ctx, tx, l)
	}
	current, epoch, err := familyVerificationComparison(ctx, tx, l)
	if err != nil {
		return current, epoch, err
	}
	if err = guardFamilyVerificationLifetime(ctx, tx, current); err != nil {
		return current, epoch, err
	}
	var frozen bool
	err = tx.QueryRow(ctx, `SELECT c.frozen AND m.frozen FROM job_ignore_manifests c JOIN job_ignore_legacy_manifests m USING(job_id) WHERE c.job_id=$1::uuid`, l.Job.ID).Scan(&frozen)
	if err != nil {
		return current, epoch, storageError(err)
	}
	if !frozen {
		return current, epoch, domain.ErrInventoryInvalidated
	}
	return current, epoch, nil
}

func commitVerificationMode(ctx context.Context, tx pgx.Tx, l domain.JobLease, epoch int64, family bool) error {
	if family {
		return commitFamilyVerification(ctx, tx, l, epoch)
	}
	return commitIgnoreVerification(ctx, tx, l, epoch)
}

func guardFamilyVerificationComplete(ctx context.Context, tx pgx.Tx, l domain.JobLease) error {
	if err := guardFamilyVerificationLifetime(ctx, tx, l); err != nil {
		return err
	}
	var complete bool
	err := tx.QueryRow(ctx, `SELECT c.completed AND s.completed AND b.completed AND c.verified_rows=m.rows AND s.verified_queries=n.queries AND b.verified_queries=n.baseline_queries AND m.frozen AND n.frozen AND NOT m.invalidated AND NOT n.invalidated FROM job_ignore_verifications c JOIN job_ignore_legacy_verifications s USING(job_id) JOIN job_ignore_legacy_baseline_verifications b USING(job_id) JOIN job_ignore_manifests m USING(job_id) JOIN job_ignore_legacy_manifests n USING(job_id) WHERE c.job_id=$1::uuid`, l.Job.ID).Scan(&complete)
	if err != nil {
		return storageError(err)
	}
	if !complete {
		return domain.ErrConflict
	}
	return nil
}

func (s *Store) SealFamilyIgnoreVerification(ctx context.Context, l domain.JobLease) error {
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	current, epoch, err := verificationModeComparison(ctx, tx, l, true)
	if err != nil {
		return err
	}
	if err = guardFamilyVerificationComplete(ctx, tx, current); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE job_ignore_verifications SET sealed_until=COALESCE(sealed_until,LEAST(deadline,clock_timestamp()+interval '30 seconds')) WHERE job_id=$1::uuid`, l.Job.ID)
	if err != nil {
		return storageError(err)
	}
	return commitFamilyVerification(ctx, tx, current, epoch)
}

func familyVerificationComparison(ctx context.Context, tx pgx.Tx, l domain.JobLease) (domain.JobLease, int64, error) {
	current, epoch, revision, err := comparisonModeFence(ctx, tx, l, true)
	if err != nil {
		return current, epoch, err
	}
	c, err := loadIgnoreComparison(ctx, tx, l.Job.ID)
	if err != nil {
		return current, epoch, err
	}
	if c.epoch != epoch || c.token.BaselineRevision != revision {
		return current, epoch, domain.ErrInventoryInvalidated
	}
	if !c.complete || c.counts.Unknown != 0 {
		return current, epoch, domain.ErrConflict
	}
	return current, epoch, nil
}

// BeginFamilyIgnoreVerification freezes both source manifests and creates all
// three checkpoints atomically. The custom checkpoint anchors their common
// deadline. A same-generation retry cannot reset progress or extend evidence.
func (s *Store) BeginFamilyIgnoreVerification(ctx context.Context, l domain.JobLease) error {
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	current, epoch, err := familyVerificationComparison(ctx, tx, l)
	if err != nil {
		return err
	}
	var generation int64
	err = tx.QueryRow(ctx, `SELECT generation FROM job_ignore_verifications WHERE job_id=$1::uuid FOR UPDATE`, l.Job.ID).Scan(&generation)
	if err == nil && generation == l.Generation {
		return commitFamilyVerification(ctx, tx, current, epoch)
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return storageError(err)
	}
	if err = familyBaselineRootCoverage(ctx, tx, current); err != nil {
		return err
	}
	var deadline time.Time
	// Independently started legacy checks from this same generation must
	// not gain a later deadline when joining the coordinated verification.
	err = tx.QueryRow(ctx, `SELECT LEAST(j.lease_until,clock_timestamp()+interval '120 seconds',
 (SELECT deadline FROM job_ignore_legacy_verifications WHERE job_id=j.id AND generation=j.generation),
 (SELECT deadline FROM job_ignore_legacy_baseline_verifications WHERE job_id=j.id AND generation=j.generation)) FROM jobs j WHERE j.id=$1::uuid`, l.Job.ID).Scan(&deadline)
	if err != nil {
		return storageError(err)
	}
	for _, table := range []string{"job_ignore_manifests", "job_ignore_legacy_manifests"} {
		if _, err = tx.Exec(ctx, `UPDATE `+table+` SET frozen=true WHERE job_id=$1::uuid`, l.Job.ID); err != nil {
			return storageError(err)
		}
	}
	seed := sha256.Sum256([]byte("jelee-ignore-verification-v1"))
	_, err = tx.Exec(ctx, `INSERT INTO job_ignore_verifications(job_id,generation,deadline,digest) VALUES($1::uuid,$2,$3,$4) ON CONFLICT(job_id) DO UPDATE SET generation=EXCLUDED.generation,deadline=EXCLUDED.deadline,sequence=0,after_root_id=NULL,after_directory='',verified_rows=0,digest=EXCLUDED.digest,completed=false,sealed_until=NULL`, l.Job.ID, l.Generation, deadline, seed[:])
	if err != nil {
		return storageError(err)
	}
	for _, spec := range []struct{ table, seed string }{
		{"job_ignore_legacy_verifications", "jelee-legacy-query-verification-v1"},
		{"job_ignore_legacy_baseline_verifications", "jelee-legacy-baseline-verification-v1"},
	} {
		seed = sha256.Sum256([]byte(spec.seed))
		_, err = tx.Exec(ctx, `INSERT INTO `+spec.table+`(job_id,generation,deadline,digest) VALUES($1::uuid,$2,$3,$4) ON CONFLICT(job_id) DO UPDATE SET generation=EXCLUDED.generation,deadline=EXCLUDED.deadline,sequence=0,after_root_id=NULL,after_directory='',verified_queries=0,digest=EXCLUDED.digest,completed=false`, l.Job.ID, l.Generation, deadline, seed[:])
		if err != nil {
			return storageError(err)
		}
	}
	return commitFamilyVerification(ctx, tx, current, epoch)
}

// This guard concerns checkpoint lifetime only. Keeping invalidation separate
// permits a failed source observation to commit its invalidation marker.
func guardFamilyVerificationLifetime(ctx context.Context, tx pgx.Tx, l domain.JobLease) error {
	var valid bool
	err := tx.QueryRow(ctx, `SELECT c.generation=$2 AND s.generation=$2 AND b.generation=$2 AND c.deadline=s.deadline AND c.deadline=b.deadline AND c.deadline>clock_timestamp() AND (c.sealed_until IS NULL OR c.sealed_until>clock_timestamp()) FROM job_ignore_verifications c JOIN job_ignore_legacy_verifications s USING(job_id) JOIN job_ignore_legacy_baseline_verifications b USING(job_id) WHERE c.job_id=$1::uuid`, l.Job.ID, l.Generation).Scan(&valid)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrInventoryInvalidated
	}
	if err != nil {
		return storageError(err)
	}
	if !valid {
		return domain.ErrInventoryInvalidated
	}
	return nil
}

func commitFamilyVerification(ctx context.Context, tx pgx.Tx, l domain.JobLease, epoch int64) error {
	if err := guardedJobUpdate(ctx, tx, l, `UPDATE jobs SET generation=generation WHERE id=$1::uuid`, l.Job.ID); err != nil {
		return err
	}
	if err := guardInventoryFinish(ctx, tx, l, &epoch, true); err != nil {
		return err
	}
	if err := guardFamilyVerificationLifetime(ctx, tx, l); err != nil {
		return err
	}
	return storageError(tx.Commit(ctx))
}
