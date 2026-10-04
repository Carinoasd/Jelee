package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/images"
	"github.com/MoYuanCN/Jelee/internal/adapter/scan"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/logging"
)

// consistencyWorld is one library whose catalog, baseline, derived tables
// and files agree; inject then breaks one record of every checked kind.
type consistencyWorld struct {
	jobFixture
	t        *testing.T
	root     string
	store    string
	library  string
	rootID   string
	day      string
	items    map[string]string
	sources  map[string]string
	files    map[string][2]int64
	variants []string
}

func newConsistencyWorld(t *testing.T) *consistencyWorld {
	t.Helper()
	f := newJobFixture(t)
	w := &consistencyWorld{jobFixture: f, t: t, library: f.registration.Library.ID, rootID: f.registration.RootID, store: t.TempDir(),
		items: map[string]string{}, sources: map[string]string{}, files: map[string][2]int64{}}
	w.day = time.Now().UTC().AddDate(0, 0, -1).Format(time.DateOnly)
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT path FROM library_roots WHERE id=$1::uuid`, w.rootID).Scan(&w.root); err != nil {
		t.Fatal(err)
	}
	w.items["Alpha"] = w.item("Alpha", "Movie")
	w.items["Show"] = w.item("Show", "Series")
	w.sources["Alpha"] = w.source("Alpha", "Alpha/Alpha.mkv")
	w.indexed("Alpha/Alpha.mkv", "video", 100)
	w.indexed("Alpha/Alpha.nfo", "nfo", 50)
	w.indexed("Alpha/poster.jpg", "image", 20)
	w.indexed("Alpha/Alpha.en.srt", "other", 10)
	w.exec(`INSERT INTO user_item_data(user_id,item_id,last_source_id,play_count,played,updated_at) VALUES($1::uuid,$2::uuid,$3::uuid,1,true,now())`, w.a.UserID, w.items["Alpha"], w.sources["Alpha"])
	w.session("k1", "Alpha", w.sources["Alpha"], true, "first")
	w.exec(`INSERT INTO watch_stats_daily(user_id,day,item_id,library_id,effective_ms,sessions,views,first_plays,rewatches,completions,completion_milli) VALUES($1::uuid,$2::date,$3::uuid,$4::uuid,3600000,1,1,1,0,1,900)`, w.a.UserID, w.day, w.items["Alpha"], w.library)
	w.image("Alpha", "Primary", "Alpha/poster.jpg", w.files["Alpha/poster.jpg"][0])
	w.sidecar("Alpha", "Alpha/Alpha.en.srt")
	w.observe("Alpha", w.sources["Alpha"], domain.NFOItemObservedValid, w.files["Alpha/Alpha.nfo"][0], w.files["Alpha/Alpha.nfo"][1])
	if err := f.s.EnsureProbePolicy(f.ctx, domain.DefaultProbeCachePolicy()); err != nil {
		t.Fatal(err)
	}
	identity, err := f.s.RegisterProbeIdentity(f.ctx, probeTestIdentity())
	if err != nil {
		t.Fatal(err)
	}
	w.exec(`INSERT INTO probe_library_quota(library_id,row_limit,byte_limit) SELECT $1::uuid,library_row_limit,library_byte_limit FROM probe_cache_quota ON CONFLICT DO NOTHING`, w.library)
	w.probe(identity.ID, "Alpha/Alpha.mkv", w.files["Alpha/Alpha.mkv"][0], w.files["Alpha/Alpha.mkv"][1])
	w.variant(true)
	return w
}

func (w *consistencyWorld) exec(query string, args ...any) {
	w.t.Helper()
	if _, err := w.s.Pool.Exec(w.ctx, query, args...); err != nil {
		w.t.Fatalf("fixture statement failed: %v\n%s", err, query)
	}
}

func (w *consistencyWorld) file(relative string, size int) {
	w.t.Helper()
	path := filepath.Join(w.root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		w.t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		w.t.Fatal(err)
	}
	w.files[relative] = [2]int64{info.Size(), info.ModTime().UnixNano()}
}

// indexed creates a file and its baseline row.
func (w *consistencyWorld) indexed(relative, kind string, size int) {
	w.t.Helper()
	w.file(relative, size)
	w.baseline(relative, kind, w.files[relative][0], w.files[relative][1])
}

func (w *consistencyWorld) baseline(relative, kind string, size, modified int64) {
	w.t.Helper()
	w.exec(`INSERT INTO library_inventory_baseline_data(library_id,snapshot_id,root_id,path,attributes_known,kind,size,modified_unix_nano,inventory_generation,observed_revision)
 SELECT $1::uuid,l.active_inventory_snapshot,$2::uuid,$3,true,$4,$5,$6,1,l.inventory_baseline_revision FROM libraries l WHERE l.id=$1::uuid`, w.library, w.rootID, relative, kind, size, modified)
}

func (w *consistencyWorld) item(title, kind string) string {
	w.t.Helper()
	var id string
	if err := w.s.Pool.QueryRow(w.ctx, `INSERT INTO items(library_id,title,kind) VALUES($1::uuid,$2,$3) RETURNING id::text`, w.library, title, kind).Scan(&id); err != nil {
		w.t.Fatal(err)
	}
	return id
}

func (w *consistencyWorld) source(item, relative string) string {
	w.t.Helper()
	var id string
	if err := w.s.Pool.QueryRow(w.ctx, `INSERT INTO media_sources(item_id,library_id,root_id,relative_path,content_type) VALUES($1::uuid,$2::uuid,$3::uuid,$4,'video/x-matroska') RETURNING id::text`, w.items[item], w.library, w.rootID, relative).Scan(&id); err != nil {
		w.t.Fatal(err)
	}
	return id
}

func (w *consistencyWorld) session(key, item, source string, counted bool, play string) string {
	w.t.Helper()
	var id string
	var sourceArg any
	if source != "" {
		sourceArg = source
	}
	day := any(nil)
	through := any(nil)
	if counted {
		day, through = w.day, w.day+" 11:00:00+00"
	}
	err := w.s.Pool.QueryRow(w.ctx, `INSERT INTO playback_sessions(user_id,play_key,item_id,library_id,source_id,state,started_at,last_report_at,ended_at,stats_through,stats_day,stats_counted,stats_completed,stats_completion_milli,stats_play)
 VALUES($1::uuid,$2,$3::uuid,$4::uuid,$5::uuid,'stopped',$6::date+time '10:00',$6::date+time '11:00',$6::date+time '11:00',$7::timestamptz,$8::date,$9,$9,CASE WHEN $9 THEN 900 ELSE 0 END,NULLIF($10,'')) RETURNING id::text`,
		w.a.UserID, key, w.items[item], w.library, sourceArg, w.day, through, day, counted, play).Scan(&id)
	if err != nil {
		w.t.Fatal(err)
	}
	return id
}

func (w *consistencyWorld) image(item, kind, relative string, size int64) {
	w.t.Helper()
	modified := w.files[relative][1]
	w.exec(`INSERT INTO item_images(item_id,library_id,image_type,image_index,source_kind,root_id,relative_path,source_size,source_mtime_unix_nano) VALUES($1::uuid,$2::uuid,$3,0,'local',$4::uuid,$5,$6,$7)`,
		w.items[item], w.library, kind, w.rootID, relative, size, modified)
}

func (w *consistencyWorld) sidecar(item, relative string) {
	w.t.Helper()
	stamp := w.files[relative]
	w.exec(`INSERT INTO media_sidecar_tracks(source_id,library_id,root_id,relative_path,kind,format,size,modified_unix_nano) VALUES($1::uuid,$2::uuid,$3::uuid,$4,'subtitle','srt',$5,$6)`,
		w.sources[item], w.library, w.rootID, relative, stamp[0], stamp[1])
}

func (w *consistencyWorld) observe(item, source, status string, size, modified int64) {
	w.t.Helper()
	observation := map[string]any{"version": "nfo-item-observation-v1", "status": status, "sourceId": source, "rootId": w.rootID, "generation": 1,
		"identityDigest": strings.Repeat("a", 64), "candidateDigest": strings.Repeat("b", 64), "readAt": "2026-01-01T00:00:00Z", "acceptedRevision": 2,
		"stamp": map[string]any{"size": size, "modifiedUnixNano": modified, "sha256": strings.Repeat("c", 64), "fingerprintVersion": "sha256-full-v1"}}
	if status == domain.NFOItemObservedMissing {
		observation["stamp"], observation["candidateDigest"] = nil, domain.NFOCandidateDigest([]string{})
	}
	data, _ := json.Marshal(observation)
	w.exec(`INSERT INTO item_nfo_observations(item_id,observation) VALUES($1::uuid,$2::jsonb) ON CONFLICT(item_id) DO UPDATE SET observation=EXCLUDED.observation`, w.items[item], data)
}

func (w *consistencyWorld) probe(tool, relative string, size, modified int64) {
	w.t.Helper()
	metadata := `{"format":{},"streams":[{"index":0,"kind":"video","video":{}}],"chapters":[]}`
	w.exec(`INSERT INTO probe_cache(root_id,relative_path,library_id,size,modified_unix_nano,fingerprint,fingerprint_version,tool_version_id,library_generation,root_generation,state,metadata,expires_at,charge_bytes)
 SELECT r.id,$2,r.library_id,$3,$4,decode(repeat('d',64),'hex'),'edge-sha256-v1',$5::uuid,lib.probe_generation,r.probe_generation,'ready',$6::jsonb,clock_timestamp()+interval '1 day',2048+octet_length($6::jsonb::text)
 FROM library_roots r JOIN libraries lib ON lib.id=r.library_id WHERE r.id=$1::uuid`, w.rootID, relative, size, modified, tool, metadata)
}

// variant adds an index row; with file it also writes the variant into the
// live generation of the store directory.
func (w *consistencyWorld) variant(file bool) {
	w.t.Helper()
	source := strings.Repeat(fmt.Sprintf("%x", len(w.variants)+1), 64)
	key := strings.Repeat("e", 63) + fmt.Sprintf("%x", len(w.variants))
	if file {
		directory := filepath.Join(w.store, "variants", "0000000000000001", source)
		if err := os.MkdirAll(directory, 0o700); err != nil {
			w.t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, key), []byte("variant"), 0o600); err != nil {
			w.t.Fatal(err)
		}
	}
	w.exec(`INSERT INTO image_variants(content_sha256,variant_key,byte_size) VALUES(decode($1,'hex'),decode($2,'hex'),7)`, source, key)
	w.variants = append(w.variants, source+"/"+key)
}

// inject breaks one record of every checked kind and returns the expected
// finding count per code.
func (w *consistencyWorld) inject() map[string]int64 {
	w.t.Helper()
	// orphan_item: Gone's file is in neither the baseline nor the root;
	// Fresh's file exists but is newer than the baseline.
	w.items["Gone"] = w.item("Gone", "Movie")
	w.sources["Gone"] = w.source("Gone", "Gone/Gone.mkv")
	w.items["Fresh"] = w.item("Fresh", "Movie")
	w.file("Fresh/Fresh.mkv", 30)
	w.sources["Fresh"] = w.source("Fresh", "Fresh/Fresh.mkv")
	// orphan_file: an uncatalogued video, and one awaiting review.
	w.indexed("Loose/Loose.mkv", "video", 40)
	w.indexed("Pending/p.mkv", "video", 40)
	w.exec(`INSERT INTO catalog_scan_pending(library_id,root_id,relative_path,size,modified_unix_nano,parser_version,reason,kind,confidence,title,special) VALUES($1::uuid,$2::uuid,'Pending/p.mkv',40,1,'medianame-v1','confidence','movie','low','p','none')`, w.library, w.rootID)
	// version_count.
	w.items["Empty"] = w.item("Empty", "Movie")
	w.indexed("Show/extra.mkv", "video", 10)
	w.sources["Show"] = w.source("Show", "Show/extra.mkv")
	w.exec(`INSERT INTO user_item_data(user_id,item_id,last_source_id,updated_at) VALUES($1::uuid,$2::uuid,$3::uuid,now())`, w.a.UserID, w.items["Empty"], w.sources["Alpha"])
	w.session("k2", "Gone", w.sources["Alpha"], false, "")
	// watch_stats_drift: a counter drifted, and a counted session lost its row.
	w.exec(`UPDATE watch_stats_daily SET completions=0 WHERE item_id=$1::uuid`, w.items["Alpha"])
	w.session("k3", "Empty", "", true, "first")
	// image_file: the poster changed since it was read; the fanart is gone.
	w.exec(`UPDATE item_images SET source_size=source_size+1 WHERE item_id=$1::uuid`, w.items["Alpha"])
	w.image("Alpha", "Backdrop", "Alpha/fanart.jpg", 5)
	// image_variant_index.
	w.variant(false)
	// nfo_state.
	w.observe("Gone", w.sources["Gone"], domain.NFOItemObservedValid, 10, 1)
	w.indexed("Fresh/Fresh.nfo", "nfo", 5)
	w.observe("Fresh", w.sources["Fresh"], domain.NFOItemObservedMissing, 0, 0)
	w.observe("Alpha", w.sources["Alpha"], domain.NFOItemObservedValid, w.files["Alpha/Alpha.nfo"][0]+1, w.files["Alpha/Alpha.nfo"][1])
	w.observe("Empty", w.sources["Alpha"], domain.NFOItemObservedValid, 10, 1)
	// sidecar_file.
	w.exec(`INSERT INTO media_sidecar_tracks(source_id,library_id,root_id,relative_path,kind,format,size,modified_unix_nano) VALUES($1::uuid,$2::uuid,$3::uuid,'Alpha/Alpha.fr.srt','subtitle','srt',3,1)`, w.sources["Alpha"], w.library, w.rootID)
	// probe_cache_stale.
	w.exec(`UPDATE probe_cache SET size=size+1 WHERE relative_path='Alpha/Alpha.mkv'`)
	var tool string
	if err := w.s.Pool.QueryRow(w.ctx, `SELECT tool_version_id::text FROM probe_cache LIMIT 1`).Scan(&tool); err != nil {
		w.t.Fatal(err)
	}
	w.probe(tool, "Old/Old.mkv", 1, 1)
	// constraint_state: an unvalidated check and an index a failed
	// concurrent build left invalid.
	w.exec(`ALTER TABLE items ADD CONSTRAINT consistency_probe_check CHECK(length(title)>0) NOT VALID`)
	w.exec(`CREATE TABLE consistency_duplicates(v integer)`)
	w.exec(`INSERT INTO consistency_duplicates VALUES(1),(1)`)
	if _, err := w.s.Pool.Exec(w.ctx, `CREATE UNIQUE INDEX CONCURRENTLY consistency_duplicates_idx ON consistency_duplicates(v)`); err == nil {
		w.t.Fatal("duplicate rows built a unique index")
	}
	return map[string]int64{
		domain.ConsistencySourceMissing: 1, domain.ConsistencyVideoNotCataloged: 1,
		domain.ConsistencyVideoWithoutSource: 1, domain.ConsistencyContainerWithSource: 1, domain.ConsistencyUserDataForeignSource: 1, domain.ConsistencySessionForeignSource: 1,
		domain.ConsistencyDailyCounterDrift: 1, domain.ConsistencyDailyRowMissing: 1,
		domain.ConsistencyImageSourceChanged: 1, domain.ConsistencyImageSourceMissing: 1, domain.ConsistencyVariantFileMissing: 1,
		domain.ConsistencyNFOMissing: 1, domain.ConsistencyNFOAppeared: 1, domain.ConsistencyNFOChanged: 1, domain.ConsistencyNFOForeignSource: 1,
		domain.ConsistencySidecarMissing: 1, domain.ConsistencyProbeCacheChanged: 1, domain.ConsistencyProbeCacheOrphan: 1,
		domain.ConsistencyConstraintNotValid: 1, domain.ConsistencyIndexInvalid: 1,
	}
}

func (w *consistencyWorld) checker(mode logging.PathMode) *app.ConsistencyChecker {
	w.t.Helper()
	prober, err := images.NewVariantProber(w.store)
	if err != nil {
		w.t.Fatal(err)
	}
	c, err := app.NewConsistencyChecker(w.s, scan.ConsistencyProber{}, prober, logging.NewRedactor(logging.IPRedact, mode, []string{w.root}), w.s)
	if err != nil {
		w.t.Fatal(err)
	}
	return c
}

func (w *consistencyWorld) run(fix bool) domain.ConsistencyReport {
	w.t.Helper()
	report, err := w.checker(logging.PathRelative).Run(w.ctx, app.ConsistencyOptions{Origin: domain.ConsistencyOriginCLI, Library: w.library, Fix: fix, CallTimeout: 15 * time.Second})
	if err != nil {
		w.t.Fatal(err)
	}
	return report
}

// consistencyCodes counts findings by code from the samples, which hold
// every finding while a check has fewer than the sample bound.
func consistencyCodes(t *testing.T, report domain.ConsistencyReport) map[string]int64 {
	t.Helper()
	codes := map[string]int64{}
	results := append([]domain.ConsistencyCheckResult(nil), report.Global...)
	for _, l := range report.Libraries {
		results = append(results, l.Checks...)
	}
	for _, r := range results {
		if r.SamplesTruncated || int64(len(r.Samples)) != r.Findings {
			t.Fatalf("check %s holds %d samples for %d findings", r.Check, len(r.Samples), r.Findings)
		}
		for _, f := range r.Samples {
			codes[f.Code]++
		}
	}
	return codes
}

func consistencyResult(t *testing.T, report domain.ConsistencyReport, check string) domain.ConsistencyCheckResult {
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
	t.Fatal("check missing from report", check)
	return domain.ConsistencyCheckResult{}
}

func TestConsistencyCleanLibraryHasNoFindingsPostgres(t *testing.T) {
	w := newConsistencyWorld(t)
	report := w.run(false)
	if report.State != domain.ConsistencyRunCompleted || report.Totals.Findings != 0 || report.Schema != domain.ConsistencyReportSchema {
		data, _ := json.MarshalIndent(report, "", " ")
		t.Fatalf("consistent library reported findings:\n%s", data)
	}
	for _, check := range domain.ConsistencyChecks() {
		r := consistencyResult(t, report, check)
		if r.Status != domain.ConsistencyStatusOK || r.Examined == 0 && check != domain.ConsistencyOrphanFile {
			t.Fatalf("check %s: status %s examined %d", check, r.Status, r.Examined)
		}
	}
	if r := consistencyResult(t, report, domain.ConsistencyWatchStats); r.Info[domain.ConsistencyInfoSampled] != 2 {
		t.Fatalf("statistics sample: %+v", r.Info)
	}
	metrics, err := w.s.OpsMetrics(w.ctx)
	if err != nil || metrics.ConsistencyLastFinished.IsZero() || metrics.ConsistencyFindings != (domain.ConsistencyCheckCounts{}) {
		t.Fatalf("metrics after a clean run: %+v %v", metrics, err)
	}
}

func TestConsistencyDetectsEveryInjectedProblemPostgres(t *testing.T) {
	w := newConsistencyWorld(t)
	want := w.inject()
	report := w.run(false)
	if got := consistencyCodes(t, report); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("finding codes:\n got %v\nwant %v", got, want)
	}
	if report.State != domain.ConsistencyRunCompleted || report.Totals.Findings != 20 || report.Totals.Fixable != 3 || report.Totals.Fixed != 0 {
		t.Fatalf("totals: state=%s %+v", report.State, report.Totals)
	}
	orphans := consistencyResult(t, report, domain.ConsistencyOrphanItem)
	if orphans.Info[domain.ConsistencyInfoBaselineStale] != 1 || orphans.Samples[0].Path != "Gone/Gone.mkv" || orphans.Samples[0].ItemID != w.items["Gone"] {
		t.Fatalf("orphan item result: %+v", orphans)
	}
	if files := consistencyResult(t, report, domain.ConsistencyOrphanFile); files.Info[domain.ConsistencyInfoPending] != 1 || files.Samples[0].Path != "Loose/Loose.mkv" {
		t.Fatalf("orphan file result: %+v", files)
	}
	if drift := consistencyResult(t, report, domain.ConsistencyWatchStats); drift.Fixable != 1 {
		t.Fatalf("drift result: %+v", drift)
	} else {
		for _, f := range drift.Samples {
			if f.Code == domain.ConsistencyDailyCounterDrift && (f.Expected["completions"] != 1 || f.Actual["completions"] != 0 || f.Day != w.day) {
				t.Fatalf("drift sample: %+v", f)
			}
		}
	}
	// The stored report is the same document, and it holds no absolute path.
	document, err := w.s.LatestConsistencyReport(w.ctx, w.library)
	if err != nil {
		t.Fatal(err)
	}
	var stored domain.ConsistencyReport
	if err = json.Unmarshal(document, &stored); err != nil || stored.RunID != report.RunID || stored.Totals != report.Totals {
		t.Fatalf("stored report differs: %v", err)
	}
	if strings.Contains(string(document), w.root) || strings.Contains(string(document), w.store) {
		t.Fatal("stored report contains an absolute path")
	}
	metrics, err := w.s.OpsMetrics(w.ctx)
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for i, check := range domain.ConsistencyChecks() {
		if metrics.ConsistencyFindings[i] != consistencyResult(t, report, check).Findings {
			t.Fatalf("metric for %s: %d", check, metrics.ConsistencyFindings[i])
		}
		total += metrics.ConsistencyFindings[i]
	}
	if total != 20 {
		t.Fatalf("metric total %d", total)
	}
}

func TestConsistencyRedactsPathsByDefaultPostgres(t *testing.T) {
	w := newConsistencyWorld(t)
	w.inject()
	report, err := w.checker(logging.PathRedact).Run(w.ctx, app.ConsistencyOptions{Origin: domain.ConsistencyOriginCLI, Library: w.library})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(report)
	if strings.Contains(string(data), "Gone/Gone.mkv") || strings.Contains(string(data), w.root) || !strings.Contains(string(data), `"path":"[redacted]"`) {
		t.Fatal("redact path mode leaked a path")
	}
}

func TestConsistencyFixIsJournaledAuditedAndReversiblePostgres(t *testing.T) {
	w := newConsistencyWorld(t)
	w.inject()
	fixed := w.run(true)
	if fixed.Totals.Fixed != 3 || fixed.Mode != domain.ConsistencyModeFix {
		t.Fatalf("fix run: %+v", fixed.Totals)
	}
	var lastSource, sessionSource *string
	var completions int
	if err := w.s.Pool.QueryRow(w.ctx, `SELECT (SELECT last_source_id::text FROM user_item_data WHERE item_id=$1::uuid),(SELECT source_id::text FROM playback_sessions WHERE play_key='k2'),(SELECT completions FROM watch_stats_daily WHERE item_id=$2::uuid)`,
		w.items["Empty"], w.items["Alpha"]).Scan(&lastSource, &sessionSource, &completions); err != nil {
		t.Fatal(err)
	}
	if lastSource != nil || sessionSource != nil || completions != 1 {
		t.Fatalf("repairs not applied: %v %v %d", lastSource, sessionSource, completions)
	}
	var journal, fixAudits int
	if err := w.s.Pool.QueryRow(w.ctx, `SELECT (SELECT count(*) FROM consistency_fix_journal WHERE run_id=$1::uuid AND reverted_at IS NULL),(SELECT count(*) FROM audit_logs WHERE event='consistency.fixed' AND target_id=$1::uuid)`, fixed.RunID).Scan(&journal, &fixAudits); err != nil {
		t.Fatal(err)
	}
	if journal != 3 || fixAudits < 1 {
		t.Fatalf("journal %d, audits %d", journal, fixAudits)
	}
	// Repairs never delete: every record the findings named still exists.
	after := w.run(false)
	if after.Totals.Findings != 17 || after.Totals.Fixable != 0 {
		t.Fatalf("after repair: %+v", after.Totals)
	}
	// A second fix run has nothing left to repair and journals nothing.
	if again := w.run(true); again.Totals.Fixed != 0 {
		t.Fatalf("repeated fix: %+v", again.Totals)
	}
	result, err := w.s.RevertConsistencyRun(w.ctx, fixed.RunID)
	if err != nil || result.Reverted != 3 || result.Skipped != 0 {
		t.Fatalf("revert: %+v %v", result, err)
	}
	if err = w.s.Pool.QueryRow(w.ctx, `SELECT (SELECT last_source_id::text FROM user_item_data WHERE item_id=$1::uuid),(SELECT source_id::text FROM playback_sessions WHERE play_key='k2'),(SELECT completions FROM watch_stats_daily WHERE item_id=$2::uuid)`,
		w.items["Empty"], w.items["Alpha"]).Scan(&lastSource, &sessionSource, &completions); err != nil {
		t.Fatal(err)
	}
	if lastSource == nil || *lastSource != w.sources["Alpha"] || sessionSource == nil || *sessionSource != w.sources["Alpha"] || completions != 0 {
		t.Fatalf("revert did not restore: %v %v %d", lastSource, sessionSource, completions)
	}
	if again, err := w.s.RevertConsistencyRun(w.ctx, fixed.RunID); err != nil || again.Reverted != 0 {
		t.Fatalf("second revert replayed entries: %+v %v", again, err)
	}
	var revertAudits int
	if err = w.s.Pool.QueryRow(w.ctx, `SELECT count(*) FROM audit_logs WHERE event='consistency.reverted' AND target_id=$1::uuid`, fixed.RunID).Scan(&revertAudits); err != nil || revertAudits != 1 {
		t.Fatalf("revert audits %d %v", revertAudits, err)
	}
	if back := w.run(false); back.Totals.Findings != 20 || back.Totals.Fixable != 3 {
		t.Fatalf("after revert: %+v", back.Totals)
	}
	if _, err = w.s.RevertConsistencyRun(w.ctx, consistencyZeroID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("unknown run reverted", err)
	}
}

func TestConsistencyRevertSkipsChangedRowsPostgres(t *testing.T) {
	w := newConsistencyWorld(t)
	w.inject()
	fixed := w.run(true)
	// The roll-up changed the counters again after the repair: the revert
	// must not overwrite it.
	w.exec(`UPDATE watch_stats_daily SET completion_milli=completion_milli+1 WHERE item_id=$1::uuid`, w.items["Alpha"])
	result, err := w.s.RevertConsistencyRun(w.ctx, fixed.RunID)
	if err != nil || result.Reverted != 2 || result.Skipped != 1 {
		t.Fatalf("revert of a changed row: %+v %v", result, err)
	}
}

// baselineSwap replaces the baseline while the run reads it.
type baselineSwap struct {
	*Store
	swapped bool
}

func (b *baselineSwap) ConsistencyBaselineCurrent(ctx context.Context, library domain.ConsistencyLibrary) (bool, error) {
	if !b.swapped {
		b.swapped = true
		if _, err := b.Pool.Exec(ctx, `UPDATE libraries SET inventory_baseline_revision=inventory_baseline_revision+1 WHERE id=$1::uuid`, library.ID); err != nil {
			return false, err
		}
	}
	return b.Store.ConsistencyBaselineCurrent(ctx, library)
}

func TestConsistencyVoidsComparisonsWhenTheBaselineChangesPostgres(t *testing.T) {
	w := newConsistencyWorld(t)
	w.inject()
	c, err := app.NewConsistencyChecker(&baselineSwap{Store: w.s}, scan.ConsistencyProber{}, nil, logging.NewRedactor(logging.IPRedact, logging.PathRedact, nil), w.s)
	if err != nil {
		t.Fatal(err)
	}
	report, err := c.Run(w.ctx, app.ConsistencyOptions{Origin: domain.ConsistencyOriginCLI, Library: w.library})
	if err != nil {
		t.Fatal(err)
	}
	if report.State != domain.ConsistencyRunPartial {
		t.Fatalf("state %s", report.State)
	}
	for _, check := range domain.ConsistencyChecks() {
		r := consistencyResult(t, report, check)
		switch {
		case check == domain.ConsistencyImageVariant:
			if r.Status != domain.ConsistencyStatusSkipped {
				t.Fatalf("variant check without a store: %s", r.Status)
			}
		case domain.ConsistencyBaselineCheck(check):
			if r.Status != domain.ConsistencyStatusIncomplete || r.Reason != domain.ConsistencyReasonBaselineChanged || r.Findings != 0 {
				t.Fatalf("%s kept findings of a replaced baseline: %+v", check, r)
			}
		default:
			if r.Findings == 0 {
				t.Fatalf("%s lost findings independent of the baseline", check)
			}
		}
	}
}

func TestConsistencySkipsBaselineChecksWithoutBaselinePostgres(t *testing.T) {
	f := newJobFixture(t)
	c, err := app.NewConsistencyChecker(f.s, scan.ConsistencyProber{}, nil, logging.NewRedactor(logging.IPRedact, logging.PathRedact, nil), f.s)
	if err != nil {
		t.Fatal(err)
	}
	report, err := c.Run(f.ctx, app.ConsistencyOptions{Origin: domain.ConsistencyOriginCLI})
	if err != nil || len(report.Libraries) != 1 || report.Libraries[0].Baseline.Available {
		t.Fatalf("report: %+v %v", report, err)
	}
	for _, r := range report.Libraries[0].Checks {
		if domain.ConsistencyBaselineCheck(r.Check) != (r.Status == domain.ConsistencyStatusSkipped && r.Reason == domain.ConsistencyReasonNoBaseline) {
			t.Fatalf("check %s: %s %s", r.Check, r.Status, r.Reason)
		}
	}
	if _, err = c.Run(f.ctx, app.ConsistencyOptions{Origin: domain.ConsistencyOriginCLI, Library: consistencyZeroID}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("unknown library accepted", err)
	}
	if id, err := f.s.ResolveLibrary(f.ctx, "primary"); err != nil || id != f.registration.Library.ID {
		t.Fatalf("resolve by name: %s %v", id, err)
	}
	if id, err := f.s.ResolveLibrary(f.ctx, f.registration.Library.ID); err != nil || id != f.registration.Library.ID {
		t.Fatalf("resolve by ID: %s %v", id, err)
	}
}

func TestConsistencyJobLifecyclePostgres(t *testing.T) {
	w := newConsistencyWorld(t)
	w.inject()
	if _, err := w.s.ClaimJobWithCapabilities(w.ctx, "no-checker", false, time.Minute, domain.ScanCapabilities{}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("empty queue claimed", err)
	}
	job, err := w.s.EnqueueConsistencyCheck(w.ctx, w.library, domain.JobPriorityManual, w.policy)
	if err != nil || job.Kind != domain.JobConsistencyCheck || job.State != domain.JobQueued {
		t.Fatalf("enqueue: %+v %v", job, err)
	}
	if _, err = w.s.EnqueueConsistencyCheck(w.ctx, w.library, domain.JobPriorityManual, w.policy); !errors.Is(err, domain.ErrJobBusy) {
		t.Fatal("second active job admitted", err)
	}
	if _, err = w.s.ClaimJobWithCapabilities(w.ctx, "no-checker", false, time.Minute, domain.ScanCapabilities{CatalogSync: true}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("worker without a checker claimed a consistency job", err)
	}
	lease, err := w.s.ClaimJobWithCapabilities(w.ctx, "checker", false, time.Minute, domain.ScanCapabilities{ConsistencyCheck: true})
	if err != nil || lease.Job.ID != job.ID {
		t.Fatalf("claim: %+v %v", lease, err)
	}
	report, err := w.checker(logging.PathRedact).Run(w.ctx, app.ConsistencyOptions{Origin: domain.ConsistencyOriginJob, JobID: job.ID, Library: w.library})
	if err != nil || report.JobID != job.ID || report.Totals.Findings != 20 {
		t.Fatalf("job run: %+v %v", report.Totals, err)
	}
	if err = w.s.FinishJob(w.ctx, lease, domain.JobSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	finished, err := w.s.GetJob(w.ctx, w.a, job.ID)
	if err != nil || finished.State != domain.JobSucceeded {
		t.Fatalf("finished job: %+v %v", finished, err)
	}
	var runJob string
	var audits int
	if err = w.s.Pool.QueryRow(w.ctx, `SELECT (SELECT job_id::text FROM consistency_runs WHERE id=$1::uuid),(SELECT count(*) FROM audit_logs WHERE target_id=$2::uuid AND event IN ('consistency.submitted','consistency.finished'))`, report.RunID, job.ID).Scan(&runJob, &audits); err != nil || runJob != job.ID || audits != 2 {
		t.Fatalf("run job %s audits %d %v", runJob, audits, err)
	}
	snapshot, err := w.s.JobMetrics(w.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if g := snapshot.Groups[5]; g.Kind != domain.JobConsistencyCheck || g.Priority != domain.JobPriorityManual || g.Succeeded != 1 {
		t.Fatalf("job metrics group: %+v", g)
	}

	// A cancelled job closes the run it left open.
	job, err = w.s.EnqueueConsistencyCheck(w.ctx, w.library, domain.JobPriorityBackground, w.policy)
	if err != nil {
		t.Fatal(err)
	}
	lease, err = w.s.ClaimJobWithCapabilities(w.ctx, "checker", true, time.Minute, domain.ScanCapabilities{ConsistencyCheck: true})
	if err != nil {
		t.Fatal(err)
	}
	run, err := w.s.StartConsistencyRun(w.ctx, domain.ConsistencyRun{JobID: job.ID, LibraryID: w.library, Origin: domain.ConsistencyOriginJob, Mode: domain.ConsistencyModeReport})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.s.CancelJob(w.ctx, w.a, job.ID); err != nil {
		t.Fatal(err)
	}
	if err = w.s.FinishConsistencyCheck(w.ctx, lease, domain.JobSucceeded, ""); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("cancelled job finished as succeeded", err)
	}
	if err = w.s.FinishConsistencyCheck(w.ctx, lease, domain.JobCancelled, ""); err != nil {
		t.Fatal(err)
	}
	var state string
	if err = w.s.Pool.QueryRow(w.ctx, `SELECT state FROM consistency_runs WHERE id=$1::uuid`, run.ID).Scan(&state); err != nil || state != domain.ConsistencyRunCancelled {
		t.Fatalf("run left by a cancelled job: %s %v", state, err)
	}
	if err = w.s.FinishConsistencyCheck(w.ctx, lease, domain.JobFailed, "scan_io"); err == nil {
		t.Fatal("finished a job twice")
	}
}

func TestConsistencyScheduleDispatchPostgres(t *testing.T) {
	f := newJobFixture(t)
	if _, err := f.s.DispatchConsistencyCheck(f.ctx, time.Minute, f.policy); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("sub-hour interval accepted", err)
	}
	worked, err := f.s.DispatchConsistencyCheck(f.ctx, time.Hour, f.policy)
	if err != nil || !worked {
		t.Fatalf("first dispatch: %t %v", worked, err)
	}
	var kind, priority string
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT kind,priority FROM jobs WHERE library_id=$1::uuid`, f.registration.Library.ID).Scan(&kind, &priority); err != nil || kind != domain.JobConsistencyCheck || priority != domain.JobPriorityBackground {
		t.Fatalf("dispatched job: %s %s %v", kind, priority, err)
	}
	if worked, err = f.s.DispatchConsistencyCheck(f.ctx, time.Hour, f.policy); err != nil || worked {
		t.Fatalf("a library checked within the interval was queued again: %t %v", worked, err)
	}
}

