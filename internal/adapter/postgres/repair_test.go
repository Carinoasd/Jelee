package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/images"
	"github.com/MoYuanCN/Jelee/internal/adapter/scan"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/logging"
)

// Self-healing repair actions (G50.4) against the injected problems of the
// consistency world: for every action the dry run lists exactly what the
// execution then changes, a second execution changes nothing, and the
// journaled actions revert.

func (w *consistencyWorld) repairer(scans app.RepairScanSubmitter) *app.Repairer {
	w.t.Helper()
	prober, err := images.NewVariantProber(w.store)
	if err != nil {
		w.t.Fatal(err)
	}
	r, err := app.NewRepairer(w.s, scan.ConsistencyProber{}, prober, logging.NewRedactor(logging.IPRedact, logging.PathRelative, []string{w.root}))
	if err != nil {
		w.t.Fatal(err)
	}
	if scans != nil {
		r = r.WithServer(scans, nil)
	}
	return r
}

func (w *consistencyWorld) repair(r *app.Repairer, action, library string, dryRun bool) domain.RepairResult {
	w.t.Helper()
	result, err := r.Run(w.ctx, app.RepairOptions{Action: action, Library: library, DryRun: dryRun, Origin: domain.RepairOriginCLI, Policy: w.policy, CallTimeout: 15 * time.Second})
	if err != nil {
		w.t.Fatalf("repair %s dry=%t: %v", action, dryRun, err)
	}
	return result
}

// chargeProbeQuota makes the probe quotas account for the cache rows the
// fixture inserted directly, as the probe pipeline would have.
func (w *consistencyWorld) chargeProbeQuota() {
	w.t.Helper()
	w.exec(`UPDATE probe_library_quota q SET rows_used=c.n,bytes_used=c.b FROM (SELECT count(*) n,COALESCE(sum(charge_bytes),0) b FROM probe_cache WHERE library_id=$1::uuid) c WHERE q.library_id=$1::uuid`, w.library)
	w.exec(`UPDATE probe_cache_quota SET rows_used=c.n,bytes_used=c.b FROM (SELECT count(*) n,COALESCE(sum(charge_bytes),0) b FROM probe_cache) c WHERE singleton`)
}

func (w *consistencyWorld) count(query string, args ...any) int64 {
	w.t.Helper()
	var n int64
	if err := w.s.Pool.QueryRow(w.ctx, query, args...).Scan(&n); err != nil {
		w.t.Fatalf("count failed: %v\n%s", err, query)
	}
	return n
}

func repairTargets(result domain.RepairResult) map[string]int64 {
	out := map[string]int64{}
	for _, t := range result.Targets {
		out[t.Kind] = t.Planned
	}
	return out
}

// repairCycle runs dry run, execution and a second execution and checks the
// G50.7 contract: the dry run writes nothing and plans exactly what the
// execution applies; the rerun plans and applies nothing.
func (w *consistencyWorld) repairCycle(r *app.Repairer, action, library string, want map[string]int64) (domain.RepairResult, domain.RepairResult) {
	w.t.Helper()
	runs, audits := w.count(`SELECT count(*) FROM repair_runs`), w.count(`SELECT count(*) FROM audit_logs`)
	dry := w.repair(r, action, library, true)
	if dry.State != domain.RepairStatePlanned || !dry.DryRun || dry.RunID != "" || dry.Applied != 0 || fmt.Sprint(repairTargets(dry)) != fmt.Sprint(want) {
		w.t.Fatalf("%s dry run: %+v", action, dry)
	}
	if w.count(`SELECT count(*) FROM repair_runs`) != runs || w.count(`SELECT count(*) FROM audit_logs`) != audits {
		w.t.Fatalf("%s dry run wrote a run or an audit row", action)
	}
	done := w.repair(r, action, library, false)
	if done.State != domain.RepairStateCompleted || done.RunID == "" || done.Planned != dry.Planned || done.Applied != dry.Planned || done.Skipped != 0 {
		w.t.Fatalf("%s execution: %+v (dry run planned %d)", action, done, dry.Planned)
	}
	if fmt.Sprint(repairTargets(done)) != fmt.Sprint(want) {
		w.t.Fatalf("%s execution targets: %v", action, repairTargets(done))
	}
	var state string
	var applied int64
	var stored []byte
	if err := w.s.Pool.QueryRow(w.ctx, `SELECT state,applied,result::text FROM repair_runs WHERE id=$1::uuid`, done.RunID).Scan(&state, &applied, &stored); err != nil || state != domain.RepairStateCompleted || applied != done.Applied {
		w.t.Fatalf("%s stored run: state=%s applied=%d err=%v", action, state, applied, err)
	}
	if strings.Contains(string(stored), w.root) || strings.Contains(string(stored), w.store) {
		w.t.Fatalf("%s stored result holds an absolute path", action)
	}
	if n := w.count(`SELECT count(*) FROM audit_logs WHERE event='repair.finished' AND target_id=$1::uuid`, done.RunID); n != 1 {
		w.t.Fatalf("%s finish audits: %d", action, n)
	}
	again := w.repair(r, action, library, false)
	if again.State != domain.RepairStateCompleted || again.Applied != 0 {
		w.t.Fatalf("%s rerun changed something: %+v", action, again)
	}
	return dry, done
}

