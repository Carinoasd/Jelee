package postgres

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/nfo"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/resources"
	"github.com/jackc/pgx/v5/pgconn"
)

// nfoCreateService is the preparation service of a library whose NFO does not
// exist yet: the item has its media file only.
func nfoCreateService(t *testing.T, f jobFixture) (*app.NFOWritePreparations, domain.NFOItemScope, domain.NFOWritePrepareRequest) {
	t.Helper()
	scope, _ := nfoItemApplyLibrary(t, f)
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE libraries SET nfo_write_enabled=true WHERE id=$1::uuid`, scope.LibraryID); err != nil {
		t.Fatal("enable NFO write fixture")
	}
	budget, err := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 1, Queue: 32})
	if err != nil {
		t.Fatal(err)
	}
	preparer, err := nfo.NewWritePreparer(budget)
	if err != nil {
		t.Fatal(err)
	}
	service, err := app.NewNFOWritePreparations(f.s, preparer)
	if err != nil {
		t.Fatal(err)
	}
	request := domain.NFOWritePrepareRequest{ItemID: scope.ItemID, Revision: scope.Revision, Edits: []domain.NFOWriteTextEdit{{Field: "title", Value: "新建標題"}, {Field: "plot", Value: "created plot"}}, MaxBytes: domain.NFODefaultSourceBytes, Create: true}
	return service, scope, request
}

// newNFOCreateJob admits one create preparation through the production path.
func newNFOCreateJob(t *testing.T) nfoWriteJobFixtureState {
	t.Helper()
	f := newJobFixture(t)
	service, _, request := nfoCreateService(t, f)
	prepared, replay, err := service.Prepare(f.ctx, f.a, "create-source", request)
	if err != nil || replay || !prepared.Request.Create || len(prepared.Original) != 0 {
		t.Fatal("create preparation failed", err)
	}
	return submitNFOCreateJob(t, f, "create-batch", prepared)
}

func submitNFOCreateJob(t *testing.T, f jobFixture, key string, prepared ...domain.NFOWritePreparation) nfoWriteJobFixtureState {
	t.Helper()
	ids := make([]string, len(prepared))
	for i, p := range prepared {
		ids[i] = p.ID
	}
	job, replay, err := f.s.SubmitNFOWriteJob(f.ctx, f.a, f.registration.Library.ID, key, domain.JobPriorityManual, ids, f.policy)
	if err != nil || replay || job.Kind != domain.JobNFOWrite || job.State != domain.JobQueued {
		t.Fatal("nfo create admission failed", err)
	}
	return nfoWriteJobFixtureState{f: f, job: job, prepared: prepared}
}

// directoryNames lists what a directory holds, sorted, without sidecar locks.
func directoryNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".lock") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names
}

func (s nfoWriteJobFixtureState) targetPath(i int) string {
	p := s.prepared[i]
	return filepath.Join(p.Scope.Source.RootPath, filepath.FromSlash(p.Scope.Source.RelativePath))
}

func (s nfoWriteJobFixtureState) phases(t *testing.T) []int16 {
	t.Helper()
	rows, err := s.f.s.Pool.Query(s.f.ctx, `SELECT s.phase FROM nfo_write_commit_settlements s JOIN nfo_write_commit_journal w ON w.token=s.token WHERE w.job_id=$1::uuid ORDER BY s.phase`, s.job.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var phases []int16
	for rows.Next() {
		var phase int16
		if err := rows.Scan(&phase); err != nil {
			t.Fatal(err)
		}
		phases = append(phases, phase)
	}
	return phases
}

func TestNFOCreateWorkerEndToEnd(t *testing.T) {
	s := newNFOCreateJob(t)
	p := s.prepared[0]
	if _, err := os.Lstat(s.targetPath(0)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("create fixture started with a target")
	}
	startNFOWriteRunner(t, s.f.s, nfoWriteTestWriter(t))
	s.await(t, domain.JobSucceeded)
	if !bytes.Equal(s.target(t, 0), p.Replacement) || !bytes.HasPrefix(p.Replacement, []byte("<?xml")) {
		t.Fatal("created file differs from the prepared bytes")
	}
	info, err := os.Lstat(s.targetPath(0))
	if err != nil || !info.Mode().IsRegular() {
		t.Fatal("created target is not a regular file")
	}
	s.resolved(t)
	if got := s.phases(t); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatal("create phases differ", got)
	}
	// The runner's cleaner removes the token names; the created file stays and
	// keeps its content. A create makes no backup.
	dir := filepath.Dir(s.targetPath(0))
	deadline := time.Now().Add(30 * time.Second)
	want := []string{filepath.Base(s.targetPath(0)), "film.mkv"}
	sort.Strings(want)
	for {
		if names := directoryNames(t, dir); strings.Join(names, ",") == strings.Join(want, ",") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("token names were not cleaned", directoryNames(t, dir))
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !bytes.Equal(s.target(t, 0), p.Replacement) {
		t.Fatal("cleanup changed the created target")
	}
	if os.IsNotExist(func() error { _, err := os.Lstat(s.targetPath(0) + ".jelee.bak"); return err }()) == false {
		t.Fatal("a create left a backup")
	}
}

// tokenNames lists the commit names a directory holds.
func tokenNames(t *testing.T, dir string) []string {
	t.Helper()
	var names []string
	for _, name := range directoryNames(t, dir) {
		if strings.HasPrefix(name, ".jelee-nfo-commit-") {
			names = append(names, name)
		}
	}
	return names
}

func (s nfoWriteJobFixtureState) runWorker(t *testing.T, repository app.NFOWriteExecutionRepository, l domain.JobLease) error {
	t.Helper()
	worker, err := app.NewNFOWriteWorker(repository, nfoWriteTestWriter(t), nfoWriteTestOwner, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return worker.Run(s.f.ctx, l)
}

// A target that exists at stage time is never touched and nothing is created
// next to it: the entry is rejected before a plan exists.
func TestNFOCreateRefusesATargetThatAppearsBeforeStage(t *testing.T) {
	s := newNFOCreateJob(t)
	foreign := []byte("<movie><title>written by someone else</title></movie>")
	if err := os.WriteFile(s.targetPath(0), foreign, 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(s.targetPath(0))
	if err != nil {
		t.Fatal(err)
	}
	l := s.claim(t, nfoWriteTestOwner)
	if err := s.runWorker(t, s.f.s, l); !errors.Is(err, app.ErrNFOWritePartial) {
		t.Fatal("the rejected create was not reported", err)
	}
	after, err := os.Lstat(s.targetPath(0))
	if err != nil || !os.SameFile(before, after) || !bytes.Equal(s.target(t, 0), foreign) {
		t.Fatal("the foreign file was replaced or modified")
	}
	if names := tokenNames(t, filepath.Dir(s.targetPath(0))); len(names) != 0 {
		t.Fatal("a refused create left token files", names)
	}
	var plans int
	if err := s.f.s.Pool.QueryRow(s.f.ctx, `SELECT count(*) FROM nfo_write_commit_file_plans p JOIN nfo_write_commit_journal w ON w.token=p.token WHERE w.job_id=$1::uuid`, s.job.ID).Scan(&plans); err != nil || plans != 0 {
		t.Fatal("a plan was recorded for a target that was already present", plans, err)
	}
	if resolved, err := s.f.s.FinishNFOWriteJob(s.f.ctx, l, domain.JobFailed, "nfo_write_failed"); err != nil || !resolved {
		t.Fatal("rejected create job did not resolve", err)
	}
	s.resolved(t)
}

// phaseHookStore runs a function around the settlement saves of one phase.
type phaseHookStore struct {
	*Store
	phase  domain.NFOWriteCommitSettlementPhase
	before func()
	after  func()
}

func (s *phaseHookStore) SaveNFOWriteCommitSettlement(ctx context.Context, l domain.JobLease, seq int, token string, attempt uint8, phase domain.NFOWriteCommitSettlementPhase) (domain.NFOWriteCommitSettlement, error) {
	if phase == s.phase && s.before != nil {
		s.before()
	}
	saved, err := s.Store.SaveNFOWriteCommitSettlement(ctx, l, seq, token, attempt, phase)
	if err == nil && phase == s.phase && s.after != nil {
		s.after()
	}
	return saved, err
}

// The name appears after phase 1 is durable and before the link: the link
// fails, phase 3 records that this token never linked, the entry is rejected
// and the foreign file keeps its bytes and identity.
func TestNFOCreateFileAppearingAfterPhaseOneIsNeverOverwritten(t *testing.T) {
	s := newNFOCreateJob(t)
	foreign := []byte("<movie><title>appeared concurrently</title></movie>")
	var before os.FileInfo
	repository := &phaseHookStore{Store: s.f.s, phase: domain.NFOWriteCommitBackedUp, after: func() {
		if err := os.WriteFile(s.targetPath(0), foreign, 0o600); err != nil {
			t.Error(err)
		}
		before, _ = os.Lstat(s.targetPath(0))
	}}
	l := s.claim(t, nfoWriteTestOwner)
	if err := s.runWorker(t, repository, l); !errors.Is(err, app.ErrNFOWritePartial) {
		t.Fatal("the entry was not rejected", err)
	}
	after, err := os.Lstat(s.targetPath(0))
	if err != nil || before == nil || !os.SameFile(before, after) || !bytes.Equal(s.target(t, 0), foreign) {
		t.Fatal("the appeared file was replaced or modified")
	}
	if got := s.phases(t); len(got) != 2 || got[0] != 1 || got[1] != 3 {
		t.Fatal("phase 1 then phase 3 is expected", got)
	}
	if resolved, err := s.f.s.FinishNFOWriteJob(s.f.ctx, l, domain.JobFailed, "nfo_write_failed"); err != nil || !resolved {
		t.Fatal("rejected create job did not resolve", err)
	}
	s.resolved(t)
	// The cleaner removes the token's own names and leaves the foreign file.
	startNFOWriteRunner(t, s.f.s, nfoWriteTestWriter(t))
	deadline := time.Now().Add(30 * time.Second)
	for len(tokenNames(t, filepath.Dir(s.targetPath(0)))) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("token names were not cleaned", tokenNames(t, filepath.Dir(s.targetPath(0))))
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !bytes.Equal(s.target(t, 0), foreign) {
		t.Fatal("cleanup touched the foreign file")
	}
}

// stopUnresolved ends the lease of a running job the way a dead worker does:
// the next claim sweep stops it with its journal retained and unresolved.
func (s nfoWriteJobFixtureState) stopUnresolved(t *testing.T) {
	t.Helper()
	if _, err := s.f.s.Pool.Exec(s.f.ctx, `UPDATE jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, s.job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.f.s.ClaimJob(s.f.ctx, "sweeper", false, time.Minute); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("claim sweep unavailable", err)
	}
	if state, code := s.state(t); state != domain.JobFailed || code != "job_timeout" {
		t.Fatal("expired journaled write not stopped", state, code)
	}
}

