package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/consistency"
)

const cliRepairRun = "00000000-0000-4000-8000-0000000000d1"

// repairStoreStub holds one drifted statistics row until a repair applies
// it, so the second execution finds nothing.
type repairStoreStub struct {
	drift    bool
	started  int
	applied  int
	finished []domain.RepairResult
	reverted int
	root     string
}

func (s *repairStoreStub) ConsistencyLibraries(context.Context, string) ([]domain.ConsistencyLibrary, error) {
	root := s.root
	if root == "" {
		root = "/srv/private-media"
	}
	return []domain.ConsistencyLibrary{{ID: cliConsistencyLibrary, Roots: []domain.ConsistencyRoot{{ID: cliConsistencyRoot, Path: root}}, Baseline: true, Revision: 4, Entries: 9}}, nil
}
func (*repairStoreStub) ConsistencyPage(context.Context, string, domain.ConsistencyScope, string) (domain.ConsistencyPage, error) {
	return domain.ConsistencyPage{}, nil
}
func (s *repairStoreStub) RepairStatsPage(context.Context, domain.ConsistencyScope, string) (domain.ConsistencyPage, error) {
	if !s.drift {
		return domain.ConsistencyPage{Examined: 1}, nil
	}
	return domain.ConsistencyPage{Examined: 1, Candidates: []domain.ConsistencyFinding{{Code: domain.ConsistencyDailyCounterDrift, LibraryID: cliConsistencyLibrary, UserID: cliConsistencyRoot, ItemID: cliConsistencyRun,
		Day: "2026-10-01", RootID: cliConsistencyRoot, RelativePath: "Movie/Movie.mkv", Fixable: true}}}, nil
}
func (*repairStoreStub) RepairVariantPage(context.Context, string) (domain.ConsistencyPage, error) {
	return domain.ConsistencyPage{}, nil
}
func (*repairStoreStub) RepairActiveJob(context.Context, string) (domain.RepairActiveJob, bool, error) {
	return domain.RepairActiveJob{}, false, nil
}
func (s *repairStoreStub) StartRepairRun(_ context.Context, run domain.RepairRun) (domain.RepairRun, error) {
	s.started++
	run.ID, run.StartedAt = cliRepairRun, time.Now()
	return run, nil
}
func (s *repairStoreStub) ApplyRepairFixes(_ context.Context, _ domain.RepairRun, fixes []domain.ConsistencyFinding) (domain.ConsistencyFixResult, error) {
	s.applied += len(fixes)
	s.drift = false
	return domain.ConsistencyFixResult{Applied: int64(len(fixes))}, nil
}
func (*repairStoreStub) PurgeRepairProbeCache(context.Context, domain.RepairRun, []domain.ConsistencyFinding) (domain.ConsistencyFixResult, error) {
	return domain.ConsistencyFixResult{}, nil
}
func (*repairStoreStub) PurgeRepairVariantRows(context.Context, domain.RepairRun, []domain.ConsistencyFinding) (domain.ConsistencyFixResult, error) {
	return domain.ConsistencyFixResult{}, nil
}
func (*repairStoreStub) EnqueueRepairCatalogSync(context.Context, domain.RepairRun, string, domain.JobPolicy) (domain.RepairJob, error) {
	return domain.RepairJob{}, domain.ErrJobBusy
}
func (s *repairStoreStub) FinishRepairRun(_ context.Context, _ domain.RepairRun, result domain.RepairResult) error {
	s.finished = append(s.finished, result)
	return nil
}
func (s *repairStoreStub) RevertRepairRun(_ context.Context, _ domain.Actor, run string) (domain.RepairRevertResult, error) {
	if run != cliRepairRun {
		return domain.RepairRevertResult{}, domain.ErrConflict
	}
	s.reverted++
	return domain.RepairRevertResult{RunID: run, Reverted: 1}, nil
}
func (s *repairStoreStub) ResolveLibrary(_ context.Context, reference string) (string, error) {
	if reference != "Movies" && reference != cliConsistencyLibrary {
		return "", domain.ErrNotFound
	}
	return cliConsistencyLibrary, nil
}

