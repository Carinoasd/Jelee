package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

// Storage of the self-healing repair actions (G50.4). Plans are read with
// the consistency checker's page queries plus the full scans below; every
// write re-checks the row it changes, so a row changed since the plan was
// read is skipped instead of overwritten. Reversible repairs share the
// consistency checker's journaled repairs and are recorded in repair_journal.
// docs/repair.md describes the actions.

var _ app.RepairRepository = (*Store)(nil)

const (
	// repairResultLimit matches the result size constraint of schema 83.
	repairResultLimit = 256 << 10
	// repairProbeBatch bounds the cache rows one probe transaction deletes;
	// a probe transaction has a two second budget.
	repairProbeBatch = 64
	// repairStaleRun closes a run nobody finished: its process died.
	repairStaleRun = 24 * time.Hour
)

// RepairStatsPage reads one page of every daily statistics row in key order
// and reports each row of the library whose counters differ from the
// recount of its sessions. Unlike the consistency check it samples nothing.
// Days before scope.SessionCutoff are left alone: their sessions may already
// be removed by retention, so a recount would undercount them. Paging
// follows the primary key of all rows; the library is a filter, so a page
// reads at most PageSize rows of other libraries too, and the recount runs
// only for rows of the library.
func (s *Store) RepairStatsPage(ctx context.Context, scope domain.ConsistencyScope, cursor string) (domain.ConsistencyPage, error) {
	if ctx == nil || !domain.ValidID(scope.Library.ID) {
		return domain.ConsistencyPage{}, domain.ErrInvalid
	}
	user, day, item := consistencyZeroID, "1970-01-01", consistencyZeroID
	if cursor != "" {
		parts := strings.Split(cursor, "/")
		if len(parts) != 3 || !domain.ValidID(parts[0]) || !domain.ValidID(parts[2]) {
			return domain.ConsistencyPage{}, domain.ErrInvalid
		}
		if _, err := domain.ParseWatchStatsDate(parts[1]); err != nil {
			return domain.ConsistencyPage{}, domain.ErrInvalid
		}
		user, day, item = parts[0], parts[1], parts[2]
	}
	rows, err := s.Pool.Query(ctx, `SELECT d.user_id::text,to_char(d.day,'YYYY-MM-DD'),d.item_id::text,c.mine,
 d.sessions,d.first_plays,d.rewatches,d.views,d.completions,d.completion_milli,
 COALESCE(c.sessions,0),COALESCE(c.first_plays,0),COALESCE(c.rewatches,0),COALESCE(c.completions,0),COALESCE(c.completion_milli,0)
 FROM (SELECT * FROM watch_stats_daily WHERE (user_id,day,item_id)>($2::uuid,$3::date,$4::uuid) ORDER BY user_id,day,item_id LIMIT $5) d
 LEFT JOIN LATERAL (SELECT true AS mine,r.* FROM (`+watchStatsRecount+`) r WHERE d.library_id=$1::uuid AND ($6::date IS NULL OR d.day>=$6::date)) c ON true
 ORDER BY d.user_id,d.day,d.item_id`, scope.Library.ID, user, day, item, domain.ConsistencyPageSize, optionalDay(scope.SessionCutoff))
	if err != nil {
		return domain.ConsistencyPage{}, storageError(err)
	}
	defer rows.Close()
	var page domain.ConsistencyPage
	read, last := 0, ""
	for rows.Next() {
		var f domain.ConsistencyFinding
		var mine *bool
		var have, want [6]int64
		if err = rows.Scan(&f.UserID, &f.Day, &f.ItemID, &mine, &have[0], &have[1], &have[2], &have[3], &have[4], &have[5], &want[0], &want[1], &want[2], &want[4], &want[5]); err != nil {
			return domain.ConsistencyPage{}, storageError(err)
		}
		read++
		last = f.UserID + "/" + f.Day + "/" + f.ItemID
		if mine == nil || !*mine {
			continue
		}
		want[3] = want[1] + want[2]
		page.Examined++
		if have != want {
			names := [6]string{"sessions", "firstPlays", "rewatches", "views", "completions", "completionMilli"}
			f.Expected, f.Actual = map[string]int64{}, map[string]int64{}
			for i, name := range names {
				f.Expected[name], f.Actual[name] = want[i], have[i]
			}
			f.Code, f.LibraryID, f.Fixable = domain.ConsistencyDailyCounterDrift, scope.Library.ID, true
			page.Candidates = append(page.Candidates, f)
		}
	}
	if err = rows.Err(); err != nil {
		return domain.ConsistencyPage{}, storageError(err)
	}
	page.Next = nextCursor(read, last)
	return page, nil
}