// secondCreateItem adds an item whose media has another extension but the same
// basename, so its NFO name is the same absent target in the same directory.
func secondCreateItem(t *testing.T, f jobFixture, service *app.NFOWritePreparations, key string) domain.NFOWritePreparation {
	t.Helper()
	item := metadataItem(t, f)
	var root string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT id::text FROM library_roots WHERE library_id=$1::uuid`, f.registration.Library.ID).Scan(&root); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO media_sources(item_id,library_id,root_id,relative_path,content_type) VALUES($1::uuid,$2::uuid,$3::uuid,'film.mp4','video/mp4')`, item, f.registration.Library.ID, root); err != nil {
		t.Fatal(err)
	}
	scope, err := f.s.ResolveItemNFO(f.ctx, f.a, item, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scope.Source.RootPath, filepath.FromSlash(scope.MediaPath)), []byte("owned second media fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	request := domain.NFOWritePrepareRequest{ItemID: item, Revision: 1, Edits: []domain.NFOWriteTextEdit{{Field: "title", Value: "第二個"}}, MaxBytes: domain.NFODefaultSourceBytes, Create: true}
	prepared, _, err := service.Prepare(f.ctx, f.a, key, request)
	if err != nil {
		t.Fatal("second create preparation unavailable", err)
	}
	return prepared
}

