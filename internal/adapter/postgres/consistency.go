package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

var (
	_ app.ConsistencyRepository = (*Store)(nil)
	_ app.ConsistencyFixer      = (*Store)(nil)
)

const (
	consistencyZeroID = "00000000-0000-0000-0000-000000000000"
	// consistencyReportLimit matches the report size constraint of schema 74.
	consistencyReportLimit = 4 << 20
	// consistencyStaleRun closes a run nobody finished: its process died.
	consistencyStaleRun = 24 * time.Hour
)

// ConsistencyLibraries returns the selected libraries ordered by ID, with
// roots and the baseline active now. Root paths stay private to the checker.
func (s *Store) ConsistencyLibraries(ctx context.Context, id string) ([]domain.ConsistencyLibrary, error) {
	if ctx == nil || id != "" && !domain.ValidID(id) {
		return nil, domain.ErrInvalid
	}
	rows, err := s.Pool.Query(ctx, `SELECT l.id::text,l.active_inventory_snapshot,l.inventory_baseline_revision,
 (SELECT count(*) FROM library_inventory_baseline_data b WHERE b.library_id=l.id AND b.snapshot_id=l.active_inventory_snapshot)
 FROM libraries l WHERE ($1='' OR l.id=NULLIF($1,'')::uuid) ORDER BY l.id`, id)
	if err != nil {
		return nil, storageError(err)
	}
	libraries, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.ConsistencyLibrary, error) {
		var l domain.ConsistencyLibrary
		err := r.Scan(&l.ID, &l.Snapshot, &l.Revision, &l.Entries)
		l.Baseline = l.Entries > 0
		return l, err
	})
	if err != nil {
		return nil, storageError(err)
	}
	index := make(map[string]int, len(libraries))
	ids := make([]string, len(libraries))
	for i, l := range libraries {
		index[l.ID], ids[i] = i, l.ID
	}
	rows, err = s.Pool.Query(ctx, `SELECT library_id::text,id::text,path FROM library_roots WHERE library_id=ANY($1::uuid[]) ORDER BY library_id,id`, ids)
	if err != nil {
		return nil, storageError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var library string
		var root domain.ConsistencyRoot
		if err = rows.Scan(&library, &root.ID, &root.Path); err != nil {
			return nil, storageError(err)
		}
		if i, ok := index[library]; ok {
			libraries[i].Roots = append(libraries[i].Roots, root)
		}
	}
	return libraries, storageError(rows.Err())
}

// ConsistencyBaselineCurrent reports whether library still has the baseline
// snapshot and revision it was read with.
func (s *Store) ConsistencyBaselineCurrent(ctx context.Context, library domain.ConsistencyLibrary) (bool, error) {
	if ctx == nil || !domain.ValidID(library.ID) {
		return false, domain.ErrInvalid
	}
	var current bool
	err := s.Pool.QueryRow(ctx, `SELECT active_inventory_snapshot=$2 AND inventory_baseline_revision=$3 FROM libraries WHERE id=$1::uuid`, library.ID, library.Snapshot, library.Revision).Scan(&current)
	return current, storageError(err)
}