func repairDeps(store *repairStoreStub, leak bool) repairCLIDependencies {
	cfg := config.Config{DatabaseURL: "postgres://private:secret@localhost/database", MaxConnections: 2, Jobs: config.DefaultJobsConfig()}
	return repairCLIDependencies{
		load:   func() (config.Config, error) { return cfg, nil },
		lookup: func(string) (string, bool) { return "", false },
		open: func(context.Context, config.Config) (repairCLIStore, func(), error) {
			if store == nil {
				return nil, nil, errors.New("dial postgres://private:secret@localhost/database")
			}
			return store, func() {}, nil
		},
		repairer: func(c config.Config, s repairCLIStore) (*app.Repairer, error) {
			if leak {
				return app.NewRepairer(s, cliFiles{}, nil, leakingPaths{})
			}
			return consistency.NewRepairer(c, s)
		},
	}
}

func runRepairTest(deps repairCLIDependencies, stdin string, args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := runRepairCLIWith(context.Background(), args, strings.NewReader(stdin), &stdout, &stderr, deps)
	return code, stdout.String(), stderr.String()
}

func TestRepairCLIUsageAndConfirmation(t *testing.T) {
	deps := repairDeps(&repairStoreStub{}, false)
	for _, args := range [][]string{nil, {"rebuild"}, {"stats", "--dry-run", "--yes"}, {"stats", "--stat-budget", "-1"}, {"revert", "--yes"}, {"revert", "--run", "x", "--yes"}, {"stats", "extra", "--dry-run"}} {
		if code, _, stderr := runRepairTest(deps, "", args...); code != 2 || !strings.Contains(stderr, "usage") {
			t.Fatalf("%v: %d %q", args, code, stderr)
		}
	}
	for _, args := range [][]string{{"stats"}, {"revert", "--run", cliRepairRun}} {
		if code, _, stderr := runRepairTest(deps, "", args...); code != 2 || !strings.Contains(stderr, "repair_confirmation_required") {
			t.Fatalf("%v: %d %q", args, code, stderr)
		}
	}
}

func TestRepairCLIDryRunThenApplyThenRerun(t *testing.T) {
	store := &repairStoreStub{drift: true}
	deps := repairDeps(store, false)
	code, stdout, stderr := runRepairTest(deps, "", "stats", "--library", "Movies", "--dry-run")
	if code != repairPlannedExit || store.started != 0 || store.applied != 0 || !strings.Contains(stdout, "planned 1, applied 0") || !strings.Contains(stdout, "repair stats --yes") || stderr != "" {
		t.Fatalf("dry run: %d %q %q", code, stdout, stderr)
	}
	code, stdout, _ = runRepairTest(deps, "", "stats", "--library", "Movies", "--yes", "--json")
	var result domain.RepairResult
	if err := json.Unmarshal([]byte(stdout), &result); err != nil || code != 0 || result.Applied != 1 || result.RunID != cliRepairRun || !result.Revertible || len(store.finished) != 1 {
		t.Fatalf("apply: %d %v %+v", code, err, result)
	}
	code, stdout, _ = runRepairTest(deps, "", "stats", "--yes")
	if code != 0 || !strings.Contains(stdout, "planned 0, applied 0") {
		t.Fatalf("rerun: %d %q", code, stdout)
	}
	code, stdout, _ = runRepairTest(deps, "", "revert", "--run", cliRepairRun, "--yes")
	if code != 0 || store.reverted != 1 || !strings.Contains(stdout, "1 repairs reverted") {
		t.Fatalf("revert: %d %q", code, stdout)
	}
}

