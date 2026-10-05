package app

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

const (
	consistencyTestLibrary = "00000000-0000-4000-8000-000000000001"
	consistencyTestRoot    = "00000000-0000-4000-8000-000000000002"
	consistencyTestJob     = "00000000-0000-4000-8000-000000000003"
	consistencyTestRun     = "00000000-0000-4000-8000-000000000004"
)

type consistencyRepositoryFake struct {
	libraries    []domain.ConsistencyLibrary
	librariesErr error
	pages        map[string][]domain.ConsistencyPage
	pageErr      map[string]error
	scopes       map[string]domain.ConsistencyScope
	current      bool
	currentErr   error
	startErr     error
	finishErr    error
	started      []domain.ConsistencyRun
	finished     []domain.ConsistencyReport
	finishCtx    error
	onPage       func(string)
}

func (f *consistencyRepositoryFake) ConsistencyLibraries(_ context.Context, id string) ([]domain.ConsistencyLibrary, error) {
	if id == "" {
		return f.libraries, f.librariesErr
	}
	var out []domain.ConsistencyLibrary
	for _, l := range f.libraries {
		if l.ID == id {
			out = append(out, l)
		}
	}
	return out, f.librariesErr
}

func (f *consistencyRepositoryFake) ConsistencyPage(_ context.Context, check string, scope domain.ConsistencyScope, cursor string) (domain.ConsistencyPage, error) {
	if f.scopes == nil {
		f.scopes = map[string]domain.ConsistencyScope{}
	}
	f.scopes[check] = scope
	if f.onPage != nil {
		f.onPage(check)
	}
	if err := f.pageErr[check]; err != nil {
		return domain.ConsistencyPage{}, err
	}
	pages := f.pages[check]
	index := 0
	if cursor != "" {
		index, _ = strconv.Atoi(cursor)
	}
	if index >= len(pages) {
		return domain.ConsistencyPage{}, nil
	}
	return pages[index], nil
}

func (f *consistencyRepositoryFake) ConsistencyBaselineCurrent(context.Context, domain.ConsistencyLibrary) (bool, error) {
	return f.current, f.currentErr
}

func (f *consistencyRepositoryFake) StartConsistencyRun(_ context.Context, run domain.ConsistencyRun) (domain.ConsistencyRun, error) {
	f.started = append(f.started, run)
	run.ID, run.StartedAt = consistencyTestRun, time.Unix(1700000000, 0)
	return run, f.startErr
}

func (f *consistencyRepositoryFake) FinishConsistencyRun(ctx context.Context, report domain.ConsistencyReport) error {
	f.finishCtx = ctx.Err()
	f.finished = append(f.finished, report)
	return f.finishErr
}

type consistencyFilesFake map[string]error

// FileExists: a key with a nil error exists, a missing key is absent.
func (f consistencyFilesFake) FileExists(_ context.Context, _, relative string) (bool, error) {
	err, ok := f[relative]
	return ok && err == nil, err
}

type consistencyVariantsFake map[string]error

func (f consistencyVariantsFake) VariantExists(_ context.Context, object string) (bool, error) {
	err, ok := f[object]
	return ok && err == nil, err
}

type consistencyPathsFake struct{}

func (consistencyPathsFake) RenderPath(root, relative string) string {
	return "rendered:" + relative + ":" + root
}

type consistencyFixerFake struct {
	calls [][]domain.ConsistencyFinding
	err   error
}

func (f *consistencyFixerFake) ApplyConsistencyFixes(_ context.Context, run string, fixes []domain.ConsistencyFinding) (domain.ConsistencyFixResult, error) {
	if run != consistencyTestRun {
		return domain.ConsistencyFixResult{}, domain.ErrInvalid
	}
	f.calls = append(f.calls, fixes)
	return domain.ConsistencyFixResult{Applied: int64(len(fixes))}, f.err
}

func consistencyTestFixture() (*consistencyRepositoryFake, *consistencyFixerFake) {
	library := domain.ConsistencyLibrary{ID: consistencyTestLibrary, Roots: []domain.ConsistencyRoot{{ID: consistencyTestRoot, Path: "/media"}}, Baseline: true, Snapshot: 3, Revision: 7, Entries: 10}
	return &consistencyRepositoryFake{libraries: []domain.ConsistencyLibrary{library}, pages: map[string][]domain.ConsistencyPage{}, pageErr: map[string]error{}, current: true}, &consistencyFixerFake{}
}

