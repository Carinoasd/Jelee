//go:build jelee_probe_tests

package runtime

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	httpapi "github.com/MoYuanCN/Jelee/internal/adapter/http"
	"github.com/MoYuanCN/Jelee/internal/adapter/nfo"
	"github.com/MoYuanCN/Jelee/internal/adapter/postgres"
	"github.com/MoYuanCN/Jelee/internal/adapter/scan"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	jobworker "github.com/MoYuanCN/Jelee/internal/platform/jobs"
	"github.com/MoYuanCN/Jelee/internal/platform/password"
	"github.com/jackc/pgx/v5"
)

// The shutdown-only barrier is after a real completed file read. It creates a
// reproducible in-flight API call; it does not claim to emulate blocked OS I/O.
type acceptanceNFOReader struct {
	app.NFOReader
	block   atomic.Bool
	entered chan struct{}
	once    sync.Once
}

func (r *acceptanceNFOReader) Read(ctx context.Context, s domain.NFOSource) (app.NFOReadSource, error) {
	source, err := r.NFOReader.Read(ctx, s)
	if err == nil && r.block.Load() {
		r.once.Do(func() { close(r.entered) })
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return source, err
}

// Uses the production protected helper, real PostgreSQL and the production Fx
// lifetime hook/listener. No result or operation count comes from a fake repo.
func TestProductionNFOWorkerAcceptance(t *testing.T) {
	withFamily := os.Getenv("JELEE_FAMILY_IGNORE_ACCEPTANCE") == "true"
	withIgnore := withFamily || os.Getenv("JELEE_NFO_IGNORE_ACCEPTANCE") == "true"
	withSustained := os.Getenv("JELEE_FAMILY_IGNORE_SUSTAINED_ACCEPTANCE") == "true"
	if withSustained && !withFamily {
		t.Fatal("sustained mixed acceptance requires family mode")
	}
	if os.Getenv("JELEE_REQUIRE_NFO_WORKER") != "true" {
		t.Skip("NFO worker acceptance requires controlled production container")
	}
	if os.Getuid() != 65532 {
		t.Fatal("nonroot production profile required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	total, nfoCount, videoCount, imageCount, changedNFO, changedImages, faultCount := int64(1000), int64(400), int64(100), int64(500), int64(17), int64(23), int64(10)
	if os.Getenv("JELEE_NFO_FIXTURE_FILES") == "100" {
		total, nfoCount, videoCount, imageCount, changedNFO, changedImages, faultCount = 100, 40, 10, 50, 3, 4, 1
	}
	fmt.Printf("{\"fixtureFiles\":%d}\n", total)
	defer cancel()
	u, name, err := acceptanceDatabase()
	if err != nil {
		t.Fatal("owned dedicated database required")
	}
	admin, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatal("connect acceptance database")
	}
	defer admin.Close(context.Background())
	schema := pgx.Identifier{name}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal("create owned schema")
	}
	defer func() {
		c, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, e := admin.Exec(c, "DROP SCHEMA "+schema+" CASCADE"); e != nil {
			t.Error("clean owned schema")
		}
	}()
	query := u.Query()
	query.Set("search_path", name)
	u.RawQuery = query.Encode()
	if version, dirty, e := postgres.Migrate(ctx, u.String(), "up"); e != nil || dirty || version != postgres.SchemaVersion {
		t.Fatal("migrate owned schema")
	}
	store, err := postgres.Open(ctx, u.String(), 8)
	if err != nil {
		t.Fatal("open acceptance store")
	}
	defer store.Pool.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	startup, stopStartup := context.WithTimeout(ctx, 10*time.Second)
	probing, err := newProbeService(startup, true, store, prepareProductionProbe)
	stopStartup()
	if err != nil || probing == nil || !probing.Available() {
		t.Fatal("protected production helper unavailable")
	}
	probing.logger = logger
	defer func() {
		if probing.Close() != nil {
			t.Error("probe cleanup")
		}
	}()
	base, err := nfo.NewSummaryReader(domain.NFODefaultSourceBytes)
	if err != nil {
		t.Fatal(err)
	}
	controlled := &acceptanceNFOReader{NFOReader: base, entered: make(chan struct{})}
	validation, err := newNFOService(ctx, store, controlled)
	if err != nil || !validation.Available() {
		t.Fatal("NFO runtime unavailable")
	}
	validation.logger = logger
	values := map[string]string{"JELEE_DATABASE_URL": u.String(), "JELEE_ENABLE_ACCOUNTS": "true", "JELEE_ENABLE_JOBS": "true", "JELEE_ENABLE_PROBE": "true"}
	cfg, err := config.LoadWith(func(k string) (string, bool) { v, ok := values[k]; return v, ok })
	if err != nil {
		t.Fatal("config")
	}
	hasher, err := password.New(password.Config{MemoryKiB: password.MinMemoryKiB, Iterations: password.MinIterations, Parallelism: 1, MaxConcurrent: 1})
	if err != nil {
		t.Fatal("hasher")
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		t.Fatal("entropy")
	}
	secret := hex.EncodeToString(random[:])
	hash, err := hasher.Hash(ctx, secret)
	if err != nil {
		t.Fatal("password hash")
	}
	if _, err = store.BootstrapAdmin(ctx, domain.UserInput{Name: "nfo-admin", DisplayName: "Acceptance", Locale: "en-US", PasswordHash: hash}); err != nil {
		t.Fatal("bootstrap administrator")
	}
	accounts, err := app.NewAccounts(store, hasher, app.AccountOptions{SessionTTL: time.Hour, MaxSessions: 8, LockAfter: 5, LockFor: time.Minute})
	if err != nil {
		t.Fatal("accounts")
	}
	grant, err := accounts.Login(ctx, "nfo-admin", secret, "acceptance", "127.0.0.1")
	if err != nil {
		t.Fatal("login")
	}
	registration, err := store.RegisterLibrary(ctx, "nfo acceptance", "/media")
	if err != nil {
		t.Fatal("register readonly source")
	}
	jobs, err := app.NewJobsWithScanStages(store, cfg.Jobs.Policy(), store, app.ScanServices{Probes: store, ProbeIdentity: probing.identity, ProbeCapability: probing.Capability, NFOAdmin: store, NFOQueries: store, Images: store, NFOIdentity: validation.identity, NFOAvailable: validation.Available})
	if err != nil {
		t.Fatal("stage use cases")
	}
	var ignoring *familyIgnoreService
	if withFamily {
		ignoring, err = newFamilyIgnoreService(ctx, true, prepareProductionFamilyIgnore)
		if err != nil || !ignoring.Available() {
			t.Fatal("family helper health unavailable")
		}
		defer ignoring.Close()
		jobs, err = app.NewJobsWithScanStages(store, cfg.Jobs.Policy(), store, app.ScanServices{FamilyIgnoreAvailable: ignoring.Available, Probes: store, ProbeIdentity: probing.identity, ProbeCapability: probing.Capability, NFOAdmin: store, NFOQueries: store, Images: store, NFOIdentity: validation.identity, NFOAvailable: validation.Available})
		if err != nil {
			t.Fatal("family stage use cases")
		}
	} else if withIgnore {
		jobs, err = app.NewJobsWithScanStages(store, cfg.Jobs.Policy(), store, app.ScanServices{IgnoreAvailable: func() bool { return true }, Probes: store, ProbeIdentity: probing.identity, ProbeCapability: probing.Capability, NFOAdmin: store, NFOQueries: store, Images: store, NFOIdentity: validation.identity, NFOAvailable: validation.Available})
		if err != nil {
			t.Fatal("ignore stage use cases")
		}
	}
	handler, err := httpapi.NewWithJobs(cfg, store, app.NewCatalog(store), store, logger, accounts, jobs)
	if err != nil {
		t.Fatal("HTTP handler")
	}
	options := jobworker.DefaultOptions()
	options.PollInterval = 100 * time.Millisecond
	options.Probe = &jobworker.ProbeOptions{Repository: store, Prober: probing, LeaseDuration: 30 * time.Second, MaxConcurrent: 2, Available: probing.Available, OnRuntimeUnavailable: probing.Disable}
	options.NFO = &jobworker.NFOOptions{Repository: store, Reader: validation, MaxConcurrent: 2, Available: validation.Available, OnRuntimeUnavailable: validation.Disable}
	if withFamily {
		options.FamilyIgnore = &jobworker.FamilyIgnoreOptions{Repository: store, Scanner: scan.NewFamilyIgnoreScanner(ignoring), Available: ignoring.Available}
	} else if withIgnore {
		scanner := scan.NewIgnoreScanner()
		options.Ignore = &jobworker.IgnoreOptions{Repository: store, Scanner: scanner, Observer: scanner}
	}
	runner, err := jobworker.New(store, scan.New(), options, logger)
	if err != nil {
		t.Fatal("worker")
	}
	worker := &probeWorker{worker: runner, probe: probing, nfo: validation}
	lifetime, application, closed, address := testLifetime(t, worker, handler)
	if withFamily {
		lifetime.closeIgnore = ignoring.Close
	}
	signals := application.Wait()
	if err = application.Start(ctx); err != nil {
		t.Fatal("Fx start")
	}
	stopped := false
	defer func() {
		if !stopped {
			c, stop := context.WithTimeout(context.Background(), 10*time.Second)
			defer stop()
			if application.Stop(c) != nil {
				t.Error("Fx cleanup")
			}
		}
	}()
	client := &http.Client{Timeout: 10 * time.Second}
	defer client.CloseIdleConnections()
	request := func(method, path, body, key string) json.RawMessage {
		t.Helper()
		req, e := http.NewRequestWithContext(ctx, method, address()+path, strings.NewReader(body))
		if e != nil {
			t.Fatal("HTTP request")
		}
		req.Header.Set("Authorization", "Bearer "+grant.Token)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		response, e := client.Do(req)
		if e != nil {
			t.Fatal("HTTP transport")
		}
		defer response.Body.Close()
		raw, e := io.ReadAll(io.LimitReader(response.Body, 128<<10))
		if e != nil || response.StatusCode != 200 && response.StatusCode != 202 {
			t.Fatalf("HTTP status %d", response.StatusCode)
		}
		if strings.Contains(string(raw), "PRIVATE_FIXTURE") || strings.Contains(string(raw), "/media/") {
			t.Fatal("private source data in public JSON")
		}
		var envelope struct {
			Data json.RawMessage `json:"data"`
		}
		if json.Unmarshal(raw, &envelope) != nil {
			t.Fatal("HTTP JSON")
		}
		return envelope.Data
	}
	path := "/api/v1/libraries/" + registration.Library.ID
	var policy domain.NFOLibraryPolicy
	if json.Unmarshal(request("GET", path+"/nfo/policy", "", ""), &policy) != nil || policy.Mode != domain.NFOModeOff {
		t.Fatal("NFO not default off")
	}
	request("PUT", path+"/nfo/policy", fmt.Sprintf(`{"mode":"read-only","expectedGeneration":%d}`, policy.Generation), "enable-nfo")
	request("GET", "/readyz", "", "")
	var sustainedStarted time.Time
	var sustainedRounds int
	var baselineMemory goruntime.MemStats
	var peakHeap uint64
	var baselineGoroutines int
	for round := 0; round < 3 || withSustained && time.Since(sustainedStarted) < 5*time.Minute; round++ {
		before := validation.Stats()
		beforeProbe := probing.probeCalls.Load()
		beforeProcess := probing.processStats()
		started := time.Now()
		var job domain.Job
		body := `{"nfo":true,"probe":true}`
		if withIgnore {
			body = `{"nfo":true,"probe":true,"ignore":{"mode":"jeleeignore","caseMode":"sensitive"}}`
			if withFamily {
				body = `{"nfo":true,"probe":true,"ignore":{"mode":"jeleeignore-legacy-v1","caseMode":"sensitive"}}`
			}
		}
		if json.Unmarshal(request("POST", path+"/scan", body, fmt.Sprintf("mixed-%d", round)), &job) != nil || !domain.ValidID(job.ID) {
			t.Fatal("job admission")
		}
		for {
			if json.Unmarshal(request("GET", "/api/v1/jobs/"+job.ID, "", ""), &job) != nil {
				t.Fatal("job response")
			}
			if job.State == domain.JobSucceeded {
				break
			}
			if job.State == domain.JobFailed || job.State == domain.JobCancelled {
				t.Fatalf("job terminated %s %s", job.State, job.ErrorCode)
			}
			select {
			case <-ctx.Done():
				t.Fatal("job timeout")
			case <-time.After(100 * time.Millisecond):
			}
		}
		var summary domain.NFOJobSummary
		if withIgnore {
			var report domain.IgnoreReport
			if json.Unmarshal(request("GET", "/api/v1/jobs/"+job.ID+"/ignore", "", ""), &report) != nil || report.ExcludedFiles != 3 || len(report.Entries) != 3 || report.ReviewRequired || report.Invalidated {
				t.Fatal("mixed ignore report differs")
			}
			for _, entry := range report.Entries {
				if withFamily {
					wantFamily := domain.IgnoreFamilyLegacy
					if entry.Path == "ignored-video.mp4" {
						wantFamily = domain.IgnoreFamilyCustom
					}
					if entry.Family != wantFamily || entry.Reason != domain.IgnoreReasonRule {
						t.Fatal("mixed family provenance differs")
					}
				}
				if entry.Source != "scan" || !strings.HasPrefix(entry.Path, "ignored-") || entry.RuleDirectory != "." || entry.RuleLine != 1 || entry.MatchedPath != entry.Path {
					t.Fatal("mixed ignore provenance differs")
				}
			}
			var leaked int
			if store.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM nfo_cache WHERE relative_path LIKE 'ignored-%')+(SELECT count(*) FROM probe_cache WHERE relative_path LIKE 'ignored-%')+(SELECT count(*) FROM library_inventory_baseline WHERE path LIKE 'ignored-%')`).Scan(&leaked) != nil || leaked != 0 {
				t.Fatal("excluded media reached metadata or baseline")
			}
		}
		var probe domain.ProbeJobSummary
		var images domain.ImageJobSummary
		if json.Unmarshal(request("GET", "/api/v1/jobs/"+job.ID+"/nfo", "", ""), &summary) != nil || json.Unmarshal(request("GET", "/api/v1/jobs/"+job.ID+"/probe", "", ""), &probe) != nil || json.Unmarshal(request("GET", "/api/v1/jobs/"+job.ID+"/images", "", ""), &images) != nil {
			t.Fatal("phase summaries")
		}
		stats := validation.Stats()
		proc := probing.processStats()
		assertionRound := round
		if assertionRound > 2 {
			assertionRound = 1
		}
		wantParse := []uint64{uint64(nfoCount), 0, uint64(changedNFO)}[assertionRound]
		wantHit := []int64{0, nfoCount - 2*faultCount, nfoCount - 2*faultCount - changedNFO}[assertionRound]
		wantNegative := []int64{0, 2 * faultCount, 2 * faultCount}[assertionRound]
		wantProbe := []uint64{uint64(videoCount), 0, 0}[assertionRound]
		if stats.ReadCalls-before.ReadCalls != uint64(2*nfoCount) || stats.CompletedReads-before.CompletedReads != uint64(2*nfoCount) || stats.HashCompletions-before.HashCompletions != uint64(2*nfoCount) || stats.ParseCalls-before.ParseCalls != wantParse || stats.ActiveCalls != 0 || stats.PeakCalls < 1 || stats.PeakCalls > 2 {
			t.Fatalf("reader actual counters differ in round %d: %+v", round+1, stats)
		}
		if summary.Phase != domain.NFOSummaryDone || summary.Processed != nfoCount || summary.Parsed != int64(wantParse) || summary.Hits != wantHit || summary.NegativeHits != wantNegative || summary.Valid != nfoCount-2*faultCount || summary.Invalid != 2*faultCount || summary.WarningFiles != faultCount || summary.Changed+summary.Unavailable+summary.Rejected != 0 {
			t.Fatalf("NFO committed progress differs: %+v", summary)
		}
		if probing.probeCalls.Load()-beforeProbe != wantProbe || proc.Started-beforeProcess.Started != wantProbe || proc.Active != 0 || proc.Peak > 2 || probe.Processed != videoCount || probe.Succeeded != int64(wantProbe) || probe.Hits != videoCount-int64(wantProbe) || probe.Failed+probe.Changed+probe.Unavailable+probe.NegativeHits != 0 {
			t.Fatal("probe actual starts/progress differs")
		}
		wantImages := []domain.ImageProgress{{Added: imageCount, ComparisonComplete: true}, {Unchanged: imageCount, ComparisonComplete: true}, {Changed: changedImages, Unchanged: imageCount - changedImages, ComparisonComplete: true}}[assertionRound]
		if images.ImageProgress != wantImages {
			t.Fatalf("image attributes differ: %+v", images.ImageProgress)
		}
		var rows, bytes, actualRows, actualBytes, invalid, leases int64
		if err = store.Pool.QueryRow(ctx, `SELECT rows_used,bytes_used,(SELECT count(*) FROM nfo_cache),(SELECT COALESCE(sum(charge_bytes),0) FROM nfo_cache),(SELECT count(*) FROM nfo_cache WHERE status='invalid' AND summary->>'failureCode'='nfo_invalid_xml'),(SELECT active_leases FROM probe_cache_quota) FROM nfo_cache_quota`).Scan(&rows, &bytes, &actualRows, &actualBytes, &invalid, &leases); err != nil || rows != nfoCount || actualRows != nfoCount || bytes != actualBytes || invalid != faultCount || leases != 0 {
			t.Fatal("persisted summary/quota/negative XML assertions failed")
		}
		records, e := store.Pool.Query(ctx, `SELECT relative_path,encode(source_sha256,'hex'),summary FROM nfo_cache`)
		if e != nil {
			t.Fatal("cache verification query")
		}
		checked := int64(0)
		semantic, multiple, warning := int64(0), int64(0), int64(0)
		encodings := map[string]bool{}
		var sourceBytes uint64
		for records.Next() {
			var name, digest string
			var raw []byte
			if records.Scan(&name, &digest, &raw) != nil {
				records.Close()
				t.Fatal("cache verification row")
			}
			content, e := os.ReadFile(filepath.Join("/media", name))
			sum := sha256.Sum256(content)
			var s domain.NFOValidationSummary
			if e != nil || hex.EncodeToString(sum[:]) != digest || json.Unmarshal(raw, &s) != nil || domain.ValidateNFOSummary(s) != nil {
				records.Close()
				t.Fatal("stored summary/full hash differs from source")
			}
			sourceBytes += uint64(len(content))
			if s.Status == domain.NFOStatusInvalid && s.FailureCode == "" {
				semantic++
			}
			if s.Entries == 2 {
				multiple++
			}
			if s.WarningCount > 0 {
				warning++
			}
			encodings[s.Encoding] = true
			checked++
		}
		e = records.Err()
		records.Close()
		if e != nil || checked != nfoCount || stats.CompletedReadBytes-before.CompletedReadBytes != 2*sourceBytes || semantic != faultCount || multiple != faultCount || warning != faultCount || !encodings["UTF-8"] || !encodings["UTF-16LE"] || !encodings["UTF-16BE"] || !encodings["GBK"] {
			t.Fatal("full-read count/bytes mismatch")
		}
		request("GET", path+"/nfo/current-validations?limit=25", "", "")
		record, _ := json.Marshal(map[string]any{"round": round + 1, "sustained": withSustained && round >= 3, "elapsedMillis": time.Since(started).Milliseconds(), "readCalls": stats.ReadCalls - before.ReadCalls, "fullHashes": stats.HashCompletions - before.HashCompletions, "completedReadBytes": stats.CompletedReadBytes - before.CompletedReadBytes, "parseCalls": stats.ParseCalls - before.ParseCalls, "activeNFOCalls": stats.ActiveCalls, "peakNFOCalls": stats.PeakCalls, "metadataProberCalls": probing.probeCalls.Load() - beforeProbe, "metadataChildStarts": proc.Started - beforeProcess.Started, "activeChildLifecycles": proc.Active, "peakChildLifecycles": proc.Peak, "nfo": summary, "images": images, "cacheRows": rows, "cacheBytes": bytes, "persistedInvalidXML": invalid})
		fmt.Println(string(record))
		if round == 1 {
			fmt.Println(`{"readyForReplacement":true}`)
			for {
				if _, e := os.Stat("/control/continue"); e == nil {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("replacement timeout")
				case <-time.After(100 * time.Millisecond):
				}
			}
		}
		if withSustained && round == 2 {
			goruntime.GC()
			goruntime.ReadMemStats(&baselineMemory)
			peakHeap = baselineMemory.HeapAlloc
			baselineGoroutines = goruntime.NumGoroutine()
			sustainedStarted = time.Now()
		} else if withSustained && round >= 3 {
			sustainedRounds++
			var sample goruntime.MemStats
			goruntime.ReadMemStats(&sample)
			if sample.HeapAlloc > peakHeap {
				peakHeap = sample.HeapAlloc
			}
			if peakHeap > 256<<20 {
				t.Fatal("mixed parent Go heap exceeded 256MiB sample bound", peakHeap)
			}
		}
	}
	if withSustained {
		elapsed := time.Since(sustainedStarted)
		goruntime.GC()
		var finalMemory goruntime.MemStats
		goruntime.ReadMemStats(&finalMemory)
		finalGoroutines := goruntime.NumGoroutine()
		if elapsed < 5*time.Minute || sustainedRounds < 5 || finalMemory.HeapAlloc > baselineMemory.HeapAlloc+(64<<20) || finalGoroutines > baselineGoroutines+8 {
			t.Fatal("mixed sustained coverage or parent resource bound failed", elapsed, sustainedRounds, baselineMemory.HeapAlloc, finalMemory.HeapAlloc, baselineGoroutines, finalGoroutines)
		}
		record, _ := json.Marshal(map[string]any{"sustainedAcceptance": "passed", "seconds": elapsed.Seconds(), "rounds": sustainedRounds, "baselineHeap": baselineMemory.HeapAlloc, "finalHeap": finalMemory.HeapAlloc, "peakSampleHeap": peakHeap, "baselineGoroutines": baselineGoroutines, "finalGoroutines": finalGoroutines})
		fmt.Println(string(record))
	}
	// Cancellation is observed after actual directory inventory and a real NFO
	// read. The previous successful image baseline must survive the failed round.
	probing.Disable()
	controlled.block.Store(true)
	baseline := func() string {
		t.Helper()
		var value string
		if store.Pool.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(b) ORDER BY root_id,path),'[]'::jsonb)::text FROM library_inventory_baseline b WHERE library_id=$1::uuid`, registration.Library.ID).Scan(&value) != nil {
			t.Fatal("read accepted baseline")
		}
		return value
	}
	acceptedBaseline := baseline()
	nfoPath, nfoBody := path+"/nfo/validate", `{}`
	if withIgnore {
		nfoPath, nfoBody = path+"/scan", `{"nfo":true,"ignore":{"mode":"jeleeignore","caseMode":"sensitive"}}`
		if withFamily {
			nfoBody = `{"nfo":true,"ignore":{"mode":"jeleeignore-legacy-v1","caseMode":"sensitive"}}`
		}
	}
	var cancelled domain.Job
	if json.Unmarshal(request("POST", nfoPath, nfoBody, "cancel-nfo"), &cancelled) != nil {
		t.Fatal("cancel-round admission")
	}
	select {
	case <-controlled.entered:
	case <-ctx.Done():
		t.Fatal("cancel-round read not reached")
	}
	request("POST", "/api/v1/jobs/"+cancelled.ID+"/cancel", `{}`, "")
	for {
		if json.Unmarshal(request("GET", "/api/v1/jobs/"+cancelled.ID, "", ""), &cancelled) != nil {
			t.Fatal("cancel-round status")
		}
		if cancelled.State == domain.JobCancelled {
			break
		}
		if cancelled.State == domain.JobSucceeded || cancelled.State == domain.JobFailed {
			t.Fatal("cancel round did not cancel")
		}
		select {
		case <-ctx.Done():
			t.Fatal("cancel-round timeout")
		case <-time.After(100 * time.Millisecond):
		}
	}
	var partial domain.ImageJobSummary
	if json.Unmarshal(request("GET", "/api/v1/jobs/"+cancelled.ID+"/images", "", ""), &partial) != nil || partial.Missing != 0 || partial.ComparisonComplete || baseline() != acceptedBaseline || validation.Stats().ActiveCalls != 0 {
		t.Fatal("cancelled image comparison published missing/baseline or retained reader")
	}
	controlled.block.Store(false)
	beforeRecovery := validation.Stats()
	beforeRecoveryProcess := probing.processStats()
	var recovered domain.Job
	if json.Unmarshal(request("POST", nfoPath, nfoBody, "recover-nfo"), &recovered) != nil {
		t.Fatal("recovery admission")
	}
	for {
		if json.Unmarshal(request("GET", "/api/v1/jobs/"+recovered.ID, "", ""), &recovered) != nil {
			t.Fatal("recovery status")
		}
		if recovered.State == domain.JobSucceeded {
			break
		}
		if recovered.State == domain.JobFailed || recovered.State == domain.JobCancelled {
			t.Fatal("recovery did not complete")
		}
		select {
		case <-ctx.Done():
			t.Fatal("recovery timeout")
		case <-time.After(100 * time.Millisecond):
		}
	}
	var restored domain.ImageJobSummary
	if json.Unmarshal(request("GET", "/api/v1/jobs/"+recovered.ID+"/images", "", ""), &restored) != nil || restored.ImageProgress != (domain.ImageProgress{Unchanged: imageCount, ComparisonComplete: true}) {
		t.Fatal("image comparison did not recover")
	}
	afterRecovery := validation.Stats()
	if afterRecovery.ParseCalls != beforeRecovery.ParseCalls || afterRecovery.ReadCalls-beforeRecovery.ReadCalls != uint64(2*nfoCount) || afterRecovery.HashCompletions-beforeRecovery.HashCompletions != uint64(2*nfoCount) || afterRecovery.ActiveCalls != 0 || probing.processStats().Started != beforeRecoveryProcess.Started {
		t.Fatal("recovery reads/cache/probe counts differ")
	}
	fmt.Println(`{"cancellationRecovery":"passed","cancelledMissing":0,"cancelledComparisonComplete":false,"baselineUnchanged":true,"recoveredComparisonComplete":true,"recoveryParses":0,"recoveryChildStarts":0}`)
	// No read call is active now; rearm the test-only barrier for shutdown.
	controlled.entered = make(chan struct{})
	controlled.once = sync.Once{}
	controlled.block.Store(true)
	// A final NFO-only request proves capability independence and lifecycle join.
	var inFlight domain.Job
	if json.Unmarshal(request("POST", nfoPath, nfoBody, "shutdown-nfo"), &inFlight) != nil {
		t.Fatal("NFO-only admission while probe disabled")
	}
	select {
	case <-controlled.entered:
	case <-ctx.Done():
		t.Fatal("in-flight read not reached")
	}
	if validation.Stats().ActiveCalls != 1 {
		t.Fatal("expected in-flight NFO call")
	}
	fmt.Println(`{"readyForSIGTERM":true}`)
	select {
	case sig := <-signals:
		if sig.Signal != syscall.SIGTERM {
			t.Fatal("unexpected shutdown signal")
		}
	case <-ctx.Done():
		t.Fatal("SIGTERM not received")
	}
	stopCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	err = application.Stop(stopCtx)
	stop()
	stopped = true
	if err != nil || closed.Load() != 1 || validation.Stats().ActiveCalls != 0 || probing.processStats().Active != 0 {
		t.Fatal("Fx stop did not join all active work")
	}
	var activeJobs, activeLeases int64
	if err = store.Pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE owner IS NOT NULL),(SELECT active_leases FROM probe_cache_quota) FROM jobs`).Scan(&activeJobs, &activeLeases); err != nil || activeJobs != 0 || activeLeases != 0 {
		t.Fatal("shutdown retained active lease")
	}
	response, e := client.Get(address() + "/readyz")
	if e == nil {
		response.Body.Close()
		t.Fatal("HTTP listener remained open")
	}
	fmt.Println(`{"shutdown":"passed","signal":"SIGTERM","httpClosed":true,"activeNFOCalls":0,"activeChildLifecycles":0,"activeLeases":0,"readBarrier":"after a real read; cancelled API joined"}`)
}
