package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

// Safe, reversible repairs of the consistency checker (G50.3). A repair only
// clears a reference that points at another item's version or rewrites
// statistics counters to their recount; nothing is deleted. Each applied
// repair is journaled with its exact before and after values, so
// RevertConsistencyRun can restore the before values while a row still holds
// the repaired ones. Every batch is audited.

const (
	fixUserData = "user_item_data.last_source_id"
	fixSession  = "playback_sessions.source_id"
	fixDaily    = "watch_stats_daily.counters"
	// consistencyRevertBatch bounds the journal rows one revert transaction
	// restores.
	consistencyRevertBatch = 500
)

// dailyCounters are the recountable counters of one watch_stats_daily row.
type dailyCounters struct {
	Sessions        int64 `json:"sessions"`
	FirstPlays      int64 `json:"firstPlays"`
	Rewatches       int64 `json:"rewatches"`
	Views           int64 `json:"views"`
	Completions     int64 `json:"completions"`
	CompletionMilli int64 `json:"completionMilli"`
}

type fixTarget struct {
	UserID    string `json:"userId,omitempty"`
	ItemID    string `json:"itemId,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
	Day       string `json:"day,omitempty"`
}

type sourceState struct {
	SourceID *string `json:"sourceId"`
}

func journalFix(ctx context.Context, tx pgx.Tx, run, fix string, target, before, after any) error {
	encoded := make([][]byte, 3)
	for i, v := range []any{target, before, after} {
		data, err := json.Marshal(v)
		if err != nil {
			return domain.ErrInvalid
		}
		encoded[i] = data
	}
	_, err := tx.Exec(ctx, `INSERT INTO consistency_fix_journal(run_id,fix,target,before_state,after_state) VALUES($1::uuid,$2,$3::jsonb,$4::jsonb,$5::jsonb)`, run, fix, encoded[0], encoded[1], encoded[2])
	return storageError(err)
}

func lockWatchStats(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout='5s'`); err != nil {
		return storageError(err)
	}
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext(current_schema()),$1)`, watchStatsLock)
	return storageError(err)
}

// ApplyConsistencyFixes applies the fixable findings of one page in one
// transaction. A finding whose row no longer matches what the check read is
// skipped. The run must be a running fix run.
func (s *Store) ApplyConsistencyFixes(ctx context.Context, runID string, fixes []domain.ConsistencyFinding) (domain.ConsistencyFixResult, error) {
	if ctx == nil || !domain.ValidID(runID) || len(fixes) == 0 || len(fixes) > domain.ConsistencyPageSize {
		return domain.ConsistencyFixResult{}, domain.ErrInvalid
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return domain.ConsistencyFixResult{}, storageError(err)
	}
	defer tx.Rollback(ctx)
	var mode string
	if err = tx.QueryRow(ctx, `SELECT mode FROM consistency_runs WHERE id=$1::uuid AND state='running' FOR UPDATE`, runID).Scan(&mode); err != nil {
		return domain.ConsistencyFixResult{}, storageError(err)
	}
	if mode != domain.ConsistencyModeFix {
		return domain.ConsistencyFixResult{}, domain.ErrConflict
	}
	for _, f := range fixes {
		if f.Code == domain.ConsistencyDailyCounterDrift {
			// The roll-up adds deltas under this lock; a recount must not
			// interleave with a batch.
			if err = lockWatchStats(ctx, tx); err != nil {
				return domain.ConsistencyFixResult{}, err
			}
			break
		}
	}
	var result domain.ConsistencyFixResult
	codes := map[string]int64{}
	for _, f := range fixes {
		applied, err := applyConsistencyFix(ctx, tx, runID, f)
		if err != nil {
			return domain.ConsistencyFixResult{}, err
		}
		if applied {
			result.Applied++
			codes[f.Code]++
		} else {
			result.Skipped++
		}
	}
	if err = auditAccount(ctx, tx, domain.Actor{}, "consistency.fixed", runID, nil, map[string]any{"applied": result.Applied, "skipped": result.Skipped, "codes": codes}); err != nil {
		return domain.ConsistencyFixResult{}, err
	}
	return result, storageError(tx.Commit(ctx))
}

func applyConsistencyFix(ctx context.Context, tx pgx.Tx, run string, f domain.ConsistencyFinding) (bool, error) {
	switch f.Code {
	case domain.ConsistencyUserDataForeignSource:
		if !domain.ValidID(f.UserID) || !domain.ValidID(f.ItemID) || !domain.ValidID(f.SourceID) {
			return false, domain.ErrInvalid
		}
		tag, err := tx.Exec(ctx, `UPDATE user_item_data d SET last_source_id=NULL WHERE d.user_id=$1::uuid AND d.item_id=$2::uuid AND d.last_source_id=$3::uuid
 AND EXISTS(SELECT 1 FROM media_sources ms WHERE ms.id=$3::uuid AND ms.item_id<>d.item_id)`, f.UserID, f.ItemID, f.SourceID)
		if err != nil || tag.RowsAffected() != 1 {
			return false, storageError(err)
		}
		source := f.SourceID
		return true, journalFix(ctx, tx, run, fixUserData, fixTarget{UserID: f.UserID, ItemID: f.ItemID}, sourceState{&source}, sourceState{})
	case domain.ConsistencySessionForeignSource:
		if !domain.ValidID(f.Object) || !domain.ValidID(f.SourceID) {
			return false, domain.ErrInvalid
		}
		tag, err := tx.Exec(ctx, `UPDATE playback_sessions p SET source_id=NULL WHERE p.id=$1::uuid AND p.source_id=$2::uuid
 AND EXISTS(SELECT 1 FROM media_sources ms WHERE ms.id=$2::uuid AND ms.item_id<>p.item_id)`, f.Object, f.SourceID)
		if err != nil || tag.RowsAffected() != 1 {
			return false, storageError(err)
		}
		source := f.SourceID
		return true, journalFix(ctx, tx, run, fixSession, fixTarget{SessionID: f.Object}, sourceState{&source}, sourceState{})
	case domain.ConsistencyDailyCounterDrift:
		return applyDailyRecount(ctx, tx, run, f)
	}
	return false, domain.ErrInvalid
}

// applyDailyRecount recounts one daily row inside the repair transaction,
// under the roll-up lock, instead of trusting the values the check read.
func applyDailyRecount(ctx context.Context, tx pgx.Tx, run string, f domain.ConsistencyFinding) (bool, error) {
	if !domain.ValidID(f.UserID) || !domain.ValidID(f.ItemID) {
		return false, domain.ErrInvalid
	}
	if _, err := domain.ParseWatchStatsDate(f.Day); err != nil {
		return false, domain.ErrInvalid
	}
	var before, after dailyCounters
	err := tx.QueryRow(ctx, `SELECT d.sessions,d.first_plays,d.rewatches,d.views,d.completions,d.completion_milli,
 c.sessions,c.first_plays,c.rewatches,c.first_plays+c.rewatches,c.completions,c.completion_milli
 FROM watch_stats_daily d CROSS JOIN LATERAL (`+watchStatsRecount+`) c
 WHERE d.user_id=$1::uuid AND d.day=$2::date AND d.item_id=$3::uuid FOR UPDATE OF d`, f.UserID, f.Day, f.ItemID).Scan(
		&before.Sessions, &before.FirstPlays, &before.Rewatches, &before.Views, &before.Completions, &before.CompletionMilli,
		&after.Sessions, &after.FirstPlays, &after.Rewatches, &after.Views, &after.Completions, &after.CompletionMilli)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && before == after {
		return false, nil
	}
	if err != nil {
		return false, storageError(err)
	}
	if err = setDailyCounters(ctx, tx, f.UserID, f.Day, f.ItemID, after); err != nil {
		return false, err
	}
	return true, journalFix(ctx, tx, run, fixDaily, fixTarget{UserID: f.UserID, ItemID: f.ItemID, Day: f.Day}, before, after)
}

func setDailyCounters(ctx context.Context, tx pgx.Tx, user, day, item string, v dailyCounters) error {
	tag, err := tx.Exec(ctx, `UPDATE watch_stats_daily SET sessions=$4,first_plays=$5,rewatches=$6,views=$7,completions=$8,completion_milli=$9,updated_at=now()
 WHERE user_id=$1::uuid AND day=$2::date AND item_id=$3::uuid`, user, day, item, v.Sessions, v.FirstPlays, v.Rewatches, v.Views, v.Completions, v.CompletionMilli)
	if err != nil {
		return storageError(err)
	}
	if tag.RowsAffected() != 1 {
		return domain.ErrConflict
	}
	return nil
}

// RevertConsistencyRun restores the before values of every repair of a run,
// newest first, in bounded transactions. An entry whose row no longer holds
// the repaired value, or whose old reference no longer exists, is left alone
// and counted as skipped; reverted entries are marked and never replayed.
func (s *Store) RevertConsistencyRun(ctx context.Context, runID string) (domain.ConsistencyRevertResult, error) {
	if ctx == nil || !domain.ValidID(runID) {
		return domain.ConsistencyRevertResult{}, domain.ErrInvalid
	}
	result := domain.ConsistencyRevertResult{RunID: runID}
	var exists bool
	if err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM consistency_runs WHERE id=$1::uuid)`, runID).Scan(&exists); err != nil {
		return result, storageError(err)
	}
	if !exists {
		return result, domain.ErrNotFound
	}
	for {
		done, err := s.revertConsistencyBatch(ctx, runID, &result)
		if err != nil || done {
			return result, err
		}
	}
}