func consistencyTestChecker(t *testing.T, repo *consistencyRepositoryFake, files ConsistencyFileProber, variants ConsistencyVariantProber, fixer ConsistencyFixer) *ConsistencyChecker {
	t.Helper()
	c, err := NewConsistencyChecker(repo, files, variants, consistencyPathsFake{}, fixer)
	if err != nil {
		t.Fatal(err)
	}
	c.now = func() time.Time { return time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC) }
	return c
}

func consistencyTestResult(t *testing.T, report domain.ConsistencyReport, check string) domain.ConsistencyCheckResult {
	t.Helper()
	for _, r := range report.Global {
		if r.Check == check {
			return r
		}
	}
	for _, l := range report.Libraries {
		for _, r := range l.Checks {
			if r.Check == check {
				return r
			}
		}
	}
	t.Fatal("missing check", check)
	return domain.ConsistencyCheckResult{}
}

func TestConsistencyCheckerValidation(t *testing.T) {
	repo, fixer := consistencyTestFixture()
	if _, err := NewConsistencyChecker(nil, consistencyFilesFake{}, nil, consistencyPathsFake{}, nil); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("nil repository accepted")
	}
	if _, err := NewConsistencyChecker(repo, nil, nil, consistencyPathsFake{}, nil); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("nil prober accepted")
	}
	if _, err := NewConsistencyChecker(repo, consistencyFilesFake{}, nil, nil, nil); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("nil renderer accepted")
	}
	c := consistencyTestChecker(t, repo, consistencyFilesFake{}, nil, nil)
	cli := ConsistencyOptions{Origin: domain.ConsistencyOriginCLI}
	for name, o := range map[string]ConsistencyOptions{
		"origin":            {Origin: "web"},
		"job without id":    {Origin: domain.ConsistencyOriginJob, Library: consistencyTestLibrary},
		"job without scope": {Origin: domain.ConsistencyOriginJob, JobID: consistencyTestJob},
		"job fix":           {Origin: domain.ConsistencyOriginJob, JobID: consistencyTestJob, Library: consistencyTestLibrary, Fix: true},
		"cli job id":        {Origin: domain.ConsistencyOriginCLI, JobID: consistencyTestJob},
		"library":           {Origin: domain.ConsistencyOriginCLI, Library: "nope"},
		"stat budget":       {Origin: domain.ConsistencyOriginCLI, StatBudget: domain.ConsistencyMaxStatBudget + 1},
		"watch sample":      {Origin: domain.ConsistencyOriginCLI, WatchSample: -1},
		"variant sample":    {Origin: domain.ConsistencyOriginCLI, VariantSample: domain.ConsistencyMaxWatchSample + 1},
		"retention":         {Origin: domain.ConsistencyOriginCLI, SessionRetention: -time.Hour},
		"fix without fixer": {Origin: domain.ConsistencyOriginCLI, Fix: true},
	} {
		if _, err := c.Run(context.Background(), o); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("%s accepted: %v", name, err)
		}
	}
	if _, err := c.Run(nil, cli); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("nil context accepted")
	}
	if len(repo.started) != 0 {
		t.Fatal("invalid runs started")
	}
	cli.Library = "00000000-0000-4000-8000-0000000000ff"
	if _, err := c.Run(context.Background(), cli); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("unknown library", err)
	}
	repo.librariesErr = domain.ErrDatabase
	if _, err := c.Run(context.Background(), ConsistencyOptions{Origin: domain.ConsistencyOriginCLI}); !errors.Is(err, domain.ErrDatabase) {
		t.Fatal("library read error lost", err)
	}
	repo.librariesErr, repo.startErr = nil, domain.ErrDatabase
	if _, err := c.Run(context.Background(), ConsistencyOptions{Origin: domain.ConsistencyOriginCLI}); !errors.Is(err, domain.ErrDatabase) {
		t.Fatal("start error lost", err)
	}
	_ = fixer
}

