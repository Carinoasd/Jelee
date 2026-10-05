package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

const (
	repairLibrary = "00000000-0000-4000-8000-0000000000e1"
	repairRoot    = "00000000-0000-4000-8000-0000000000e2"
	repairRunID   = "00000000-0000-4000-8000-0000000000e3"
	repairUser    = "00000000-0000-4000-8000-0000000000e4"
	repairSession = "00000000-0000-4000-8000-0000000000e5"
)

// repairFake serves one page per check from candidates and applies every
// repair once: applied candidates disappear from later plans.
type repairFake struct {
	baseline   bool
	candidates map[string][]domain.ConsistencyFinding
	variants   []domain.ConsistencyFinding
	stuck      bool
	applyErr   error
	active     *domain.RepairActiveJob
	started    []domain.RepairRun
	finished   []domain.RepairResult
	enqueued   int
	finishErr  error
}

func (f *repairFake) ConsistencyLibraries(_ context.Context, id string) ([]domain.ConsistencyLibrary, error) {
	if id != "" && id != repairLibrary {
		return nil, nil
	}
	return []domain.ConsistencyLibrary{{ID: repairLibrary, Roots: []domain.ConsistencyRoot{{ID: repairRoot, Path: "/media"}}, Baseline: f.baseline, Snapshot: 1, Revision: 1, Entries: 3}}, nil
}

func (f *repairFake) page(key, cursor string) (domain.ConsistencyPage, error) {
	if f.stuck {
		return domain.ConsistencyPage{Next: "same"}, nil
	}
	if cursor != "" {
		return domain.ConsistencyPage{}, domain.ErrInvalid
	}
	page := domain.ConsistencyPage{Examined: int64(len(f.candidates[key])), Candidates: append([]domain.ConsistencyFinding(nil), f.candidates[key]...), Info: map[string]int64{domain.ConsistencyInfoPending: 1}}
	return page, nil
}

func (f *repairFake) ConsistencyPage(_ context.Context, check string, _ domain.ConsistencyScope, cursor string) (domain.ConsistencyPage, error) {
	return f.page(check, cursor)
}
func (f *repairFake) RepairStatsPage(_ context.Context, scope domain.ConsistencyScope, cursor string) (domain.ConsistencyPage, error) {
	if !scope.SessionCutoff.IsZero() {
		return domain.ConsistencyPage{}, nil
	}
	return f.page("stats", cursor)
}
func (f *repairFake) RepairVariantPage(_ context.Context, cursor string) (domain.ConsistencyPage, error) {
	if cursor != "" {
		return domain.ConsistencyPage{}, domain.ErrInvalid
	}
	return domain.ConsistencyPage{Examined: int64(len(f.variants)), Candidates: append([]domain.ConsistencyFinding(nil), f.variants...)}, nil
}
func (f *repairFake) RepairActiveJob(context.Context, string) (domain.RepairActiveJob, bool, error) {
	if f.active == nil {
		return domain.RepairActiveJob{}, false, nil
	}
	return *f.active, true, nil
}
func (f *repairFake) StartRepairRun(_ context.Context, run domain.RepairRun) (domain.RepairRun, error) {
	run.ID = repairRunID
	f.started = append(f.started, run)
	return run, nil
}
func (f *repairFake) remove(applied []domain.ConsistencyFinding) {
	for key, list := range f.candidates {
		kept := list[:0]
		for _, c := range list {
			gone := false
			for _, a := range applied {
				gone = gone || a.Code == c.Code && a.RelativePath == c.RelativePath && a.UserID == c.UserID && a.ItemID == c.ItemID && a.Day == c.Day && a.Object == c.Object
			}
			if !gone {
				kept = append(kept, c)
			}
		}
		f.candidates[key] = kept
	}
}
func (f *repairFake) apply(fixes []domain.ConsistencyFinding) (domain.ConsistencyFixResult, error) {
	if f.applyErr != nil {
		return domain.ConsistencyFixResult{}, f.applyErr
	}
	f.remove(fixes)
	return domain.ConsistencyFixResult{Applied: int64(len(fixes))}, nil
}
func (f *repairFake) ApplyRepairFixes(_ context.Context, _ domain.RepairRun, fixes []domain.ConsistencyFinding) (domain.ConsistencyFixResult, error) {
	return f.apply(fixes)
}
func (f *repairFake) PurgeRepairProbeCache(_ context.Context, _ domain.RepairRun, targets []domain.ConsistencyFinding) (domain.ConsistencyFixResult, error) {
	return f.apply(targets)
}
func (f *repairFake) PurgeRepairVariantRows(_ context.Context, _ domain.RepairRun, targets []domain.ConsistencyFinding) (domain.ConsistencyFixResult, error) {
	f.variants = nil
	return domain.ConsistencyFixResult{Applied: int64(len(targets)) - 1, Skipped: 1}, nil
}
func (f *repairFake) EnqueueRepairCatalogSync(context.Context, domain.RepairRun, string, domain.JobPolicy) (domain.RepairJob, error) {
	f.enqueued++
	return domain.RepairJob{LibraryID: repairLibrary, JobID: repairRunID, Replayed: f.enqueued > 1}, nil
}
func (f *repairFake) FinishRepairRun(_ context.Context, _ domain.RepairRun, result domain.RepairResult) error {
	f.finished = append(f.finished, result)
	return f.finishErr
}
func (f *repairFake) RevertRepairRun(_ context.Context, _ domain.Actor, run string) (domain.RepairRevertResult, error) {
	return domain.RepairRevertResult{RunID: run, Reverted: 1}, nil
}