func TestConsistencyRunRetentionKeepsJournaledRunsPostgres(t *testing.T) {
	f := newJobFixture(t)
	finish := func(mode string) string {
		run, err := f.s.StartConsistencyRun(f.ctx, domain.ConsistencyRun{Origin: domain.ConsistencyOriginCLI, Mode: mode})
		if err != nil {
			t.Fatal(err)
		}
		if mode == domain.ConsistencyModeFix {
			if _, err = f.s.Pool.Exec(f.ctx, `INSERT INTO consistency_fix_journal(run_id,fix,target,before_state,after_state) VALUES($1::uuid,'playback_sessions.source_id','{}','{}','{}')`, run.ID); err != nil {
				t.Fatal(err)
			}
		}
		err = f.s.FinishConsistencyRun(f.ctx, domain.ConsistencyReport{Schema: domain.ConsistencyReportSchema, RunID: run.ID, State: domain.ConsistencyRunCompleted, Libraries: []domain.ConsistencyLibraryReport{}, Global: []domain.ConsistencyCheckResult{}})
		if err != nil {
			t.Fatal(err)
		}
		return run.ID
	}
	journaled := finish(domain.ConsistencyModeFix)
	for range domain.ConsistencyRunRetention + 5 {
		finish(domain.ConsistencyModeReport)
	}
	var runs int
	var kept bool
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*),bool_or(id=$1::uuid) FROM consistency_runs`, journaled).Scan(&runs, &kept); err != nil {
		t.Fatal(err)
	}
	if runs != domain.ConsistencyRunRetention+1 || !kept {
		t.Fatalf("retention kept %d runs, journaled kept %t", runs, kept)
	}
	// A run nobody finished is closed by the next start.
	stale, err := f.s.StartConsistencyRun(f.ctx, domain.ConsistencyRun{Origin: domain.ConsistencyOriginCLI, Mode: domain.ConsistencyModeReport})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE consistency_runs SET started_at=started_at-interval '2 days' WHERE id=$1::uuid`, stale.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.StartConsistencyRun(f.ctx, domain.ConsistencyRun{Origin: domain.ConsistencyOriginCLI, Mode: domain.ConsistencyModeReport}); err != nil {
		t.Fatal(err)
	}
	var state string
	if err = f.s.Pool.QueryRow(f.ctx, `SELECT state FROM consistency_runs WHERE id=$1::uuid`, stale.ID).Scan(&state); err != nil || state != domain.ConsistencyRunFailed {
		t.Fatalf("abandoned run state %s %v", state, err)
	}
	if err = f.s.FinishConsistencyRun(f.ctx, domain.ConsistencyReport{Schema: domain.ConsistencyReportSchema, RunID: stale.ID, State: domain.ConsistencyRunCompleted}); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("finished an abandoned run", err)
	}
}

