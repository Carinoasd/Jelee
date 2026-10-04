package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/consistency"
)

const (
	cliConsistencyLibrary = "00000000-0000-4000-8000-0000000000c1"
	cliConsistencyRoot    = "00000000-0000-4000-8000-0000000000c2"
	cliConsistencyRun     = "00000000-0000-4000-8000-0000000000c3"
)

type consistencyStoreStub struct {
	candidates  []domain.ConsistencyFinding
	resolveErr  error
	report      []byte
	reportErr   error
	revert      domain.ConsistencyRevertResult
	revertErr   error
	enqueueErr  error
	enqueued    string
	fixes       int
	finished    []domain.ConsistencyReport
	libraryUsed string
}

func (s *consistencyStoreStub) ConsistencyLibraries(_ context.Context, id string) ([]domain.ConsistencyLibrary, error) {
	s.libraryUsed = id
	return []domain.ConsistencyLibrary{{ID: cliConsistencyLibrary, Roots: []domain.ConsistencyRoot{{ID: cliConsistencyRoot, Path: "/srv/private-media"}}, Baseline: true, Revision: 4, Entries: 9}}, nil
}

func (s *consistencyStoreStub) ConsistencyPage(_ context.Context, check string, _ domain.ConsistencyScope, _ string) (domain.ConsistencyPage, error) {
	if check != domain.ConsistencyVersionCount {
		return domain.ConsistencyPage{Examined: 1}, nil
	}
	return domain.ConsistencyPage{Examined: int64(len(s.candidates)), Candidates: s.candidates}, nil
}

func (s *consistencyStoreStub) ConsistencyBaselineCurrent(context.Context, domain.ConsistencyLibrary) (bool, error) {
	return true, nil
}

func (s *consistencyStoreStub) StartConsistencyRun(_ context.Context, run domain.ConsistencyRun) (domain.ConsistencyRun, error) {
	run.ID = cliConsistencyRun
	return run, nil
}

func (s *consistencyStoreStub) FinishConsistencyRun(_ context.Context, report domain.ConsistencyReport) error {
	s.finished = append(s.finished, report)
	return nil
}

func (s *consistencyStoreStub) ApplyConsistencyFixes(_ context.Context, _ string, fixes []domain.ConsistencyFinding) (domain.ConsistencyFixResult, error) {
	s.fixes += len(fixes)
	return domain.ConsistencyFixResult{Applied: int64(len(fixes))}, nil
}

func (s *consistencyStoreStub) ResolveLibrary(_ context.Context, reference string) (string, error) {
	if s.resolveErr != nil {
		return "", s.resolveErr
	}
	if reference != "Movies" && reference != cliConsistencyLibrary {
		return "", domain.ErrNotFound
	}
	return cliConsistencyLibrary, nil
}

func (s *consistencyStoreStub) LatestConsistencyReport(context.Context, string) ([]byte, error) {
	return s.report, s.reportErr
}

func (s *consistencyStoreStub) RevertConsistencyRun(context.Context, string) (domain.ConsistencyRevertResult, error) {
	return s.revert, s.revertErr
}

func (s *consistencyStoreStub) EnqueueConsistencyCheck(_ context.Context, library, priority string, policy domain.JobPolicy) (domain.Job, error) {
	if s.enqueueErr != nil {
		return domain.Job{}, s.enqueueErr
	}
	if policy.QueueLimit < 1 {
		return domain.Job{}, domain.ErrInvalid
	}
	s.enqueued = library + "/" + priority
	return domain.Job{ID: cliConsistencyRun, LibraryID: library, Kind: domain.JobConsistencyCheck, State: domain.JobQueued, Priority: priority}, nil
}

type leakingPaths struct{}

func (leakingPaths) RenderPath(root, relative string) string { return root + "/" + relative }

func consistencyDeps(store *consistencyStoreStub, leak bool) consistencyCLIDependencies {
	cfg := config.Config{DatabaseURL: "postgres://private:secret@localhost/database", MaxConnections: 2, Jobs: config.DefaultJobsConfig()}
	return consistencyCLIDependencies{
		load:   func() (config.Config, error) { return cfg, nil },
		lookup: func(string) (string, bool) { return "", false },
		open: func(context.Context, config.Config) (consistencyCLIStore, func(), error) {
			if store == nil {
				return nil, nil, errors.New("dial postgres://private:secret@localhost/database")
			}
			return store, func() {}, nil
		},
		checker: func(c config.Config, s consistencyCLIStore) (*app.ConsistencyChecker, error) {
			if leak {
				return app.NewConsistencyChecker(s, cliFiles{}, nil, leakingPaths{}, s)
			}
			return consistency.NewChecker(c, s)
		},
	}
}

type cliFiles struct{}

func (cliFiles) FileExists(context.Context, string, string) (bool, error) { return false, nil }

