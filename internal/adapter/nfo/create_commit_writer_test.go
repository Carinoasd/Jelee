//go:build linux

package nfo

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/resources"
)

// createWriterFixture is a real create preparation of a library without the
// NFO, with the repository fake that models the schema rules of the rewrite.
type createWriterFixture struct {
	root     string
	scope    domain.NFOItemScope
	prepared domain.NFOWritePreparation
	budget   *resources.Budget
	repo     *settlementRepository
	lease    domain.JobLease
	record   domain.NFOWriteCommitRecord
}

func newCreateWriterFixture(t *testing.T) createWriterFixture {
	t.Helper()
	scope, root := createScope(t, "Movie")
	b := writerBudget(t, 1, 0)
	preparer, _ := NewWritePreparer(b)
	prepared, err := preparer.PrepareNFOWrite(context.Background(), scope, createRequest(scope))
	if err != nil {
		t.Fatal("create preparation failed", err)
	}
	lease, record := stagingLease()
	repo := &settlementRepository{attemptStagingRepository: &attemptStagingRepository{retainedStagingRepository: &retainedStagingRepository{stagingRepository: &stagingRepository{read: func(_ context.Context, l domain.JobLease, seq int) (domain.NFOWriteTask, error) {
		return domain.NFOWriteTask{JobID: l.Job.ID, Sequence: seq, Preparation: prepared}, nil
	}}}}}
	return createWriterFixture{root: root, scope: scope, prepared: prepared, budget: b, repo: repo, lease: lease, record: record}
}

func (f createWriterFixture) targetPath() string {
	return filepath.Join(f.root, filepath.FromSlash(f.scope.Source.RelativePath))
}