func TestConsistencyMigrationGuardsRetainedStatePostgres(t *testing.T) {
	f := newJobFixture(t)
	version := downgradeAboveMigration(t, f, "consistency_checks")
	if _, err := f.s.EnqueueConsistencyCheck(f.ctx, f.registration.Library.ID, domain.JobPriorityManual, f.policy); err != nil {
		t.Fatal(err)
	}
	refused := refuseRetainedDowngrade(t, f, "consistency_checks", "a retained consistency job was downgraded")
	if refused != version-1 {
		t.Fatalf("refused at %d, want %d", refused, version-1)
	}
}

func TestConsistencyMigrationEmptyRoundTripPostgres(t *testing.T) {
	f := newJobFixture(t)
	version := downgradeAboveMigration(t, f, "consistency_checks")
	dsn := f.s.Pool.Config().ConnString()
	if got, dirty, err := Migrate(f.ctx, dsn, "down"); err != nil || dirty || got != version-1 {
		t.Fatalf("empty downgrade: %d %t %v", got, dirty, err)
	}
	var kinds string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname='jobs_kind_check' AND connamespace=current_schema()::regnamespace`).Scan(&kinds); err != nil || strings.Contains(kinds, "consistency_check") {
		t.Fatalf("downgrade kept the job kind: %s %v", kinds, err)
	}
	if got, dirty, err := Migrate(f.ctx, dsn, "up"); err != nil || dirty || got != SchemaVersion {
		t.Fatalf("upgrade: %d %t %v", got, dirty, err)
	}
	var totals, buckets int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM job_metric_totals WHERE kind='consistency_check'),(SELECT count(*) FROM job_metric_buckets WHERE kind='consistency_check')`).Scan(&totals, &buckets); err != nil || totals != 2 || buckets != 52 {
		t.Fatalf("metric rows %d/%d %v", totals, buckets, err)
	}
}