func TestConsistencyCheckerConfirmsCandidatesWithinBudget(t *testing.T) {
	repo, _ := consistencyTestFixture()
	absent := func(path string) domain.ConsistencyFinding {
		return domain.ConsistencyFinding{Code: domain.ConsistencySourceMissing, RootID: consistencyTestRoot, RelativePath: path, Confirm: domain.ConsistencyConfirmAbsent}
	}
	repo.pages[domain.ConsistencyOrphanItem] = []domain.ConsistencyPage{
		{Examined: 3, Candidates: []domain.ConsistencyFinding{absent("present.mkv"), absent("gone.mkv"), absent("unreadable.mkv")}, Next: "1"},
		{Examined: 2, Candidates: []domain.ConsistencyFinding{absent("late.mkv"), {Code: domain.ConsistencySourceMissing, RootID: "unknown-root", RelativePath: "x.mkv", Confirm: domain.ConsistencyConfirmAbsent}}},
	}
	files := consistencyFilesFake{"present.mkv": nil, "unreadable.mkv": domain.ErrScanIO}
	c := consistencyTestChecker(t, repo, files, nil, nil)
	report, err := c.Run(context.Background(), ConsistencyOptions{Origin: domain.ConsistencyOriginCLI, StatBudget: 3})
	if err != nil {
		t.Fatal(err)
	}
	r := consistencyTestResult(t, report, domain.ConsistencyOrphanItem)
	if r.Examined != 5 || r.Findings != 4 || r.Status != domain.ConsistencyStatusFindings || r.Info[domain.ConsistencyInfoBaselineStale] != 1 || r.Info[domain.ConsistencyInfoUnconfirmed] != 3 {
		t.Fatalf("result: %+v", r)
	}
	codes := []string{domain.ConsistencySourceMissing, domain.ConsistencySourceUnconfirmed, domain.ConsistencySourceUnconfirmed, domain.ConsistencySourceUnconfirmed}
	for i, f := range r.Samples {
		if f.Code != codes[i] {
			t.Fatalf("sample %d code %s", i, f.Code)
		}
	}
	if r.Samples[0].Path != "rendered:gone.mkv:/media" {
		t.Fatalf("path rendered as %q", r.Samples[0].Path)
	}
	if report.Limits.StatsUsed != 3 || report.Limits.StatBudget != 3 || report.State != domain.ConsistencyRunCompleted || report.Totals.Findings != 4 {
		t.Fatalf("report: %+v %+v", report.Limits, report.Totals)
	}
	if len(repo.finished) != 1 || repo.finished[0].RunID != consistencyTestRun || repo.started[0].Mode != domain.ConsistencyModeReport {
		t.Fatal("run not persisted")
	}
}

func TestConsistencyCheckerVariantsAndSamples(t *testing.T) {
	repo, _ := consistencyTestFixture()
	var candidates []domain.ConsistencyFinding
	for i := range 60 {
		candidates = append(candidates, domain.ConsistencyFinding{Code: domain.ConsistencyVariantFileMissing, Object: "v" + strconv.Itoa(i), Confirm: domain.ConsistencyConfirmVariant})
	}
	repo.pages[domain.ConsistencyImageVariant] = []domain.ConsistencyPage{{Examined: 60, Candidates: candidates}}
	variants := consistencyVariantsFake{"v0": nil, "v1": domain.ErrImageUnavailable}
	repo.pages[domain.ConsistencyConstraints] = []domain.ConsistencyPage{{Examined: 1, Candidates: []domain.ConsistencyFinding{{Code: domain.ConsistencyIndexInvalid, Object: "t.i"}}}}
	report, err := consistencyTestChecker(t, repo, consistencyFilesFake{}, variants, nil).Run(context.Background(), ConsistencyOptions{Origin: domain.ConsistencyOriginCLI})
	if err != nil {
		t.Fatal(err)
	}
	r := consistencyTestResult(t, report, domain.ConsistencyImageVariant)
	if r.Findings != 58 || r.Status != domain.ConsistencyStatusIncomplete || r.Reason != domain.ConsistencyReasonUnverified || r.Info[domain.ConsistencyInfoUnverified] != 1 {
		t.Fatalf("variant result: %+v", r)
	}
	// Ten checks share 2000 samples, so each keeps the per-check maximum.
	if len(r.Samples) != domain.ConsistencySamplesPerCheck || !r.SamplesTruncated || report.Limits.SamplesPerCheck != domain.ConsistencySamplesPerCheck {
		t.Fatalf("samples %d truncated %t", len(r.Samples), r.SamplesTruncated)
	}
	if report.State != domain.ConsistencyRunPartial || report.Totals.Findings != 59 {
		t.Fatalf("report %s %+v", report.State, report.Totals)
	}
	if c := consistencyTestResult(t, report, domain.ConsistencyConstraints); c.Status != domain.ConsistencyStatusFindings {
		t.Fatalf("constraints %+v", c)
	}
	// Without a variant prober the check is skipped, not failed.
	report, err = consistencyTestChecker(t, repo, consistencyFilesFake{}, nil, nil).Run(context.Background(), ConsistencyOptions{Origin: domain.ConsistencyOriginCLI})
	if err != nil {
		t.Fatal(err)
	}
	if r := consistencyTestResult(t, report, domain.ConsistencyImageVariant); r.Status != domain.ConsistencyStatusSkipped || r.Reason != domain.ConsistencyReasonStoreDisabled {
		t.Fatalf("variant without store: %+v", r)
	}
}