func TestRepairStatsRecountsJournalsAndRevertsPostgres(t *testing.T) {
	w := newConsistencyWorld(t)
	w.inject()
	r := w.repairer(nil)
	_, done := w.repairCycle(r, domain.RepairStats, w.library, map[string]int64{domain.RepairTargetDailyCounters: 1})
	if again := w.repair(r, domain.RepairStats, w.library, true); again.Planned != 0 {
		t.Fatalf("drift left after the recount: %+v", again)
	}
	completions := func() int64 {
		return w.count(`SELECT completions FROM watch_stats_daily WHERE item_id=$1::uuid`, w.items["Alpha"])
	}
	if completions() != 1 || w.count(`SELECT count(*) FROM audit_logs WHERE event='repair.applied' AND target_id=$1::uuid`, done.RunID) != 1 ||
		w.count(`SELECT count(*) FROM repair_journal WHERE run_id=$1::uuid AND fix='watch_stats_daily.counters'`, done.RunID) != 1 {
		t.Fatal("recount was not applied, audited and journaled")
	}
	reverted, err := r.Revert(w.ctx, domain.Actor{}, done.RunID)
	if err != nil || reverted.Reverted != 1 || reverted.Skipped != 0 || completions() != 0 {
		t.Fatalf("revert: %+v %v completions=%d", reverted, err, completions())
	}
	if again, err := r.Revert(w.ctx, domain.Actor{}, done.RunID); err != nil || again.Reverted != 0 || again.Skipped != 0 {
		t.Fatalf("second revert replayed: %+v %v", again, err)
	}
	// The revert restored the drift, so the plan finds it again.
	if dry := w.repair(r, domain.RepairStats, w.library, true); dry.Planned != 1 {
		t.Fatalf("plan after revert: %+v", dry)
	}
}

func TestRepairStatsLeavesDaysBeforeRetentionPostgres(t *testing.T) {
	w := newConsistencyWorld(t)
	w.inject()
	r := w.repairer(nil)
	// With one day of playback history the drifted row (yesterday) may have
	// lost sessions to retention; recounting it would undercount.
	result, err := r.Run(w.ctx, app.RepairOptions{Action: domain.RepairStats, Library: w.library, DryRun: true, Origin: domain.RepairOriginCLI, SessionRetention: 24 * time.Hour})
	if err != nil || result.Planned != 0 {
		t.Fatalf("retention: %+v %v", result, err)
	}
}

func TestRepairCountsClearsForeignReferencesAndRevertsPostgres(t *testing.T) {
	w := newConsistencyWorld(t)
	w.inject()
	r := w.repairer(nil)
	dry, done := w.repairCycle(r, domain.RepairCounts, w.library, map[string]int64{domain.RepairTargetUserData: 1, domain.RepairTargetSession: 1})
	if dry.Info["manual"] != 2 || len(dry.Samples) != 2 {
		t.Fatalf("structural findings or samples: %+v", dry)
	}
	foreign := func() int64 {
		return w.count(`SELECT (SELECT count(*) FROM user_item_data WHERE item_id=$1::uuid AND last_source_id IS NOT NULL)+(SELECT count(*) FROM playback_sessions WHERE play_key='k2' AND source_id IS NOT NULL)`, w.items["Empty"])
	}
	if foreign() != 0 {
		t.Fatal("foreign references remain")
	}
	reverted, err := r.Revert(w.ctx, domain.Actor{}, done.RunID)
	if err != nil || reverted.Reverted != 2 || foreign() != 2 {
		t.Fatalf("revert: %+v %v", reverted, err)
	}
	if n := w.count(`SELECT count(*) FROM audit_logs WHERE event='repair.reverted' AND target_id=$1::uuid`, done.RunID); n != 1 {
		t.Fatalf("revert audits: %d", n)
	}
}

