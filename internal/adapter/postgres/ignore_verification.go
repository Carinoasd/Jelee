package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

type ignoreVerification struct {
	token    domain.IgnoreVerificationToken
	rows     int64
	digest   [32]byte
	complete bool
}

func loadIgnoreVerification(ctx context.Context, tx pgx.Tx, l domain.JobLease) (ignoreVerification, error) {
	v := ignoreVerification{}
	v.token.JobID = l.Job.ID
	var digest []byte
	var live bool
	err := tx.QueryRow(ctx, `SELECT generation,sequence,COALESCE(after_root_id::text,''),after_directory,verified_rows,digest,completed,deadline>clock_timestamp() FROM job_ignore_verifications WHERE job_id=$1::uuid FOR UPDATE`, l.Job.ID).Scan(&v.token.Generation, &v.token.Sequence, &v.token.After.RootID, &v.token.After.Directory, &v.rows, &digest, &v.complete, &live)
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

func verificationComparison(ctx context.Context, tx pgx.Tx, l domain.JobLease) (domain.JobLease, int64, error) {
	current, epoch, revision, err := ignoreComparisonFence(ctx, tx, l)
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

// The final guarded UPDATE is performed before rechecking the fixed evidence
// deadline, so a delayed database write cannot commit an expired checkpoint.
func commitIgnoreVerification(ctx context.Context, tx pgx.Tx, l domain.JobLease, epoch int64) error {
	if err := guardedJobUpdate(ctx, tx, l, `UPDATE jobs SET generation=generation WHERE id=$1::uuid`, l.Job.ID); err != nil {
		return err
	}
	if err := guardInventoryFinish(ctx, tx, l, &epoch, true); err != nil {
		return err
	}
	var live bool
	err := tx.QueryRow(ctx, `SELECT generation=$2 AND deadline>clock_timestamp() AND (sealed_until IS NULL OR sealed_until>clock_timestamp()) FROM job_ignore_verifications WHERE job_id=$1::uuid`, l.Job.ID, l.Generation).Scan(&live)
	if err != nil {
		return storageError(err)
	}
	if !live {
		return domain.ErrInventoryInvalidated
	}
	return storageError(tx.Commit(ctx))
}

// BeginIgnoreVerification freezes the manifest and starts a fixed deadline.
// Repeating it under the same lease does not renew evidence. A new generation
// must restart at the first proof and never inherits an old seal.
func (s *Store) BeginIgnoreVerification(ctx context.Context, l domain.JobLease) error {
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	current, epoch, err := verificationComparison(ctx, tx, l)
	if err != nil {
		return err
	}
	var generation int64
	err = tx.QueryRow(ctx, `SELECT generation FROM job_ignore_verifications WHERE job_id=$1::uuid FOR UPDATE`, l.Job.ID).Scan(&generation)
	if err == nil && generation == l.Generation {
		if _, err = loadIgnoreVerification(ctx, tx, l); err != nil {
			return err
		}
		return commitIgnoreVerification(ctx, tx, current, epoch)
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return storageError(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE job_ignore_manifests SET frozen=true WHERE job_id=$1::uuid`, l.Job.ID); err != nil {
		return storageError(err)
	}
	seed := sha256.Sum256([]byte("jelee-ignore-verification-v1"))
	_, err = tx.Exec(ctx, `INSERT INTO job_ignore_verifications(job_id,generation,deadline,digest) SELECT id,generation,LEAST(lease_until,clock_timestamp()+interval '120 seconds'),$2 FROM jobs WHERE id=$1::uuid ON CONFLICT(job_id) DO UPDATE SET generation=EXCLUDED.generation,deadline=EXCLUDED.deadline,sequence=0,after_root_id=NULL,after_directory='',verified_rows=0,digest=EXCLUDED.digest,completed=false,sealed_until=NULL`, l.Job.ID, seed[:])
	if err != nil {
		return storageError(err)
	}
	return commitIgnoreVerification(ctx, tx, current, epoch)
}

func verificationProofs(ctx context.Context, tx pgx.Tx, id string, after domain.IgnoreProofCursor) ([]domain.IgnoreDirectoryProof, error) {
	rows, err := tx.Query(ctx, listIgnoreProofsSQL, id, after.RootID, after.Directory, domain.IgnoreProofPageSize)
	if err != nil {
		return nil, storageError(err)
	}
	defer rows.Close()
	var result []domain.IgnoreDirectoryProof
	for rows.Next() {
		p, err := scanIgnoreProof(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	return result, storageError(rows.Err())
}

func (s *Store) NextIgnoreVerificationPage(ctx context.Context, l domain.JobLease) (domain.IgnoreVerificationPage, error) {
	return s.nextIgnoreVerificationPage(ctx, l, false)
}

func (s *Store) nextIgnoreVerificationPage(ctx context.Context, l domain.JobLease, family bool) (domain.IgnoreVerificationPage, error) {
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return domain.IgnoreVerificationPage{}, err
	}
	defer tx.Rollback(ctx)
	current, epoch, err := verificationModeComparison(ctx, tx, l, family)
	if err != nil {
		return domain.IgnoreVerificationPage{}, err
	}
	v, err := loadIgnoreVerification(ctx, tx, l)
	if err != nil {
		return domain.IgnoreVerificationPage{}, err
	}
	page := domain.IgnoreVerificationPage{Token: v.token, Complete: v.complete}
	if !v.complete {
		page.Proofs, err = verificationProofs(ctx, tx, l.Job.ID, v.token.After)
		if err != nil {
			return domain.IgnoreVerificationPage{}, err
		}
	}
	if err = commitVerificationMode(ctx, tx, current, epoch, family); err != nil {
		return domain.IgnoreVerificationPage{}, err
	}
	return page, nil
}

// Each row extends a versioned, length-framed chain. The result is independent
// of page boundaries, and includes every proof field, including absence.
func extendIgnoreVerification(prior [32]byte, p domain.IgnoreDirectoryProof) [32]byte {
	b := append([]byte("jelee-ignore-proof-v1"), prior[:]...)
	for _, s := range []string{p.RootID, p.Directory} {
		b = binary.BigEndian.AppendUint64(b, uint64(len(s)))
		b = append(b, s...)
	}
	for _, value := range [][32]byte{p.ParentIdentity, p.Identity, p.RuleIdentity, p.RuleSHA256} {
		b = append(b, value[:]...)
	}
	b = binary.BigEndian.AppendUint64(b, uint64(p.RuleSize))
	b = binary.BigEndian.AppendUint64(b, uint64(p.RuleModifiedNano))
	for _, value := range []bool{p.MissingDirectory, p.RulePresent} {
		if value {
			b = append(b, 1)
		} else {
			b = append(b, 0)
		}
	}
	return sha256.Sum256(b)
}

// Observations must be freshly obtained by the native source adapter. The
// database proves equality to the exact pending prefix, not filesystem truth.
func (s *Store) CommitIgnoreVerificationPage(ctx context.Context, l domain.JobLease, token domain.IgnoreVerificationToken, observed []domain.IgnoreDirectoryProof) error {
	return s.commitIgnoreVerificationPage(ctx, l, token, observed, false)
}

func (s *Store) commitIgnoreVerificationPage(ctx context.Context, l domain.JobLease, token domain.IgnoreVerificationToken, observed []domain.IgnoreDirectoryProof, family bool) error {
	if token.JobID != l.Job.ID || token.Generation != l.Generation || len(observed) > domain.IgnoreProofPageSize {
		return domain.ErrInvalid
	}
	for _, p := range observed {
		if err := domain.ValidateIgnoreDirectoryProof(p); err != nil {
			return err
		}
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	current, epoch, err := verificationModeComparison(ctx, tx, l, family)
	if err != nil {
		return err
	}
	v, err := loadIgnoreVerification(ctx, tx, l)
	if err != nil {
		return err
	}
	if token != v.token || v.complete {
		return domain.ErrConflict
	}
	expected, err := verificationProofs(ctx, tx, l.Job.ID, v.token.After)
	if err != nil {
		return err
	}
	if len(expected) != len(observed) {
		return domain.ErrConflict
	}
	for i, p := range expected {
		if p.RootID != observed[i].RootID || p.Directory != observed[i].Directory {
			return domain.ErrConflict
		}
		if p != observed[i] {
			if _, err = tx.Exec(ctx, `UPDATE job_ignore_manifests SET invalidated=true WHERE job_id=$1::uuid`, l.Job.ID); err != nil {
				return storageError(err)
			}
			if err = commitVerificationMode(ctx, tx, current, epoch, family); err != nil {
				return err
			}
			return domain.ErrInventoryInvalidated
		}
		v.digest = extendIgnoreVerification(v.digest, p)
	}
	v.rows += int64(len(expected))
	var total int64
	if err = tx.QueryRow(ctx, `SELECT rows FROM job_ignore_manifests WHERE job_id=$1::uuid`, l.Job.ID).Scan(&total); err != nil {
		return storageError(err)
	}
	if v.rows > total || len(expected) == 0 && v.rows != total {
		return domain.ErrInventoryInvalidated
	}
	if len(expected) > 0 {
		tail := expected[len(expected)-1]
		v.token.After = domain.IgnoreProofCursor{RootID: tail.RootID, Directory: tail.Directory}
	}
	_, err = tx.Exec(ctx, `UPDATE job_ignore_verifications SET sequence=sequence+1,after_root_id=NULLIF($2,'')::uuid,after_directory=$3,verified_rows=$4,digest=$5,completed=$6 WHERE job_id=$1::uuid`, l.Job.ID, v.token.After.RootID, v.token.After.Directory, v.rows, v.digest[:], len(expected) == 0)
	if err != nil {
		return storageError(err)
	}
	return commitVerificationMode(ctx, tx, current, epoch, family)
}

// Seal lifetime is capped by the original verification deadline and is never
// renewed by retries or heartbeat. Publication must check it again in its tx.
func (s *Store) SealIgnoreVerification(ctx context.Context, l domain.JobLease) error {
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	current, epoch, err := verificationComparison(ctx, tx, l)
	if err != nil {
		return err
	}
	v, err := loadIgnoreVerification(ctx, tx, l)
	if err != nil {
		return err
	}
	if !v.complete {
		return domain.ErrConflict
	}
	_, err = tx.Exec(ctx, `UPDATE job_ignore_verifications SET sealed_until=COALESCE(sealed_until,LEAST(deadline,clock_timestamp()+interval '30 seconds')) WHERE job_id=$1::uuid`, l.Job.ID)
	if err != nil {
		return storageError(err)
	}
	return commitIgnoreVerification(ctx, tx, current, epoch)
}
