package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

var (
	_ app.NFOWriteExecutionRepository = (*Store)(nil)
	_ app.NFOWriteJobRepository       = (*Store)(nil)
)

// readNFOCommitSettlement summarizes the append-only phase rows of a token.
func readNFOCommitSettlement(ctx context.Context, tx pgx.Tx, token string) (domain.NFOWriteCommitSettlement, error) {
	var result domain.NFOWriteCommitSettlement
	rows, err := tx.Query(ctx, `SELECT phase,attempt,recorded_at FROM nfo_write_commit_settlements WHERE token=$1::uuid ORDER BY phase`, token)
	if err != nil {
		return result, storageError(err)
	}
	defer rows.Close()
	seen := false
	for rows.Next() {
		var phase, attempt int16
		var recorded time.Time
		if err := rows.Scan(&phase, &attempt, &recorded); err != nil {
			return domain.NFOWriteCommitSettlement{}, storageError(err)
		}
		value := domain.NFOWriteCommitSettlementPhase(phase)
		if !value.Valid() || attempt < 0 || uint8(attempt) > domain.NFOWriteCommitAttemptLimit || seen && uint8(attempt) != result.Attempt || result.Phase.Terminal() {
			return domain.NFOWriteCommitSettlement{}, domain.ErrDatabase
		}
		seen = true
		result.Attempt = uint8(attempt)
		result.Phase = value
		if value == domain.NFOWriteCommitBackedUp {
			result.BackedUpAt = recorded
		} else {
			result.SettledAt = recorded
		}
	}
	if err := rows.Err(); err != nil {
		return domain.NFOWriteCommitSettlement{}, storageError(err)
	}
	if result.Phase == domain.NFOWriteCommitReplaced && result.BackedUpAt.IsZero() {
		return domain.NFOWriteCommitSettlement{}, domain.ErrDatabase
	}
	var legacyOutput, legacyRollback, attemptOutput, attemptRollback []byte
	var selected *int16
	err = tx.QueryRow(ctx, `SELECT (SELECT output_identity FROM nfo_write_commit_files_ready WHERE token=$1::uuid),(SELECT rollback_identity FROM nfo_write_commit_files_ready WHERE token=$1::uuid),(SELECT attempt FROM nfo_write_commit_attempt_ready WHERE token=$1::uuid),(SELECT output_identity FROM nfo_write_commit_attempt_ready WHERE token=$1::uuid),(SELECT rollback_identity FROM nfo_write_commit_attempt_ready WHERE token=$1::uuid)`, token).Scan(&legacyOutput, &legacyRollback, &selected, &attemptOutput, &attemptRollback)
	if err != nil {
		return domain.NFOWriteCommitSettlement{}, storageError(err)
	}
	output, rollback := legacyOutput, legacyRollback
	if selected != nil {
		if legacyOutput != nil || *selected < 1 || uint8(*selected) > domain.NFOWriteCommitAttemptLimit {
			return domain.NFOWriteCommitSettlement{}, domain.ErrDatabase
		}
		result.ReadyAttempt, output, rollback = uint8(*selected), attemptOutput, attemptRollback
	}
	if output != nil {
		if len(output) != 48 || len(rollback) != 48 {
			return domain.NFOWriteCommitSettlement{}, domain.ErrDatabase
		}
		copy(result.Ready.OutputIdentity[:], output)
		copy(result.Ready.RollbackIdentity[:], rollback)
		if domain.ValidateNFOWriteCommitFilesReady(result.Ready) != nil {
			return domain.NFOWriteCommitSettlement{}, domain.ErrDatabase
		}
		result.ReadyRecorded = true
	}
	if seen && (!result.ReadyRecorded || result.ReadyAttempt != result.Attempt) {
		return domain.NFOWriteCommitSettlement{}, domain.ErrDatabase
	}
	return result, nil
}