// RepairVariantPage reads one page of the whole image variant index in key
// order. Every row is a candidate the caller confirms against the store;
// Expected["lastAccessMicro"] is the access time it was read with, so a
// variant used meanwhile is not removed.
func (s *Store) RepairVariantPage(ctx context.Context, cursor string) (domain.ConsistencyPage, error) {
	if ctx == nil {
		return domain.ConsistencyPage{}, domain.ErrInvalid
	}
	source, key := strings.Repeat("0", 64), strings.Repeat("0", 64)
	if cursor != "" {
		var ok bool
		source, key, ok = strings.Cut(cursor, "/")
		if !ok || !repairHex(source) || !repairHex(key) {
			return domain.ConsistencyPage{}, domain.ErrInvalid
		}
	}
	rows, err := s.Pool.Query(ctx, `SELECT encode(content_sha256,'hex'),encode(variant_key,'hex'),(extract(epoch FROM last_access)*1000000)::bigint FROM image_variants
 WHERE (content_sha256,variant_key)>(decode($1,'hex'),decode($2,'hex')) ORDER BY content_sha256,variant_key LIMIT $3`, source, key, domain.ConsistencyPageSize)
	if err != nil {
		return domain.ConsistencyPage{}, storageError(err)
	}
	defer rows.Close()
	var page domain.ConsistencyPage
	last := ""
	for rows.Next() {
		var source, variant string
		var access int64
		if err = rows.Scan(&source, &variant, &access); err != nil {
			return domain.ConsistencyPage{}, storageError(err)
		}
		page.Examined++
		last = source + "/" + variant
		page.Candidates = append(page.Candidates, domain.ConsistencyFinding{Code: domain.ConsistencyVariantFileMissing, Object: last, Confirm: domain.ConsistencyConfirmVariant,
			Expected: map[string]int64{"lastAccessMicro": access}})
	}
	if err = rows.Err(); err != nil {
		return domain.ConsistencyPage{}, storageError(err)
	}
	page.Next = nextCursor(int(page.Examined), last)
	return page, nil
}