func (f createWriterFixture) writer(t *testing.T) *Writer {
	t.Helper()
	w, err := NewWriterWithBudget(f.budget)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func (f createWriterFixture) stage(t *testing.T) {
	t.Helper()
	if err := f.writer(t).stageCreateFiles(context.Background(), f.lease, f.record, f.repo, nativeNFOWriteOperations()); err != nil {
		t.Fatal("create stage failed", err)
	}
}

func (f createWriterFixture) settle(t *testing.T, ops nfoWriteOperations) error {
	t.Helper()
	err := f.writer(t).settleCreateFiles(context.Background(), f.lease, f.record, f.repo, ops)
	if used, _ := f.budget.PayloadBytes(); used != 0 {
		t.Fatal("create settle leaked payload bytes")
	}
	return err
}

func (f createWriterFixture) requireTargetAbsent(t *testing.T) {
	t.Helper()
	if _, err := os.Lstat(f.targetPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the target must be absent", err)
	}
}

func (f createWriterFixture) requireTarget(t *testing.T, want []byte) {
	t.Helper()
	data, err := os.ReadFile(f.targetPath())
	if err != nil || !bytes.Equal(data, want) {
		t.Fatal("unexpected target", err)
	}
}

func TestCommitNFOWriteEntryCreatesTheTarget(t *testing.T) {
	f := newCreateWriterFixture(t)
	w := f.writer(t)
	if err := w.CommitNFOWriteEntry(context.Background(), f.lease, f.record, f.repo); err != nil {
		t.Fatal("create entry failed", err)
	}
	f.requireTarget(t, f.prepared.Replacement)
	if !phasesEqual(f.repo.recorded(), domain.NFOWriteCommitBackedUp, domain.NFOWriteCommitReplaced) {
		t.Fatal("create phases", f.repo.recorded())
	}
	// A replay after the terminal phase changes nothing.
	if err := w.CommitNFOWriteEntry(context.Background(), f.lease, f.record, f.repo); err != nil {
		t.Fatal("create replay failed", err)
	}
	f.requireTarget(t, f.prepared.Replacement)
}

func TestCommitNFOWriteEntryRefusesATargetThatAppeared(t *testing.T) {
	f := newCreateWriterFixture(t)
	foreign := []byte("<movie><title>someone else</title></movie>")
	if err := os.WriteFile(f.targetPath(), foreign, 0o600); err != nil {
		t.Fatal(err)
	}
	err := f.writer(t).CommitNFOWriteEntry(context.Background(), f.lease, f.record, f.repo)
	if err == nil || !errors.Is(err, domain.ErrNFOWriteRejected) {
		t.Fatal("a foreign target must be a rejection", err)
	}
	f.requireTarget(t, foreign)
	if len(f.repo.recorded()) != 0 {
		t.Fatal("nothing may be settled", f.repo.recorded())
	}
}

func TestSettleCreateFilesTargetAppearsAfterStage(t *testing.T) {
	f := newCreateWriterFixture(t)
	f.stage(t)
	foreign := []byte("<movie><title>someone else</title></movie>")
	if err := os.WriteFile(f.targetPath(), foreign, 0o600); err != nil {
		t.Fatal(err)
	}
	err := f.settle(t, nativeNFOWriteOperations())
	// The scope recheck or the link finds the object; either way it is untouched
	// and the token never reports a created target.
	if err == nil || errors.Is(err, nil) {
		t.Fatal("a foreign target must fail the settlement")
	}
	f.requireTarget(t, foreign)
	for _, phase := range f.repo.recorded() {
		if phase == domain.NFOWriteCommitReplaced {
			t.Fatal("the foreign object was reported as created")
		}
	}
}

func TestSettleCreateFilesLinkErrorWithForeignTargetConcludesTheToken(t *testing.T) {
	f := newCreateWriterFixture(t)
	f.stage(t)
	foreign := []byte("<movie><title>raced</title></movie>")
	ops := nativeNFOWriteOperations()
	ops.link = func(_ *os.Root, _, _ string) error {
		if err := os.WriteFile(f.targetPath(), foreign, 0o600); err != nil {
			t.Fatal(err)
		}
		return os.ErrExist
	}
	err := f.settle(t, ops)
	if !errors.Is(err, ErrTargetAppeared) {
		t.Fatal("link race", err)
	}
	f.requireTarget(t, foreign)
	if !phasesEqual(f.repo.recorded(), domain.NFOWriteCommitBackedUp, domain.NFOWriteCommitRolledBack) {
		t.Fatal("the token must conclude without a target", f.repo.recorded())
	}
}

func TestSettleCreateFilesDirectorySyncFailureRollsBack(t *testing.T) {
	f := newCreateWriterFixture(t)
	f.stage(t)
	ops := nativeNFOWriteOperations()
	native := ops.syncDirectory
	failed := false
	ops.syncDirectory = func(directory *os.Root) error {
		if !failed {
			failed = true
			return errors.New("sync failed")
		}
		return native(directory)
	}
	err := f.settle(t, ops)
	if !errors.Is(err, ErrRolledBack) {
		t.Fatal("sync failure", err)
	}
	f.requireTargetAbsent(t)
	if !phasesEqual(f.repo.recorded(), domain.NFOWriteCommitBackedUp, domain.NFOWriteCommitRolledBack) {
		t.Fatal("rollback phases", f.repo.recorded())
	}
	// The concluded token is stable: a second settle reports the rollback.
	if err := f.settle(t, nativeNFOWriteOperations()); !errors.Is(err, ErrRolledBack) {
		t.Fatal("rolled back replay", err)
	}
	f.requireTargetAbsent(t)
}

func TestSettleCreateFilesPhaseOneFailureCreatesNothing(t *testing.T) {
	f := newCreateWriterFixture(t)
	f.stage(t)
	f.repo.beforeSave = func(domain.NFOWriteCommitSettlementPhase) error { return domain.ErrDatabase }
	if err := f.settle(t, nativeNFOWriteOperations()); err == nil {
		t.Fatal("a failed phase 1 save must fail the settlement")
	}
	f.requireTargetAbsent(t)
	if len(f.repo.recorded()) != 0 {
		t.Fatal("no phase may be recorded", f.repo.recorded())
	}
}

func TestSettleCreateFilesResumesAfterALostReplacedResponse(t *testing.T) {
	f := newCreateWriterFixture(t)
	w := f.writer(t)
	f.stage(t)
	f.repo.afterSave = func(phase domain.NFOWriteCommitSettlementPhase) error {
		if phase == domain.NFOWriteCommitReplaced {
			return domain.ErrDatabase
		}
		return nil
	}
	if err := f.settle(t, nativeNFOWriteOperations()); err == nil {
		t.Fatal("a lost response must surface")
	}
	// The link happened and phase 2 is durable in the fake; retry from phase 1.
	f.requireTarget(t, f.prepared.Replacement)
	f.repo.afterSave = nil
	f.repo.smu.Lock()
	f.repo.phases = f.repo.phases[:1]
	f.repo.smu.Unlock()
	if err := w.CommitNFOWriteEntry(context.Background(), f.lease, f.record, f.repo); err != nil {
		t.Fatal("resume of a linked output failed", err)
	}
	f.requireTarget(t, f.prepared.Replacement)
	if !phasesEqual(f.repo.recorded(), domain.NFOWriteCommitBackedUp, domain.NFOWriteCommitReplaced) {
		t.Fatal("resume phases", f.repo.recorded())
	}
}

func TestSettleCreateFilesCancelRequestedAborts(t *testing.T) {
	// Before any link the cancel concludes the token with the target absent.
	f := newCreateWriterFixture(t)
	f.stage(t)
	f.lease.Job.CancelRequested = true
	if err := f.settle(t, nativeNFOWriteOperations()); !errors.Is(err, ErrRolledBack) {
		t.Fatal("cancel before link", err)
	}
	f.requireTargetAbsent(t)
	if !phasesEqual(f.repo.recorded(), domain.NFOWriteCommitRolledBack) {
		t.Fatal("cancel phases", f.repo.recorded())
	}
}

func TestAbortNFOWriteEntryRemovesALinkedOutputOnlyWhileUnchanged(t *testing.T) {
	linked := func(t *testing.T) createWriterFixture {
		t.Helper()
		f := newCreateWriterFixture(t)
		f.stage(t)
		f.repo.afterSave = func(phase domain.NFOWriteCommitSettlementPhase) error {
			if phase == domain.NFOWriteCommitReplaced {
				return domain.ErrDatabase
			}
			return nil
		}
		if err := f.settle(t, nativeNFOWriteOperations()); err == nil {
			t.Fatal("expected the lost response")
		}
		f.repo.afterSave = nil
		f.repo.smu.Lock()
		f.repo.phases = f.repo.phases[:1]
		f.repo.smu.Unlock()
		return f
	}
	t.Run("unchanged", func(t *testing.T) {
		f := linked(t)
		phase, err := f.writer(t).AbortNFOWriteEntry(context.Background(), f.lease, f.record, f.repo)
		if err != nil || phase != domain.NFOWriteCommitRolledBack {
			t.Fatal("abort", phase, err)
		}
		f.requireTargetAbsent(t)
	})
	t.Run("edited in place", func(t *testing.T) {
		f := linked(t)
		edited := append(append([]byte(nil), f.prepared.Replacement...), []byte("<!-- edited -->")...)
		if err := os.WriteFile(f.targetPath(), edited, 0o600); err != nil {
			t.Fatal(err)
		}
		phase, err := f.writer(t).AbortNFOWriteEntry(context.Background(), f.lease, f.record, f.repo)
		if !errors.Is(err, ErrChanged) || phase == domain.NFOWriteCommitRolledBack {
			t.Fatal("an edited target must not be removed", phase, err)
		}
		f.requireTarget(t, edited)
	})
	t.Run("without phase one", func(t *testing.T) {
		f := newCreateWriterFixture(t)
		f.stage(t)
		phase, err := f.writer(t).AbortNFOWriteEntry(context.Background(), f.lease, f.record, f.repo)
		if err != nil || phase != domain.NFOWriteCommitRolledBack {
			t.Fatal("abort before phase 1", phase, err)
		}
		f.requireTargetAbsent(t)
	})
}

func TestCreateWriterRejectsInvalidInput(t *testing.T) {
	f := newCreateWriterFixture(t)
	w := f.writer(t)
	ctx := context.Background()
	ops := nativeNFOWriteOperations()
	badRecord := f.record
	badRecord.Token = "not-an-id"
	otherJob := f.record
	otherJob.JobID = "a0000000-0000-0000-0000-0000000000ff"
	badSequence := f.record
	badSequence.Sequence = 101
	for name, call := range map[string]func() error{
		"stage nil writer":     func() error { return (*Writer)(nil).stageCreateFiles(ctx, f.lease, f.record, f.repo, ops) },
		"stage nil repository": func() error { return w.stageCreateFiles(ctx, f.lease, f.record, nil, ops) },
		"stage bad token":      func() error { return w.stageCreateFiles(ctx, f.lease, badRecord, f.repo, ops) },
		"stage other job":      func() error { return w.stageCreateFiles(ctx, f.lease, otherJob, f.repo, ops) },
		"stage bad sequence":   func() error { return w.stageCreateFiles(ctx, f.lease, badSequence, f.repo, ops) },
		"settle nil writer":    func() error { return (*Writer)(nil).settleCreateFiles(ctx, f.lease, f.record, f.repo, ops) },
		"settle nil repo":      func() error { return w.settleCreateFiles(ctx, f.lease, f.record, nil, ops) },
		"settle bad token":     func() error { return w.settleCreateFiles(ctx, f.lease, badRecord, f.repo, ops) },
	} {
		if err := call(); !errors.Is(err, ErrInvalidInput) {
			t.Fatal(name, err)
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := w.stageCreateFiles(cancelled, f.lease, f.record, f.repo, ops); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled stage", err)
	}
	if err := w.settleCreateFiles(cancelled, f.lease, f.record, f.repo, ops); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled settle", err)
	}
	f.requireTargetAbsent(t)
	// A recovery lease may act for the original owner.
	recovery := f.lease
	recovery.Owner, recovery.RecoveryEpoch = "other-owner", 1
	if !nfoCreateRecordValid(recovery, f.record) || nfoCreateRecordValid(domain.JobLease{Job: f.lease.Job, Owner: "other-owner", Generation: f.lease.Generation}, f.record) {
		t.Fatal("only a recovery lease may differ in owner")
	}
}

func TestStageCreateFilesRefusesInconsistentEvidenceAndPreparations(t *testing.T) {
	f := newCreateWriterFixture(t)
	w := f.writer(t)
	ctx := context.Background()
	stage := func() error {
		return w.stageCreateFiles(ctx, f.lease, f.record, f.repo, nativeNFOWriteOperations())
	}
	// A rewrite preparation is not a create.
	rewrite := f.prepared
	rewrite.Request.Create = false
	f.repo.read = func(_ context.Context, l domain.JobLease, seq int) (domain.NFOWriteTask, error) {
		return domain.NFOWriteTask{JobID: l.Job.ID, Sequence: seq, Preparation: rewrite}, nil
	}
	if err := stage(); !errors.Is(err, ErrInvalidInput) {
		t.Fatal("rewrite preparation", err)
	}
	// A task of another job is refused.
	f.repo.read = func(_ context.Context, _ domain.JobLease, seq int) (domain.NFOWriteTask, error) {
		return domain.NFOWriteTask{JobID: "a0000000-0000-0000-0000-0000000000ff", Sequence: seq, Preparation: f.prepared}, nil
	}
	if err := stage(); !errors.Is(err, ErrInvalidInput) {
		t.Fatal("foreign task", err)
	}
	f.repo.read = func(_ context.Context, l domain.JobLease, seq int) (domain.NFOWriteTask, error) {
		return domain.NFOWriteTask{JobID: l.Job.ID, Sequence: seq, Preparation: f.prepared}, nil
	}
	// A plan that is not a create plan contradicts the preparation.
	f.repo.retainedStagingRepository.mu.Lock()
	f.repo.evidence.PlanRecorded = true
	f.repo.evidence.Plan = domain.NFOWriteCommitFilePlan{Version: domain.NFOWriteCommitPlanVersion}
	f.repo.retainedStagingRepository.mu.Unlock()
	if err := stage(); !errors.Is(err, ErrChanged) {
		t.Fatal("rewrite plan for a create", err)
	}
	f.repo.retainedStagingRepository.mu.Lock()
	f.repo.evidence = domain.NFOWriteCommitFileEvidence{ReadyRecorded: true}
	f.repo.retainedStagingRepository.mu.Unlock()
	if err := stage(); !errors.Is(err, ErrChanged) {
		t.Fatal("ready without plan", err)
	}
	// A task that cannot be read fails the stage.
	f.repo.read = func(context.Context, domain.JobLease, int) (domain.NFOWriteTask, error) {
		return domain.NFOWriteTask{}, domain.ErrDatabase
	}
	if err := stage(); !errors.Is(err, domain.ErrDatabase) {
		t.Fatal("task read error", err)
	}
	f.requireTargetAbsent(t)
}

func TestOpenNFOCreateDirectoryRefusesUnsafeLocations(t *testing.T) {
	scope, root := createScope(t, "Movie")
	opened, directory, err := openNFOCreateDirectory(scope)
	if err != nil {
		t.Fatal("open", err)
	}
	if info, err := directory.Lstat("."); err != nil || !info.IsDir() {
		t.Fatal("directory", err)
	}
	_ = directory.Close()
	_ = opened.Close()

	missingRoot := scope
	missingRoot.Source.RootPath = filepath.Join(root, "absent")
	if _, _, err := openNFOCreateDirectory(missingRoot); !errors.Is(err, ErrChanged) {
		t.Fatal("missing root", err)
	}
	fileRoot := scope
	fileRoot.Source.RootPath = filepath.Join(root, "Show", "e01 電影.mkv")
	if _, _, err := openNFOCreateDirectory(fileRoot); !errors.Is(err, ErrChanged) {
		t.Fatal("root that is a file", err)
	}
	missingParent := scope
	missingParent.Source.RelativePath = "Absent/e01.nfo"
	if _, _, err := openNFOCreateDirectory(missingParent); !errors.Is(err, ErrChanged) {
		t.Fatal("missing ancestor", err)
	}
	fileParent := scope
	fileParent.Source.RelativePath = "Show/e01 電影.mkv/e01.nfo"
	if _, _, err := openNFOCreateDirectory(fileParent); !errors.Is(err, ErrChanged) {
		t.Fatal("ancestor that is a file", err)
	}
	if runtime.GOOS == "linux" {
		if err := os.Symlink(filepath.Join(root, "Show"), filepath.Join(root, "Link")); err != nil {
			t.Fatal(err)
		}
		linked := scope
		linked.Source.RelativePath = "Link/e01.nfo"
		if _, _, err := openNFOCreateDirectory(linked); !errors.Is(err, ErrChanged) {
			t.Fatal("symlink ancestor", err)
		}
		linkRoot := scope
		linkRoot.Source.RootPath = filepath.Join(root, "Link")
		if _, _, err := openNFOCreateDirectory(linkRoot); !errors.Is(err, ErrChanged) {
			t.Fatal("symlink root", err)
		}
	}
}

func TestBoundCreateCheckObservesTheReceipt(t *testing.T) {
	f := newCreateWriterFixture(t)
	opened, directory, err := openNFOCreateDirectory(f.scope)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = opened.Close() }()
	defer func() { _ = directory.Close() }()
	receipt := f.prepared.NativeObservation
	check := boundCreateCheck(directory, f.prepared.Scope, receipt)
	if err := check(context.Background()); err != nil {
		t.Fatal("untouched scope", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := check(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled check", err)
	}
	// A receipt of an existing target is not a create receipt.
	if err := boundCreateCheck(directory, f.prepared.Scope, domain.NFONativePreparationReceipt{})(context.Background()); !errors.Is(err, ErrInvalidInput) {
		t.Fatal("zero receipt", err)
	}
	// The held directory must be the observed parent.
	other := t.TempDir()
	otherRoot, err := os.OpenRoot(other)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = otherRoot.Close() }()
	if err := boundCreateCheck(otherRoot, f.prepared.Scope, receipt)(context.Background()); !errors.Is(err, ErrChanged) {
		t.Fatal("other directory", err)
	}
}