// GetNFOWriteCommitSettlement observes the durable phases under the same fence
// as other evidence reads. It never derives a phase from the filesystem.
func (s *Store) GetNFOWriteCommitSettlement(ctx context.Context, lease domain.JobLease, sequence int, token string) (domain.NFOWriteCommitSettlement, error) {
	if ctx == nil || sequence < 1 || sequence > 100 || !domain.ValidID(token) {
		return domain.NFOWriteCommitSettlement{}, domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return domain.NFOWriteCommitSettlement{}, err
	}
	defer tx.Rollback(ctx)
	current, err := fencedNFOCommitLease(ctx, tx, lease)
	if err != nil {
		return domain.NFOWriteCommitSettlement{}, err
	}
	if current.Job.Kind != domain.JobNFOWrite {
		return domain.NFOWriteCommitSettlement{}, domain.ErrInvalid
	}
	if current.Job.CancelRequested && lease.RecoveryEpoch == 0 {
		return domain.NFOWriteCommitSettlement{}, context.Canceled
	}
	var owned string
	if err := tx.QueryRow(ctx, `SELECT token::text FROM nfo_write_commit_journal WHERE token=$1::uuid AND job_id=$2::uuid AND sequence=$3 AND generation=$4 AND (owner=$5 OR $6)`, token, lease.Job.ID, sequence, current.Generation, lease.Owner, lease.RecoveryEpoch != 0).Scan(&owned); err != nil {
		return domain.NFOWriteCommitSettlement{}, storageError(err)
	}
	result, err := readNFOCommitSettlement(ctx, tx, token)
	if err != nil {
		return domain.NFOWriteCommitSettlement{}, err
	}
	if err := nfoCommitLeaseLive(ctx, tx, current); err != nil {
		return domain.NFOWriteCommitSettlement{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.NFOWriteCommitSettlement{}, storageError(err)
	}
	return result, nil
}

// SaveNFOWriteCommitSettlement records one phase. Replay keeps the first time.
// SQL triggers enforce forward-only phases, the selected ready attempt, the
// live lease, the actor, and (for backup and replacement) the catalog scope.
func (s *Store) SaveNFOWriteCommitSettlement(ctx context.Context, lease domain.JobLease, sequence int, token string, attempt uint8, phase domain.NFOWriteCommitSettlementPhase) (domain.NFOWriteCommitSettlement, error) {
	if ctx == nil || !domain.ValidID(token) || !phase.Valid() || attempt > domain.NFOWriteCommitAttemptLimit {
		return domain.NFOWriteCommitSettlement{}, domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return domain.NFOWriteCommitSettlement{}, err
	}
	defer tx.Rollback(ctx)
	if phase == domain.NFOWriteCommitRolledBack {
		// Rollback only restores the prepared original and must survive catalog
		// drift; it keeps the lease, actor and token ownership fences.
		err = checkNFOCommitSettlementToken(ctx, tx, lease, sequence, token)
	} else {
		err = checkNFOCommitFileToken(ctx, tx, lease, sequence, token)
	}
	if err != nil {
		return domain.NFOWriteCommitSettlement{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO nfo_write_commit_settlements(token,phase,attempt,recorded_at,lease_until) VALUES($1::uuid,$2,$3,clock_timestamp(),clock_timestamp()+interval '1 second') ON CONFLICT(token,phase) DO NOTHING`, token, int16(phase), int16(attempt)); err != nil {
		return domain.NFOWriteCommitSettlement{}, storageError(err)
	}
	var saved int16
	// No-op UPDATE fires the deferred live-lease check on replay too.
	if err := tx.QueryRow(ctx, `UPDATE nfo_write_commit_settlements SET token=token WHERE token=$1::uuid AND phase=$2 RETURNING attempt`, token, int16(phase)).Scan(&saved); err != nil {
		return domain.NFOWriteCommitSettlement{}, storageError(err)
	}
	if saved != int16(attempt) {
		return domain.NFOWriteCommitSettlement{}, domain.ErrConflict
	}
	result, err := readNFOCommitSettlement(ctx, tx, token)
	if err != nil {
		return domain.NFOWriteCommitSettlement{}, err
	}
	// Progress is derived, so replay cannot double count it.
	if _, err := tx.Exec(ctx, `UPDATE jobs SET files=(SELECT count(*) FROM nfo_write_commit_settlements s JOIN nfo_write_commit_journal w ON w.token=s.token WHERE w.job_id=$1::uuid AND s.phase=2),skipped=(SELECT count(*) FROM nfo_write_commit_settlements s JOIN nfo_write_commit_journal w ON w.token=s.token WHERE w.job_id=$1::uuid AND s.phase=3) WHERE id=$1::uuid`, lease.Job.ID); err != nil {
		return domain.NFOWriteCommitSettlement{}, storageError(err)
	}
	if phase != domain.NFOWriteCommitRolledBack {
		if err := checkNFOCommitCatalogScope(ctx, tx, lease.Job.ID, sequence); err != nil {
			return domain.NFOWriteCommitSettlement{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.NFOWriteCommitSettlement{}, storageError(err)
	}
	return result, nil
}

// checkNFOCommitSettlementToken is checkNFOCommitFileToken without the catalog
// comparison. It still fences the lease, actor and journal ownership.
func checkNFOCommitSettlementToken(ctx context.Context, tx pgx.Tx, lease domain.JobLease, sequence int, token string) error {
	record, err := recordNFOWriteCommit(ctx, tx, lease, sequence)
	if err != nil {
		return err
	}
	if record.Token != token {
		return domain.ErrConflict
	}
	var existing string
	if err := tx.QueryRow(ctx, `SELECT token::text FROM nfo_write_commit_journal WHERE token=$1::uuid AND job_id=$2::uuid AND sequence=$3 AND generation=$4 AND (owner=$5 OR $6)`, token, lease.Job.ID, sequence, record.Generation, lease.Owner, lease.RecoveryEpoch != 0).Scan(&existing); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrJobLeaseLost
		}
		return storageError(err)
	}
	return nil
}

// ListNFOWriteCommitEntries observes every entry of the job in sequence order.
// Entries without a journal for the current generation have an empty token.
func (s *Store) ListNFOWriteCommitEntries(ctx context.Context, lease domain.JobLease) ([]domain.NFOWriteCommitEntryState, error) {
	if ctx == nil {
		return nil, domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	current, err := fencedNFOCommitLease(ctx, tx, lease)
	if err != nil {
		return nil, err
	}
	if current.Job.Kind != domain.JobNFOWrite {
		return nil, domain.ErrInvalid
	}
	if current.Job.CancelRequested && lease.RecoveryEpoch == 0 {
		return nil, context.Canceled
	}
	rows, err := tx.Query(ctx, `SELECT e.sequence,COALESCE(w.token::text,''),p.token IS NOT NULL,r.token IS NOT NULL OR a.token IS NOT NULL,COALESCE((SELECT max(phase) FROM nfo_write_commit_settlements s WHERE s.token=w.token),0)
 FROM nfo_write_entries e LEFT JOIN nfo_write_commit_journal w ON w.job_id=e.job_id AND w.sequence=e.sequence AND w.generation=$2
 LEFT JOIN nfo_write_commit_file_plans p ON p.token=w.token LEFT JOIN nfo_write_commit_files_ready r ON r.token=w.token LEFT JOIN nfo_write_commit_attempt_ready a ON a.token=w.token
 WHERE e.job_id=$1::uuid ORDER BY e.sequence`, lease.Job.ID, current.Generation)
	if err != nil {
		return nil, storageError(err)
	}
	var result []domain.NFOWriteCommitEntryState
	for rows.Next() {
		var entry domain.NFOWriteCommitEntryState
		var phase int16
		if err := rows.Scan(&entry.Sequence, &entry.Token, &entry.PlanRecorded, &entry.ReadyRecorded, &phase); err != nil {
			rows.Close()
			return nil, storageError(err)
		}
		entry.Settlement = domain.NFOWriteCommitSettlementPhase(phase)
		if entry.Sequence != len(result)+1 || phase != 0 && !entry.Settlement.Valid() || entry.Token == "" && (entry.PlanRecorded || phase != 0) {
			rows.Close()
			return nil, domain.ErrDatabase
		}
		result = append(result, entry)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, storageError(err)
	}
	if len(result) < 1 || len(result) > 100 {
		return nil, domain.ErrDatabase
	}
	if err := nfoCommitLeaseLive(ctx, tx, current); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, storageError(err)
	}
	return result, nil
}

func validNFOWriteFinish(state, code string) bool {
	if state == domain.JobSucceeded || state == domain.JobCancelled {
		return code == ""
	}
	return state == domain.JobFailed && (code == "nfo_write_failed" || code == "job_timeout" || code == "scan_unavailable")
}

// FinishNFOWriteJob stops the running lease. Success requires a replaced
// settlement for every entry, even if cancellation arrived after the last
// replacement committed. A job whose journal has no token between backup and a
// terminal phase is resolved in the same transaction; otherwise it stays
// unresolved for the recovery lease, which may still need to roll back. A
// cancelled stop also sets the persisted flag, so SQL keeps recovery rollback-only.
func (s *Store) FinishNFOWriteJob(ctx context.Context, lease domain.JobLease, state, code string) (bool, error) {
	if ctx == nil || !validNFOWriteFinish(state, code) {
		return false, domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	current, err := fencedJob(ctx, tx, lease)
	if err != nil {
		return false, err
	}
	if current.Job.Kind != domain.JobNFOWrite {
		return false, domain.ErrInvalid
	}
	var journaled, pending, complete bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nfo_write_commit_journal WHERE job_id=$1::uuid),
 EXISTS(SELECT 1 FROM nfo_write_commit_journal w JOIN nfo_write_commit_settlements s ON s.token=w.token AND s.phase=1 WHERE w.job_id=$1::uuid AND NOT EXISTS(SELECT 1 FROM nfo_write_commit_settlements t WHERE t.token=w.token AND t.phase>1)),
 NOT EXISTS(SELECT 1 FROM nfo_write_entries e WHERE e.job_id=$1::uuid AND NOT EXISTS(SELECT 1 FROM nfo_write_commit_journal w JOIN nfo_write_commit_settlements s ON s.token=w.token AND s.phase=2 WHERE w.job_id=e.job_id AND w.sequence=e.sequence AND w.generation=$2))`, lease.Job.ID, current.Generation).Scan(&journaled, &pending, &complete)
	if err != nil {
		return false, storageError(err)
	}
	if state == domain.JobSucceeded && (!journaled || pending || !complete) {
		return false, domain.ErrConflict
	}
	resolved := journaled && !pending
	if resolved {
		if _, err := tx.Exec(ctx, `INSERT INTO nfo_write_commit_resolutions(job_id,owner,epoch,recorded_at) VALUES($1::uuid,$2,0,clock_timestamp())`, lease.Job.ID, lease.Owner); err != nil {
			return false, storageError(err)
		}
	}
	if err := guardedJobUpdate(ctx, tx, lease, `UPDATE jobs SET state=$2,error_code=$3,cancel_requested=cancel_requested OR $2='cancelled',finished_at=clock_timestamp(),owner=NULL,lease_until=NULL WHERE id=$1::uuid`, lease.Job.ID, state, code); err != nil {
		return false, err
	}
	if err := auditAccount(ctx, tx, domain.Actor{}, "job.finished", lease.Job.ID, nil, map[string]any{"state": state, "errorCode": code, "resolved": resolved}); err != nil {
		return false, err
	}
	if state == domain.JobSucceeded {
		if err := appendNFOWritten(ctx, tx, s.webhooksOn(), lease.Job.ID); err != nil {
			return false, err
		}
	}
	if err := trimJobs(ctx, tx, current.Policy.HistoryLimit); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, storageError(err)
	}
	return resolved || !journaled, nil
}

// ListNFOWriteCommitRecoveries returns stopped, unresolved journaled jobs whose
// recovery lease is absent or expired, oldest first. It grants nothing.
func (s *Store) ListNFOWriteCommitRecoveries(ctx context.Context, limit int) ([]string, error) {
	if ctx == nil || limit < 1 || limit > 100 {
		return nil, domain.ErrInvalid
	}
	rows, err := s.Pool.Query(ctx, `SELECT j.id::text FROM jobs j WHERE j.kind='nfo_write' AND j.state IN ('failed','cancelled') AND j.owner IS NULL
 AND EXISTS(SELECT 1 FROM nfo_write_commit_journal w WHERE w.job_id=j.id)
 AND NOT EXISTS(SELECT 1 FROM nfo_write_commit_resolutions r WHERE r.job_id=j.id)
 AND NOT EXISTS(SELECT 1 FROM nfo_write_commit_recovery_leases l WHERE l.job_id=j.id AND l.lease_until>clock_timestamp())
 ORDER BY j.finished_at,j.id LIMIT $1`, limit)
	if err != nil {
		return nil, storageError(err)
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, storageError(err)
		}
		result = append(result, id)
	}
	return result, storageError(rows.Err())
}