func TestRepairCachesDropsStaleProbeRowsPostgres(t *testing.T) {
	w := newConsistencyWorld(t)
	w.inject()
	w.chargeProbeQuota()
	r := w.repairer(nil)
	quota := func() int64 {
		return w.count(`SELECT rows_used FROM probe_library_quota WHERE library_id=$1::uuid`, w.library)
	}
	before := quota()
	_, done := w.repairCycle(r, domain.RepairCaches, w.library, map[string]int64{domain.RepairTargetProbeStale: 1})
	if w.count(`SELECT count(*) FROM probe_cache WHERE relative_path='Alpha/Alpha.mkv'`) != 0 || w.count(`SELECT count(*) FROM probe_cache WHERE relative_path='Old/Old.mkv'`) != 1 {
		t.Fatal("caches removed the wrong rows")
	}
	if quota() != before-1 {
		t.Fatalf("quota %d, was %d", quota(), before)
	}
	if _, err := r.Revert(w.ctx, domain.Actor{}, done.RunID); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("revert of a cache repair: %v", err)
	}
}

func TestRepairOrphansRemovesConfirmedOrphansOnlyPostgres(t *testing.T) {
	w := newConsistencyWorld(t)
	w.inject()
	// A cache row of a file the baseline lacks but the disk has is not an
	// orphan: the baseline is stale.
	var tool string
	if err := w.s.Pool.QueryRow(w.ctx, `SELECT tool_version_id::text FROM probe_cache LIMIT 1`).Scan(&tool); err != nil {
		t.Fatal(err)
	}
	w.file("Late/Late.mkv", 5)
	w.probe(tool, "Late/Late.mkv", 5, 1)
	w.chargeProbeQuota()
	r := w.repairer(nil)
	dry, _ := w.repairCycle(r, domain.RepairOrphans, "", map[string]int64{domain.RepairTargetProbeOrphan: 1, domain.RepairTargetVariantOrphan: 1})
	if dry.Info[domain.ConsistencyInfoBaselineStale] != 1 {
		t.Fatalf("stale baseline info: %+v", dry.Info)
	}
	if w.count(`SELECT count(*) FROM probe_cache WHERE relative_path IN ('Old/Old.mkv')`) != 0 || w.count(`SELECT count(*) FROM probe_cache WHERE relative_path IN ('Late/Late.mkv','Alpha/Alpha.mkv')`) != 2 {
		t.Fatal("orphans removed the wrong cache rows")
	}
	if w.count(`SELECT count(*) FROM image_variants`) != 1 {
		t.Fatal("orphans removed a variant whose file exists")
	}
	// A library run leaves the database-wide variant index alone.
	w.variant(false)
	if result := w.repair(r, domain.RepairOrphans, w.library, true); result.Planned != 0 {
		t.Fatalf("library run planned variant rows: %+v", result)
	}
	// A library run still removes the probe orphans of that library.
	w.probe(tool, "Old/Old.mkv", 1, 1)
	w.chargeProbeQuota()
	if result := w.repair(r, domain.RepairOrphans, w.library, false); result.Applied != 1 || result.Targets[0].Kind != domain.RepairTargetProbeOrphan {
		t.Fatalf("library run: %+v", result)
	}
}

func TestRepairItemsQueuesOneCatalogSyncPostgres(t *testing.T) {
	w := newConsistencyWorld(t)
	w.inject()
	r := w.repairer(nil)
	dry, done := w.repairCycle(r, domain.RepairItems, w.library, map[string]int64{domain.RepairTargetCatalogVideo: 1})
	if dry.Info[domain.ConsistencyInfoPending] != 1 || len(done.Jobs) != 1 || done.Jobs[0].Replayed {
		t.Fatalf("items: %+v / %+v", dry, done)
	}
	var kind, state string
	if err := w.s.Pool.QueryRow(w.ctx, `SELECT kind,state FROM jobs WHERE id=$1::uuid`, done.Jobs[0].JobID).Scan(&kind, &state); err != nil || kind != domain.JobCatalogSync || state != domain.JobQueued {
		t.Fatalf("queued job: %s %s %v", kind, state, err)
	}
	// The rerun found the job still queued and queued nothing new.
	again := w.repair(r, domain.RepairItems, w.library, false)
	if len(again.Jobs) != 1 || !again.Jobs[0].Replayed || again.Jobs[0].JobID != done.Jobs[0].JobID || again.Skipped != 1 {
		t.Fatalf("rerun: %+v", again)
	}
	if n := w.count(`SELECT count(*) FROM jobs WHERE kind='catalog_sync'`); n != 1 {
		t.Fatalf("catalog_sync jobs: %d", n)
	}
	// Another active job keeps the library busy.
	w.exec(`UPDATE jobs SET state='cancelled',finished_at=now() WHERE id=$1::uuid`, done.Jobs[0].JobID)
	w.submit(t, "repair-busy")
	if _, err := r.Run(w.ctx, app.RepairOptions{Action: domain.RepairItems, Library: w.library, Origin: domain.RepairOriginCLI, Policy: w.policy}); !errors.Is(err, domain.ErrJobBusy) {
		t.Fatalf("busy library: %v", err)
	}
}

