package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Legacy database import (G04.6). The source is read in batches; each batch
// is applied in its own transaction together with its phase checkpoint, so
// a failure rolls back exactly the batch in progress and a later run resumes
// after the last committed one. The ledger (legacy_import_map) records which
// Jelee row every source user, library, item and user data row became, which
// makes a repeated import change nothing. docs/legacy-import.md describes
// the procedure and the report.

// legacyImportLock is the session advisory lock that admits one import.
const legacyImportLock = 17481260

// legacyImportBatchHook, when set by a test, runs inside every batch
// transaction after the batch was applied and before it commits.
var legacyImportBatchHook func(phase string, batch int) error

// legacyDB is what both a pooled connection and a transaction offer.
type legacyDB interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// legacyPhaseState is a checkpoint's counters: the outcomes so far.
type legacyPhaseState struct {
	Categories       map[string]*domain.LegacyCategoryReport `json:"categories"`
	Samples          []domain.LegacySample                   `json:"samples"`
	PasswordReset    int64                                   `json:"passwordReset"`
	FavoritesDropped int64                                   `json:"favoritesDropped"`
}

func (p *legacyPhaseState) category(name string) *domain.LegacyCategoryReport {
	if p.Categories == nil {
		p.Categories = map[string]*domain.LegacyCategoryReport{}
	}
	c := p.Categories[name]
	if c == nil {
		c = &domain.LegacyCategoryReport{}
		p.Categories[name] = c
	}
	return c
}

func (p *legacyPhaseState) conflict(category, sourceID, reason string) {
	p.category(category).Conflict(reason, 1)
	if len(p.Samples) < domain.LegacyImportSampleLimit {
		p.Samples = append(p.Samples, domain.LegacySample{Category: category, SourceID: sourceID, Reason: reason})
	}
}

func (p *legacyPhaseState) add(o legacyPhaseState) {
	for name, c := range o.Categories {
		mine := p.category(name)
		mine.Add(c)
		if c.Derived {
			mine.Derived = true
			mine.Source += c.Source
		}
	}
	for _, s := range o.Samples {
		if len(p.Samples) < domain.LegacyImportSampleLimit {
			p.Samples = append(p.Samples, s)
		}
	}
	p.PasswordReset += o.PasswordReset
	p.FavoritesDropped += o.FavoritesDropped
}

// legacyImporter carries one invocation.
type legacyImporter struct {
	s       *Store
	conn    legacyDB
	src     domain.LegacySource
	opts    domain.LegacyImportOptions
	runID   string
	batches int
	// Names and root paths seen in this invocation, so two source rows that
	// collide are told apart even before the first is written.
	seenUsers map[string]string
	seenLibs  map[string]string
	seenRoots map[string]string
}

func legacyOptionsDigest(opts domain.LegacyImportOptions) []byte {
	data, _ := json.Marshal(struct {
		PathMap       []domain.LegacyPathRule `json:"pathMap"`
		MergeUsers    bool                    `json:"mergeUsers"`
		SkipConflicts bool                    `json:"skipConflicts"`
	}{opts.PathMap, opts.MergeUsers, opts.SkipConflicts})
	sum := sha256.Sum256(data)
	return sum[:]
}

// legacySourceCounts maps report categories to source table counts.
func legacySourceCounts(info domain.LegacySourceInfo) map[string]int64 {
	return map[string]int64{
		domain.LegacyCatUsers: info.Counts["Users"], domain.LegacyCatPermissions: info.Counts["Permissions"], domain.LegacyCatPreferences: info.Counts["Preferences"],
		domain.LegacyCatLibraries: info.Counts["CollectionFolders"], domain.LegacyCatLibraryRoots: info.Counts["PhysicalLocations"],
		domain.LegacyCatItems: info.Counts["BaseItems"], domain.LegacyCatUserData: info.Counts["UserData"],
	}
}