type repairFiles map[string]bool

func (f repairFiles) FileExists(_ context.Context, _, relative string) (bool, error) {
	exists, known := f[relative]
	if !known {
		return false, errors.New("unreadable")
	}
	return exists, nil
}

type repairVariants map[string]bool

func (v repairVariants) VariantExists(_ context.Context, object string) (bool, error) {
	exists, known := v[object]
	if !known {
		return false, errors.New("unreadable")
	}
	return exists, nil
}

type repairPaths struct{}

func (repairPaths) RenderPath(_, relative string) string { return "rel:" + relative }

type repairScans struct {
	calls int
	err   error
}

func (s *repairScans) SubmitScanOptions(_ context.Context, _ domain.Actor, library, key, priority string, probe, nfo bool, _ domain.IgnoreIntent) (domain.Job, bool, error) {
	s.calls++
	if s.err != nil {
		return domain.Job{}, false, s.err
	}
	if !nfo || probe || priority != domain.JobPriorityManual || len(key) > 128 {
		return domain.Job{}, false, domain.ErrInvalid
	}
	return domain.Job{ID: repairSession, LibraryID: library}, false, nil
}

type repairStore struct {
	entries int64
	err     error
}

func (s *repairStore) LiveVariants() (int64, int64) { return s.entries, s.entries * 10 }
func (s *repairStore) ClearVariants(context.Context) error {
	if s.err != nil {
		return s.err
	}
	s.entries = 0
	return nil
}

