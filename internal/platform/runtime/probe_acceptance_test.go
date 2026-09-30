//go:build jelee_probe_tests

package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	httpapi "github.com/MoYuanCN/Jelee/internal/adapter/http"
	"github.com/MoYuanCN/Jelee/internal/adapter/postgres"
	"github.com/MoYuanCN/Jelee/internal/adapter/scan"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	jobworker "github.com/MoYuanCN/Jelee/internal/platform/jobs"
	"github.com/MoYuanCN/Jelee/internal/platform/password"
	"github.com/MoYuanCN/Jelee/internal/platform/proberuntime"
	"github.com/MoYuanCN/Jelee/internal/platform/sandbox"
	"github.com/jackc/pgx/v5"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == sandbox.HelperCommand {
		os.Exit(proberuntime.Helper(os.Args[2:]))
	}
	if len(os.Args) == 2 && os.Args[1] == "--cleanup-probe-worker" {
		if cleanupAcceptanceSchema() {
			fmt.Println("owned acceptance schema cleaned")
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, "owned acceptance schema cleanup failed")
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func acceptanceDatabase() (*url.URL, string, error) {
	u, err := url.Parse(os.Getenv("JELEE_TEST_DATABASE_URL"))
	if err != nil || u == nil || u.Path != "/jelee_test" || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return nil, "", fmt.Errorf("dedicated jelee_test required")
	}
	name := os.Getenv("JELEE_PROBE_TEST_SCHEMA")
	suffix := strings.TrimPrefix(name, "jelee_probe_worker_")
	if len(suffix) != 32 || suffix == name || strings.ToLower(suffix) != suffix {
		return nil, "", fmt.Errorf("owned schema required")
	}
	if _, err = hex.DecodeString(suffix); err != nil {
		return nil, "", fmt.Errorf("owned schema required")
	}
	return u, name, nil
}