func (s *Store) revertConsistencyBatch(ctx context.Context, runID string, result *domain.ConsistencyRevertResult) (bool, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return false, storageError(err)
	}
	defer tx.Rollback(ctx)
	if err = lockWatchStats(ctx, tx); err != nil {
		return false, err
	}
	rows, err := tx.Query(ctx, `SELECT id,fix,target::text,before_state::text,after_state::text FROM consistency_fix_journal
 WHERE run_id=$1::uuid AND reverted_at IS NULL ORDER BY id DESC LIMIT $2 FOR UPDATE`, runID, consistencyRevertBatch)
	if err != nil {
		return false, storageError(err)
	}
	type entry struct {
		id                         int64
		fix, target, before, after string
	}
	entries, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (entry, error) {
		var e entry
		err := r.Scan(&e.id, &e.fix, &e.target, &e.before, &e.after)
		return e, err
	})
	if err != nil {
		return false, storageError(err)
	}
	if len(entries) == 0 {
		return true, storageError(tx.Commit(ctx))
	}
	var reverted, skipped int64
	for _, e := range entries {
		ok, err := revertConsistencyEntry(ctx, tx, e.fix, e.target, e.before, e.after)
		if err != nil {
			return false, err
		}
		if ok {
			reverted++
		} else {
			skipped++
		}
		if _, err = tx.Exec(ctx, `UPDATE consistency_fix_journal SET reverted_at=clock_timestamp() WHERE id=$1`, e.id); err != nil {
			return false, storageError(err)
		}
	}
	if err = auditAccount(ctx, tx, domain.Actor{}, "consistency.reverted", runID, nil, map[string]any{"reverted": reverted, "skipped": skipped}); err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, storageError(err)
	}
	result.Reverted += reverted
	result.Skipped += skipped
	return len(entries) < consistencyRevertBatch, nil
}