func newRepairFake() *repairFake {
	return &repairFake{baseline: true, candidates: map[string][]domain.ConsistencyFinding{
		"stats": {{Code: domain.ConsistencyDailyCounterDrift, LibraryID: repairLibrary, UserID: repairUser, ItemID: repairSession, Day: "2026-10-01", Fixable: true}},
		domain.ConsistencyVersionCount: {
			{Code: domain.ConsistencyVideoWithoutSource, ItemID: repairSession},
			{Code: domain.ConsistencyUserDataForeignSource, UserID: repairUser, ItemID: repairSession, SourceID: repairRoot, Fixable: true},
		},
		domain.ConsistencyProbeCache: {
			{Code: domain.ConsistencyProbeCacheChanged, RootID: repairRoot, RelativePath: "a.mkv", LibraryID: repairLibrary},
			{Code: domain.ConsistencyProbeCacheOrphan, RootID: repairRoot, RelativePath: "gone.mkv", LibraryID: repairLibrary},
			{Code: domain.ConsistencyProbeCacheOrphan, RootID: repairRoot, RelativePath: "back.mkv", LibraryID: repairLibrary},
			{Code: domain.ConsistencyProbeCacheOrphan, RootID: repairRoot, RelativePath: "unreadable.mkv", LibraryID: repairLibrary},
		},
		domain.ConsistencyOrphanFile: {{Code: domain.ConsistencyVideoNotCataloged, RootID: repairRoot, RelativePath: "new.mkv", LibraryID: repairLibrary}},
		domain.ConsistencyNFOState:   {{Code: domain.ConsistencyNFOChanged, ItemID: repairSession, LibraryID: repairLibrary}},
	}, variants: []domain.ConsistencyFinding{
		{Code: domain.ConsistencyVariantFileMissing, Object: "a/1", Expected: map[string]int64{"lastAccessMicro": 1}},
		{Code: domain.ConsistencyVariantFileMissing, Object: "a/2", Expected: map[string]int64{"lastAccessMicro": 1}},
		{Code: domain.ConsistencyVariantFileMissing, Object: "a/3", Expected: map[string]int64{"lastAccessMicro": 1}},
		{Code: domain.ConsistencyVariantFileMissing, Object: "a/4", Expected: map[string]int64{"lastAccessMicro": 1}},
	}}
}

func newTestRepairer(t *testing.T, f *repairFake) *Repairer {
	t.Helper()
	r, err := NewRepairer(f, repairFiles{"gone.mkv": false, "back.mkv": true}, repairVariants{"a/1": false, "a/2": false, "a/3": true}, repairPaths{})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func repairOptions(action string, dryRun bool) RepairOptions {
	return RepairOptions{Action: action, DryRun: dryRun, Origin: domain.RepairOriginCLI, Policy: domain.JobPolicy{QueueLimit: 4}, CallTimeout: time.Second}
}

func TestRepairerValidatesOptions(t *testing.T) {
	f := newRepairFake()
	r := newTestRepairer(t, f)
	if _, err := NewRepairer(nil, repairFiles{}, nil, repairPaths{}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("repairer without repository")
	}
	admin := domain.Actor{UserID: repairUser, SessionID: repairSession}
	for _, o := range []RepairOptions{
		{Action: "rebuild", Origin: domain.RepairOriginCLI},
		{Action: domain.RepairStats, Origin: "job"},
		{Action: domain.RepairStats, Origin: domain.RepairOriginAPI},
		{Action: domain.RepairStats, Origin: domain.RepairOriginCLI, Actor: admin},
		{Action: domain.RepairStats, Origin: domain.RepairOriginCLI, Library: "x"},
		{Action: domain.RepairImageVariants, Origin: domain.RepairOriginCLI, Library: repairLibrary},
		{Action: domain.RepairStats, Origin: domain.RepairOriginCLI, StatBudget: -1},
		{Action: domain.RepairItems, Origin: domain.RepairOriginCLI},
	} {
		if _, err := r.Run(context.Background(), o); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("%+v accepted: %v", o, err)
		}
	}
	//nolint:staticcheck // SA1012: a nil context must be refused
	if _, err := r.Run(nil, repairOptions(domain.RepairStats, true)); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("nil context accepted")
	}
	if _, err := r.Run(context.Background(), RepairOptions{Action: domain.RepairStats, Library: repairSession, Origin: domain.RepairOriginCLI}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown library: %v", err)
	}
	for _, action := range []string{domain.RepairNFO, domain.RepairImageVariants} {
		if _, err := r.Run(context.Background(), repairOptions(action, true)); !errors.Is(err, domain.ErrRepairUnavailable) {
			t.Fatalf("%s without the server: %v", action, err)
		}
	}
	if len(f.started) != 0 {
		t.Fatal("a refused run started")
	}
	if _, err := r.Revert(context.Background(), domain.Actor{}, "x"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("invalid run accepted")
	}
	if got, err := r.Revert(context.Background(), admin, repairRunID); err != nil || got.Reverted != 1 {
		t.Fatalf("revert: %+v %v", got, err)
	}
}