func runConsistencyTest(deps consistencyCLIDependencies, args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := runConsistencyCLIWith(context.Background(), args, &stdout, &stderr, deps)
	return code, stdout.String(), stderr.String()
}

func TestConsistencyCLIUsage(t *testing.T) {
	deps := consistencyDeps(&consistencyStoreStub{}, false)
	for _, args := range [][]string{
		nil, {"unknown"}, {"check", "extra"}, {"check", "--stat-budget", "-1"}, {"check", "--stat-budget", "100001"}, {"check", "--watch-sample", "10001"},
		{"check", "--timeout", "0s"}, {"revert"}, {"revert", "--run", "nope"}, {"enqueue"}, {"enqueue", "--library", "Movies", "--priority", "urgent"},
		{"report", "--library", strings.Repeat("x", 129)}, {"report", "--fix"},
	} {
		if code, _, stderr := runConsistencyTest(deps, args...); code != 2 || !strings.Contains(stderr, "usage: jelee-cli consistency") {
			t.Fatalf("%v: exit %d %q", args, code, stderr)
		}
	}
}

func TestConsistencyCLIFailuresAreStableCodes(t *testing.T) {
	deps := consistencyDeps(nil, false)
	if code, _, stderr := runConsistencyTest(deps, "check"); code != 1 || strings.Contains(stderr, "secret") || !strings.Contains(stderr, "consistency_database_unavailable") {
		t.Fatalf("open failure: %d %q", code, stderr)
	}
	deps.load = func() (config.Config, error) { return config.Config{}, errors.New("JELEE_DATABASE_URL=postgres://x") }
	if code, _, stderr := runConsistencyTest(deps, "check"); code != 1 || stderr != "consistency_configuration_invalid\n" {
		t.Fatalf("config failure: %d %q", code, stderr)
	}
	store := &consistencyStoreStub{}
	deps = consistencyDeps(store, false)
	if code, _, stderr := runConsistencyTest(deps, "check", "--library", "Unknown"); code != 1 || stderr != "consistency_not_found\n" {
		t.Fatalf("unknown library: %d %q", code, stderr)
	}
	store.resolveErr = domain.ErrDatabase
	if code, _, stderr := runConsistencyTest(deps, "report", "--library", "Movies"); code != 1 || stderr != "consistency_library_unavailable\n" {
		t.Fatalf("library lookup failure: %d %q", code, stderr)
	}
	store.resolveErr, store.enqueueErr = nil, domain.ErrJobBusy
	if code, _, stderr := runConsistencyTest(deps, "enqueue", "--library", "Movies"); code != 1 || !strings.HasPrefix(stderr, "consistency_library_busy") {
		t.Fatalf("busy library: %d %q", code, stderr)
	}
	store.enqueueErr = domain.ErrJobQueueFull
	if code, _, stderr := runConsistencyTest(deps, "enqueue", "--library", "Movies"); code != 1 || stderr != "consistency_queue_full\n" {
		t.Fatalf("full queue: %d %q", code, stderr)
	}
	store.reportErr = domain.ErrNotFound
	if code, _, stderr := runConsistencyTest(deps, "report"); code != 1 || stderr != "consistency_not_found\n" {
		t.Fatalf("no report: %d %q", code, stderr)
	}
	store.revertErr = context.Canceled
	if code, _, stderr := runConsistencyTest(deps, "revert", "--run", cliConsistencyRun); code != 130 || stderr != "consistency_cancelled\n" {
		t.Fatalf("cancelled revert: %d %q", code, stderr)
	}
	store.revertErr = context.DeadlineExceeded
	if code, _, stderr := runConsistencyTest(deps, "revert", "--run", cliConsistencyRun); code != 1 || stderr != "consistency_timeout\n" {
		t.Fatalf("revert timeout: %d %q", code, stderr)
	}
	store.revertErr = domain.ErrDatabase
	if code, _, stderr := runConsistencyTest(deps, "revert", "--run", cliConsistencyRun); code != 1 || stderr != "consistency_revert_failed\n" {
		t.Fatalf("revert failure: %d %q", code, stderr)
	}
	deps.checker = func(config.Config, consistencyCLIStore) (*app.ConsistencyChecker, error) {
		return nil, domain.ErrInvalid
	}
	if code, _, stderr := runConsistencyTest(deps, "check"); code != 1 || stderr != "consistency_unavailable\n" {
		t.Fatalf("checker failure: %d %q", code, stderr)
	}
}

