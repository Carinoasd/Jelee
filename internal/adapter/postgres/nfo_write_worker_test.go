package postgres

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/nfo"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/jobs"
	"github.com/MoYuanCN/Jelee/internal/platform/resources"
)

const nfoWriteTestOwner = "b0000000-0000-4000-8000-000000000001"

type nfoWriteJobFixtureState struct {
	f        jobFixture
	job      domain.Job
	prepared []domain.NFOWritePreparation
}

// nfoWriteSecondPreparation adds another item with its own NFO to the library.
func nfoWriteSecondPreparation(t *testing.T, f jobFixture, service *app.NFOWritePreparations) domain.NFOWritePreparation {
	t.Helper()
	item := metadataItem(t, f)
	var root string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT id::text FROM library_roots WHERE library_id=$1::uuid`, f.registration.Library.ID).Scan(&root); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO media_sources(item_id,library_id,root_id,relative_path,content_type) VALUES($1::uuid,$2::uuid,$3::uuid,'second.mkv','video/x-matroska')`, item, f.registration.Library.ID, root); err != nil {
		t.Fatal(err)
	}
	scope, err := f.s.ResolveItemNFO(f.ctx, f.a, item, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scope.Source.RootPath, filepath.FromSlash(scope.MediaPath)), []byte("owned second media fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scope.Source.RootPath, filepath.FromSlash(scope.Source.RelativePath)), []byte("<movie><title>second</title></movie>"), 0600); err != nil {
		t.Fatal(err)
	}
	request := domain.NFOWritePrepareRequest{ItemID: item, Revision: 1, Edits: []domain.NFOWriteTextEdit{{Field: "title", Value: "第二個"}}, MaxBytes: domain.NFODefaultSourceBytes, Backups: 2}
	prepared, _, err := service.Prepare(f.ctx, f.a, "second-source", request)
	if err != nil {
		t.Fatal("second preparation unavailable", err)
	}
	return prepared
}

// newNFOWriteJob admits the prepared intents through the production path.
func newNFOWriteJob(t *testing.T, entries int) nfoWriteJobFixtureState {
	t.Helper()
	f, service, _, request := nfoWritePreparationFixture(t)
	first, _, err := service.Prepare(f.ctx, f.a, "first-source", request)
	if err != nil {
		t.Fatal(err)
	}
	state := nfoWriteJobFixtureState{f: f, prepared: []domain.NFOWritePreparation{first}}
	if entries > 1 {
		state.prepared = append(state.prepared, nfoWriteSecondPreparation(t, f, service))
	}
	ids := make([]string, len(state.prepared))
	for i, p := range state.prepared {
		ids[i] = p.ID
	}
	job, replay, err := f.s.SubmitNFOWriteJob(f.ctx, f.a, f.registration.Library.ID, "write-batch", domain.JobPriorityManual, ids, f.policy)
	if err != nil || replay || job.Kind != domain.JobNFOWrite || job.State != domain.JobQueued {
		t.Fatal("nfo write admission failed", err)
	}
	again, replay, err := f.s.SubmitNFOWriteJob(f.ctx, f.a, f.registration.Library.ID, "write-batch", domain.JobPriorityManual, ids, f.policy)
	if err != nil || !replay || again.ID != job.ID {
		t.Fatal("nfo write admission replay differs", err)
	}
	state.job = job
	return state
}

func (s nfoWriteJobFixtureState) target(t *testing.T, i int) []byte {
	t.Helper()
	p := s.prepared[i]
	data, err := os.ReadFile(filepath.Join(p.Scope.Source.RootPath, filepath.FromSlash(p.Scope.Source.RelativePath)))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func (s nfoWriteJobFixtureState) backup(t *testing.T, i int) []byte {
	t.Helper()
	p := s.prepared[i]
	data, err := os.ReadFile(filepath.Join(p.Scope.Source.RootPath, filepath.FromSlash(p.Scope.Source.RelativePath)+".jelee.bak"))
	if err != nil {
		t.Fatal("newest backup missing")
	}
	return data
}

func (s nfoWriteJobFixtureState) claim(t *testing.T, owner string) domain.JobLease {
	t.Helper()
	if _, err := s.f.s.ClaimJob(s.f.ctx, "incapable-worker", false, time.Minute); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("worker without NFO write capability claimed the job", err)
	}
	l, err := s.f.s.ClaimJobWithCapabilities(s.f.ctx, owner, false, time.Minute, domain.ScanCapabilities{NFOWrite: true})
	if err != nil || l.Job.ID != s.job.ID || l.Job.Kind != domain.JobNFOWrite {
		t.Fatal("capable worker could not claim the job", err)
	}
	return l
}