// TestRepairerDryRunMatchesExecution is the G50.7 contract for every action
// the repository decides: the dry run plans what the execution applies and
// starts nothing; the rerun applies nothing.
func TestRepairerDryRunMatchesExecution(t *testing.T) {
	for _, c := range []struct {
		action  string
		planned int64
		kinds   int
	}{
		{domain.RepairStats, 1, 1},
		{domain.RepairCounts, 1, 1},
		{domain.RepairCaches, 1, 1},
		{domain.RepairItems, 1, 1},
	} {
		t.Run(c.action, func(t *testing.T) {
			f := newRepairFake()
			r := newTestRepairer(t, f)
			dry, err := r.Run(context.Background(), repairOptions(c.action, true))
			if err != nil || dry.Planned != c.planned || dry.Applied != 0 || dry.RunID != "" || len(dry.Targets) != c.kinds || len(f.started) != 0 || dry.State != domain.RepairStatePlanned {
				t.Fatalf("dry run: %+v %v", dry, err)
			}
			done, err := r.Run(context.Background(), repairOptions(c.action, false))
			if err != nil || done.Applied != dry.Planned || done.Planned != dry.Planned || done.State != domain.RepairStateCompleted || len(f.finished) != 1 || done.RunID != repairRunID {
				t.Fatalf("execution: %+v %v", done, err)
			}
			if done.Revertible != domain.RepairRevertible(c.action) {
				t.Fatalf("revertible: %t", done.Revertible)
			}
			again, err := r.Run(context.Background(), repairOptions(c.action, false))
			if err != nil || again.Applied != 0 {
				t.Fatalf("rerun: %+v %v", again, err)
			}
		})
	}
}

func TestRepairerCountsReportsStructuralProblems(t *testing.T) {
	dry, err := newTestRepairer(t, newRepairFake()).Run(context.Background(), repairOptions(domain.RepairCounts, true))
	if err != nil || dry.Info["manual"] != 1 || dry.Targets[0].Kind != domain.RepairTargetUserData {
		t.Fatalf("counts: %+v %v", dry, err)
	}
}

func TestRepairerOrphansConfirmEveryRemoval(t *testing.T) {
	f := newRepairFake()
	r := newTestRepairer(t, f)
	dry, err := r.Run(context.Background(), repairOptions(domain.RepairOrphans, true))
	if err != nil {
		t.Fatal(err)
	}
	// gone.mkv is confirmed absent; back.mkv exists (stale baseline);
	// unreadable.mkv cannot be probed. Variants a/1 and a/2 are missing,
	// a/3 exists and a/4 cannot be probed.
	want := map[string]int64{domain.RepairTargetProbeOrphan: 1, domain.RepairTargetVariantOrphan: 2}
	for _, target := range dry.Targets {
		if want[target.Kind] != target.Planned {
			t.Fatalf("targets: %+v", dry.Targets)
		}
	}
	if dry.Info[domain.ConsistencyInfoBaselineStale] != 1 || dry.Info[domain.ConsistencyInfoUnconfirmed] != 1 || dry.Info[domain.ConsistencyInfoUnverified] != 1 || dry.Reason != domain.RepairReasonUnconfirmed {
		t.Fatalf("info: %+v reason %s", dry.Info, dry.Reason)
	}
	if dry.Samples[0].Path != "rel:gone.mkv" {
		t.Fatalf("samples: %+v", dry.Samples)
	}
	done, err := r.Run(context.Background(), repairOptions(domain.RepairOrphans, false))
	if err != nil || done.Planned != 3 || done.Applied != 2 || done.Skipped != 1 {
		t.Fatalf("execution (one variant row changed meanwhile): %+v %v", done, err)
	}
	// A budget of one probe leaves the rest unconfirmed; a library run
	// never touches the database-wide variant index.
	f = newRepairFake()
	options := repairOptions(domain.RepairOrphans, true)
	options.StatBudget, options.Library = 1, repairLibrary
	limited, err := newTestRepairer(t, f).Run(context.Background(), options)
	if err != nil || limited.Planned != 1 || limited.Info[domain.ConsistencyInfoUnconfirmed] != 2 {
		t.Fatalf("budget: %+v %v", limited, err)
	}
	// Without a variant prober the index is skipped and reported.
	plain, err := NewRepairer(newRepairFake(), repairFiles{"gone.mkv": false}, nil, repairPaths{})
	if err != nil {
		t.Fatal(err)
	}
	skipped, err := plain.Run(context.Background(), repairOptions(domain.RepairOrphans, true))
	if err != nil || skipped.Info[domain.RepairReasonStoreDisabled] != 1 {
		t.Fatalf("no store: %+v %v", skipped, err)
	}
}