func newLegacyReport(info domain.LegacySourceInfo, opts domain.LegacyImportOptions) domain.LegacyImportReport {
	r := domain.LegacyImportReport{Schema: domain.LegacyImportReportSchema, State: domain.LegacyStatePreflight, TargetSchemaVersion: SchemaVersion,
		Source: domain.LegacySourceReport{File: info.File, SHA256: info.SHA256, Size: info.Size, WALSHA256: info.WALSHA256, LatestMigration: info.LatestMigration,
			Migrations: info.Migrations, Tested: info.Tested, Tables: info.Counts},
		PathMap: opts.PathMap, MergeUsers: opts.MergeUsers, SkipConflicts: opts.SkipConflicts, BatchSize: opts.BatchSize,
		Phases: map[string]string{}, Conflicts: []domain.LegacySample{}, Pending: []domain.LegacySample{}, StartedAt: time.Now().UTC()}
	if r.PathMap == nil {
		r.PathMap = []domain.LegacyPathRule{}
	}
	if r.Source.Tables == nil {
		r.Source.Tables = map[string]int64{}
	}
	for name, n := range legacySourceCounts(info) {
		r.Category(name).Source = n
	}
	r.Category(domain.LegacyCatLibraryAccess).Derived = true
	for _, phase := range domain.LegacyImportPhases() {
		r.Phases[phase] = "pending"
	}
	return r
}