func TestRepairCLIRefusesWhatItCannotDo(t *testing.T) {
	deps := repairDeps(&repairStoreStub{}, false)
	for _, action := range []string{domain.RepairNFO, domain.RepairImageVariants} {
		if code, _, stderr := runRepairTest(deps, "", action, "--dry-run"); code != 1 || !strings.Contains(stderr, "repair_requires_server") {
			t.Fatalf("%s: %d %q", action, code, stderr)
		}
	}
	if code, _, stderr := runRepairTest(deps, "", "stats", "--library", "Nope", "--dry-run"); code != 1 || !strings.Contains(stderr, "repair_not_found") {
		t.Fatalf("unknown library: %d %q", code, stderr)
	}
	if code, _, stderr := runRepairTest(deps, "", "revert", "--run", "00000000-0000-4000-8000-0000000000ff", "--yes"); code != 1 || !strings.Contains(stderr, "repair_not_revertible") {
		t.Fatalf("not revertible: %d %q", code, stderr)
	}
	if code, stdout, _ := runRepairTest(deps, "", "items", "--yes"); code != 0 || !strings.Contains(stdout, "planned 0") {
		t.Fatalf("items with nothing to queue: %d %q", code, stdout)
	}
	code, _, stderr := runRepairTest(repairDeps(nil, false), "", "stats", "--dry-run")
	if code != 1 || !strings.Contains(stderr, "repair_database_unavailable") || strings.Contains(stderr, "secret") {
		t.Fatalf("database down: %d %q", code, stderr)
	}
}

func TestRepairCLIWithholdsUnsafeOutput(t *testing.T) {
	store := &repairStoreStub{drift: true}
	code, stdout, stderr := runRepairTest(repairDeps(store, true), "", "stats", "--dry-run")
	if code != 1 || stdout != "" || !strings.Contains(stderr, "output_unsafe") {
		t.Fatalf("leaking renderer: %d %q %q", code, stdout, stderr)
	}
}

func TestRepairCLIThroughTheAPI(t *testing.T) {
	var seen map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("t", 43) {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"error":{"code":"authentication_required"}}`))
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&seen)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/admin/repairs":
			if seen["action"] == domain.RepairNFO {
				w.WriteHeader(503)
				_, _ = w.Write([]byte(`{"error":{"code":"nfo_reader_unavailable","message":"x"}}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": domain.RepairResult{Schema: domain.RepairResultSchema, Action: domain.RepairImageVariants, Origin: domain.RepairOriginAPI, DryRun: true,
				State: domain.RepairStatePlanned, Planned: 5, Targets: []domain.RepairTargetCount{{Kind: domain.RepairTargetImageVariant, Planned: 5}}, Samples: []domain.RepairSample{}}})
		case "/api/v1/admin/repairs/" + cliRepairRun + "/revert":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": domain.RepairRevertResult{RunID: cliRepairRun, Reverted: 3}})
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	deps := repairDeps(nil, false)
	deps.client = server.Client()
	token := strings.Repeat("t", 43) + "\n"
	code, stdout, stderr := runRepairTest(deps, token, "image-variants", "--dry-run", "--token-stdin", "--url", server.URL)
	if code != repairPlannedExit || !strings.Contains(stdout, "planned 5") || seen["dryRun"] != true || seen["iUnderstand"] != false {
		t.Fatalf("API dry run: %d %q %q %v", code, stdout, stderr, seen)
	}
	if code, _, stderr = runRepairTest(deps, token, "nfo", "--yes", "--token-stdin", "--url", server.URL); code != 1 || !strings.Contains(stderr, "HTTP 503 nfo_reader_unavailable") {
		t.Fatalf("API refusal: %d %q", code, stderr)
	}
	if code, stdout, _ = runRepairTest(deps, token, "revert", "--run", cliRepairRun, "--yes", "--token-stdin", "--url", server.URL); code != 0 || !strings.Contains(stdout, "3 repairs reverted") || seen["iUnderstand"] != true {
		t.Fatalf("API revert: %d %q", code, stdout)
	}
	if code, _, stderr = runRepairTest(deps, token, "stats", "--dry-run", "--library", "Movies", "--token-stdin", "--url", server.URL); code != 2 || !strings.Contains(stderr, "library ID") {
		t.Fatalf("API library name: %d %q", code, stderr)
	}
	if code, _, stderr = runRepairTest(deps, "short", "stats", "--dry-run", "--token-stdin", "--url", server.URL); code != 2 || !strings.Contains(stderr, "repair_token_invalid") {
		t.Fatalf("bad token: %d %q", code, stderr)
	}
	if code, _, _ = runRepairTest(deps, token, "stats", "--dry-run", "--token-stdin", "--url", "http://example.com"); code != 2 {
		t.Fatalf("remote http URL accepted: %d", code)
	}
}