// rawJournal inserts a journal row for the claimed lease directly, the way
// BeginNFOWriteCommit does, and reports the database error unchanged.
func rawJournal(t *testing.T, s nfoWriteJobFixtureState, l domain.JobLease) error {
	t.Helper()
	_, err := s.f.s.Pool.Exec(s.f.ctx, `INSERT INTO nfo_write_commit_journal(job_id,sequence,generation,owner) VALUES($1::uuid,1,$2,$3)`, l.Job.ID, l.Generation, l.Owner)
	return err
}

func requireUnresolvedClaim(t *testing.T, err error) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" || pgErr.ConstraintName != "nfo_native_target_unresolved" {
		t.Fatalf("expected the unresolved physical claim constraint, got %v", err)
	}
}

func claimKinds(t *testing.T, s nfoWriteJobFixtureState) map[string]int {
	t.Helper()
	rows, err := s.f.s.Pool.Query(s.f.ctx, `SELECT c.kind,count(*) FROM nfo_write_native_claims c JOIN nfo_write_commit_journal w ON w.token=c.token WHERE w.job_id=$1::uuid GROUP BY c.kind`, s.job.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	kinds := map[string]int{}
	for rows.Next() {
		var kind string
		var n int
		if err := rows.Scan(&kind, &n); err != nil {
			t.Fatal(err)
		}
		kinds[kind] = n
	}
	return kinds
}

// Invariant 4, first half: two unresolved create tokens for the same parent
// directory and file name cannot coexist, although their media differ.
func TestNFOCreateTokensForTheSameAbsentTargetCannotCoexist(t *testing.T) {
	f := newJobFixture(t)
	service, _, request := nfoCreateService(t, f)
	first, _, err := service.Prepare(f.ctx, f.a, "create-first", request)
	if err != nil {
		t.Fatal(err)
	}
	second := secondCreateItem(t, f, service, "create-second")
	if first.Scope.Source.RelativePath != second.Scope.Source.RelativePath || first.NativeObservation.MediaIdentity() == second.NativeObservation.MediaIdentity() {
		t.Fatal("fixture items must share the NFO name and not the media object")
	}
	s1 := submitNFOCreateJob(t, f, "create-one", first)
	l1 := s1.claim(t, nfoWriteTestOwner)
	record, err := f.s.BeginNFOWriteCommit(f.ctx, l1, 1)
	if err != nil {
		t.Fatal("first create token did not open", err)
	}
	if kinds := claimKinds(t, s1); kinds["media"] != 1 || kinds["nfo_absent"] != 1 || len(kinds) != 2 {
		t.Fatal("a create token holds its media and the absent target", kinds)
	}
	var key []byte
	var derived bool
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT c.identity,c.identity=nfo_absent_target_key(substring($2::bytea FROM octet_length($2::bytea)-47 FOR 48),'film.nfo') FROM nfo_write_native_claims c WHERE c.token=$1::uuid AND c.kind='nfo_absent'`, record.Token, mustReceipt(t, first)).Scan(&key, &derived); err != nil || !derived || len(key) != 48 || key[0] != 255 {
		t.Fatal("the absent-target claim is not the domain-separated key of parent and name", err)
	}
	s1.stopUnresolved(t)
	// The second create is admitted (no active job) but cannot open a journal.
	s2 := submitNFOCreateJob(t, f, "create-two", second)
	l2 := s2.claim(t, nfoWriteTestOwner)
	if _, err := f.s.BeginNFOWriteCommit(f.ctx, l2, 1); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("a second create for the same target opened a journal", err)
	}
	requireUnresolvedClaim(t, rawJournal(t, s2, l2))
	var count int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM nfo_write_commit_journal WHERE job_id=$1::uuid`, s2.job.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("a refused create left a journal", count, err)
	}
	// The worker reports the refusal as a rejected entry without a token.
	if err := s2.runWorker(t, f.s, l2); !errors.Is(err, app.ErrNFOWritePartial) {
		t.Fatal("the refused create was not reported", err)
	}
	if _, err := os.Lstat(s2.targetPath(0)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a refused create produced a file")
	}
	if resolved, err := f.s.FinishNFOWriteJob(f.ctx, l2, domain.JobFailed, "nfo_write_failed"); err != nil || resolved != true {
		t.Fatal("the job with no token did not finish", err)
	}
	// Recovery of the first job (rolled back, as it is stopped) frees the name.
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET cancel_requested=true WHERE id=$1::uuid`, s1.job.ID); err != nil {
		t.Fatal(err)
	}
	worker, err := app.NewNFOWriteWorker(f.s, nfoWriteTestWriter(t), nfoWriteTestOwner, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worker.Recover(f.ctx, s1.job.ID); err != nil {
		t.Fatal("recovery of the first create failed", err)
	}
	s1.resolved(t)
	if _, err := os.Lstat(s1.targetPath(0)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a rolled back create produced a file")
	}
	s3 := submitNFOCreateJob(t, f, "create-three", second)
	l3 := s3.claim(t, nfoWriteTestOwner)
	if _, err := f.s.BeginNFOWriteCommit(f.ctx, l3, 1); err != nil {
		t.Fatal("the name stayed claimed after resolution", err)
	}
}

func mustReceipt(t *testing.T, p domain.NFOWritePreparation) []byte {
	t.Helper()
	data, err := p.NativeObservation.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// Invariant 4, second half: while the create token is unresolved a rewrite of
// the created file cannot open a journal, because the token claims the
// output's identity the moment it becomes the target. After resolution the
// rewrite proceeds, backs the created file up and replaces it.
func TestNFOCreateExcludesARewriteOfTheCreatedFileUntilResolved(t *testing.T) {
	s := newNFOCreateJob(t)
	l := s.claim(t, nfoWriteTestOwner)
	if err := s.runWorker(t, s.f.s, l); err != nil {
		t.Fatal("create run failed", err)
	}
	if kinds := claimKinds(t, s); kinds["media"] != 1 || kinds["nfo_absent"] != 1 || kinds["nfo"] != 1 || len(kinds) != 3 {
		t.Fatal("a ready create holds media, the absent name and its output", kinds)
	}
	var output, claimed []byte
	if err := s.f.s.Pool.QueryRow(s.f.ctx, `SELECT r.output_identity,c.identity FROM nfo_write_commit_files_ready r JOIN nfo_write_commit_journal w ON w.token=r.token JOIN nfo_write_native_claims c ON c.token=r.token AND c.kind='nfo' WHERE w.job_id=$1::uuid`, s.job.ID).Scan(&output, &claimed); err != nil || !bytes.Equal(output, claimed) {
		t.Fatal("the output claim is not the ready output identity", err)
	}
	// The job stops without being finished: the token stays unresolved.
	s.stopUnresolved(t)
	created := s.target(t, 0)

	budget, _ := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 1, Queue: 32})
	preparer, _ := nfo.NewWritePreparer(budget)
	service, _ := app.NewNFOWritePreparations(s.f.s, preparer)
	rewrite := domain.NFOWritePrepareRequest{ItemID: s.prepared[0].Scope.ItemID, Revision: 1, Edits: []domain.NFOWriteTextEdit{{Field: "title", Value: "改寫"}}, MaxBytes: domain.NFODefaultSourceBytes, Backups: 2}
	prepared, _, err := service.Prepare(s.f.ctx, s.f.a, "rewrite-created", rewrite)
	if err != nil || prepared.Request.Create || !bytes.Equal(prepared.Original, created) {
		t.Fatal("the created file cannot be prepared for a rewrite", err)
	}
	s2 := submitNFOCreateJob(t, s.f, "rewrite-job", prepared)
	l2 := s2.claim(t, nfoWriteTestOwner)
	if _, err := s.f.s.BeginNFOWriteCommit(s.f.ctx, l2, 1); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("a rewrite of the created file started while its create is unresolved", err)
	}
	requireUnresolvedClaim(t, rawJournal(t, s2, l2))

	// Recovery resolves the create (it is complete), releasing every claim.
	worker, err := app.NewNFOWriteWorker(s.f.s, nfoWriteTestWriter(t), nfoWriteTestOwner, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if state, err := worker.Recover(s.f.ctx, s.job.ID); err != nil || state != domain.JobSucceeded {
		t.Fatal("recovery did not resolve the create", state, err)
	}
	s.resolved(t)
	if err := s2.runWorker(t, s.f.s, l2); err != nil {
		t.Fatal("the rewrite did not run after resolution", err)
	}
	if got := s2.target(t, 0); !bytes.Equal(got, prepared.Replacement) || !bytes.Contains(got, []byte("改寫")) {
		t.Fatal("rewrite bytes differ")
	}
	if backup, err := os.ReadFile(s2.targetPath(0) + ".jelee.bak"); err != nil || !bytes.Equal(backup, created) {
		t.Fatal("the rewrite did not back up the created file", err)
	}
}

// cancelAfterPhase cancels the job once the given phase is durable, as a user
// does between two steps of a settlement.
func cancelAfterPhase(t *testing.T, s nfoWriteJobFixtureState, phase domain.NFOWriteCommitSettlementPhase) *phaseHookStore {
	t.Helper()
	return &phaseHookStore{Store: s.f.s, phase: phase, after: func() {
		if _, err := s.f.s.CancelJob(s.f.ctx, s.f.a, s.job.ID); err != nil {
			t.Error("owned cancellation failed", err)
		}
	}}
}

func TestNFOCreateCancellationAfterTheLinkRemovesTheTarget(t *testing.T) {
	s := newNFOCreateJob(t)
	l := s.claim(t, nfoWriteTestOwner)
	// Cancel once phase 1 is durable: the link still happens, but phase 2 cannot
	// be confirmed for a cancelled job.
	if err := s.runWorker(t, cancelAfterPhase(t, s, domain.NFOWriteCommitBackedUp), l); err == nil {
		t.Fatal("a cancelled create reported success")
	}
	if !bytes.Equal(s.target(t, 0), s.prepared[0].Replacement) {
		t.Fatal("fixture did not stop between the link and its confirmation")
	}
	if resolved, err := s.f.s.FinishNFOWriteJob(s.f.ctx, l, domain.JobCancelled, ""); err != nil || resolved {
		t.Fatal("cancelled stop resolved a token between phase 1 and the link", err)
	}
	worker, err := app.NewNFOWriteWorker(s.f.s, nfoWriteTestWriter(t), nfoWriteTestOwner, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if resolved, err := worker.RecoverPending(s.f.ctx, 4); err != nil || resolved != 1 {
		t.Fatal("cancelled create not recovered", resolved, err)
	}
	if _, err := os.Lstat(s.targetPath(0)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the cancelled create left its target")
	}
	if state, _ := s.state(t); state != domain.JobCancelled {
		t.Fatal("cancelled job changed state", state)
	}
	if got := s.phases(t); len(got) != 2 || got[0] != 1 || got[1] != 3 {
		t.Fatal("cancelled phases differ", got)
	}
	s.resolved(t)
}

func TestNFOCreateCancellationLeavesAnEditedOrReplacedTarget(t *testing.T) {
	for _, change := range []string{"edited in place", "replaced"} {
		t.Run(change, func(t *testing.T) {
			s := newNFOCreateJob(t)
			l := s.claim(t, nfoWriteTestOwner)
			if err := s.runWorker(t, cancelAfterPhase(t, s, domain.NFOWriteCommitBackedUp), l); err == nil {
				t.Fatal("a cancelled create reported success")
			}
			if _, err := s.f.s.FinishNFOWriteJob(s.f.ctx, l, domain.JobCancelled, ""); err != nil {
				t.Fatal(err)
			}
			var want []byte
			if change == "edited in place" {
				edit, err := os.OpenFile(s.targetPath(0), os.O_WRONLY|os.O_APPEND, 0)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := edit.WriteString("<!--edited by the user-->"); err != nil || edit.Close() != nil {
					t.Fatal(err)
				}
				want = append(append([]byte{}, s.prepared[0].Replacement...), []byte("<!--edited by the user-->")...)
			} else {
				want = []byte("<movie><title>replaced by the user</title></movie>")
				replaced := s.targetPath(0) + ".user"
				if err := os.WriteFile(replaced, want, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(replaced, s.targetPath(0)); err != nil {
					t.Fatal(err)
				}
			}
			worker, err := app.NewNFOWriteWorker(s.f.s, nfoWriteTestWriter(t), nfoWriteTestOwner, 10*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			resolved, err := worker.RecoverPending(s.f.ctx, 4)
			if resolved != 0 || err == nil || !errors.Is(err, nfo.ErrChanged) {
				t.Fatal("recovery of a changed target must stop with ErrChanged", resolved, err)
			}
			if !bytes.Equal(s.target(t, 0), want) {
				t.Fatal("the changed target was removed or modified")
			}
			if got := s.phases(t); len(got) != 1 || got[0] != 1 {
				t.Fatal("a changed target must stay unresolved at phase 1", got)
			}
			var resolutions int
			if err := s.f.s.Pool.QueryRow(s.f.ctx, `SELECT count(*) FROM nfo_write_commit_resolutions WHERE job_id=$1::uuid`, s.job.ID).Scan(&resolutions); err != nil || resolutions != 0 {
				t.Fatal("a changed target was resolved", resolutions, err)
			}
		})
	}
}

// A movie, a series (tvshow.nfo), a season (season.nfo) and an episode
// (episodedetails) are created by one job, each named as the reader looks it
// up and with the empty root of its kind.
func TestNFOCreateCoversMovieSeriesSeasonAndEpisode(t *testing.T) {
	f := newJobFixture(t)
	service, _, _ := nfoCreateService(t, f)
	library, rootID := f.registration.Library.ID, f.registration.RootID
	var root string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT path FROM library_roots WHERE id=$1::uuid`, rootID).Scan(&root); err != nil {
		t.Fatal(err)
	}
	newItem := func(kind string) string {
		var id string
		if err := f.s.Pool.QueryRow(f.ctx, `INSERT INTO items(library_id,title,kind) VALUES($1::uuid,'Scanned name',$2) RETURNING id::text`, library, kind).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	directory := func(item, kind, dir string) {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(dir)), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO item_directory_sources(item_id,library_id,kind,root_id,relative_path) VALUES($1::uuid,$2::uuid,$3,$4::uuid,$5)`, item, library, kind, rootID, dir); err != nil {
			t.Fatal(err)
		}
	}
	series, season, episode, movie := newItem("Series"), newItem("Season"), newItem("Episode"), newItem("Movie")
	directory(series, "Series", "Show")
	directory(season, "Season", "Show/Season 1")
	if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO media_sources(item_id,library_id,root_id,relative_path,content_type) VALUES($1::uuid,$2::uuid,$3::uuid,'Show/Season 1/e01.mkv','video/x-matroska'),($4::uuid,$2::uuid,$3::uuid,'bare.mkv','video/x-matroska')`, episode, library, rootID, movie); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Show/Season 1/e01.mkv", "bare.mkv"} {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(name)), []byte("media"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var prepared []domain.NFOWritePreparation
	for i, item := range []string{series, season, episode, movie} {
		request := domain.NFOWritePrepareRequest{ItemID: item, Revision: 1, Edits: []domain.NFOWriteTextEdit{{Field: "title", Value: "Created " + item[:4]}}, MaxBytes: domain.NFODefaultSourceBytes, Create: true}
		p, _, err := service.Prepare(f.ctx, f.a, "kinds-"+strconv.Itoa(i), request)
		if err != nil {
			t.Fatal("create preparation failed", item, err)
		}
		prepared = append(prepared, p)
	}
	s := submitNFOCreateJob(t, f, "kinds", prepared...)
	startNFOWriteRunner(t, f.s, nfoWriteTestWriter(t))
	s.await(t, domain.JobSucceeded)
	for i, want := range []struct{ path, root string }{
		{"Show/tvshow.nfo", "tvshow"}, {"Show/Season 1/season.nfo", "season"}, {"Show/Season 1/e01.nfo", "episodedetails"}, {"bare.nfo", "movie"},
	} {
		if s.prepared[i].Scope.Source.RelativePath != want.path {
			t.Fatal("create path differs from the reader's lookup", s.prepared[i].Scope.Source.RelativePath)
		}
		data := s.target(t, i)
		document, err := nfo.Read(f.ctx, bytes.NewReader(data), domain.NFODefaultSourceBytes)
		if err != nil || document.Root != want.root || !bytes.Equal(data, s.prepared[i].Replacement) || !strings.HasPrefix(document.Entries[0].Title, "Created ") {
			t.Fatal("created file differs", want.path, err)
		}
		if _, err := os.Lstat(s.targetPath(i) + ".jelee.bak"); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("a create made a backup", want.path)
		}
	}
	s.resolved(t)
}

