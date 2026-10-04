package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"slices"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

type legacyBaselineVerification struct {
	token    domain.LegacyIgnoreBaselineVerificationToken
	count    int64
	digest   [32]byte
	complete bool
}

func loadLegacyBaselineVerification(ctx context.Context, tx pgx.Tx, l domain.JobLease) (legacyBaselineVerification, error) {
	v := legacyBaselineVerification{}
	v.token.JobID = l.Job.ID
	var digest []byte
	var live bool
	err := tx.QueryRow(ctx, `SELECT generation,sequence,COALESCE(after_root_id::text,''),after_directory,verified_queries,digest,completed,deadline>clock_timestamp() FROM job_ignore_legacy_baseline_verifications WHERE job_id=$1::uuid FOR UPDATE`, l.Job.ID).Scan(&v.token.Generation, &v.token.Sequence, &v.token.After.RootID, &v.token.After.Directory, &v.count, &digest, &v.complete, &live)
	if err != nil {
		return v, storageError(err)
	}
	if v.token.Generation != l.Generation {
		return v, domain.ErrJobLeaseLost
	}
	if !live {
		return v, domain.ErrInventoryInvalidated
	}
	if len(digest) != 32 {
		return v, domain.ErrDatabase
	}
	copy(v.digest[:], digest)
	return v, nil
}

func commitLegacyBaselineVerification(ctx context.Context, tx pgx.Tx, l domain.JobLease, epoch int64) error {
	if err := guardedJobUpdate(ctx, tx, l, `UPDATE jobs SET generation=generation WHERE id=$1::uuid`, l.Job.ID); err != nil {
		return err
	}
	if err := guardInventoryFinish(ctx, tx, l, &epoch, true); err != nil {
		return err
	}
	var live bool
	err := tx.QueryRow(ctx, `SELECT generation=$2 AND deadline>clock_timestamp() FROM job_ignore_legacy_baseline_verifications WHERE job_id=$1::uuid`, l.Job.ID, l.Generation).Scan(&live)
	if err != nil {
		return storageError(err)
	}
	if !live {
		return domain.ErrInventoryInvalidated
	}
	return storageError(tx.Commit(ctx))
}

// A fixed deadline cannot be renewed by repeating Begin or a heartbeat.
// A new lease generation restarts from the first query and an empty digest.
func (s *Store) BeginLegacyIgnoreBaselineVerification(ctx context.Context, l domain.JobLease) error {
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	current, epoch, err := legacyVerificationFence(ctx, tx, l)
	if err != nil {
		return err
	}
	var generation int64
	err = tx.QueryRow(ctx, `SELECT generation FROM job_ignore_legacy_baseline_verifications WHERE job_id=$1::uuid FOR UPDATE`, l.Job.ID).Scan(&generation)
	if err == nil && generation == l.Generation {
		if _, err = loadLegacyBaselineVerification(ctx, tx, l); err != nil {
			return err
		}
		return commitLegacyBaselineVerification(ctx, tx, current, epoch)
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return storageError(err)
	}
	seed := sha256.Sum256([]byte("jelee-legacy-baseline-verification-v1"))
	_, err = tx.Exec(ctx, `INSERT INTO job_ignore_legacy_baseline_verifications(job_id,generation,deadline,digest) SELECT id,generation,LEAST(lease_until,clock_timestamp()+interval '120 seconds'),$2 FROM jobs WHERE id=$1::uuid ON CONFLICT(job_id) DO UPDATE SET generation=EXCLUDED.generation,deadline=EXCLUDED.deadline,sequence=0,after_root_id=NULL,after_directory='',verified_queries=0,digest=EXCLUDED.digest,completed=false`, l.Job.ID, seed[:])
	if err != nil {
		return storageError(err)
	}
	return commitLegacyBaselineVerification(ctx, tx, current, epoch)
}