func repairHex(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// StartRepairRun records a running execution. Runs a dead process left
// running are closed as failed first.
func (s *Store) StartRepairRun(ctx context.Context, run domain.RepairRun) (domain.RepairRun, error) {
	if ctx == nil || !domain.ValidRepairAction(run.Action) || run.Origin != domain.RepairOriginCLI && run.Origin != domain.RepairOriginAPI ||
		run.LibraryID != "" && !domain.ValidID(run.LibraryID) || run.Actor.UserID != "" && !domain.ValidID(run.Actor.UserID) ||
		(run.Origin == domain.RepairOriginAPI) != (run.Actor.UserID != "") {
		return domain.RepairRun{}, domain.ErrInvalid
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return domain.RepairRun{}, storageError(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `UPDATE repair_runs r SET state='failed',finished_at=clock_timestamp(),
 result=jsonb_build_object('schema',$1::text,'runId',r.id::text,'action',r.action,'state','failed','abandoned',true)
 WHERE r.state='running' AND r.started_at<clock_timestamp()-$2*interval '1 microsecond'`, domain.RepairResultSchema, repairStaleRun.Microseconds()); err != nil {
		return domain.RepairRun{}, storageError(err)
	}
	var library, actor *string
	if run.LibraryID != "" {
		library = &run.LibraryID
	}
	if run.Actor.UserID != "" {
		actor = &run.Actor.UserID
	}
	if err = tx.QueryRow(ctx, `INSERT INTO repair_runs(action,library_id,origin,actor_id) VALUES($1,$2::uuid,$3,$4::uuid) RETURNING id::text,started_at`,
		run.Action, library, run.Origin, actor).Scan(&run.ID, &run.StartedAt); err != nil {
		return domain.RepairRun{}, storageError(err)
	}
	return run, storageError(tx.Commit(ctx))
}

// repairRunning locks a running run of action inside tx.
func repairRunning(ctx context.Context, tx pgx.Tx, run domain.RepairRun, actions ...string) error {
	var action string
	if err := tx.QueryRow(ctx, `SELECT action FROM repair_runs WHERE id=$1::uuid AND state='running' FOR UPDATE`, run.ID).Scan(&action); err != nil {
		return storageError(err)
	}
	for _, a := range actions {
		if a == action {
			return nil
		}
	}
	return domain.ErrConflict
}

// ApplyRepairFixes applies one page of the journaled repairs of the stats
// and counts actions in one transaction, journals each one in
// repair_journal and audits the batch. A finding whose row no longer
// matches what the plan read is skipped.
func (s *Store) ApplyRepairFixes(ctx context.Context, run domain.RepairRun, fixes []domain.ConsistencyFinding) (domain.ConsistencyFixResult, error) {
	if ctx == nil || !domain.ValidID(run.ID) || len(fixes) == 0 || len(fixes) > domain.ConsistencyPageSize {
		return domain.ConsistencyFixResult{}, domain.ErrInvalid
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return domain.ConsistencyFixResult{}, storageError(err)
	}
	defer tx.Rollback(ctx)
	if err = repairRunning(ctx, tx, run, domain.RepairStats, domain.RepairCounts); err != nil {
		return domain.ConsistencyFixResult{}, err
	}
	for _, f := range fixes {
		if f.Code == domain.ConsistencyDailyCounterDrift {
			if err = lockWatchStats(ctx, tx); err != nil {
				return domain.ConsistencyFixResult{}, err
			}
			break
		}
	}
	var result domain.ConsistencyFixResult
	codes := map[string]int64{}
	for _, f := range fixes {
		applied, err := applyConsistencyFix(ctx, tx, repairJournal, run.ID, f)
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
	if err = auditAccount(ctx, tx, run.Actor, "repair.applied", run.ID, nil, map[string]any{"action": run.Action, "applied": result.Applied, "skipped": result.Skipped, "codes": codes}); err != nil {
		return domain.ConsistencyFixResult{}, err
	}
	return result, storageError(tx.Commit(ctx))
}

// PurgeRepairProbeCache deletes probe cache rows the caches or orphans
// action planned, releasing their quota like eviction does. A row that is
// leased by a running probe, is no longer ready, or whose stamp changed
// since it was read is skipped. Nothing is journaled: a cache row is
// rebuilt by the next probe, never restored.
func (s *Store) PurgeRepairProbeCache(ctx context.Context, run domain.RepairRun, targets []domain.ConsistencyFinding) (domain.ConsistencyFixResult, error) {
	if ctx == nil || !domain.ValidID(run.ID) || len(targets) == 0 || len(targets) > domain.ConsistencyPageSize {
		return domain.ConsistencyFixResult{}, domain.ErrInvalid
	}
	for _, t := range targets {
		if !domain.ValidID(t.RootID) || t.RelativePath == "" || t.Code != domain.ConsistencyProbeCacheChanged && t.Code != domain.ConsistencyProbeCacheOrphan {
			return domain.ConsistencyFixResult{}, domain.ErrInvalid
		}
	}
	var result domain.ConsistencyFixResult
	for start := 0; start < len(targets); start += repairProbeBatch {
		batch := targets[start:min(start+repairProbeBatch, len(targets))]
		applied, skipped, err := s.purgeProbeBatch(ctx, run, batch)
		result.Applied += applied
		result.Skipped += skipped
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

func (s *Store) purgeProbeBatch(parent context.Context, run domain.RepairRun, batch []domain.ConsistencyFinding) (applied, skipped int64, err error) {
	ctx, cancel, tx, err := s.probeTransaction(parent)
	if err != nil {
		return 0, 0, err
	}
	defer cancel()
	defer tx.Rollback(ctx)
	if err = repairRunning(ctx, tx, run, domain.RepairCaches, domain.RepairOrphans); err != nil {
		return 0, 0, err
	}
	var freed int64
	codes := map[string]int64{}
	for _, t := range batch {
		c, err := readCachedProbe(ctx, tx, t.RootID, t.RelativePath)
		if errors.Is(err, domain.ErrNotFound) {
			skipped++
			continue
		}
		if err != nil {
			return 0, 0, err
		}
		stale := t.Code == domain.ConsistencyProbeCacheOrphan || c.lease.Stamp.Size == t.Actual["size"] && c.lease.Stamp.ModifiedUnixNano == t.Actual["modifiedUnixNano"]
		if c.leased || c.state != "ready" || c.lease.LibraryID != t.LibraryID || !stale {
			skipped++
			continue
		}
		if err = deleteProbeRow(ctx, tx, c); err != nil {
			return 0, 0, err
		}
		applied++
		freed += c.charge
		codes[t.Code]++
	}
	if applied > 0 {
		if err = auditAccount(ctx, tx, run.Actor, "repair.applied", run.ID, nil, map[string]any{"action": run.Action, "applied": applied, "skipped": skipped, "codes": codes, "freedBytes": freed}); err != nil {
			return 0, 0, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, 0, storageError(err)
	}
	return applied, skipped, nil
}

// PurgeRepairVariantRows deletes image variant index rows whose file the
// orphans action found missing from the live generation. A row accessed
// since it was read, by a render that rebuilt the file, is kept.
func (s *Store) PurgeRepairVariantRows(ctx context.Context, run domain.RepairRun, targets []domain.ConsistencyFinding) (domain.ConsistencyFixResult, error) {
	if ctx == nil || !domain.ValidID(run.ID) || len(targets) == 0 || len(targets) > domain.ConsistencyPageSize {
		return domain.ConsistencyFixResult{}, domain.ErrInvalid
	}
	sources, keys, accesses := make([]string, 0, len(targets)), make([]string, 0, len(targets)), make([]int64, 0, len(targets))
	for _, t := range targets {
		source, key, ok := strings.Cut(t.Object, "/")
		access, known := t.Expected["lastAccessMicro"]
		if !ok || !repairHex(source) || !repairHex(key) || !known || t.Code != domain.ConsistencyVariantFileMissing {
			return domain.ConsistencyFixResult{}, domain.ErrInvalid
		}
		sources, keys, accesses = append(sources, source), append(keys, key), append(accesses, access)
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return domain.ConsistencyFixResult{}, storageError(err)
	}
	defer tx.Rollback(ctx)
	if err = repairRunning(ctx, tx, run, domain.RepairOrphans); err != nil {
		return domain.ConsistencyFixResult{}, err
	}
	tag, err := tx.Exec(ctx, `DELETE FROM image_variants v USING unnest($1::text[],$2::text[],$3::bigint[]) t(source,variant,access)
 WHERE v.content_sha256=decode(t.source,'hex') AND v.variant_key=decode(t.variant,'hex') AND (extract(epoch FROM v.last_access)*1000000)::bigint=t.access`, sources, keys, accesses)
	if err != nil {
		return domain.ConsistencyFixResult{}, storageError(err)
	}
	result := domain.ConsistencyFixResult{Applied: tag.RowsAffected(), Skipped: int64(len(targets)) - tag.RowsAffected()}
	if result.Applied > 0 {
		if err = auditAccount(ctx, tx, run.Actor, "repair.applied", run.ID, nil, map[string]any{"action": run.Action, "applied": result.Applied, "skipped": result.Skipped, "codes": map[string]int64{domain.ConsistencyVariantFileMissing: result.Applied}}); err != nil {
			return domain.ConsistencyFixResult{}, err
		}
	}
	return result, storageError(tx.Commit(ctx))
}

// RepairActiveJob returns the active job of a library, if any. NFO is true
// for a scan that validates NFO files.
func (s *Store) RepairActiveJob(ctx context.Context, library string) (domain.RepairActiveJob, bool, error) {
	if ctx == nil || !domain.ValidID(library) {
		return domain.RepairActiveJob{}, false, domain.ErrInvalid
	}
	var job domain.RepairActiveJob
	err := s.Pool.QueryRow(ctx, `SELECT j.id::text,j.kind,COALESCE((SELECT r.requested FROM nfo_job_requests r WHERE r.job_id=j.id),false)
 FROM jobs j WHERE j.library_id=$1::uuid AND j.state IN ('queued','running') LIMIT 1`, library).Scan(&job.ID, &job.Kind, &job.NFO)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.RepairActiveJob{}, false, nil
	}
	if err != nil {
		return domain.RepairActiveJob{}, false, storageError(err)
	}
	return job, true, nil
}

// EnqueueRepairCatalogSync queues catalog synchronisation of the library's
// accepted baseline for the items action. A catalog_sync job already active
// for the library is returned as a replay; any other active job is
// ErrJobBusy. The command line (no actor) queues like the automatic
// synchronisation does; an administrator is authorized like any job
// submission.
func (s *Store) EnqueueRepairCatalogSync(ctx context.Context, run domain.RepairRun, library string, policy domain.JobPolicy) (domain.RepairJob, error) {
	if ctx == nil || !domain.ValidID(run.ID) || !domain.ValidID(library) || !validJobPolicy(policy) {
		return domain.RepairJob{}, domain.ErrInvalid
	}
	var tx pgx.Tx
	var err error
	if run.Actor.UserID == "" {
		tx, err = s.jobTransaction(ctx)
	} else {
		tx, err = s.authorizedJobs(ctx, run.Actor)
	}
	if err != nil {
		return domain.RepairJob{}, err
	}
	defer tx.Rollback(ctx)
	if err = repairRunning(ctx, tx, run, domain.RepairItems); err != nil {
		return domain.RepairJob{}, err
	}
	var active, kind string
	err = tx.QueryRow(ctx, `SELECT id::text,kind FROM jobs WHERE library_id=$1::uuid AND state IN ('queued','running')`, library).Scan(&active, &kind)
	switch {
	case err == nil && kind == domain.JobCatalogSync:
		return domain.RepairJob{LibraryID: library, JobID: active, Replayed: true}, storageError(tx.Commit(ctx))
	case err == nil:
		return domain.RepairJob{}, domain.ErrJobBusy
	case !errors.Is(err, pgx.ErrNoRows):
		return domain.RepairJob{}, storageError(err)
	}
	baseline, err := lockLibraryBaseline(ctx, tx, library)
	if err != nil {
		return domain.RepairJob{}, err
	}
	if err = libraryAdmission(ctx, tx, library, policy); err != nil {
		return domain.RepairJob{}, err
	}
	var key string
	if err = tx.QueryRow(ctx, `SELECT 'repair-items:'||gen_random_uuid()::text`).Scan(&key); err != nil {
		return domain.RepairJob{}, storageError(err)
	}
	var actor *string
	if run.Actor.UserID != "" {
		actor = &run.Actor.UserID
	}
	job, err := insertCatalogSyncJob(ctx, tx, library, actor, key, domain.JobPriorityManual, domain.CatalogSyncModeSync, nil, baseline, 0, policy)
	if err != nil {
		return domain.RepairJob{}, err
	}
	if err = auditAccount(ctx, tx, run.Actor, "catalog_sync.submitted", job.ID, nil, map[string]any{"libraryId": library, "baselineRevision": baseline.revision, "repairRunId": run.ID}); err != nil {
		return domain.RepairJob{}, err
	}
	if err = trimJobs(ctx, tx, policy.HistoryLimit); err != nil {
		return domain.RepairJob{}, err
	}
	return domain.RepairJob{LibraryID: library, JobID: job.ID}, storageError(tx.Commit(ctx))
}

// FinishRepairRun stores the result, audits the run and trims old runs that
// have no journal.
func (s *Store) FinishRepairRun(ctx context.Context, run domain.RepairRun, result domain.RepairResult) error {
	if ctx == nil || !domain.ValidID(run.ID) || result.RunID != run.ID || result.Schema != domain.RepairResultSchema || result.Action != run.Action {
		return domain.ErrInvalid
	}
	switch result.State {
	case domain.RepairStateCompleted, domain.RepairStatePartial, domain.RepairStateFailed:
	default:
		return domain.ErrInvalid
	}
	document, err := repairDocument(result)
	if err != nil {
		return err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return storageError(err)
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE repair_runs SET state=$2,finished_at=clock_timestamp(),planned=$3,applied=$4,skipped=$5,result=$6::jsonb WHERE id=$1::uuid AND state='running'`,
		run.ID, result.State, result.Planned, result.Applied, result.Skipped, document)
	if err != nil {
		return storageError(err)
	}
	if tag.RowsAffected() != 1 {
		return domain.ErrConflict
	}
	jobs := make([]string, 0, len(result.Jobs))
	for _, j := range result.Jobs {
		if !j.Replayed {
			jobs = append(jobs, j.JobID)
		}
	}
	after := map[string]any{"action": result.Action, "state": result.State, "planned": result.Planned, "applied": result.Applied, "skipped": result.Skipped, "origin": result.Origin}
	if result.LibraryID != "" {
		after["libraryId"] = result.LibraryID
	}
	if len(jobs) > 0 {
		after["jobIds"] = jobs
	}
	if err = auditAccount(ctx, tx, run.Actor, "repair.finished", run.ID, nil, after); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM repair_runs WHERE id IN (SELECT r.id FROM repair_runs r WHERE r.state<>'running'
 AND NOT EXISTS(SELECT 1 FROM repair_journal j WHERE j.run_id=r.id) ORDER BY r.finished_at DESC,r.id DESC OFFSET $1)`, domain.RepairRunRetention); err != nil {
		return storageError(err)
	}
	return storageError(tx.Commit(ctx))
}

// repairDocument encodes a result within the stored size bound; an
// oversized result keeps its counts and drops samples.
func repairDocument(result domain.RepairResult) ([]byte, error) {
	document, err := json.Marshal(result)
	if err != nil {
		return nil, domain.ErrInvalid
	}
	if len(document) <= repairResultLimit/2 {
		return document, nil
	}
	result.Samples, result.SamplesTruncated = []domain.RepairSample{}, true
	document, err = json.Marshal(result)
	if err != nil || len(document) > repairResultLimit/2 {
		return nil, domain.ErrInvalid
	}
	return document, nil
}

// RevertRepairRun restores the before values of every journaled repair of a
// stats or counts run, newest first, in bounded transactions. An entry whose
// row no longer holds the repaired value is skipped; reverted entries are
// never replayed. Other actions cannot be reverted: ErrConflict.
func (s *Store) RevertRepairRun(ctx context.Context, actor domain.Actor, runID string) (domain.RepairRevertResult, error) {
	if ctx == nil || !domain.ValidID(runID) || actor.UserID != "" && !domain.ValidID(actor.UserID) {
		return domain.RepairRevertResult{}, domain.ErrInvalid
	}
	result := domain.RepairRevertResult{RunID: runID}
	var action, state string
	if err := s.Pool.QueryRow(ctx, `SELECT action,state FROM repair_runs WHERE id=$1::uuid`, runID).Scan(&action, &state); err != nil {
		return result, storageError(err)
	}
	if !domain.RepairRevertible(action) || state == "running" {
		return result, domain.ErrConflict
	}
	for {
		reverted, skipped, done, err := s.revertJournalBatch(ctx, repairJournal, runID, "repair.reverted", actor)
		result.Reverted += reverted
		result.Skipped += skipped
		if err != nil || done {
			return result, err
		}
	}
}