func TestConsistencyOpsMetricsPostgres(t *testing.T) {
	f := newJobFixture(t)
	empty, err := f.s.OpsMetrics(f.ctx)
	if err != nil || empty != (app.OpsMetricsSnapshot{}) {
		t.Fatalf("empty snapshot: %+v %v", empty, err)
	}
	// Three trailing failed scans after a success.
	for i, state := range []string{"succeeded", "failed", "failed", "failed"} {
		code := ""
		if state == "failed" {
			code = "scan_io"
		}
		if _, err = f.s.Pool.Exec(f.ctx, `INSERT INTO jobs(library_id,idempotency_key,priority,state,error_code,queue_limit,history_limit,max_entries,max_directories,max_attempts,missing_count_limit,missing_percent_limit,started_at,finished_at)
 VALUES($1::uuid,$2,'manual',$3,$4,16,16,10000,1000,3,10,50,clock_timestamp(),clock_timestamp()+$5*interval '1 second')`, f.registration.Library.ID, fmt.Sprintf("scan-%d", i), state, code, i); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE dev_mode_state SET active=true,enabled_at=clock_timestamp()-interval '1 hour',expires_at=clock_timestamp()+interval '1 hour',source='test'`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `INSERT INTO webhooks(id,name,url,timeout_ms,max_attempts,base_delay_ms,max_delay_ms,jitter,secret_sealed) VALUES(gen_random_uuid(),'hook','https://example.invalid/',1000,1,1000,1000,0,decode(repeat('ab',40),'hex'))`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `WITH o AS (INSERT INTO webhook_outbox(event_type,occurred_at,subject_kind) SELECT 'system.alert',clock_timestamp(),'system' FROM generate_series(1,3) RETURNING id)
 INSERT INTO webhook_deliveries(outbox_id,webhook_id,state,next_attempt_at) SELECT o.id,w.id,CASE WHEN o.id=(SELECT min(id) FROM o) THEN 'pending' ELSE 'dead' END,CASE WHEN o.id=(SELECT min(id) FROM o) THEN clock_timestamp() END FROM o CROSS JOIN webhooks w`); err != nil {
		t.Fatal(err)
	}
	v, err := f.s.OpsMetrics(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if v.ScanConsecutiveFailures != 3 || v.ScanFailingLibraries != 1 || !v.DevModeActive || v.DevModeActiveSeconds < 3500 || v.WebhookDead != 2 || v.WebhookPending != 1 {
		t.Fatalf("snapshot: %+v", v)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `INSERT INTO jobs(library_id,idempotency_key,priority,state,queue_limit,history_limit,max_entries,max_directories,max_attempts,missing_count_limit,missing_percent_limit,started_at,finished_at)
 VALUES($1::uuid,'scan-ok','manual','succeeded',16,16,10000,1000,3,10,50,clock_timestamp(),clock_timestamp()+interval '1 minute')`, f.registration.Library.ID); err != nil {
		t.Fatal(err)
	}
	if v, err = f.s.OpsMetrics(f.ctx); err != nil || v.ScanConsecutiveFailures != 0 || v.ScanFailingLibraries != 0 {
		t.Fatalf("after a success: %+v %v", v, err)
	}
}

// TestConsistencyScale100000Postgres measures a run over a library of
// 100,000 items, sources and baseline files. It is opt-in
// (JELEE_CONSISTENCY_SCALE=1) because it inserts 300,000 rows.
func TestConsistencyScale100000Postgres(t *testing.T) {
	if os.Getenv("JELEE_CONSISTENCY_SCALE") != "1" {
		t.Skip("consistency scale run NOT RUN: set JELEE_CONSISTENCY_SCALE=1")
	}
	ctx, s, _ := accountTestStoreWithTimeout(t, 15*time.Minute)
	registration, err := s.RegisterLibrary(ctx, "scale", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := jobFixture{ctx: ctx, s: s, registration: registration, policy: jobTestPolicy()}
	library, root := f.registration.Library.ID, f.registration.RootID
	start := time.Now()
	if _, err := f.s.Pool.Exec(f.ctx, `WITH i AS (INSERT INTO items(library_id,title,kind) SELECT $1::uuid,'Movie '||n,'Movie' FROM generate_series(1,100000) n RETURNING id,title)
 INSERT INTO media_sources(item_id,library_id,root_id,relative_path,content_type) SELECT i.id,$1::uuid,$2::uuid,'m/'||i.title||'.mkv','video/x-matroska' FROM i`, library, root); err != nil {
		t.Fatal(err)
	}
	t.Logf("catalog fixture: %s", time.Since(start).Round(time.Millisecond))
	if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO library_inventory_baseline_data(library_id,snapshot_id,root_id,path,attributes_known,kind,size,modified_unix_nano,inventory_generation,observed_revision)
 SELECT $1::uuid,0,$2::uuid,relative_path,true,'video',1,1,1,1 FROM media_sources WHERE library_id=$1::uuid`, library, root); err != nil {
		t.Fatal(err)
	}
	// One percent of the files left the baseline; their stat probes are
	// bounded by the default budget.
	if _, err := f.s.Pool.Exec(f.ctx, `DELETE FROM library_inventory_baseline_data WHERE library_id=$1::uuid AND path LIKE 'm/Movie %00.mkv'`, library); err != nil {
		t.Fatal(err)
	}
	t.Logf("fixture: %s", time.Since(start).Round(time.Millisecond))
	c, err := app.NewConsistencyChecker(f.s, scan.ConsistencyProber{}, nil, logging.NewRedactor(logging.IPRedact, logging.PathRedact, nil), f.s)
	if err != nil {
		t.Fatal(err)
	}
	var peak uint64
	done := make(chan struct{})
	sampled := make(chan struct{})
	go func() {
		defer close(sampled)
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			var m runtimeMem
			readMem(&m)
			peak = max(peak, m.heap)
			select {
			case <-done:
				return
			case <-ticker.C:
			}
		}
	}()
	var before runtimeMem
	readMem(&before)
	start = time.Now()
	report, err := c.Run(f.ctx, app.ConsistencyOptions{Origin: domain.ConsistencyOriginCLI, Library: library, CallTimeout: 15 * time.Second})
	elapsed := time.Since(start)
	close(done)
	<-sampled
	if err != nil {
		t.Fatal(err)
	}
	orphans := consistencyResult(t, report, domain.ConsistencyOrphanItem)
	if orphans.Examined != 100000 || orphans.Findings != 1000 || orphans.Info[domain.ConsistencyInfoUnconfirmed] != 0 || len(orphans.Samples) != domain.ConsistencySamplesPerCheck {
		t.Fatalf("orphan items at scale: examined %d findings %d info %v samples %d", orphans.Examined, orphans.Findings, orphans.Info, len(orphans.Samples))
	}
	growth := int64(peak) - int64(before.heap)
	t.Logf("100000 items: run %s, peak heap %d MiB (growth %d KiB), report %d findings, stats used %d", elapsed.Round(time.Millisecond), peak>>20, growth>>10, report.Totals.Findings, report.Limits.StatsUsed)
	if elapsed > 2*time.Minute || growth > 64<<20 {
		t.Fatalf("run over 100000 items is not bounded: %s, heap growth %d bytes", elapsed, growth)
	}
}

type runtimeMem struct{ heap uint64 }

func readMem(m *runtimeMem) {
	var s runtime.MemStats
	runtime.ReadMemStats(&s)
	m.heap = s.HeapAlloc
}
