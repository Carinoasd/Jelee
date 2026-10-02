//go:build jelee_probe_tests

package runtime

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/postgres"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/password"
	"github.com/jackc/pgx/v5"
)

const scanMemoryFixture = "Jelee synthetic inventory fixture\n"

type scanMemoryAcceptanceReport struct {
	Version                  int                      `json:"version"`
	Result                   string                   `json:"result"`
	ErrorCode                string                   `json:"errorCode"`
	FixtureFiles             int64                    `json:"fixtureFiles"`
	FixtureBytes             int64                    `json:"fixtureBytes"`
	Configuration            scanMemoryConfiguration  `json:"configuration"`
	Scan                     scanMemoryScanResult     `json:"scan"`
	OriginalSamplesUnchanged bool                     `json:"originalSamplesUnchanged"`
	Shutdown                 scanMemoryShutdownResult `json:"shutdown"`
	MemoryProfile            scanMemoryReport         `json:"memoryProfile"`
}

type scanMemoryConfiguration struct {
	Workers    int    `json:"workers"`
	MaxEntries int    `json:"maxEntries"`
	Probe      bool   `json:"probe"`
	NFOMode    string `json:"nfoMode"`
	IgnoreMode string `json:"ignoreMode"`
	GOMAXPROCS int    `json:"gomaxprocs"`
}

type scanMemoryScanResult struct {
	State              string `json:"state"`
	Attempts           int    `json:"attempts"`
	Files              int64  `json:"files"`
	Bytes              int64  `json:"bytes"`
	Skipped            int64  `json:"skipped"`
	Missing            int64  `json:"missing"`
	ReviewRequired     bool   `json:"reviewRequired"`
	StartedNanos       int64  `json:"startedNanos"`
	FinishedNanos      int64  `json:"finishedNanos"`
	ElapsedNanos       int64  `json:"elapsedNanos"`
	InventoryRows      int64  `json:"inventoryRows"`
	BaselineRows       int64  `json:"baselineRows"`
	RootCount          int64  `json:"rootCount"`
	DoneDirectories    int64  `json:"doneDirectories"`
	PendingDirectories int64  `json:"pendingDirectories"`
}

type scanMemoryShutdownResult struct {
	Signal            string `json:"signal"`
	HTTPClosed        bool   `json:"httpClosed"`
	LifetimeCancelled bool   `json:"lifetimeCancelled"`
	ActiveLeases      int64  `json:"activeLeases"`
	PoolConnections   int64  `json:"poolConnections"`
	Result            string `json:"result"`
}

func TestScanMemoryAcceptance(t *testing.T) {
	if os.Getenv("JELEE_SCAN_MEMORY_ACCEPTANCE") != "true" {
		t.Skip("scan memory acceptance requires its controlled container")
	}
	report := scanMemoryAcceptanceReport{Version: 1, Result: "failed"}
	var profile *scanMemoryProfile
	defer func() {
		if profile != nil {
			var err error
			report.MemoryProfile, err = profile.finish()
			if err != nil && report.ErrorCode == "" {
				report.ErrorCode = "memory_profile_failed"
			}
		}
		if report.ErrorCode == "" && !t.Failed() {
			report.Result = "passed"
		} else {
			t.Error("scan memory acceptance failed", report.ErrorCode)
		}
		body, err := json.Marshal(map[string]any{"scanMemoryAcceptance": report})
		if err != nil || len(body) > 2<<20 {
			t.Error("scan memory report exceeded its encoding boundary")
			fmt.Println(`{"scanMemoryAcceptance":{"version":1,"result":"failed","errorCode":"report_encoding_failed"}}`)
			return
		}
		fmt.Println(string(body))
	}()
	count, err := strconv.ParseInt(os.Getenv("JELEE_SCAN_MEMORY_FILES"), 10, 64)
	if err != nil || count != 1000 && count != 500000 || goruntime.GOOS != "linux" || os.Getuid() != 65532 {
		report.ErrorCode = "invalid_acceptance_environment"
		return
	}
	report.FixtureFiles, report.FixtureBytes = count, count*int64(len(scanMemoryFixture))
	profile, err = startScanMemoryProfile()
	if err != nil {
		report.ErrorCode = "memory_profile_start_failed"
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Minute)
	defer cancel()
	report.ErrorCode = runScanMemoryAcceptance(ctx, profile, &report)
}