func TestConsistencyCLICheckReportsAndFixes(t *testing.T) {
	store := &consistencyStoreStub{candidates: []domain.ConsistencyFinding{
		{Code: domain.ConsistencyUserDataForeignSource, ItemID: cliConsistencyLibrary, Fixable: true},
		{Code: domain.ConsistencyVideoWithoutSource, ItemID: cliConsistencyRoot},
	}}
	deps := consistencyDeps(store, false)
	code, stdout, stderr := runConsistencyTest(deps, "check", "--library", "Movies")
	if code != consistencyFindingsExit || stderr != "" || store.libraryUsed != cliConsistencyLibrary {
		t.Fatalf("check: %d %q", code, stderr)
	}
	for _, want := range []string{"Consistency run " + cliConsistencyRun + " (cli, report): completed", "version_count", "user_data_foreign_source", "Findings: 2 (fixable 1, fixed 0)."} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("table lacks %q:\n%s", want, stdout)
		}
	}
	if store.fixes != 0 {
		t.Fatal("a report run repaired")
	}
	code, stdout, _ = runConsistencyTest(deps, "check", "--fix", "--json", "--stat-budget", "5", "--watch-sample", "7")
	var report domain.ConsistencyReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil || code != consistencyFindingsExit || report.Mode != domain.ConsistencyModeFix || report.Totals.Fixed != 1 || store.fixes != 1 || report.Limits.StatBudget != 5 || report.Limits.WatchSample != 7 {
		t.Fatalf("fix run: %d %v %+v", code, err, report.Totals)
	}
	store.candidates = store.candidates[:1]
	code, stdout, _ = runConsistencyTest(deps, "check", "--fix")
	if code != 0 || !strings.Contains(stdout, "jelee-cli consistency revert --run "+cliConsistencyRun) {
		t.Fatalf("all findings fixed: %d\n%s", code, stdout)
	}
	store.candidates = nil
	if code, _, _ = runConsistencyTest(deps, "check"); code != 0 {
		t.Fatalf("clean check exit %d", code)
	}
}

func TestConsistencyCLIWithholdsLeakingOutput(t *testing.T) {
	store := &consistencyStoreStub{candidates: []domain.ConsistencyFinding{{Code: domain.ConsistencySourceMissing, RootID: cliConsistencyRoot, RelativePath: "Movie/a.mkv"}}}
	code, stdout, stderr := runConsistencyTest(consistencyDeps(store, true), "check", "--json")
	if code != 1 || stdout != "" || !strings.Contains(stderr, "output_unsafe") {
		t.Fatalf("leaking output printed: %d %q %q", code, stdout, stderr)
	}
}

func TestConsistencyCLIReportRevertAndEnqueue(t *testing.T) {
	stored, _ := json.Marshal(domain.ConsistencyReport{Schema: domain.ConsistencyReportSchema, RunID: cliConsistencyRun, Origin: domain.ConsistencyOriginJob, Mode: domain.ConsistencyModeReport, State: domain.ConsistencyRunPartial,
		Libraries: []domain.ConsistencyLibraryReport{{LibraryID: cliConsistencyLibrary, Checks: []domain.ConsistencyCheckResult{{Check: domain.ConsistencyOrphanFile, Status: domain.ConsistencyStatusSkipped, Reason: domain.ConsistencyReasonNoBaseline}}}}})
	store := &consistencyStoreStub{report: stored, revert: domain.ConsistencyRevertResult{RunID: cliConsistencyRun, Reverted: 2, Skipped: 1}}
	deps := consistencyDeps(store, false)
	code, stdout, _ := runConsistencyTest(deps, "report", "--library", "Movies")
	if code != 0 || !strings.Contains(stdout, "(job, report): partial") || !strings.Contains(stdout, "no baseline: run a scan") || !strings.Contains(stdout, "no_baseline") {
		t.Fatalf("report table: %d\n%s", code, stdout)
	}
	if code, stdout, _ = runConsistencyTest(deps, "report", "--json"); code != 0 || strings.TrimSpace(stdout) != string(stored) {
		t.Fatalf("report json: %d %q", code, stdout)
	}
	store.report = []byte("not json")
	if code, _, stderr := runConsistencyTest(deps, "report"); code != 1 || stderr != "consistency_output_failed\n" {
		t.Fatalf("corrupt report: %d %q", code, stderr)
	}
	if code, stdout, _ = runConsistencyTest(deps, "revert", "--run", cliConsistencyRun); code != 0 || !strings.Contains(stdout, "2 repairs reverted, 1 skipped") {
		t.Fatalf("revert: %d %q", code, stdout)
	}
	if code, stdout, _ = runConsistencyTest(deps, "revert", "--run", cliConsistencyRun, "--json"); code != 0 || !strings.Contains(stdout, `"reverted":2`) {
		t.Fatalf("revert json: %d %q", code, stdout)
	}
	if code, stdout, _ = runConsistencyTest(deps, "enqueue", "--library", "Movies", "--priority", "background"); code != 0 || store.enqueued != cliConsistencyLibrary+"/background" || !strings.Contains(stdout, "Queued consistency check job") {
		t.Fatalf("enqueue: %d %q", code, stdout)
	}
	if code, stdout, _ = runConsistencyTest(deps, "enqueue", "--library", "Movies", "--json"); code != 0 || !strings.Contains(stdout, `"kind":"consistency_check"`) {
		t.Fatalf("enqueue json: %d %q", code, stdout)
	}
}