func TestRepairerSkipsLibrariesWithoutBaseline(t *testing.T) {
	f := newRepairFake()
	f.baseline = false
	options := repairOptions(domain.RepairCaches, true)
	options.Library = repairLibrary
	result, err := newTestRepairer(t, f).Run(context.Background(), options)
	if err != nil || result.Planned != 0 || result.Reason != domain.RepairReasonNoBaseline {
		t.Fatalf("no baseline: %+v %v", result, err)
	}
	// Statistics do not depend on the baseline; with a retention cutoff the
	// fake reports nothing.
	options = repairOptions(domain.RepairStats, true)
	options.SessionRetention = time.Hour
	if result, err = newTestRepairer(t, f).Run(context.Background(), options); err != nil || result.Planned != 0 {
		t.Fatalf("retention: %+v %v", result, err)
	}
}

func TestRepairerItemsQueueOnceAndReplay(t *testing.T) {
	f := newRepairFake()
	r := newTestRepairer(t, f)
	done, err := r.Run(context.Background(), repairOptions(domain.RepairItems, false))
	if err != nil || len(done.Jobs) != 1 || done.Jobs[0].Replayed || done.Applied != 1 || done.Info[domain.ConsistencyInfoPending] != 1 {
		t.Fatalf("items: %+v %v", done, err)
	}
	again, err := r.Run(context.Background(), repairOptions(domain.RepairItems, false))
	if err != nil || !again.Jobs[0].Replayed || again.Applied != 0 || again.Skipped != 1 {
		t.Fatalf("replay: %+v %v", again, err)
	}
}

func TestRepairerNFOThroughTheServer(t *testing.T) {
	f := newRepairFake()
	scans := &repairScans{}
	r := newTestRepairer(t, f).WithServer(scans, nil)
	admin := domain.Actor{UserID: repairUser, SessionID: repairSession}
	options := repairOptions(domain.RepairNFO, false)
	options.Origin, options.Actor = domain.RepairOriginAPI, admin
	done, err := r.Run(context.Background(), options)
	if err != nil || scans.calls != 1 || done.Applied != 1 || len(done.Jobs) != 1 || done.Jobs[0].JobID != repairSession {
		t.Fatalf("nfo: %+v %v", done, err)
	}
	f.active = &domain.RepairActiveJob{ID: repairRunID, Kind: "inventory_scan", NFO: true}
	again, err := r.Run(context.Background(), options)
	if err != nil || scans.calls != 1 || !again.Jobs[0].Replayed || again.Skipped != 1 {
		t.Fatalf("active NFO scan: %+v %v", again, err)
	}
	f.active = &domain.RepairActiveJob{ID: repairRunID, Kind: "catalog_sync"}
	busy, err := r.Run(context.Background(), options)
	if !errors.Is(err, domain.ErrJobBusy) || busy.State != domain.RepairStateFailed || busy.Reason != domain.RepairReasonFailed {
		t.Fatalf("busy library: %+v %v", busy, err)
	}
	f.active = nil
	scans.err = domain.ErrNFODisabled
	off, err := r.Run(context.Background(), options)
	if err != nil || off.Info["nfoDisabled"] != 1 || off.Skipped != 1 {
		t.Fatalf("NFO disabled: %+v %v", off, err)
	}
}