// LegacyImport runs or resumes the import of src. The report is returned
// for refusals and failures too.
func (s *Store) LegacyImport(ctx context.Context, src domain.LegacySource, opts domain.LegacyImportOptions) (domain.LegacyImportReport, error) {
	if opts.BatchSize == 0 {
		opts.BatchSize = domain.LegacyImportDefaultBatch
	}
	if ctx == nil || src == nil {
		return domain.LegacyImportReport{}, domain.ErrInvalid
	}
	info := src.Info()
	report := newLegacyReport(info, opts)
	if opts.BatchSize < 1 || opts.BatchSize > domain.LegacyImportMaxBatch || opts.MaxBatches < 0 || len(info.SHA256) != 64 {
		return report, domain.ErrInvalid
	}
	for _, rule := range opts.PathMap {
		if rule.From == "" || rule.To == "" {
			return report, domain.ErrInvalid
		}
	}
	pooled, err := s.Pool.Acquire(ctx)
	if err != nil {
		return report, storageError(err)
	}
	defer pooled.Release()
	conn := pooled.Conn()
	var locked bool
	if err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtext(current_schema()),$1)`, legacyImportLock).Scan(&locked); err != nil {
		return report, storageError(err)
	}
	if !locked {
		return report, domain.ErrLegacyImportBusy
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if _, unlockErr := conn.Exec(unlockCtx, `SELECT pg_advisory_unlock(hashtext(current_schema()),$1)`, legacyImportLock); unlockErr != nil {
			// A connection that could not unlock must not return to the pool
			// holding the lock.
			_ = conn.Close(unlockCtx)
		}
	}()
	var version int
	var dirty, setupOpen bool
	if err = conn.QueryRow(ctx, `SELECT version,dirty,EXISTS(SELECT 1 FROM setup_state WHERE completed_at IS NULL) FROM schema_migrations`).Scan(&version, &dirty, &setupOpen); err != nil {
		return report, storageError(err)
	}
	if dirty || version != SchemaVersion {
		return report, domain.ErrLegacyImportSchema
	}
	im := &legacyImporter{s: s, conn: conn, src: src, opts: opts, seenUsers: map[string]string{}, seenLibs: map[string]string{}, seenRoots: map[string]string{}}
	// Preflight: resolve every account and library without writing.
	preview, err := im.preflight(ctx)
	if err != nil {
		return report, err
	}
	for name, c := range preview.Categories {
		report.Category(name).Add(c)
	}
	report.Conflicts = append(report.Conflicts, preview.Samples...)
	if setupOpen {
		// A half-finished setup wizard owns the accounts; finish it first.
		report.Conflicts = append(report.Conflicts, domain.LegacySample{Category: "setup", Reason: "setup_in_progress"})
		report.State = domain.LegacyStateRefused
		return report, domain.ErrLegacyImportConflict
	}
	conflicts := report.ConflictTotal() > 0 && !opts.SkipConflicts
	if opts.PreflightOnly {
		if conflicts {
			return report, domain.ErrLegacyImportConflict
		}
		return report, nil
	}
	if conflicts {
		report.State = domain.LegacyStateRefused
		return report, domain.ErrLegacyImportConflict
	}
	im.seenUsers, im.seenLibs, im.seenRoots = map[string]string{}, map[string]string{}, map[string]string{}
	resumed, err := im.beginRun(ctx, info)
	if err != nil {
		if errors.Is(err, domain.ErrLegacyImportRunMismatch) {
			report.State = domain.LegacyStateRefused
		}
		return report, err
	}
	report.RunID, report.Resumed = im.runID, resumed
	paused, err := im.runPhases(ctx)
	if err != nil {
		im.recordFailure(ctx, err)
		out, readErr := im.readReport(ctx, report)
		if readErr == nil {
			report = out
		}
		report.State, report.Error = domain.LegacyStateFailed, legacyErrorCode(err)
		return report, err
	}
	if paused {
		out, readErr := im.readReport(ctx, report)
		if readErr != nil {
			return report, readErr
		}
		out.State = domain.LegacyStatePaused
		return out, nil
	}
	return im.finish(ctx, report)
}

func legacyErrorCode(err error) string {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "cancelled"
	case errors.Is(err, domain.ErrLegacySourceUnsupported):
		return domain.ErrLegacySourceUnsupported.Error()
	}
	return "batch_failed"
}

func (im *legacyImporter) preflight(ctx context.Context) (legacyPhaseState, error) {
	var state legacyPhaseState
	cursor := ""
	for {
		users, next, err := im.src.Users(ctx, cursor, im.opts.BatchSize)
		if err != nil {
			return state, err
		}
		if len(users) == 0 {
			break
		}
		plans, err := im.resolveUsers(ctx, im.conn, users)
		if err != nil {
			return state, err
		}
		for _, p := range plans {
			if p.conflict != "" {
				state.conflict(domain.LegacyCatUsers, p.user.ID, p.conflict)
			}
		}
		cursor = next
	}
	cursor = ""
	for {
		libraries, next, err := im.src.Libraries(ctx, cursor, im.opts.BatchSize)
		if err != nil {
			return state, err
		}
		if len(libraries) == 0 {
			break
		}
		plans, err := im.resolveLibraries(ctx, im.conn, libraries)
		if err != nil {
			return state, err
		}
		for _, p := range plans {
			if p.conflict != "" {
				state.conflict(domain.LegacyCatLibraries, p.library.ID, p.conflict)
			}
			for _, root := range p.roots {
				if root.conflict != "" {
					state.conflict(domain.LegacyCatLibraryRoots, p.library.ID, root.conflict)
				}
			}
		}
		cursor = next
	}
	return state, nil
}

// beginRun resumes the unfinished run of this source and options or starts
// a new one.
func (im *legacyImporter) beginRun(ctx context.Context, info domain.LegacySourceInfo) (bool, error) {
	sum, err := hex.DecodeString(info.SHA256)
	if err != nil {
		return false, domain.ErrInvalid
	}
	digest := legacyOptionsDigest(im.opts)
	tx, err := legacyBegin(ctx, im.conn)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var id string
	var runSum, runDigest []byte
	err = tx.QueryRow(ctx, `SELECT id::text,source_sha256,options_digest FROM legacy_import_runs WHERE state='running' FOR UPDATE`).Scan(&id, &runSum, &runDigest)
	switch {
	case err == nil && !im.opts.Restart && slices.Equal(runSum, sum) && slices.Equal(runDigest, digest):
		im.runID = id
		return true, storageError(tx.Commit(ctx))
	case err == nil && !im.opts.Restart:
		return false, domain.ErrLegacyImportRunMismatch
	case err == nil:
		if _, err = tx.Exec(ctx, `UPDATE legacy_import_runs SET state='abandoned',finished_at=now(),updated_at=now() WHERE id=$1::uuid`, id); err != nil {
			return false, storageError(err)
		}
	case !errors.Is(err, pgx.ErrNoRows):
		return false, storageError(err)
	}
	counts, _ := json.Marshal(legacySourceCounts(info))
	if err = tx.QueryRow(ctx, `INSERT INTO legacy_import_runs(source_sha256,source_size,options_digest,state,source_counts) VALUES($1,$2,$3,'running',$4::jsonb) RETURNING id::text`,
		sum, info.Size, digest, string(counts)).Scan(&im.runID); err != nil {
		return false, storageError(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO legacy_import_checkpoints(run_id,phase) SELECT $1::uuid,unnest($2::text[])`, im.runID, domain.LegacyImportPhases()); err != nil {
		return false, storageError(err)
	}
	if err = appendAudit(ctx, tx, AuditEntry{Event: "legacy.import_started", TargetID: im.runID,
		After: map[string]any{"sourceSha256": info.SHA256, "sourceSize": info.Size, "latestMigration": info.LatestMigration, "mergeUsers": im.opts.MergeUsers,
			"skipConflicts": im.opts.SkipConflicts, "pathRules": len(im.opts.PathMap)}}); err != nil {
		return false, err
	}
	return false, storageError(tx.Commit(ctx))
}