// CompleteNFOWriteCommitRecovery resolves the stopped job under the live
// recovery lease. SQL refuses while a token is between backup and a terminal
// phase. A failed, uncancelled job becomes succeeded only when every entry
// has a replaced settlement; otherwise its stopped state is kept. Replay by the
// same holder returns the current state.
func (s *Store) CompleteNFOWriteCommitRecovery(ctx context.Context, lease domain.JobLease) (string, error) {
	if ctx == nil || lease.RecoveryEpoch < 1 {
		return "", domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	current, err := fencedNFOCommitRecovery(ctx, tx, lease)
	if err != nil {
		return "", err
	}
	var owner string
	var epoch int64
	err = tx.QueryRow(ctx, `SELECT owner,epoch FROM nfo_write_commit_resolutions WHERE job_id=$1::uuid`, lease.Job.ID).Scan(&owner, &epoch)
	if errors.Is(err, pgx.ErrNoRows) {
		_, err = tx.Exec(ctx, `INSERT INTO nfo_write_commit_resolutions(job_id,owner,epoch,recorded_at) VALUES($1::uuid,$2,$3,clock_timestamp())`, lease.Job.ID, lease.Owner, lease.RecoveryEpoch)
	} else if err == nil && (owner != lease.Owner || epoch != lease.RecoveryEpoch) {
		return "", domain.ErrJobLeaseLost
	}
	if err != nil {
		return "", storageError(err)
	}
	state := current.Job.State
	if state == domain.JobFailed && !current.Job.CancelRequested {
		var complete bool
		if err := tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM nfo_write_entries e WHERE e.job_id=$1::uuid AND NOT EXISTS(SELECT 1 FROM nfo_write_commit_journal w JOIN nfo_write_commit_settlements s ON s.token=w.token AND s.phase=2 WHERE w.job_id=e.job_id AND w.sequence=e.sequence AND w.generation=$2))`, lease.Job.ID, current.Generation).Scan(&complete); err != nil {
			return "", storageError(err)
		}
		if complete {
			if _, err := tx.Exec(ctx, `UPDATE jobs SET state='succeeded',error_code='',finished_at=clock_timestamp() WHERE id=$1::uuid AND state='failed' AND owner IS NULL`, lease.Job.ID); err != nil {
				return "", storageError(err)
			}
			state = domain.JobSucceeded
		}
	}
	if err := auditAccount(ctx, tx, domain.Actor{}, "job.recovered", lease.Job.ID, nil, map[string]any{"state": state, "epoch": lease.RecoveryEpoch}); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", storageError(err)
	}
	return state, nil
}

// appendNFOWritten raises nfo.written (G12.1) for every item of a succeeded
// NFO write job, in the transaction that finishes it. Paths are never part
// of the event.
func appendNFOWritten(ctx context.Context, tx pgx.Tx, enabled bool, job string) error {
	if !enabled {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT item_id::text,library_id::text FROM nfo_write_entries WHERE job_id=$1::uuid ORDER BY sequence`, job)
	if err != nil {
		return storageError(err)
	}
	type entry struct{ item, library string }
	var entries []entry
	for rows.Next() {
		var e entry
		if err = rows.Scan(&e.item, &e.library); err != nil {
			rows.Close()
			return storageError(err)
		}
		entries = append(entries, e)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return storageError(err)
	}
	now := time.Now()
	for _, e := range entries {
		if err = appendWebhook(ctx, tx, true, domain.WebhookNFOWritten, now, domain.WebhookSubject{Kind: domain.WebhookSubjectItem, ID: e.item}, map[string]any{"jobId": job, "libraryId": e.library}); err != nil {
			return err
		}
	}
	return nil
}