func TestRepairerImageVariants(t *testing.T) {
	store := &repairStore{entries: 3}
	r := newTestRepairer(t, newRepairFake()).WithServer(nil, store)
	dry, err := r.Run(context.Background(), repairOptions(domain.RepairImageVariants, true))
	if err != nil || dry.Planned != 3 || dry.Info["bytes"] != 30 || store.entries != 3 {
		t.Fatalf("dry run: %+v %v", dry, err)
	}
	done, err := r.Run(context.Background(), repairOptions(domain.RepairImageVariants, false))
	if err != nil || done.Applied != 3 || store.entries != 0 {
		t.Fatalf("execution: %+v %v", done, err)
	}
	if again, err := r.Run(context.Background(), repairOptions(domain.RepairImageVariants, false)); err != nil || again.Planned != 0 || again.Applied != 0 {
		t.Fatalf("rerun: %+v %v", again, err)
	}
	store.entries, store.err = 2, domain.ErrImageUnavailable
	if failed, err := r.Run(context.Background(), repairOptions(domain.RepairImageVariants, false)); !errors.Is(err, domain.ErrImageUnavailable) || failed.State != domain.RepairStateFailed {
		t.Fatalf("clear failure: %+v %v", failed, err)
	}
}

func TestRepairerPersistsFailures(t *testing.T) {
	f := newRepairFake()
	f.stuck = true
	r := newTestRepairer(t, f)
	if result, err := r.Run(context.Background(), repairOptions(domain.RepairStats, false)); !errors.Is(err, domain.ErrDatabase) || result.State != domain.RepairStateFailed || len(f.finished) != 1 {
		t.Fatalf("stuck repository: %+v %v", result, err)
	}
	f = newRepairFake()
	f.applyErr = domain.ErrDatabase
	if result, err := newTestRepairer(t, f).Run(context.Background(), repairOptions(domain.RepairCaches, false)); !errors.Is(err, domain.ErrDatabase) || result.State != domain.RepairStateFailed {
		t.Fatalf("apply failure: %+v %v", result, err)
	}
	f = newRepairFake()
	f.finishErr = domain.ErrConflict
	if _, err := newTestRepairer(t, f).Run(context.Background(), repairOptions(domain.RepairStats, false)); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("finish failure: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f = newRepairFake()
	if _, err := newTestRepairer(t, f).Run(ctx, repairOptions(domain.RepairStats, false)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
}

func TestRepairerSamplesAreBounded(t *testing.T) {
	f := newRepairFake()
	var many []domain.ConsistencyFinding
	for i := 0; i < domain.RepairSamples+5; i++ {
		many = append(many, domain.ConsistencyFinding{Code: domain.ConsistencyDailyCounterDrift, Day: time.Date(2026, 1, 1+i, 0, 0, 0, 0, time.UTC).Format(time.DateOnly), Fixable: true})
	}
	f.candidates["stats"] = many
	dry, err := newTestRepairer(t, f).Run(context.Background(), repairOptions(domain.RepairStats, true))
	if err != nil || len(dry.Samples) != domain.RepairSamples || !dry.SamplesTruncated || dry.Planned != int64(len(many)) {
		t.Fatalf("samples: %d truncated=%t planned=%d %v", len(dry.Samples), dry.SamplesTruncated, dry.Planned, err)
	}
}