// legacyBegin opens a batch transaction with bounded lock waits.
func legacyBegin(ctx context.Context, conn legacyDB) (pgx.Tx, error) {
	beginner, ok := conn.(interface {
		Begin(context.Context) (pgx.Tx, error)
	})
	if !ok {
		return nil, domain.ErrDatabase
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return nil, storageError(err)
	}
	if _, err = tx.Exec(ctx, `SET LOCAL TimeZone='UTC'; SET LOCAL lock_timeout='10s'; SET LOCAL statement_timeout='300s'`); err != nil {
		_ = tx.Rollback(ctx)
		return nil, storageError(err)
	}
	return tx, nil
}

type legacyCheckpoint struct {
	cursor  string
	batches int
	done    bool
	state   legacyPhaseState
	digest  []byte
}

func readCheckpoint(ctx context.Context, q legacyDB, runID, phase string) (legacyCheckpoint, error) {
	var cp legacyCheckpoint
	var counters []byte
	if err := q.QueryRow(ctx, `SELECT cursor,batches,done,counters,digest FROM legacy_import_checkpoints WHERE run_id=$1::uuid AND phase=$2`, runID, phase).
		Scan(&cp.cursor, &cp.batches, &cp.done, &counters, &cp.digest); err != nil {
		return cp, storageError(err)
	}
	if err := json.Unmarshal(counters, &cp.state); err != nil {
		return cp, domain.ErrDatabase
	}
	return cp, nil
}

// runPhases works through the phases; it reports true when MaxBatches
// stopped it.
func (im *legacyImporter) runPhases(ctx context.Context) (bool, error) {
	for _, phase := range domain.LegacyImportPhases() {
		for {
			cp, err := readCheckpoint(ctx, im.conn, im.runID, phase)
			if err != nil {
				return false, err
			}
			if cp.done {
				break
			}
			if im.opts.MaxBatches > 0 && im.batches >= im.opts.MaxBatches {
				return true, nil
			}
			if err = im.batch(ctx, phase, cp); err != nil {
				return false, err
			}
		}
	}
	return false, nil
}

