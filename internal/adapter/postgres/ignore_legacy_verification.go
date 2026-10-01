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

type legacyVerification struct {
	token    domain.LegacyIgnoreVerificationToken
	count    int64
	digest   [32]byte
	complete bool
}

func legacyVerificationFence(ctx context.Context, tx pgx.Tx, l domain.JobLease) (domain.JobLease, int64, error) {
	current, epoch, err := legacyManifestFence(ctx, tx, l)
	if err != nil {
		return current, epoch, err
	}
	var ready bool
	err = tx.QueryRow(ctx, `SELECT frozen AND NOT invalidated AND inventory_generation=$2 FROM job_ignore_legacy_manifests WHERE job_id=$1::uuid FOR UPDATE`, l.Job.ID, epoch).Scan(&ready)
	if err != nil {
		return current, epoch, storageError(err)
	}
	if !ready {
		return current, epoch, domain.ErrInventoryInvalidated
	}
	return current, epoch, nil
}

func loadLegacyVerification(ctx context.Context, tx pgx.Tx, l domain.JobLease) (legacyVerification, error) {
	v := legacyVerification{}
	v.token.JobID = l.Job.ID
	var digest []byte
	var live bool
	err := tx.QueryRow(ctx, `SELECT generation,sequence,COALESCE(after_root_id::text,''),after_directory,verified_queries,digest,completed,deadline>clock_timestamp() FROM job_ignore_legacy_verifications WHERE job_id=$1::uuid FOR UPDATE`, l.Job.ID).Scan(&v.token.Generation, &v.token.Sequence, &v.token.After.RootID, &v.token.After.Directory, &v.count, &digest, &v.complete, &live)
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

func commitLegacyVerification(ctx context.Context, tx pgx.Tx, l domain.JobLease, epoch int64) error {
	if err := guardedJobUpdate(ctx, tx, l, `UPDATE jobs SET generation=generation WHERE id=$1::uuid`, l.Job.ID); err != nil {
		return err
	}
	if err := guardInventoryFinish(ctx, tx, l, &epoch, true); err != nil {
		return err
	}
	var live bool
	err := tx.QueryRow(ctx, `SELECT generation=$2 AND deadline>clock_timestamp() FROM job_ignore_legacy_verifications WHERE job_id=$1::uuid`, l.Job.ID, l.Generation).Scan(&live)
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
func (s *Store) BeginLegacyIgnoreVerification(ctx context.Context, l domain.JobLease) error {
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
	err = tx.QueryRow(ctx, `SELECT generation FROM job_ignore_legacy_verifications WHERE job_id=$1::uuid FOR UPDATE`, l.Job.ID).Scan(&generation)
	if err == nil && generation == l.Generation {
		if _, err = loadLegacyVerification(ctx, tx, l); err != nil {
			return err
		}
		return commitLegacyVerification(ctx, tx, current, epoch)
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return storageError(err)
	}
	seed := sha256.Sum256([]byte("jelee-legacy-query-verification-v1"))
	_, err = tx.Exec(ctx, `INSERT INTO job_ignore_legacy_verifications(job_id,generation,deadline,digest) SELECT id,generation,LEAST(lease_until,clock_timestamp()+interval '120 seconds'),$2 FROM jobs WHERE id=$1::uuid ON CONFLICT(job_id) DO UPDATE SET generation=EXCLUDED.generation,deadline=EXCLUDED.deadline,sequence=0,after_root_id=NULL,after_directory='',verified_queries=0,digest=EXCLUDED.digest,completed=false`, l.Job.ID, seed[:])
	if err != nil {
		return storageError(err)
	}
	return commitLegacyVerification(ctx, tx, current, epoch)
}

func (s *Store) NextLegacyIgnoreVerificationPage(ctx context.Context, l domain.JobLease) (domain.LegacyIgnoreVerificationPage, error) {
	empty := domain.LegacyIgnoreVerificationPage{}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback(ctx)
	current, epoch, err := legacyVerificationFence(ctx, tx, l)
	if err != nil {
		return empty, err
	}
	v, err := loadLegacyVerification(ctx, tx, l)
	if err != nil {
		return empty, err
	}
	page := domain.LegacyIgnoreVerificationPage{Token: v.token, Complete: v.complete}
	if !v.complete {
		page.Observations, err = legacyObservationPage(ctx, tx, l.Job.ID, v.token.After)
		if err != nil {
			return empty, err
		}
	}
	if err = commitLegacyVerification(ctx, tx, current, epoch); err != nil {
		return empty, err
	}
	return page, nil
}

func extendLegacyVerification(prior [32]byte, o domain.LegacyIgnoreObservation) [32]byte {
	b := append([]byte("jelee-legacy-query-v1"), prior[:]...)
	b = binary.BigEndian.AppendUint64(b, uint64(len(o.Directory)))
	b = append(b, o.Directory...)
	b = binary.BigEndian.AppendUint64(b, uint64(len(o.Proofs)))
	digest := sha256.Sum256(b)
	for _, p := range o.Proofs {
		digest = extendIgnoreVerification(digest, p.IgnoreDirectoryProof)
		b = append(digest[:len(digest):len(digest)], 0)
		if p.Checked {
			b[len(b)-1] = 1
		}
		digest = sha256.Sum256(b)
	}
	return digest
}

func (s *Store) CommitLegacyIgnoreVerificationPage(ctx context.Context, l domain.JobLease, token domain.LegacyIgnoreVerificationToken, observed []domain.LegacyIgnoreObservation) error {
	if token.JobID != l.Job.ID || token.Generation != l.Generation || token.Sequence < 0 || len(observed) > legacyObservationPageSize {
		return domain.ErrInvalid
	}
	for _, o := range observed {
		if err := domain.ValidateLegacyIgnoreObservation(o); err != nil {
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
	v, err := loadLegacyVerification(ctx, tx, l)
	if err != nil {
		return err
	}
	if token != v.token || v.complete {
		return domain.ErrConflict
	}
	expected, err := legacyObservationPage(ctx, tx, l.Job.ID, v.token.After)
	if err != nil {
		return err
	}
	if len(expected) != len(observed) {
		return domain.ErrConflict
	}
	for i, o := range expected {
		got := observed[i]
		if o.Directory != got.Directory || o.Proofs[0].RootID != got.Proofs[0].RootID {
			return domain.ErrConflict
		}
		if o.Version != got.Version || !slices.Equal(o.Proofs, got.Proofs) {
			_, err = tx.Exec(ctx, `UPDATE job_ignore_legacy_manifests SET invalidated=true WHERE job_id=$1::uuid`, l.Job.ID)
			if err != nil {
				return storageError(err)
			}
			if err = commitLegacyVerification(ctx, tx, current, epoch); err != nil {
				return err
			}
			return domain.ErrInventoryInvalidated
		}
		v.digest = extendLegacyVerification(v.digest, o)
	}
	v.count += int64(len(expected))
	var total int64
	if err = tx.QueryRow(ctx, `SELECT queries FROM job_ignore_legacy_manifests WHERE job_id=$1::uuid`, l.Job.ID).Scan(&total); err != nil {
		return storageError(err)
	}
	if v.count > total || len(expected) == 0 && v.count != total {
		return domain.ErrInventoryInvalidated
	}
	if len(expected) > 0 {
		last := expected[len(expected)-1]
		v.token.After = domain.IgnoreProofCursor{RootID: last.Proofs[0].RootID, Directory: last.Directory}
	}
	_, err = tx.Exec(ctx, `UPDATE job_ignore_legacy_verifications SET sequence=sequence+1,after_root_id=NULLIF($2,'')::uuid,after_directory=$3,verified_queries=$4,digest=$5,completed=$6 WHERE job_id=$1::uuid`, l.Job.ID, v.token.After.RootID, v.token.After.Directory, v.count, v.digest[:], len(expected) == 0)
	if err != nil {
		return storageError(err)
	}
	return commitLegacyVerification(ctx, tx, current, epoch)
}