func (s nfoWriteJobFixtureState) state(t *testing.T) (string, string) {
	t.Helper()
	var state, code string
	if err := s.f.s.Pool.QueryRow(s.f.ctx, `SELECT state,error_code FROM jobs WHERE id=$1::uuid`, s.job.ID).Scan(&state, &code); err != nil {
		t.Fatal(err)
	}
	return state, code
}

func (s nfoWriteJobFixtureState) resolved(t *testing.T) {
	t.Helper()
	var resolutions, claims int
	if err := s.f.s.Pool.QueryRow(s.f.ctx, `SELECT (SELECT count(*) FROM nfo_write_commit_resolutions WHERE job_id=$1::uuid),(SELECT count(*) FROM nfo_write_native_claims c JOIN nfo_write_commit_journal w ON w.token=c.token WHERE w.job_id=$1::uuid)`, s.job.ID).Scan(&resolutions, &claims); err != nil || resolutions != 1 || claims != 0 {
		t.Fatal("job not resolved or claims retained", resolutions, claims, err)
	}
}

func nfoWriteTestWriter(t *testing.T) *nfo.Writer {
	t.Helper()
	budget, err := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 1, Queue: 4})
	if err != nil {
		t.Fatal(err)
	}
	writer, err := nfo.NewWriterWithBudget(budget)
	if err != nil {
		t.Fatal(err)
	}
	return writer
}

type idleInventoryScanner struct{}

func (idleInventoryScanner) ScanDirectory(context.Context, domain.ScanDirectory, func(domain.ScanBatch) error) error {
	return domain.ErrScanUnavailable
}

func startNFOWriteRunner(t *testing.T, s *Store, writer *nfo.Writer) *jobs.Runner {
	t.Helper()
	opts := jobs.DefaultOptions()
	opts.Workers = 1
	opts.NFOWrite = &jobs.NFOWriteOptions{Repository: s, Committer: writer, RecoveryInterval: 200 * time.Millisecond}
	runner, err := jobs.New(s, idleInventoryScanner{}, opts, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := runner.Stop(ctx); err != nil {
			t.Error("runner did not stop")
		}
	})
	return runner
}