func revertConsistencyEntry(ctx context.Context, tx pgx.Tx, fix, targetText, beforeText, afterText string) (bool, error) {
	var target fixTarget
	if json.Unmarshal([]byte(targetText), &target) != nil {
		return false, domain.ErrDatabase
	}
	switch fix {
	case fixUserData, fixSession:
		var before sourceState
		if json.Unmarshal([]byte(beforeText), &before) != nil || before.SourceID == nil || !domain.ValidID(*before.SourceID) {
			return false, domain.ErrDatabase
		}
		var tag interface{ RowsAffected() int64 }
		var err error
		if fix == fixUserData {
			tag, err = tx.Exec(ctx, `UPDATE user_item_data SET last_source_id=$3::uuid WHERE user_id=$1::uuid AND item_id=$2::uuid AND last_source_id IS NULL
 AND EXISTS(SELECT 1 FROM media_sources WHERE id=$3::uuid)`, target.UserID, target.ItemID, *before.SourceID)
		} else {
			tag, err = tx.Exec(ctx, `UPDATE playback_sessions SET source_id=$2::uuid WHERE id=$1::uuid AND source_id IS NULL
 AND EXISTS(SELECT 1 FROM media_sources WHERE id=$2::uuid)`, target.SessionID, *before.SourceID)
		}
		if err != nil {
			return false, storageError(err)
		}
		return tag.RowsAffected() == 1, nil
	case fixDaily:
		var before, after dailyCounters
		if json.Unmarshal([]byte(beforeText), &before) != nil || json.Unmarshal([]byte(afterText), &after) != nil {
			return false, domain.ErrDatabase
		}
		var current dailyCounters
		err := tx.QueryRow(ctx, `SELECT sessions,first_plays,rewatches,views,completions,completion_milli FROM watch_stats_daily
 WHERE user_id=$1::uuid AND day=$2::date AND item_id=$3::uuid FOR UPDATE`, target.UserID, target.Day, target.ItemID).Scan(
			&current.Sessions, &current.FirstPlays, &current.Rewatches, &current.Views, &current.Completions, &current.CompletionMilli)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, storageError(err)
		}
		if current != after {
			return false, nil
		}
		return true, setDailyCounters(ctx, tx, target.UserID, target.Day, target.ItemID, before)
	}
	return false, domain.ErrDatabase
}