func TestConsistencyCheckerSamplesShrinkWithManyLibraries(t *testing.T) {
	repo, _ := consistencyTestFixture()
	for i := range 30 {
		repo.libraries = append(repo.libraries, domain.ConsistencyLibrary{ID: "00000000-0000-4000-8000-1000000000" + strconv.Itoa(10+i)})
	}
	report, err := consistencyTestChecker(t, repo, consistencyFilesFake{}, nil, nil).Run(context.Background(), ConsistencyOptions{Origin: domain.ConsistencyOriginCLI})
	if err != nil {
		t.Fatal(err)
	}
	checks := 31*domain.ConsistencyLibraryCheckCount + domain.ConsistencyGlobalCheckCount
	if report.Limits.SamplesPerCheck != 2000/checks || len(report.Libraries) != 31 {
		t.Fatalf("samples per check %d for %d checks", report.Limits.SamplesPerCheck, checks)
	}
}

func TestConsistencyCheckerFixesThroughTheFixer(t *testing.T) {
	repo, fixer := consistencyTestFixture()
	repo.pages[domain.ConsistencyVersionCount] = []domain.ConsistencyPage{{Examined: 2, Candidates: []domain.ConsistencyFinding{
		{Code: domain.ConsistencyUserDataForeignSource, Fixable: true},
		{Code: domain.ConsistencyVideoWithoutSource},
	}}}
	c := consistencyTestChecker(t, repo, consistencyFilesFake{}, nil, fixer)
	report, err := c.Run(context.Background(), ConsistencyOptions{Origin: domain.ConsistencyOriginCLI, Fix: true})
	if err != nil {
		t.Fatal(err)
	}
	r := consistencyTestResult(t, report, domain.ConsistencyVersionCount)
	if r.Fixable != 1 || r.Fixed != 1 || r.Findings != 2 || len(fixer.calls) != 1 || len(fixer.calls[0]) != 1 || report.Mode != domain.ConsistencyModeFix || report.Totals.Fixed != 1 {
		t.Fatalf("fix result: %+v calls %d", r, len(fixer.calls))
	}
	// A report run never calls the fixer.
	fixer.calls = nil
	if _, err = c.Run(context.Background(), ConsistencyOptions{Origin: domain.ConsistencyOriginCLI}); err != nil || len(fixer.calls) != 0 {
		t.Fatal("report run repaired", err)
	}
	fixer.err = domain.ErrDatabase
	report, err = c.Run(context.Background(), ConsistencyOptions{Origin: domain.ConsistencyOriginCLI, Fix: true})
	if err != nil {
		t.Fatal(err)
	}
	if r := consistencyTestResult(t, report, domain.ConsistencyVersionCount); r.Status != domain.ConsistencyStatusIncomplete || r.Reason != domain.ConsistencyReasonFailed {
		t.Fatalf("fixer failure: %+v", r)
	}
}

func TestConsistencyCheckerReportsFailedAndLoopingPages(t *testing.T) {
	repo, _ := consistencyTestFixture()
	repo.pageErr[domain.ConsistencyOrphanFile] = domain.ErrDatabase
	repo.pages[domain.ConsistencyNFOState] = []domain.ConsistencyPage{{Examined: 1, Next: "0"}, {Examined: 1, Next: "0"}}
	report, err := consistencyTestChecker(t, repo, consistencyFilesFake{}, nil, nil).Run(context.Background(), ConsistencyOptions{Origin: domain.ConsistencyOriginCLI})
	if err != nil {
		t.Fatal(err)
	}
	if r := consistencyTestResult(t, report, domain.ConsistencyOrphanFile); r.Status != domain.ConsistencyStatusIncomplete || r.Reason != domain.ConsistencyReasonFailed {
		t.Fatalf("failed page: %+v", r)
	}
	// The second call returns the cursor it was given: a non-advancing
	// repository must not loop.
	if r := consistencyTestResult(t, report, domain.ConsistencyNFOState); r.Status != domain.ConsistencyStatusIncomplete || r.Examined != 2 {
		t.Fatalf("looping page: %+v", r)
	}
	if report.State != domain.ConsistencyRunPartial {
		t.Fatal("state", report.State)
	}
	repo.finishErr = domain.ErrDatabase
	if _, err = consistencyTestChecker(t, repo, consistencyFilesFake{}, nil, nil).Run(context.Background(), ConsistencyOptions{Origin: domain.ConsistencyOriginCLI}); !errors.Is(err, domain.ErrDatabase) {
		t.Fatal("persistence error lost", err)
	}
}