// The directory that held the NFO name when it was prepared is part of the
// first observation. A copy of the path (another directory) or a symlink in its
// place is refused at stage; nothing is created in either.
func TestNFOCreateRefusesAReplacedParentDirectory(t *testing.T) {
	for _, change := range []string{"new directory", "symlink"} {
		t.Run(change, func(t *testing.T) {
			f := newJobFixture(t)
			service, _, _ := nfoCreateService(t, f)
			library, rootID := f.registration.Library.ID, f.registration.RootID
			var root string
			if err := f.s.Pool.QueryRow(f.ctx, `SELECT path FROM library_roots WHERE id=$1::uuid`, rootID).Scan(&root); err != nil {
				t.Fatal(err)
			}
			var item string
			if err := f.s.Pool.QueryRow(f.ctx, `INSERT INTO items(library_id,title,kind) VALUES($1::uuid,'Series','Series') RETURNING id::text`, library).Scan(&item); err != nil {
				t.Fatal(err)
			}
			show := filepath.Join(root, "Show")
			if err := os.MkdirAll(show, 0o755); err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO item_directory_sources(item_id,library_id,kind,root_id,relative_path) VALUES($1::uuid,$2::uuid,'Series',$3::uuid,'Show')`, item, library, rootID); err != nil {
				t.Fatal(err)
			}
			prepared, _, err := service.Prepare(f.ctx, f.a, "replaced-parent", domain.NFOWritePrepareRequest{ItemID: item, Revision: 1, Edits: []domain.NFOWriteTextEdit{{Field: "title", Value: "Show"}}, MaxBytes: domain.NFODefaultSourceBytes, Create: true})
			if err != nil {
				t.Fatal(err)
			}
			s := submitNFOCreateJob(t, f, "replaced-parent-job", prepared)
			elsewhere := filepath.Join(root, "Elsewhere")
			if err := os.Mkdir(elsewhere, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(show, filepath.Join(root, "Show.old")); err != nil {
				t.Fatal(err)
			}
			if change == "new directory" {
				if err := os.Mkdir(show, 0o755); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Symlink(elsewhere, show); err != nil {
				t.Skip("symlinks unavailable")
			}
			l := s.claim(t, nfoWriteTestOwner)
			if err := s.runWorker(t, f.s, l); !errors.Is(err, app.ErrNFOWritePartial) {
				t.Fatal("the create over a replaced directory was not rejected", err)
			}
			for _, dir := range []string{show, elsewhere, filepath.Join(root, "Show.old")} {
				entries, err := os.ReadDir(dir)
				if err != nil || len(entries) != 0 {
					t.Fatal("a rejected create left files in", filepath.Base(dir), err)
				}
			}
		})
	}
}

// Negative control: in an owned schema, without the unique claim constraint and
// the claim scope check, a second create for the same absent target opens its
// journal. The exclusion is therefore the claim, not an accident of the fixture.
func TestNFOCreateExclusionControlWithoutClaims(t *testing.T) {
	f := newJobFixture(t)
	service, _, request := nfoCreateService(t, f)
	first, _, err := service.Prepare(f.ctx, f.a, "control-first", request)
	if err != nil {
		t.Fatal(err)
	}
	second := secondCreateItem(t, f, service, "control-second")
	s1 := submitNFOCreateJob(t, f, "control-one", first)
	l1 := s1.claim(t, nfoWriteTestOwner)
	if _, err := f.s.BeginNFOWriteCommit(f.ctx, l1, 1); err != nil {
		t.Fatal(err)
	}
	s1.stopUnresolved(t)
	s2 := submitNFOCreateJob(t, f, "control-two", second)
	l2 := s2.claim(t, nfoWriteTestOwner)
	for _, statement := range []string{
		`ALTER TABLE nfo_write_native_claims DROP CONSTRAINT nfo_native_target_unresolved`,
		`CREATE OR REPLACE FUNCTION check_nfo_create_claim_scope(schema_name text, job uuid, seq integer) RETURNS void LANGUAGE sql AS 'SELECT'`,
	} {
		if _, err := f.s.Pool.Exec(f.ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.s.BeginNFOWriteCommit(f.ctx, l2, 1); err != nil {
		t.Fatal("the control did not remove the exclusion", err)
	}
	var tokens int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(DISTINCT token) FROM nfo_write_native_claims WHERE kind='nfo_absent'`).Scan(&tokens); err != nil || tokens != 2 {
		t.Fatal("both creates must hold the same absent target in the control", tokens, err)
	}
}