func (s *Store) NextLegacyIgnoreBaselineVerificationPage(ctx context.Context, l domain.JobLease) (domain.LegacyIgnoreBaselineVerificationPage, error) {
	empty := domain.LegacyIgnoreBaselineVerificationPage{}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback(ctx)
	current, epoch, err := legacyVerificationFence(ctx, tx, l)
	if err != nil {
		return empty, err
	}
	v, err := loadLegacyBaselineVerification(ctx, tx, l)
	if err != nil {
		return empty, err
	}
	page := domain.LegacyIgnoreBaselineVerificationPage{Token: v.token, Complete: v.complete}
	if !v.complete {
		page.Observations, err = legacyBaselineObservationPage(ctx, tx, l.Job.ID, v.token.After)
		if err != nil {
			return empty, err
		}
	}
	if err = commitLegacyBaselineVerification(ctx, tx, current, epoch); err != nil {
		return empty, err
	}
	return page, nil
}

func extendLegacyBaselineVerification(prior [32]byte, o domain.LegacyIgnoreBaselineObservation) [32]byte {
	b := append([]byte("jelee-legacy-baseline-query-v1"), prior[:]...)
	b = binary.BigEndian.AppendUint64(b, uint64(len(o.LookupDirectory)))
	b = append(b, o.LookupDirectory...)
	digest := sha256.Sum256(b)
	digest = extendLegacyVerification(digest, o.Source)
	return extendIgnoreVerification(digest, o.MissingDirectory)
}

func (s *Store) CommitLegacyIgnoreBaselineVerificationPage(ctx context.Context, l domain.JobLease, token domain.LegacyIgnoreBaselineVerificationToken, observed []domain.LegacyIgnoreBaselineObservation) error {
	if token.JobID != l.Job.ID || token.Generation != l.Generation || token.Sequence < 0 || len(observed) > legacyObservationPageSize {
		return domain.ErrInvalid
	}
	for _, o := range observed {
		if err := domain.ValidateLegacyIgnoreBaselineObservation(o); err != nil {
			return err
		}
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	current, epoch, err := legacyVerificationFence(ctx, tx, l)
	if err != nil {
		return err
	}
	v, err := loadLegacyBaselineVerification(ctx, tx, l)
	if err != nil {
		return err
	}
	if token != v.token || v.complete {
		return domain.ErrConflict
	}
	expected, err := legacyBaselineObservationPage(ctx, tx, l.Job.ID, v.token.After)
	if err != nil {
		return err
	}
	if len(expected) != len(observed) {
		return domain.ErrConflict
	}
	for i, o := range expected {
		got := observed[i] //nolint:gosec // G602: lengths are compared just above
		if o.LookupDirectory != got.LookupDirectory || o.Source.Proofs[0].RootID != got.Source.Proofs[0].RootID {
			return domain.ErrConflict
		}
		if o.Version != got.Version || o.MissingDirectory != got.MissingDirectory || o.Source.Version != got.Source.Version || o.Source.Directory != got.Source.Directory || !slices.Equal(o.Source.Proofs, got.Source.Proofs) {
			_, err = tx.Exec(ctx, `UPDATE job_ignore_legacy_manifests SET invalidated=true WHERE job_id=$1::uuid`, l.Job.ID)
			if err != nil {
				return storageError(err)
			}
			if err = commitLegacyBaselineVerification(ctx, tx, current, epoch); err != nil {
				return err
			}
			return domain.ErrInventoryInvalidated
		}
		v.digest = extendLegacyBaselineVerification(v.digest, o)
	}
	v.count += int64(len(expected))
	var total int64
	if err = tx.QueryRow(ctx, `SELECT baseline_queries FROM job_ignore_legacy_manifests WHERE job_id=$1::uuid`, l.Job.ID).Scan(&total); err != nil {
		return storageError(err)
	}
	if v.count > total || len(expected) == 0 && v.count != total {
		return domain.ErrInventoryInvalidated
	}
	if len(expected) > 0 {
		last := expected[len(expected)-1]
		v.token.After = domain.IgnoreProofCursor{RootID: last.Source.Proofs[0].RootID, Directory: last.LookupDirectory}
	}
	_, err = tx.Exec(ctx, `UPDATE job_ignore_legacy_baseline_verifications SET sequence=sequence+1,after_root_id=NULLIF($2,'')::uuid,after_directory=$3,verified_queries=$4,digest=$5,completed=$6 WHERE job_id=$1::uuid`, l.Job.ID, v.token.After.RootID, v.token.After.Directory, v.count, v.digest[:], len(expected) == 0)
	if err != nil {
		return storageError(err)
	}
	return commitLegacyBaselineVerification(ctx, tx, current, epoch)
}