// nfoScans submits real NFO scans as the fixture administrator, as the
// running server's *Jobs would.
type nfoScans struct{ w *consistencyWorld }

func (n nfoScans) SubmitScanOptions(ctx context.Context, actor domain.Actor, library, key, priority string, _, nfo bool, ignore domain.IgnoreIntent) (domain.Job, bool, error) {
	identity := domain.DefaultNFOIdentity()
	return n.w.s.SubmitScanWithStages(ctx, actor, library, key, priority, domain.ScanIntent{NFO: nfo, Ignore: ignore}, n.w.policy, nil, &identity)
}

func TestRepairNFOQueuesOneValidatingScanPostgres(t *testing.T) {
	w := newConsistencyWorld(t)
	w.inject()
	if _, err := w.repairer(nil).Run(w.ctx, app.RepairOptions{Action: domain.RepairNFO, Origin: domain.RepairOriginCLI, DryRun: true}); !errors.Is(err, domain.ErrRepairUnavailable) {
		t.Fatalf("command line NFO repair: %v", err)
	}
	r := w.repairer(nfoScans{w})
	options := app.RepairOptions{Action: domain.RepairNFO, Library: w.library, Origin: domain.RepairOriginAPI, Actor: w.a, CallTimeout: 15 * time.Second}
	// A library that does not read NFO files is reported, not failed.
	disabled, err := r.Run(w.ctx, options)
	if err != nil || disabled.Applied != 0 || disabled.Skipped != 4 || disabled.Info["nfoDisabled"] != 1 {
		t.Fatalf("NFO disabled: %+v %v", disabled, err)
	}
	policy, err := w.s.GetNFOLibraryPolicy(w.ctx, w.a, w.library)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = w.s.SetNFOLibraryPolicy(w.ctx, w.a, w.library, "repair-enable", policy.Generation, domain.NFOModeReadOnly); err != nil {
		t.Fatal(err)
	}
	if err = w.s.EnsureNFOCachePolicy(w.ctx, domain.DefaultNFOCachePolicy()); err != nil {
		t.Fatal(err)
	}
	options.DryRun = true
	dry, err := r.Run(w.ctx, options)
	if err != nil || dry.Planned != 4 {
		t.Fatalf("dry run: %+v %v", dry, err)
	}
	options.DryRun = false
	done, err := r.Run(w.ctx, options)
	if err != nil || done.Applied != 4 || len(done.Jobs) != 1 || done.Jobs[0].Replayed {
		t.Fatalf("execution: %+v %v", done, err)
	}
	var actor string
	if err = w.s.Pool.QueryRow(w.ctx, `SELECT COALESCE(actor_id::text,'') FROM audit_logs WHERE event='repair.finished' AND target_id=$1::uuid`, done.RunID).Scan(&actor); err != nil || actor != w.a.UserID {
		t.Fatalf("finish audit actor %q: %v", actor, err)
	}
	if n := w.count(`SELECT count(*) FROM nfo_job_requests WHERE job_id=$1::uuid AND requested`, done.Jobs[0].JobID); n != 1 {
		t.Fatal("queued scan does not validate NFO")
	}
	again, err := r.Run(w.ctx, options)
	if err != nil || again.Applied != 0 || len(again.Jobs) != 1 || !again.Jobs[0].Replayed {
		t.Fatalf("rerun: %+v %v", again, err)
	}
}

func TestRepairResultHasNoAbsolutePathsPostgres(t *testing.T) {
	w := newConsistencyWorld(t)
	w.inject()
	r := w.repairer(nil)
	for _, action := range []string{domain.RepairItems, domain.RepairCaches, domain.RepairOrphans, domain.RepairStats, domain.RepairCounts} {
		result := w.repair(r, action, "", true)
		data, _ := json.Marshal(result)
		if strings.Contains(string(data), w.root) || strings.Contains(string(data), w.store) {
			t.Fatalf("%s result holds an absolute path: %s", action, data)
		}
	}
}

func TestRepairDowngradeRefusesRetainedRunsPostgres(t *testing.T) {
	w := newConsistencyWorld(t)
	w.inject()
	w.repair(w.repairer(nil), domain.RepairCounts, w.library, false)
	refuseRetainedDowngrade(t, w.jobFixture, "repair_runs", "retained repair state prevents downgrade")
}