// StartConsistencyRun records a running run. A job reruns under the same row
// after a lost lease. Runs that a dead process left running are closed as
// failed first, so the table never keeps a run open forever.
func (s *Store) StartConsistencyRun(ctx context.Context, run domain.ConsistencyRun) (domain.ConsistencyRun, error) {
	if ctx == nil || run.Origin != domain.ConsistencyOriginCLI && run.Origin != domain.ConsistencyOriginJob ||
		run.Mode != domain.ConsistencyModeReport && run.Mode != domain.ConsistencyModeFix ||
		run.LibraryID != "" && !domain.ValidID(run.LibraryID) || (run.Origin == domain.ConsistencyOriginJob) != domain.ValidID(run.JobID) {
		return domain.ConsistencyRun{}, domain.ErrInvalid
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return domain.ConsistencyRun{}, storageError(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `UPDATE consistency_runs r SET state='failed',finished_at=clock_timestamp(),
 report=jsonb_build_object('schema',$1::text,'runId',r.id::text,'state','failed','abandoned',true)
 WHERE r.state='running' AND (r.started_at<clock_timestamp()-$2*interval '1 microsecond'
  OR r.origin='job' AND (r.job_id IS NULL OR EXISTS(SELECT 1 FROM jobs j WHERE j.id=r.job_id AND j.state IN ('succeeded','failed','cancelled'))))`,
		domain.ConsistencyReportSchema, consistencyStaleRun.Microseconds()); err != nil {
		return domain.ConsistencyRun{}, storageError(err)
	}
	var job, library *string
	if run.JobID != "" {
		job = &run.JobID
	}
	if run.LibraryID != "" {
		library = &run.LibraryID
	}
	err = tx.QueryRow(ctx, `INSERT INTO consistency_runs AS r(job_id,library_id,origin,mode) VALUES($1::uuid,$2::uuid,$3,$4)
 ON CONFLICT ON CONSTRAINT consistency_runs_job_key DO UPDATE SET state='running',started_at=clock_timestamp(),finished_at=NULL,findings=0,fixed=0,report=NULL
 RETURNING r.id::text,r.started_at`, job, library, run.Origin, run.Mode).Scan(&run.ID, &run.StartedAt)
	if err != nil {
		return domain.ConsistencyRun{}, storageError(err)
	}
	if _, err = tx.Exec(ctx, `DELETE FROM consistency_run_checks WHERE run_id=$1::uuid`, run.ID); err != nil {
		return domain.ConsistencyRun{}, storageError(err)
	}
	return run, storageError(tx.Commit(ctx))
}

// FinishConsistencyRun stores the report, its per-check counts and trims
// old runs. Runs with a repair journal are kept so they can be reverted.
func (s *Store) FinishConsistencyRun(ctx context.Context, report domain.ConsistencyReport) error {
	if ctx == nil || !domain.ValidID(report.RunID) || report.Schema != domain.ConsistencyReportSchema {
		return domain.ErrInvalid
	}
	switch report.State {
	case domain.ConsistencyRunCompleted, domain.ConsistencyRunPartial, domain.ConsistencyRunCancelled, domain.ConsistencyRunFailed:
	default:
		return domain.ErrInvalid
	}
	document, err := consistencyDocument(report)
	if err != nil {
		return err
	}
	var libraries, checks, statuses []string
	var examined, findings, fixed []int64
	add := func(library string, r domain.ConsistencyCheckResult) {
		libraries, checks, statuses = append(libraries, library), append(checks, r.Check), append(statuses, r.Status)
		examined, findings, fixed = append(examined, r.Examined), append(findings, r.Findings), append(fixed, r.Fixed)
	}
	for _, l := range report.Libraries {
		for _, r := range l.Checks {
			add(l.LibraryID, r)
		}
	}
	for _, r := range report.Global {
		add("", r)
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return storageError(err)
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE consistency_runs SET state=$2,finished_at=clock_timestamp(),findings=$3,fixed=$4,report=$5::jsonb WHERE id=$1::uuid AND state='running'`,
		report.RunID, report.State, report.Totals.Findings, report.Totals.Fixed, document)
	if err != nil {
		return storageError(err)
	}
	if tag.RowsAffected() != 1 {
		return domain.ErrConflict
	}
	// Rows of a library deleted during the run are dropped by the join; the
	// report keeps them.
	if _, err = tx.Exec(ctx, `INSERT INTO consistency_run_checks(run_id,library_id,check_id,status,examined,findings,fixed)
 SELECT $1::uuid,NULLIF(v.library,'')::uuid,v.check_id,v.status,v.examined,v.findings,v.fixed
 FROM unnest($2::text[],$3::text[],$4::text[],$5::bigint[],$6::bigint[],$7::bigint[]) v(library,check_id,status,examined,findings,fixed)
 WHERE v.library='' OR EXISTS(SELECT 1 FROM libraries l WHERE l.id=NULLIF(v.library,'')::uuid)`,
		report.RunID, libraries, checks, statuses, examined, findings, fixed); err != nil {
		return storageError(err)
	}
	if _, err = tx.Exec(ctx, `DELETE FROM consistency_runs WHERE id IN (SELECT r.id FROM consistency_runs r WHERE r.state<>'running'
 AND NOT EXISTS(SELECT 1 FROM consistency_fix_journal f WHERE f.run_id=r.id) ORDER BY r.finished_at DESC,r.id DESC OFFSET $1)`, domain.ConsistencyRunRetention); err != nil {
		return storageError(err)
	}
	return storageError(tx.Commit(ctx))
}

// consistencyDocument encodes a report within the stored size bound. An
// oversized report keeps its counts and drops samples.
func consistencyDocument(report domain.ConsistencyReport) ([]byte, error) {
	document, err := json.Marshal(report)
	if err != nil {
		return nil, domain.ErrInvalid
	}
	if len(document) <= consistencyReportLimit/2 {
		return document, nil
	}
	trim := func(results []domain.ConsistencyCheckResult) {
		for i := range results {
			if len(results[i].Samples) > 0 {
				results[i].Samples, results[i].SamplesTruncated = []domain.ConsistencyFinding{}, true
			}
		}
	}
	libraries := make([]domain.ConsistencyLibraryReport, len(report.Libraries))
	for i, l := range report.Libraries {
		l.Checks = append([]domain.ConsistencyCheckResult(nil), l.Checks...)
		trim(l.Checks)
		libraries[i] = l
	}
	report.Libraries = libraries
	report.Global = append([]domain.ConsistencyCheckResult(nil), report.Global...)
	trim(report.Global)
	document, err = json.Marshal(report)
	if err != nil || len(document) > consistencyReportLimit/2 {
		return nil, domain.ErrInvalid
	}
	return document, nil
}

// LatestConsistencyReport returns the newest finished report that covered
// library, or the newest finished report when library is empty.
func (s *Store) LatestConsistencyReport(ctx context.Context, library string) ([]byte, error) {
	if ctx == nil || library != "" && !domain.ValidID(library) {
		return nil, domain.ErrInvalid
	}
	var document []byte
	err := s.Pool.QueryRow(ctx, `SELECT r.report::text FROM consistency_runs r WHERE r.state<>'running'
 AND ($1='' OR r.library_id=NULLIF($1,'')::uuid OR r.library_id IS NULL AND EXISTS(SELECT 1 FROM consistency_run_checks c WHERE c.run_id=r.id AND c.library_id=NULLIF($1,'')::uuid))
 ORDER BY r.finished_at DESC,r.id DESC LIMIT 1`, library).Scan(&document)
	return document, storageError(err)
}

func insertConsistencyJob(ctx context.Context, tx pgx.Tx, library, priority string, policy domain.JobPolicy) (domain.Job, error) {
	var key string
	if err := tx.QueryRow(ctx, `SELECT 'consistency:'||gen_random_uuid()::text`).Scan(&key); err != nil {
		return domain.Job{}, storageError(err)
	}
	job, err := scanJob(tx.QueryRow(ctx, `INSERT INTO jobs(library_id,actor_id,idempotency_key,kind,priority,queue_limit,history_limit,max_entries,max_directories,max_attempts,missing_count_limit,missing_percent_limit) VALUES($1::uuid,NULL,$2,'consistency_check',$3,$4,$5,$6,$7,$8,$9,$10) RETURNING `+jobColumns,
		library, key, priority, policy.QueueLimit, policy.HistoryLimit, policy.MaxEntries, policy.MaxDirectories, policy.MaxAttempts, policy.MissingCountLimit, policy.MissingPercentLimit))
	if errors.Is(err, domain.ErrConflict) {
		return domain.Job{}, domain.ErrJobBusy
	}
	if err != nil {
		return domain.Job{}, err
	}
	if err = auditAccount(ctx, tx, domain.Actor{}, "consistency.submitted", job.ID, nil, map[string]any{"libraryId": library, "priority": priority}); err != nil {
		return domain.Job{}, err
	}
	return job, trimJobs(ctx, tx, policy.HistoryLimit)
}

// EnqueueConsistencyCheck queues a consistency_check job for one library. It
// is the operator command path: the caller holds the database credentials,
// so no session is involved, and the audit names no actor.
func (s *Store) EnqueueConsistencyCheck(ctx context.Context, library, priority string, policy domain.JobPolicy) (domain.Job, error) {
	if ctx == nil || !domain.ValidID(library) || !validJobPolicy(policy) || priority != domain.JobPriorityManual && priority != domain.JobPriorityBackground {
		return domain.Job{}, domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return domain.Job{}, err
	}
	defer tx.Rollback(ctx)
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM libraries WHERE id=$1::uuid)`, library).Scan(&exists); err != nil {
		return domain.Job{}, storageError(err)
	}
	if !exists {
		return domain.Job{}, domain.ErrNotFound
	}
	if err = libraryAdmission(ctx, tx, library, policy); err != nil {
		return domain.Job{}, err
	}
	job, err := insertConsistencyJob(ctx, tx, library, priority, policy)
	if err != nil {
		return domain.Job{}, err
	}
	return job, storageError(tx.Commit(ctx))
}

// DispatchConsistencyCheck queues a background check for at most one library
// whose libraries were not checked within interval and that has no active
// job. A busy or full queue leaves the library for a later call.
func (s *Store) DispatchConsistencyCheck(ctx context.Context, interval time.Duration, policy domain.JobPolicy) (bool, error) {
	if ctx == nil || interval < time.Hour || !validJobPolicy(policy) {
		return false, domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var library string
	err = tx.QueryRow(ctx, `SELECT l.id::text FROM libraries l
 WHERE NOT EXISTS(SELECT 1 FROM jobs j WHERE j.library_id=l.id AND (j.state IN ('queued','running') OR j.kind='consistency_check' AND j.created_at>clock_timestamp()-$1*interval '1 microsecond'))
 AND NOT EXISTS(SELECT 1 FROM consistency_run_checks c JOIN consistency_runs r ON r.id=c.run_id WHERE c.library_id=l.id AND r.started_at>clock_timestamp()-$1*interval '1 microsecond')
 AND NOT EXISTS(SELECT 1 FROM consistency_runs r WHERE r.library_id=l.id AND r.started_at>clock_timestamp()-$1*interval '1 microsecond')
 ORDER BY l.id LIMIT 1`, interval.Microseconds()).Scan(&library)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, storageError(tx.Commit(ctx))
	}
	if err != nil {
		return false, storageError(err)
	}
	switch err = libraryAdmission(ctx, tx, library, policy); {
	case errors.Is(err, domain.ErrJobBusy), errors.Is(err, domain.ErrJobQueueFull):
		return false, storageError(tx.Commit(ctx))
	case err != nil:
		return false, err
	}
	if _, err = insertConsistencyJob(ctx, tx, library, domain.JobPriorityBackground, policy); err != nil {
		return false, err
	}
	return true, storageError(tx.Commit(ctx))
}

// FinishConsistencyCheck makes a consistency_check job terminal under its
// lease fence. A run the job left open is closed with the job's state.
func (s *Store) FinishConsistencyCheck(ctx context.Context, l domain.JobLease, state, code string) error {
	if !validJobError(state, code) && !(state == domain.JobFailed && code == "consistency_check_failed") {
		return domain.ErrInvalid
	}
	tx, err := s.jobTransaction(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	current, err := fencedJob(ctx, tx, l)
	if err != nil {
		return err
	}
	if current.Job.Kind != domain.JobConsistencyCheck {
		return domain.ErrInvalid
	}
	if state == domain.JobSucceeded && current.Job.CancelRequested {
		return domain.ErrConflict
	}
	if err = guardedJobUpdate(ctx, tx, current, `UPDATE jobs SET state=$2,error_code=$3,finished_at=clock_timestamp(),owner=NULL,lease_until=NULL WHERE id=$1::uuid`, l.Job.ID, state, code); err != nil {
		return err
	}
	runState := domain.ConsistencyRunFailed
	if state == domain.JobCancelled {
		runState = domain.ConsistencyRunCancelled
	}
	if _, err = tx.Exec(ctx, `UPDATE consistency_runs r SET state=$2,finished_at=clock_timestamp(),report=jsonb_build_object('schema',$3::text,'runId',r.id::text,'jobId',$1::text,'state',$2::text)
 WHERE r.job_id=$1::uuid AND r.state='running'`, l.Job.ID, runState, domain.ConsistencyReportSchema); err != nil {
		return storageError(err)
	}
	var findings int64
	var run *string
	if err = tx.QueryRow(ctx, `SELECT COALESCE((SELECT findings FROM consistency_runs WHERE job_id=$1::uuid),0),(SELECT id::text FROM consistency_runs WHERE job_id=$1::uuid)`, l.Job.ID).Scan(&findings, &run); err != nil {
		return storageError(err)
	}
	after := map[string]any{"state": state, "errorCode": code, "findings": findings}
	if run != nil {
		after["runId"] = *run
	}
	if err = auditAccount(ctx, tx, domain.Actor{}, "consistency.finished", l.Job.ID, nil, after); err != nil {
		return err
	}
	if err = trimJobs(ctx, tx, current.Policy.HistoryLimit); err != nil {
		return err
	}
	return storageError(tx.Commit(ctx))
}

// ResolveLibrary returns the ID of the library named by its ID or its name.
func (s *Store) ResolveLibrary(ctx context.Context, reference string) (string, error) {
	if ctx == nil || reference == "" || len(reference) > 128 {
		return "", domain.ErrInvalid
	}
	var id string
	err := s.Pool.QueryRow(ctx, `SELECT id::text FROM libraries WHERE id::text=$1 OR name=$1 ORDER BY id::text=$1 DESC LIMIT 1`, reference).Scan(&id)
	return id, storageError(err)
}