func runScanMemoryAcceptance(ctx context.Context, profile *scanMemoryProfile, report *scanMemoryAcceptanceReport) (failure string) {
	setCleanupFailure := func(code string) {
		if failure == "" {
			failure = code
		}
	}
	u, schemaName, err := acceptanceDatabase()
	if err != nil {
		return "owned_database_required"
	}
	admin, err := pgx.Connect(ctx, u.String())
	if err != nil {
		return "database_connection_failed"
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if admin.Close(closeCtx) != nil {
			setCleanupFailure("database_close_failed")
		}
	}()
	quotedSchema := pgx.Identifier{schemaName}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		return "owned_schema_creation_failed"
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if _, err := admin.Exec(cleanup, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			setCleanupFailure("owned_schema_cleanup_failed")
		}
	}()
	query := u.Query()
	query.Set("search_path", schemaName)
	query.Set("application_name", schemaName+"_observer")
	u.RawQuery = query.Encode()
	if version, dirty, err := postgres.Migrate(ctx, u.String(), "up"); err != nil || dirty || version != postgres.SchemaVersion {
		return "owned_schema_migration_failed"
	}
	store, err := postgres.Open(ctx, u.String(), 4)
	if err != nil {
		return "observer_store_failed"
	}
	defer store.Pool.Close()
	if err := profile.changePhase("login"); err != nil {
		return "memory_phase_failed"
	}
	applicationName := schemaName + "_runtime"
	query.Set("application_name", applicationName)
	u.RawQuery = query.Encode()
	values := map[string]string{
		"JELEE_DATABASE_URL": u.String(), "JELEE_ENABLE_ACCOUNTS": "true", "JELEE_ENABLE_JOBS": "true",
		"JELEE_ENABLE_PROBE": "false", "JELEE_ENABLE_FAMILY_IGNORE": "false", "JELEE_JOB_WORKERS": "1",
		"JELEE_SCAN_MAX_ENTRIES": "500000", "JELEE_MAX_CONNECTIONS": "8",
	}
	cfg, err := config.LoadWith(func(key string) (string, bool) { value, ok := values[key]; return value, ok })
	if err != nil || !cfg.EnableAccounts || !cfg.EnableJobs || cfg.EnableProbe || cfg.EnableFamilyIgnore || cfg.Jobs.Workers != 1 || cfg.Jobs.MaxEntries != 500000 || goruntime.GOMAXPROCS(0) != 2 {
		return "runtime_configuration_failed"
	}
	report.Configuration = scanMemoryConfiguration{Workers: cfg.Jobs.Workers, MaxEntries: cfg.Jobs.MaxEntries, Probe: cfg.EnableProbe, GOMAXPROCS: goruntime.GOMAXPROCS(0)}
	p := cfg.Accounts
	hasher, err := password.New(password.Config{MemoryKiB: uint32(p.PasswordMemoryKiB), Iterations: uint32(p.PasswordIterations), Parallelism: uint8(p.PasswordParallelism), MaxConcurrent: p.PasswordConcurrency})
	if err != nil {
		return "password_configuration_failed"
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "account_entropy_failed"
	}
	secret := hex.EncodeToString(random[:])
	hash, err := hasher.Hash(ctx, secret)
	if err != nil {
		return "account_hash_failed"
	}
	if _, err := store.BootstrapAdmin(ctx, domain.UserInput{Name: "scan-memory-admin", DisplayName: "Scan acceptance", Locale: "en-US", PasswordHash: hash}); err != nil {
		return "account_bootstrap_failed"
	}
	registration, err := store.RegisterLibrary(ctx, "scan memory acceptance", "/media")
	if err != nil {
		return "fixture_registration_failed"
	}
	samples, err := snapshotScanMemoryFixtures(report.FixtureFiles)
	if err != nil {
		return "fixture_samples_failed"
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	life := newLifetime(logger)
	address := ""
	life.listen = func(listenCtx context.Context, network, _ string) (net.Listener, error) {
		listener, err := (&net.ListenConfig{}).Listen(listenCtx, network, "127.0.0.1:0")
		if err == nil {
			address = "http://" + listener.Addr().String()
		}
		return listener, err
	}
	application := newWithLifetime(cfg, logger, life)
	if application.Err() != nil {
		return "runtime_build_failed"
	}
	stopped := false
	defer func() {
		if !stopped {
			stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if application.Stop(stopCtx) != nil {
				setCleanupFailure("runtime_cleanup_failed")
			}
		}
	}()
	signals := application.Wait()
	startup, cancelStartup := context.WithTimeout(ctx, 20*time.Second)
	err = application.Start(startup)
	cancelStartup()
	if err != nil || address == "" || life.worker == nil {
		return "runtime_start_failed"
	}
	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	login, _ := json.Marshal(map[string]string{"name": "scan-memory-admin", "password": secret, "deviceName": "scan acceptance"})
	var grant domain.SessionGrant
	if err := scanMemoryRequest(ctx, client, address, "POST", "/api/v1/auth/login", string(login), "", "", http.StatusOK, &grant); err != nil || grant.Token == "" || !grant.User.Admin {
		return "http_login_failed"
	}
	libraryPath := "/api/v1/libraries/" + registration.Library.ID
	var policy domain.NFOLibraryPolicy
	if err := scanMemoryRequest(ctx, client, address, "GET", libraryPath+"/nfo/policy", "", grant.Token, "", http.StatusOK, &policy); err != nil || policy.Mode != domain.NFOModeOff {
		return "nfo_policy_not_off"
	}
	report.Configuration.NFOMode = policy.Mode
	if err := profile.changePhase("scan"); err != nil {
		return "memory_phase_failed"
	}
	report.Scan.StartedNanos = time.Since(profile.started).Nanoseconds()
	var job domain.Job
	if err := scanMemoryRequest(ctx, client, address, "POST", libraryPath+"/scan", `{"priority":"manual","probe":false,"nfo":false}`, grant.Token, "scan-memory-once", http.StatusAccepted, &job); err != nil || !domain.ValidID(job.ID) {
		return "scan_admission_failed"
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := scanMemoryRequest(ctx, client, address, "GET", "/api/v1/jobs/"+job.ID, "", grant.Token, "", http.StatusOK, &job); err != nil {
			return "scan_status_failed"
		}
		if job.State == domain.JobSucceeded || job.State == domain.JobFailed || job.State == domain.JobCancelled {
			break
		}
		select {
		case <-ctx.Done():
			return "scan_deadline_exceeded"
		case <-ticker.C:
		}
	}
	report.Scan.FinishedNanos = time.Since(profile.started).Nanoseconds()
	report.Scan.ElapsedNanos = report.Scan.FinishedNanos - report.Scan.StartedNanos
	report.Scan.State, report.Scan.Attempts = job.State, job.Attempts
	report.Scan.Files, report.Scan.Bytes = job.Files, job.Bytes
	report.Scan.Skipped, report.Scan.Missing, report.Scan.ReviewRequired = job.Skipped, job.Missing, job.ReviewRequired
	if job.State != domain.JobSucceeded || job.Attempts != 1 || job.Files != report.FixtureFiles || job.Bytes != report.FixtureBytes || job.Skipped != 0 || job.Missing != 0 || job.ReviewRequired || job.ErrorCode != "" {
		return "scan_result_mismatch"
	}
	if code := verifyScanMemoryInventory(ctx, store, registration, job, report); code != "" {
		return code
	}
	if err := verifyScanMemoryFixtures(samples); err != nil {
		return "original_fixture_changed"
	}
	report.OriginalSamplesUnchanged = true
	fmt.Println(`{"scanMemoryReadyForSIGTERM":true}`)
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	select {
	case signal := <-signals:
		if signal.Signal != syscall.SIGTERM {
			return "unexpected_shutdown_signal"
		}
		report.Shutdown.Signal = "SIGTERM"
	case <-timer.C:
		return "shutdown_signal_timeout"
	case <-ctx.Done():
		return "shutdown_context_cancelled"
	}
	if err := profile.changePhase("shutdown"); err != nil {
		return "memory_phase_failed"
	}
	stopCtx, cancelStop := context.WithTimeout(context.Background(), 15*time.Second)
	err = application.Stop(stopCtx)
	cancelStop()
	stopped = true
	if err != nil {
		return "runtime_stop_failed"
	}
	select {
	case <-life.stopped:
	default:
		return "runtime_join_incomplete"
	}
	report.Shutdown.LifetimeCancelled = life.ctx.Err() != nil
	if !report.Shutdown.LifetimeCancelled {
		return "runtime_lifetime_not_cancelled"
	}
	response, requestErr := client.Get(address + "/readyz")
	if requestErr == nil {
		response.Body.Close()
		return "http_listener_not_closed"
	}
	report.Shutdown.HTTPClosed = true
	if err := store.Pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE owner IS NOT NULL OR lease_until IS NOT NULL`).Scan(&report.Shutdown.ActiveLeases); err != nil || report.Shutdown.ActiveLeases != 0 {
		return "job_lease_cleanup_failed"
	}
	closedCtx, cancelClosed := context.WithTimeout(ctx, 5*time.Second)
	defer cancelClosed()
	for {
		if err := admin.QueryRow(closedCtx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND application_name=$1`, applicationName).Scan(&report.Shutdown.PoolConnections); err != nil {
			return "runtime_pool_observation_failed"
		}
		if report.Shutdown.PoolConnections == 0 {
			break
		}
		select {
		case <-closedCtx.Done():
			return "runtime_pool_not_closed"
		case <-ticker.C:
		}
	}
	if err := profile.changePhase("stopped"); err != nil {
		return "memory_phase_failed"
	}
	// Finish at the stopped runtime boundary. Repeated finish in the outer
	// defer returns this same report; schema removal is outside the GC window.
	if report.MemoryProfile, err = profile.finish(); err != nil {
		return "memory_profile_failed"
	}
	report.Shutdown.Result = "passed"
	return ""
}