func TestConsistencyCheckerBaselineHandling(t *testing.T) {
	repo, _ := consistencyTestFixture()
	repo.pages[domain.ConsistencyOrphanFile] = []domain.ConsistencyPage{{Examined: 1, Candidates: []domain.ConsistencyFinding{{Code: domain.ConsistencyVideoNotCataloged}}}}
	repo.pages[domain.ConsistencyVersionCount] = []domain.ConsistencyPage{{Examined: 1, Candidates: []domain.ConsistencyFinding{{Code: domain.ConsistencyVideoWithoutSource}}}}
	for _, tc := range []struct {
		name    string
		current bool
		err     error
		reason  string
	}{{"replaced", false, nil, domain.ConsistencyReasonBaselineChanged}, {"unreadable", false, domain.ErrDatabase, domain.ConsistencyReasonFailed}} {
		repo.current, repo.currentErr = tc.current, tc.err
		report, err := consistencyTestChecker(t, repo, consistencyFilesFake{}, nil, nil).Run(context.Background(), ConsistencyOptions{Origin: domain.ConsistencyOriginCLI})
		if err != nil {
			t.Fatal(err)
		}
		if r := consistencyTestResult(t, report, domain.ConsistencyOrphanFile); r.Status != domain.ConsistencyStatusIncomplete || r.Reason != tc.reason || r.Findings != 0 || r.Examined != 1 {
			t.Fatalf("%s: %+v", tc.name, r)
		}
		if r := consistencyTestResult(t, report, domain.ConsistencyVersionCount); r.Findings != 1 {
			t.Fatalf("%s voided a check independent of the baseline", tc.name)
		}
	}
	repo.libraries[0].Baseline = false
	report, err := consistencyTestChecker(t, repo, consistencyFilesFake{}, nil, nil).Run(context.Background(), ConsistencyOptions{Origin: domain.ConsistencyOriginCLI, Library: consistencyTestLibrary})
	if err != nil {
		t.Fatal(err)
	}
	if r := consistencyTestResult(t, report, domain.ConsistencyOrphanFile); r.Status != domain.ConsistencyStatusSkipped || r.Reason != domain.ConsistencyReasonNoBaseline {
		t.Fatalf("no baseline: %+v", r)
	}
	if report.State != domain.ConsistencyRunCompleted || report.LibraryID != consistencyTestLibrary {
		t.Fatalf("skips do not make a run partial: %s", report.State)
	}
}