// batch reads one batch of phase after the checkpoint and applies it with
// the new checkpoint in one transaction. An empty read finishes the phase.
func (im *legacyImporter) batch(ctx context.Context, phase string, cp legacyCheckpoint) error {
	var rows any
	var next string
	var n int
	var err error
	switch phase {
	case domain.LegacyPhaseUsers, domain.LegacyPhaseAccess:
		var users []domain.LegacyUser
		users, next, err = im.src.Users(ctx, cp.cursor, im.opts.BatchSize)
		rows, n = users, len(users)
	case domain.LegacyPhaseLibraries:
		var libraries []domain.LegacyLibrary
		libraries, next, err = im.src.Libraries(ctx, cp.cursor, im.opts.BatchSize)
		rows, n = libraries, len(libraries)
	case domain.LegacyPhaseItems:
		var items []domain.LegacyItem
		items, next, err = im.src.Items(ctx, cp.cursor, im.opts.BatchSize)
		rows, n = items, len(items)
	default:
		var data []domain.LegacyUserData
		data, next, err = im.src.UserData(ctx, cp.cursor, im.opts.BatchSize)
		rows, n = data, len(data)
	}
	if err != nil {
		return err
	}
	tx, err := legacyBegin(ctx, im.conn)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if n == 0 {
		if _, err = tx.Exec(ctx, `UPDATE legacy_import_checkpoints SET done=true,updated_at=now() WHERE run_id=$1::uuid AND phase=$2`, im.runID, phase); err != nil {
			return storageError(err)
		}
		return storageError(tx.Commit(ctx))
	}
	// Serialize with scans, catalog imports and every other catalog writer.
	if err = lockJobs(ctx, tx); err != nil {
		return err
	}
	var out legacyPhaseState
	switch v := rows.(type) {
	case []domain.LegacyUser:
		if phase == domain.LegacyPhaseUsers {
			out, err = im.applyUsers(ctx, tx, v)
		} else {
			out, err = im.applyAccess(ctx, tx, v)
		}
	case []domain.LegacyLibrary:
		out, err = im.applyLibraries(ctx, tx, v)
	case []domain.LegacyItem:
		out, err = im.applyItems(ctx, tx, v)
	case []domain.LegacyUserData:
		out, err = im.applyUserData(ctx, tx, v)
	}
	if err != nil {
		return err
	}
	if legacyImportBatchHook != nil {
		if err = legacyImportBatchHook(phase, cp.batches+1); err != nil {
			return err
		}
	}
	cp.state.add(out)
	counters, err := json.Marshal(cp.state)
	if err != nil {
		return domain.ErrDatabase
	}
	// The digest chains every batch the phase read: SHA-256(previous ‖
	// SHA-256(batch)). Resumed runs extend the same chain.
	canonical, err := json.Marshal(rows)
	if err != nil {
		return domain.ErrDatabase
	}
	batchSum := sha256.Sum256(canonical)
	chain := sha256.Sum256(append(slices.Clone(cp.digest), batchSum[:]...))
	if _, err = tx.Exec(ctx, `UPDATE legacy_import_checkpoints SET cursor=$3,batches=batches+1,counters=$4::jsonb,digest=$5,updated_at=now() WHERE run_id=$1::uuid AND phase=$2`,
		im.runID, phase, next, string(counters), chain[:]); err != nil {
		return storageError(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE legacy_import_runs SET updated_at=now(),last_error=NULL WHERE id=$1::uuid`, im.runID); err != nil {
		return storageError(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return storageError(err)
	}
	im.batches++
	return nil
}

// recordFailure notes why the run stopped; the run stays resumable.
func (im *legacyImporter) recordFailure(ctx context.Context, cause error) {
	if im.runID == "" {
		return
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_, _ = im.conn.Exec(writeCtx, `UPDATE legacy_import_runs SET last_error=$2,updated_at=now() WHERE id=$1::uuid AND state='running'`, im.runID, legacyErrorCode(cause))
}

// readReport assembles the report from the run's checkpoints.
func (im *legacyImporter) readReport(ctx context.Context, base domain.LegacyImportReport) (domain.LegacyImportReport, error) {
	report := base
	report.Categories = nil
	report.Conflicts, report.Pending = []domain.LegacySample{}, []domain.LegacySample{}
	report.Batches = 0
	var counts []byte
	var started time.Time
	if err := im.conn.QueryRow(ctx, `SELECT source_counts,started_at FROM legacy_import_runs WHERE id=$1::uuid`, im.runID).Scan(&counts, &started); err != nil {
		return base, storageError(err)
	}
	source := map[string]int64{}
	if err := json.Unmarshal(counts, &source); err != nil {
		return base, domain.ErrDatabase
	}
	report.StartedAt = started.UTC()
	for _, name := range domain.LegacyImportCategories() {
		report.Category(name).Source = source[name]
	}
	report.Category(domain.LegacyCatLibraryAccess).Derived = true
	for _, phase := range domain.LegacyImportPhases() {
		cp, err := readCheckpoint(ctx, im.conn, im.runID, phase)
		if err != nil {
			return base, err
		}
		report.Batches += int64(cp.batches)
		switch {
		case cp.done:
			report.Phases[phase] = "done"
		case cp.batches > 0:
			report.Phases[phase] = "partial"
		default:
			report.Phases[phase] = "pending"
		}
		for name, c := range cp.state.Categories {
			mine := report.Category(name)
			mine.Add(c)
			if c.Derived {
				mine.Source += c.Source
			}
		}
		for _, sample := range cp.state.Samples {
			report.Sample(false, sample.Category, sample.SourceID, sample.Reason)
		}
		report.PasswordResetRequired += cp.state.PasswordReset
		report.FavoritesDropped += cp.state.FavoritesDropped
		if cp.digest != nil {
			for _, name := range legacyPhaseDigestCategory[phase] {
				report.Category(name).Digest = hex.EncodeToString(cp.digest)
			}
		}
	}
	rows, err := im.conn.Query(ctx, `SELECT source_key,reason FROM legacy_import_pending WHERE run_id=$1::uuid ORDER BY source_key LIMIT $2`, im.runID, domain.LegacyImportSampleLimit)
	if err != nil {
		return base, storageError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var key, reason string
		if err = rows.Scan(&key, &reason); err != nil {
			return base, storageError(err)
		}
		report.Sample(true, domain.LegacyCatItems, key, reason)
	}
	return report, storageError(rows.Err())
}

// legacyPhaseDigestCategory names the categories whose rows a phase digest
// covers.
var legacyPhaseDigestCategory = map[string][]string{
	domain.LegacyPhaseUsers: {domain.LegacyCatUsers}, domain.LegacyPhaseLibraries: {domain.LegacyCatLibraries},
	domain.LegacyPhaseAccess: {domain.LegacyCatLibraryAccess}, domain.LegacyPhaseItems: {domain.LegacyCatItems},
	domain.LegacyPhaseUserData: {domain.LegacyCatUserData},
}

// finish reconciles the run, marks it complete and audits it.
func (im *legacyImporter) finish(ctx context.Context, base domain.LegacyImportReport) (domain.LegacyImportReport, error) {
	report, err := im.readReport(ctx, base)
	if err != nil {
		return base, err
	}
	// Permission and preference rows of users that no longer exist are never
	// read with an account; whatever is unaccounted for is such an orphan.
	for _, name := range []string{domain.LegacyCatPermissions, domain.LegacyCatPreferences} {
		c := report.Category(name)
		c.Skip(domain.LegacyReasonUserMissing, c.Source-c.Accounted())
	}
	tx, err := legacyBegin(ctx, im.conn)
	if err != nil {
		return report, err
	}
	defer tx.Rollback(ctx)
	verification := &domain.LegacyVerification{RowsBalanced: report.Balanced(), TargetRows: map[string]int64{}}
	expected := map[string]int64{}
	for category, query := range map[string]string{
		domain.LegacyCatUsers:     `SELECT count(*) FROM legacy_import_map m JOIN users u ON u.id=m.target_id WHERE m.kind='user' AND m.run_id=$1::uuid`,
		domain.LegacyCatLibraries: `SELECT count(*) FROM legacy_import_map m JOIN libraries l ON l.id=m.target_id WHERE m.kind='library' AND m.run_id=$1::uuid`,
		domain.LegacyCatItems:     `SELECT count(*) FROM legacy_import_map m JOIN items i ON i.id=m.target_id WHERE m.kind='item' AND m.run_id=$1::uuid`,
		domain.LegacyCatUserData: `SELECT count(*) FROM legacy_import_map m JOIN user_item_data d ON d.user_id=m.target_user AND d.item_id=m.target_id
 WHERE m.kind='user_data' AND m.run_id=$1::uuid`,
	} {
		var n int64
		if err = tx.QueryRow(ctx, query, im.runID).Scan(&n); err != nil {
			return report, storageError(err)
		}
		verification.TargetRows[category] = n
		c := report.Category(category)
		expected[category] = c.Inserted + c.Updated + c.Matched + c.Unchanged
	}
	verification.TargetMatch = true
	for category, n := range verification.TargetRows {
		if expected[category] != n {
			verification.TargetMatch = false
		}
	}
	report.Verification = verification
	finished := time.Now().UTC()
	report.FinishedAt, report.State = &finished, domain.LegacyStateCompleted
	// Like a restored metadata backup: a server that now has accounts or
	// libraries is not sent back through the setup wizard.
	if _, err = tx.Exec(ctx, `INSERT INTO setup_state(id,version,current_step,state,adopted,completed_at)
 SELECT 1,1,'complete','{}'::jsonb,true,now() WHERE NOT EXISTS(SELECT 1 FROM setup_state)
 AND (EXISTS(SELECT 1 FROM users WHERE deleted_at IS NULL) OR EXISTS(SELECT 1 FROM libraries))`); err != nil {
		return report, storageError(err)
	}
	if err = appendAudit(ctx, tx, AuditEntry{Event: "legacy.imported", TargetRef: "sha256:" + report.Source.SHA256, After: legacyImportAudit(report)}); err != nil {
		return report, err
	}
	data, err := json.Marshal(report)
	if err != nil {
		return report, domain.ErrDatabase
	}
	if _, err = tx.Exec(ctx, `UPDATE legacy_import_runs SET state='completed',report=$2::jsonb,finished_at=$3,updated_at=now() WHERE id=$1::uuid`, im.runID, string(data), finished); err != nil {
		return report, storageError(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return report, storageError(err)
	}
	if !verification.RowsBalanced || !verification.TargetMatch {
		return report, fmt.Errorf("%w: rows=%t target=%t", domain.ErrLegacyImportUnbalanced, verification.RowsBalanced, verification.TargetMatch)
	}
	return report, nil
}

func legacyImportAudit(r domain.LegacyImportReport) map[string]any {
	out := map[string]any{"runId": r.RunID, "resumed": r.Resumed, "batches": r.Batches, "accountsWithoutCredential": r.PasswordResetRequired,
		"rowsBalanced": r.Verification != nil && r.Verification.RowsBalanced, "targetMatch": r.Verification != nil && r.Verification.TargetMatch}
	for name, c := range r.Categories {
		out[name] = map[string]int64{"source": c.Source, "inserted": c.Inserted, "updated": c.Updated, "matched": c.Matched, "unchanged": c.Unchanged,
			"skipped": sumMap(c.Skipped), "conflicts": sumMap(c.Conflicts), "pending": sumMap(c.Pending)}
	}
	return out
}

func sumMap(m map[string]int64) int64 {
	var n int64
	for _, v := range m {
		n += v
	}
	return n
}