func scanMemoryRequest(ctx context.Context, client *http.Client, address, method, path, body, token, key string, status int, result any) error {
	invalid := errors.New("scan acceptance HTTP request failed")
	request, err := http.NewRequestWithContext(ctx, method, address+path, strings.NewReader(body))
	if err != nil {
		return invalid
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	response, err := client.Do(request)
	if err != nil {
		return invalid
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (128<<10)+1))
	if err != nil || len(data) > 128<<10 || response.StatusCode != status {
		return invalid
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(data, &envelope) != nil || len(envelope.Data) == 0 || json.Unmarshal(envelope.Data, result) != nil {
		return invalid
	}
	return nil
}

type scanMemoryFixtureSnapshot struct {
	path string
	info os.FileInfo
}

func snapshotScanMemoryFixtures(total int64) ([]scanMemoryFixtureSnapshot, error) {
	result := make([]scanMemoryFixtureSnapshot, 0, 3)
	for _, index := range []int64{0, total / 2, total - 1} {
		path := filepath.Join("/media", fmt.Sprintf("dir-%04d", index/1000), fmt.Sprintf("video-%06d.mkv", index))
		info, err := readScanMemoryFixture(path)
		if err != nil {
			return nil, err
		}
		result = append(result, scanMemoryFixtureSnapshot{path: path, info: info})
	}
	return result, nil
}

func readScanMemoryFixture(path string) (os.FileInfo, error) {
	invalid := errors.New("scan fixture sample unavailable")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != int64(len(scanMemoryFixture)) {
		return nil, invalid
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, invalid
	}
	opened, statErr := file.Stat()
	data, readErr := io.ReadAll(io.LimitReader(file, int64(len(scanMemoryFixture)+1)))
	closeErr := file.Close()
	if statErr != nil || !os.SameFile(info, opened) || readErr != nil || closeErr != nil || !bytes.Equal(data, []byte(scanMemoryFixture)) {
		return nil, invalid
	}
	return info, nil
}

func verifyScanMemoryFixtures(samples []scanMemoryFixtureSnapshot) error {
	for _, before := range samples {
		after, err := readScanMemoryFixture(before.path)
		if err != nil || !os.SameFile(before.info, after) || before.info.Size() != after.Size() || !before.info.ModTime().Equal(after.ModTime()) || before.info.Mode() != after.Mode() {
			return errors.New("scan fixture sample changed")
		}
	}
	return nil
}

func verifyScanMemoryInventory(ctx context.Context, store *postgres.Store, registration domain.LibraryRegistration, job domain.Job, report *scanMemoryAcceptanceReport) string {
	queryCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var inventoryBytes, baselineBytes, invalidRows int64
	if err := store.Pool.QueryRow(queryCtx, `SELECT count(*),COALESCE(sum(size),0),count(*) FILTER(WHERE root_id<>$2::uuid OR kind<>'video' OR size<>$3) FROM job_inventory WHERE job_id=$1::uuid`, job.ID, registration.RootID, len(scanMemoryFixture)).Scan(&report.Scan.InventoryRows, &inventoryBytes, &invalidRows); err != nil || report.Scan.InventoryRows != report.FixtureFiles || inventoryBytes != report.FixtureBytes || invalidRows != 0 {
		return "inventory_rows_mismatch"
	}
	if err := store.Pool.QueryRow(queryCtx, `SELECT count(*),COALESCE(sum(size),0),count(*) FILTER(WHERE root_id<>$2::uuid OR NOT attributes_known OR kind<>'video' OR size<>$3) FROM library_inventory_baseline WHERE library_id=$1::uuid`, registration.Library.ID, registration.RootID, len(scanMemoryFixture)).Scan(&report.Scan.BaselineRows, &baselineBytes, &invalidRows); err != nil || report.Scan.BaselineRows != report.FixtureFiles || baselineBytes != report.FixtureBytes || invalidRows != 0 {
		return "baseline_rows_mismatch"
	}
	if err := store.Pool.QueryRow(queryCtx, `SELECT count(*) FROM library_roots WHERE library_id=$1::uuid`, registration.Library.ID).Scan(&report.Scan.RootCount); err != nil || report.Scan.RootCount != 1 {
		return "root_count_mismatch"
	}
	if err := store.Pool.QueryRow(queryCtx, `SELECT count(*) FILTER(WHERE done),count(*) FILTER(WHERE NOT done),count(*) FILTER(WHERE root_id<>$2::uuid) FROM job_directories WHERE job_id=$1::uuid`, job.ID, registration.RootID).Scan(&report.Scan.DoneDirectories, &report.Scan.PendingDirectories, &invalidRows); err != nil || report.Scan.DoneDirectories != (report.FixtureFiles+999)/1000+1 || report.Scan.PendingDirectories != 0 || invalidRows != 0 {
		return "scan_frontier_incomplete"
	}
	var published, ignoreRequested bool
	var optionalWork int64
	var effectiveNFO string
	if err := store.Pool.QueryRow(queryCtx, `SELECT EXISTS(SELECT 1 FROM inventory_snapshot_preparations p JOIN libraries l ON l.id=p.library_id WHERE p.job_id=$1::uuid AND p.ready AND p.cleaned AND p.copied=$2 AND p.source_files=$2 AND l.active_inventory_snapshot=p.snapshot_id),j.ignore_requested,l.nfo_mode,
	(SELECT count(*) FROM probe_requests WHERE job_id=$1::uuid)+(SELECT count(*) FROM probe_job_state WHERE job_id=$1::uuid)+(SELECT count(*) FROM nfo_job_requests WHERE job_id=$1::uuid AND (requested OR mode<>'off'))+(SELECT count(*) FROM nfo_job_state WHERE job_id=$1::uuid AND mode<>'off')+(SELECT count(*) FROM job_ignore_requests WHERE job_id=$1::uuid)
	FROM jobs j JOIN libraries l ON l.id=j.library_id WHERE j.id=$1::uuid`, job.ID, report.FixtureFiles).Scan(&published, &ignoreRequested, &effectiveNFO, &optionalWork); err != nil || !published || ignoreRequested || effectiveNFO != domain.NFOModeOff || optionalWork != 0 {
		return "snapshot_or_scan_mode_mismatch"
	}
	report.Configuration.IgnoreMode = "off" // The stored opt-in is false; the API's absent intent is normalized here.
	return ""
}