func TestConsistencyCheckerScopeCarriesBoundsAndCutoffs(t *testing.T) {
	repo, _ := consistencyTestFixture()
	c := consistencyTestChecker(t, repo, consistencyFilesFake{}, nil, nil)
	if _, err := c.Run(context.Background(), ConsistencyOptions{Origin: domain.ConsistencyOriginJob, JobID: consistencyTestJob, Library: consistencyTestLibrary, WatchSample: 7, SessionRetention: 30 * 24 * time.Hour, DailyRetention: 10 * 24 * time.Hour, CallTimeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	scope := repo.scopes[domain.ConsistencyWatchStats]
	if scope.WatchSample != 7 || scope.VariantSample != domain.ConsistencyDefaultVariantProbe || scope.Library.ID != consistencyTestLibrary {
		t.Fatalf("scope bounds: %+v", scope)
	}
	if got := scope.SessionCutoff.Format(time.DateOnly); got != "2026-09-06" {
		t.Fatalf("session cutoff %s", got)
	}
	if got := scope.DailyCutoff.Format(time.DateOnly); got != "2026-09-26" {
		t.Fatalf("daily cutoff %s", got)
	}
	if repo.started[0].JobID != consistencyTestJob || repo.finished[0].JobID != consistencyTestJob || repo.finished[0].Origin != domain.ConsistencyOriginJob {
		t.Fatal("job identity not carried")
	}
	repo.scopes = nil
	if _, err := c.Run(context.Background(), ConsistencyOptions{Origin: domain.ConsistencyOriginCLI}); err != nil {
		t.Fatal(err)
	}
	if s := repo.scopes[domain.ConsistencyWatchStats]; !s.SessionCutoff.IsZero() || !s.DailyCutoff.IsZero() || s.WatchSample != domain.ConsistencyDefaultWatchSample {
		t.Fatalf("unlimited retention: %+v", s)
	}
}

func TestConsistencyCheckerCancellationPersistsTheReport(t *testing.T) {
	repo, _ := consistencyTestFixture()
	ctx, cancel := context.WithCancel(context.Background())
	repo.onPage = func(check string) {
		if check == domain.ConsistencyVersionCount {
			cancel()
		}
	}
	report, err := consistencyTestChecker(t, repo, consistencyFilesFake{}, nil, nil).Run(ctx, ConsistencyOptions{Origin: domain.ConsistencyOriginCLI})
	if !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation not returned", err)
	}
	if report.State != domain.ConsistencyRunCancelled || len(repo.finished) != 1 || repo.finished[0].State != domain.ConsistencyRunCancelled {
		t.Fatalf("cancelled report: %s persisted %d", report.State, len(repo.finished))
	}
	if repo.finishCtx != nil {
		t.Fatal("the report was persisted with the cancelled context")
	}
	if r := consistencyTestResult(t, report, domain.ConsistencyConstraints); r.Status != domain.ConsistencyStatusIncomplete || r.Reason != domain.ConsistencyReasonCancelled {
		t.Fatalf("check after cancellation: %+v", r)
	}
}

type consistencyScheduleFake struct {
	calls    int
	interval time.Duration
	worked   bool
}

func (f *consistencyScheduleFake) DispatchConsistencyCheck(_ context.Context, interval time.Duration, _ domain.JobPolicy) (bool, error) {
	f.calls++
	f.interval = interval
	return f.worked, nil
}

type scanScheduleFake struct {
	ScheduleRepository
	worked bool
	err    error
}

func (f scanScheduleFake) DispatchScanSchedule(context.Context, domain.JobPolicy, ScheduleCalendar, *domain.ProbeIdentity, *domain.NFOIdentity, IgnoreAdmissionCapabilities) (bool, error) {
	return f.worked, f.err
}

func TestConsistencyScheduleFollowsScanSchedules(t *testing.T) {
	base := newJobService(t, jobRepositoryFake{})
	if _, err := NewJobsWithConsistencySchedule(base, &consistencyScheduleFake{}, time.Hour); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("schedule without scan schedules accepted")
	}
	scans := &scanScheduleFake{}
	if _, err := NewJobsWithSchedules(base, scans, nil); err == nil {
		t.Fatal("nil calendar accepted")
	}
	withScans := &Jobs{repository: base.repository, policy: base.policy, schedules: scans}
	repo := &consistencyScheduleFake{worked: true}
	if _, err := NewJobsWithConsistencySchedule(withScans, repo, time.Minute); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("sub-hour interval accepted")
	}
	j, err := NewJobsWithConsistencySchedule(withScans, repo, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0)
	j.consistency.now = func() time.Time { return now }
	// A due scan schedule wins; the consistency schedule waits.
	scans.worked = true
	j.schedules = *scans
	if worked, err := j.DispatchSchedule(context.Background()); err != nil || !worked || repo.calls != 0 {
		t.Fatalf("scan dispatch: %t %v calls %d", worked, err, repo.calls)
	}
	j.schedules = scanScheduleFake{}
	if worked, err := j.DispatchSchedule(context.Background()); err != nil || !worked || repo.calls != 1 || repo.interval != 24*time.Hour {
		t.Fatalf("consistency dispatch: %t %v calls %d", worked, err, repo.calls)
	}
	// Spaced: the next loop iterations within a minute do not ask again.
	now = now.Add(30 * time.Second)
	if worked, _ := j.DispatchSchedule(context.Background()); worked || repo.calls != 1 {
		t.Fatal("dispatch not spaced")
	}
	now = now.Add(time.Minute)
	if _, err := j.DispatchSchedule(context.Background()); err != nil || repo.calls != 2 {
		t.Fatal("dispatch not resumed after the spacing")
	}
	j.schedules = scanScheduleFake{err: domain.ErrDatabase}
	if _, err := j.DispatchSchedule(context.Background()); !errors.Is(err, domain.ErrDatabase) || repo.calls != 2 {
		t.Fatal("scan schedule error hidden")
	}
}