// The outer controller calls this after removing its container, including
// timeout/termination failures that bypass Go defers. No user schema is used.
func cleanupAcceptanceSchema() bool {
	u, name, err := acceptanceDatabase()
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	connection, err := pgx.Connect(ctx, u.String())
	if err != nil {
		return false
	}
	defer connection.Close(context.Background())
	if _, err = connection.Exec(ctx, "DROP SCHEMA IF EXISTS "+pgx.Identifier{name}.Sanitize()+" CASCADE"); err != nil {
		return false
	}
	var remains bool
	return connection.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_namespace WHERE nspname=$1)`, name).Scan(&remains) == nil && !remains
}

// This target requires the protected production image, actual PostgreSQL and
// 1,000 generated media paths. The outer script changes exactly 17 directory
// entries between runs, while this container keeps its media mount read-only.
func TestProductionProbeWorkerAcceptance(t *testing.T) {
	if os.Getenv("JELEE_REQUIRE_PROBE_WORKER") != "true" {
		t.Skip("production probe worker acceptance NOT RUN: required container profile absent")
	}
	if os.Getuid() != 65532 {
		t.Fatal("acceptance requires production nonroot uid")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	u, schemaName, err := acceptanceDatabase()
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatal("connect acceptance database")
	}
	defer admin.Close(context.Background())
	var random [10]byte
	if _, err = rand.Read(random[:]); err != nil {
		t.Fatal("schema entropy")
	}
	schema := pgx.Identifier{schemaName}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal("create owned schema")
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, err := admin.Exec(cleanup, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error("clean owned schema")
		}
	}()
	q := u.Query()
	q.Set("search_path", schemaName)
	u.RawQuery = q.Encode()
	if version, dirty, e := postgres.Migrate(ctx, u.String(), "up"); e != nil || dirty || version != postgres.SchemaVersion {
		t.Fatal("migrate acceptance schema")
	}
	store, err := postgres.Open(ctx, u.String(), 8)
	if err != nil {
		t.Fatal("open acceptance store")
	}
	defer store.Pool.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	startup, stop := context.WithTimeout(ctx, 10*time.Second)
	probing, err := newProbeService(startup, true, store, prepareProductionProbe)
	stop()
	if err != nil || probing == nil || !probing.Available() {
		t.Fatal("production probe health unavailable")
	}
	probing.logger = logger
	defer func() {
		if err := probing.Close(); err != nil {
			t.Error("probe cleanup")
		}
	}()
	values := map[string]string{"JELEE_DATABASE_URL": u.String(), "JELEE_ENABLE_ACCOUNTS": "true", "JELEE_ENABLE_JOBS": "true", "JELEE_ENABLE_PROBE": "true"}
	cfg, err := config.LoadWith(func(k string) (string, bool) { v, ok := values[k]; return v, ok })
	if err != nil {
		t.Fatal("acceptance config")
	}
	hasher, err := password.New(password.Config{MemoryKiB: password.MinMemoryKiB, Iterations: password.MinIterations, Parallelism: 1, MaxConcurrent: 1})
	if err != nil {
		t.Fatal(err)
	}
	secret := hex.EncodeToString(random[:]) + "-acceptance-only"
	hash, err := hasher.Hash(ctx, secret)
	if err != nil {
		t.Fatal("password hash")
	}
	if _, err = store.BootstrapAdmin(ctx, domain.UserInput{Name: "probe-admin", DisplayName: "Acceptance", Locale: "en-US", PasswordHash: hash}); err != nil {
		t.Fatal("bootstrap acceptance administrator")
	}
	accounts, err := app.NewAccounts(store, hasher, app.AccountOptions{SessionTTL: time.Hour, MaxSessions: 8, LockAfter: 5, LockFor: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	grant, err := accounts.Login(ctx, "probe-admin", secret, "acceptance", "127.0.0.1")
	if err != nil {
		t.Fatal("acceptance login")
	}
	registration, err := store.RegisterLibrary(ctx, "acceptance", "/media")
	if err != nil {
		t.Fatal("register readonly media")
	}
	jobs, err := app.NewJobsWithProbe(store, cfg.Jobs.Policy(), store, probing.identity, probing.Capability)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := httpapi.NewWithJobs(cfg, store, app.NewCatalog(store), store, logger, accounts, jobs)
	if err != nil {
		t.Fatal(err)
	}
	options := jobworker.DefaultOptions()
	options.PollInterval = 100 * time.Millisecond
	options.Probe = &jobworker.ProbeOptions{Repository: store, Prober: probing, LeaseDuration: 30 * time.Second, MaxConcurrent: 2, Available: probing.Available, OnRuntimeUnavailable: probing.Disable}
	runner, err := jobworker.New(store, scan.New(), options, logger)
	if err != nil {
		t.Fatal(err)
	}
	worker := &probeWorker{worker: runner, probe: probing}
	if err = worker.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stopCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if err := worker.Stop(stopCtx); err != nil {
			t.Error("worker join", err)
		}
	}()
	request := func(method, path, body, key string) json.RawMessage {
		t.Helper()
		request := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(body)).WithContext(ctx)
		request.RemoteAddr = "127.0.0.1:43210"
		request.Header.Set("Authorization", "Bearer "+grant.Token)
		request.Header.Set("Content-Type", "application/json")
		if key != "" {
			request.Header.Set("Idempotency-Key", key)
		}
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, request)
		if out.Code != http.StatusOK && out.Code != http.StatusAccepted {
			t.Fatalf("acceptance route %s status %d", path, out.Code)
		}
		var envelope struct {
			Data json.RawMessage `json:"data"`
		}
		if json.Unmarshal(out.Body.Bytes(), &envelope) != nil {
			t.Fatal("decode envelope")
		}
		return envelope.Data
	}
	rounds := []struct {
		calls           uint64
		hits, succeeded int64
	}{{1000, 0, 1000}, {0, 1000, 0}, {17, 983, 17}}
	for round, want := range rounds {
		started := time.Now()
		before := probing.probeCalls.Load()
		if probing.processStats == nil {
			t.Fatal("actual process counters unavailable")
		}
		beforeProcess := probing.processStats()
		raw := request("POST", "/api/v1/libraries/"+registration.Library.ID+"/scan", `{"probe":true}`, fmt.Sprintf("acceptance-%d", round))
		var job domain.Job
		if json.Unmarshal(raw, &job) != nil || !domain.ValidID(job.ID) {
			t.Fatal("accepted job")
		}
		ticker := time.NewTicker(200 * time.Millisecond)
		for {
			raw = request("GET", "/api/v1/jobs/"+job.ID, "", "")
			if json.Unmarshal(raw, &job) != nil {
				t.Fatal("job response")
			}
			if job.State == domain.JobSucceeded {
				break
			}
			if job.State == domain.JobFailed || job.State == domain.JobCancelled {
				t.Fatalf("job terminated: state=%s code=%s", job.State, job.ErrorCode)
			}
			select {
			case <-ctx.Done():
				t.Fatal("acceptance timed out")
			case <-ticker.C:
			}
		}
		ticker.Stop()
		var summary domain.ProbeJobSummary
		if json.Unmarshal(request("GET", "/api/v1/jobs/"+job.ID+"/probe", "", ""), &summary) != nil {
			t.Fatal("probe summary")
		}
		calls := probing.probeCalls.Load() - before
		processStats := probing.processStats()
		if processStats.Started-beforeProcess.Started != want.calls || processStats.Active != 0 || processStats.Peak < 1 || processStats.Peak > 2 {
			t.Fatal("actual child creation/join counts")
		}
		if calls != want.calls || summary.Phase != domain.ProbeSummaryDone || summary.Processed != 1000 || summary.Hits != want.hits || summary.Succeeded != want.succeeded || summary.Failed+summary.NegativeHits+summary.Changed+summary.Unavailable != 0 {
			t.Fatalf("round %d calls=%d summary=%+v", round+1, calls, summary)
		}
		var rows, bytes, leases, actualRows, actualBytes, readyRows int64
		if err = store.Pool.QueryRow(ctx, `SELECT rows_used,bytes_used,active_leases,(SELECT count(*) FROM probe_cache),(SELECT COALESCE(sum(charge_bytes),0) FROM probe_cache),(SELECT count(*) FROM probe_cache WHERE state='ready' AND lease_owner IS NULL) FROM probe_cache_quota`).Scan(&rows, &bytes, &leases, &actualRows, &actualBytes, &readyRows); err != nil || rows != 1000 || actualRows != rows || bytes != actualBytes || leases != 0 || readyRows != 1000 {
			t.Fatal("quota/reservation accounting after round", round+1)
		}
		metadataRows, err := store.Pool.Query(ctx, `SELECT relative_path,metadata,size FROM probe_cache ORDER BY relative_path`)
		if err != nil {
			t.Fatal("read committed metadata")
		}
		checked := 0
		for metadataRows.Next() {
			var path string
			var raw []byte
			var size int64
			if metadataRows.Scan(&path, &raw, &size) != nil {
				metadataRows.Close()
				t.Fatal("metadata row")
			}
			var metadata domain.MediaMetadata
			if json.Unmarshal(raw, &metadata) != nil {
				metadataRows.Close()
				t.Fatal("metadata JSON")
			}
			if _, err := domain.MarshalProbeMetadata(metadata); err != nil || metadata.Format.SizeBytes == nil || *metadata.Format.SizeBytes != size {
				metadataRows.Close()
				t.Fatal("metadata whitelist/size")
			}
			width, height := int64(320), int64(180)
			if round == 2 && path <= "media-0016.mp4" {
				width, height = 640, 360
			}
			video, audio := false, false
			for _, stream := range metadata.Streams {
				if stream.Kind == "video" && stream.Video != nil && stream.Video.Width != nil && stream.Video.Height != nil && stream.Codec != nil && *stream.Codec == "h264" && *stream.Video.Width == width && *stream.Video.Height == height {
					video = true
				}
				if stream.Kind == "audio" && stream.Codec != nil && *stream.Codec == "aac" {
					audio = true
				}
			}
			if !video || !audio {
				metadataRows.Close()
				t.Fatal("cached metadata differs from expected original/replacement")
			}
			checked++
		}
		if metadataRows.Err() != nil || checked != 1000 {
			metadataRows.Close()
			t.Fatal("metadata row count")
		}
		metadataRows.Close()
		record, _ := json.Marshal(map[string]any{"round": round + 1, "metadataProberCalls": calls, "metadataChildStarts": processStats.Started - beforeProcess.Started, "activeChildren": processStats.Active, "peakChildLifecycles": processStats.Peak, "summary": summary, "elapsedMillis": time.Since(started).Milliseconds(), "cacheRows": rows, "cacheBytes": bytes, "activeLeases": leases, "identityDigest": probing.IdentityDigest()})
		fmt.Println(string(record))
		if round == 1 {
			fmt.Println(`{"readyForReplacement":true}`)
			for {
				if _, e := os.Stat("/control/continue"); e == nil {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("replacement handshake timeout")
				case <-time.After(100 * time.Millisecond):
				}
			}
		}
	}
	// Core readiness survives a lost optional runtime; its public capability and
	// new admissions accurately report that loss. Persisted request replay works.
	probing.Disable()
	request("GET", "/readyz", "", "")
	request("POST", "/api/v1/libraries/"+registration.Library.ID+"/scan", `{"probe":true}`, "acceptance-0")
}