func (s nfoWriteJobFixtureState) await(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		if state, _ := s.state(t); state == want {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	state, code := s.state(t)
	t.Fatal("job did not reach state", want, state, code)
}

func TestNFOWriteWorkerEndToEnd(t *testing.T) {
	s := newNFOWriteJob(t, 2)
	startNFOWriteRunner(t, s.f.s, nfoWriteTestWriter(t))
	s.await(t, domain.JobSucceeded)
	for i, p := range s.prepared {
		if !bytes.Equal(s.target(t, i), p.Replacement) || !bytes.Equal(s.backup(t, i), p.Original) {
			t.Fatal("entry not replaced with a backup", i)
		}
	}
	if _, code := s.state(t); code != "" {
		t.Fatal("succeeded job kept an error code")
	}
	var files int64
	if err := s.f.s.Pool.QueryRow(s.f.ctx, `SELECT files FROM jobs WHERE id=$1::uuid`, s.job.ID).Scan(&files); err != nil || files != 2 {
		t.Fatal("job progress differs", files, err)
	}
	s.resolved(t)
}

func TestNFOWriteWorkerRejectedEntryContinuesBatch(t *testing.T) {
	s := newNFOWriteJob(t, 2)
	// The user replaces the first NFO after preparation: that entry is
	// rejected and concluded, the second is still written, the job fails.
	first := s.prepared[0]
	path := filepath.Join(first.Scope.Source.RootPath, filepath.FromSlash(first.Scope.Source.RelativePath))
	edited := []byte("<movie><title>edited by user</title></movie>")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, edited, 0600); err != nil {
		t.Fatal(err)
	}
	l := s.claim(t, nfoWriteTestOwner)
	worker, err := app.NewNFOWriteWorker(s.f.s, nfoWriteTestWriter(t), nfoWriteTestOwner, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Run(s.f.ctx, l); !errors.Is(err, app.ErrNFOWritePartial) {
		t.Fatal("rejected entry not reported", err)
	}
	if !bytes.Equal(s.target(t, 0), edited) || !bytes.Equal(s.target(t, 1), s.prepared[1].Replacement) {
		t.Fatal("batch files differ")
	}
	if resolved, err := s.f.s.FinishNFOWriteJob(s.f.ctx, l, domain.JobFailed, "nfo_write_failed"); err != nil || !resolved {
		t.Fatal("partial job not resolved at stop", err)
	}
	if state, code := s.state(t); state != domain.JobFailed || code != "nfo_write_failed" {
		t.Fatal("partial job state differs", state, code)
	}
	s.resolved(t)
}

// cancelAfterBackupStore requests cancellation once the backup phase commits,
// so the replacement cannot be confirmed after the target Rename.
type cancelAfterBackupStore struct {
	*Store
	cancel func()
}

func (s *cancelAfterBackupStore) SaveNFOWriteCommitSettlement(ctx context.Context, l domain.JobLease, seq int, token string, attempt uint8, phase domain.NFOWriteCommitSettlementPhase) (domain.NFOWriteCommitSettlement, error) {
	saved, err := s.Store.SaveNFOWriteCommitSettlement(ctx, l, seq, token, attempt, phase)
	if err == nil && phase == domain.NFOWriteCommitBackedUp && s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	return saved, err
}

func TestNFOWriteWorkerCancellationRollsBack(t *testing.T) {
	s := newNFOWriteJob(t, 1)
	l := s.claim(t, nfoWriteTestOwner)
	repository := &cancelAfterBackupStore{Store: s.f.s, cancel: func() {
		if _, err := s.f.s.CancelJob(s.f.ctx, s.f.a, s.job.ID); err != nil {
			t.Error("owned cancellation failed")
		}
	}}
	writer := nfoWriteTestWriter(t)
	worker, err := app.NewNFOWriteWorker(repository, writer, nfoWriteTestOwner, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Run(s.f.ctx, l); err == nil {
		t.Fatal("cancelled write reported success")
	}
	// The Rename happened but the replacement was never confirmed.
	if !bytes.Equal(s.target(t, 0), s.prepared[0].Replacement) {
		t.Fatal("fixture did not stop between Rename and confirmation")
	}
	if resolved, err := s.f.s.FinishNFOWriteJob(s.f.ctx, l, domain.JobCancelled, ""); err != nil || resolved {
		t.Fatal("cancelled stop resolved a token between backup and Rename", err)
	}
	if resolved, err := worker.RecoverPending(s.f.ctx, 4); err != nil || resolved != 1 {
		t.Fatal("cancelled job not recovered", resolved, err)
	}
	if !bytes.Equal(s.target(t, 0), s.prepared[0].Original) {
		t.Fatal("cancelled write was not rolled back")
	}
	if state, _ := s.state(t); state != domain.JobCancelled {
		t.Fatal("cancelled job changed state", state)
	}
	var phases []int16
	rows, err := s.f.s.Pool.Query(s.f.ctx, `SELECT s.phase FROM nfo_write_commit_settlements s JOIN nfo_write_commit_journal w ON w.token=s.token WHERE w.job_id=$1::uuid ORDER BY s.phase`, s.job.ID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var phase int16
		if err := rows.Scan(&phase); err != nil {
			t.Fatal(err)
		}
		phases = append(phases, phase)
	}
	rows.Close()
	if len(phases) != 2 || phases[0] != 1 || phases[1] != 3 {
		t.Fatal("cancelled phases differ", phases)
	}
	s.resolved(t)
}

// crashAfterRenameStore loses the process right after the target Rename: the
// replaced phase never commits and the worker never stops the job.
type crashAfterRenameStore struct{ *Store }

func (s crashAfterRenameStore) SaveNFOWriteCommitSettlement(ctx context.Context, l domain.JobLease, seq int, token string, attempt uint8, phase domain.NFOWriteCommitSettlementPhase) (domain.NFOWriteCommitSettlement, error) {
	if phase == domain.NFOWriteCommitReplaced {
		return domain.NFOWriteCommitSettlement{}, domain.ErrDatabase
	}
	return s.Store.SaveNFOWriteCommitSettlement(ctx, l, seq, token, attempt, phase)
}

func TestNFOWriteWorkerRecoveryLoopAfterCrash(t *testing.T) {
	s := newNFOWriteJob(t, 1)
	l := s.claim(t, nfoWriteTestOwner)
	writer := nfoWriteTestWriter(t)
	worker, err := app.NewNFOWriteWorker(crashAfterRenameStore{s.f.s}, writer, nfoWriteTestOwner, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Run(s.f.ctx, l); err == nil {
		t.Fatal("crashed worker reported success")
	}
	if !bytes.Equal(s.target(t, 0), s.prepared[0].Replacement) {
		t.Fatal("crash fixture did not rename")
	}
	// The dead worker's lease expires; a new runner stops and recovers the job.
	if _, err := s.f.s.Pool.Exec(s.f.ctx, `UPDATE jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, s.job.ID); err != nil {
		t.Fatal(err)
	}
	fresh, err := Open(s.f.ctx, s.f.s.Pool.Config().ConnString(), 6)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(fresh.Pool.Close)
	startNFOWriteRunner(t, fresh, nfoWriteTestWriter(t))
	s.await(t, domain.JobSucceeded)
	if !bytes.Equal(s.target(t, 0), s.prepared[0].Replacement) || !bytes.Equal(s.backup(t, 0), s.prepared[0].Original) {
		t.Fatal("recovered files differ")
	}
	s.resolved(t)
}
