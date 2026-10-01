//go:build !race && (linux || windows)

package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/postgres"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/password"
	"github.com/MoYuanCN/Jelee/internal/platform/process"
	"github.com/jackc/pgx/v5"
)

func TestFamilyIgnoreProductionRuntimeHTTP(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) { runFamilyProductionRuntime(t, enabled) })
	}
}
func TestFamilyIgnoreProductionRuntimeActiveChildStop(t *testing.T) {
	runFamilyProductionRuntime(t, true, "stop")
}
func TestFamilyIgnoreProductionRuntimeActiveChildHTTPCancel(t *testing.T) {
	runFamilyProductionRuntime(t, true, "cancel")
}
func runFamilyProductionRuntime(t *testing.T, enabled bool, activeStop ...string) {
	stopActive := len(activeStop) == 1
	cancelActive := stopActive && activeStop[0] == "cancel"
	dsn := os.Getenv("JELEE_TEST_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("JELEE_REQUIRE_INTEGRATION") == "true" {
			t.Fatal("required integration database unavailable")
		}
		t.Skip("dedicated PostgreSQL runtime acceptance unavailable")
	}
	u, err := url.Parse(dsn)
	if err != nil || u == nil || u.Path != "/jelee_test" || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		t.Fatal("dedicated jelee_test required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal("connect dedicated database")
	}
	defer admin.Close(context.Background())
	var entropy [10]byte
	if _, err = rand.Read(entropy[:]); err != nil {
		t.Fatal(err)
	}
	schemaName := "jelee_family_rt_" + hex.EncodeToString(entropy[:])
	schema := pgx.Identifier{schemaName}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal("create owned runtime schema")
	}
	defer func() {
		clean, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, err := admin.Exec(clean, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error("clean owned runtime schema")
		}
	}()
	q := u.Query()
	q.Set("search_path", schemaName)
	u.RawQuery = q.Encode()
	if version, dirty, err := postgres.Migrate(ctx, u.String(), "up"); err != nil || dirty || version != postgres.SchemaVersion {
		t.Fatal("migrate owned runtime schema")
	}
	store, err := postgres.Open(ctx, u.String(), 8)
	if err != nil {
		t.Fatal("open owned runtime schema")
	}
	defer store.Pool.Close()
	mediaRoot, scratch := t.TempDir(), t.TempDir()
	t.Setenv("TMPDIR", scratch)
	t.Setenv("TMP", scratch)
	t.Setenv("TEMP", scratch)
	files := map[string]string{".jeleeignore": "!keep.mkv\ncustom.tmp", ".ignore": "*.mkv", "keep.mkv": "retained", "drop.mkv": "excluded", "custom.tmp": "custom", "hidden/.ignore": "", "hidden/movie.nfo": "excluded malformed NFO"}
	if stopActive {
		files[".ignore"] = strings.Repeat("(a|aa){1,100}b\n", 4000)
		if cancelActive {
			files[".ignore"] = strings.Repeat(strings.Repeat("(a|aa){1,100}", 4)+"b\n", 4000)
		}
	}
	for name, data := range files {
		p := filepath.Join(mediaRoot, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	registration, err := store.RegisterLibrary(ctx, "runtime-fixture", mediaRoot)
	if err != nil {
		t.Fatal(err)
	}
	hasher, err := password.New(password.Config{MemoryKiB: password.MinMemoryKiB, Iterations: password.MinIterations, Parallelism: 1, MaxConcurrent: 1})
	if err != nil {
		t.Fatal(err)
	}
	secret := hex.EncodeToString(entropy[:]) + "-runtime-fixture"
	hash, err := hasher.Hash(ctx, secret)
	if err != nil {
		t.Fatal("hash fixture password")
	}
	if _, err = store.BootstrapAdmin(ctx, domain.UserInput{Name: "runtime-admin", DisplayName: "Runtime fixture", Locale: "zh-TW", PasswordHash: hash}); err != nil {
		t.Fatal("bootstrap fixture administrator")
	}
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := reserved.Addr().String()
	if err = reserved.Close(); err != nil {
		t.Fatal(err)
	}
	values := map[string]string{"JELEE_DATABASE_URL": u.String(), "JELEE_LISTEN": address, "JELEE_ENABLE_ACCOUNTS": "true", "JELEE_ENABLE_JOBS": "true", "JELEE_ENABLE_FAMILY_IGNORE": fmt.Sprint(enabled), "JELEE_JOB_POLL_MILLISECONDS": "100", "JELEE_JOB_WORKERS": "1"}
	cfg, err := config.LoadWith(func(k string) (string, bool) { v, ok := values[k]; return v, ok })
	if err != nil {
		t.Fatal("valid runtime config")
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ownedLifetime := newLifetime(logger)
	application := newWithLifetime(cfg, logger, ownedLifetime)
	if application.Err() != nil {
		t.Fatal("runtime graph construction")
	}
	if err = application.Start(ctx); err != nil {
		t.Fatal("runtime startup")
	}
	stopped := false
	defer func() {
		if !stopped {
			clean, stop := context.WithTimeout(context.Background(), 10*time.Second)
			defer stop()
			if err := application.Stop(clean); err != nil {
				t.Error("runtime stop")
			}
		}
	}()
	client := &http.Client{Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	request := func(method, path, body, key, token string) (int, []byte) {
		t.Helper()
		r, err := http.NewRequestWithContext(ctx, method, "http://"+address+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := client.Do(r)
		if err != nil {
			t.Fatal("runtime HTTP request")
		}
		defer response.Body.Close()
		data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if err != nil {
			t.Fatal("runtime HTTP response")
		}
		return response.StatusCode, data
	}
	login, _ := json.Marshal(map[string]string{"name": "runtime-admin", "password": secret, "deviceName": "runtime-fixture"})
	status, raw := request("POST", "/api/v1/auth/login", string(login), "", "")
	var grant struct {
		Data domain.SessionGrant `json:"data"`
	}
	if status != 200 || json.Unmarshal(raw, &grant) != nil || grant.Data.Token == "" {
		t.Fatal("real runtime login", status)
	}
	body := `{"ignore":{"mode":"jeleeignore-legacy-v1","caseMode":"sensitive"}}`
	path := "/api/v1/libraries/" + registration.Library.ID + "/scan"
	if status, _ := request("POST", path, body, "unauthorized", ""); status != 401 {
		t.Fatal("anonymous family admission", status)
	}
	status, raw = request("POST", path, body, "family-runtime", grant.Data.Token)
	if !enabled {
		if status != 503 {
			t.Fatal("disabled family admission", status)
		}
		var count int
		if err := store.Pool.QueryRow(ctx, `SELECT count(*) FROM jobs`).Scan(&count); err != nil || count != 0 {
			t.Fatal("disabled admission created a job")
		}
	} else {
		var job struct {
			Data domain.Job `json:"data"`
		}
		if status != 202 || json.Unmarshal(raw, &job) != nil || !domain.ValidID(job.Data.ID) {
			t.Fatal("family HTTP admission", status)
		}
		if stopActive {
			if ownedLifetime.ignoreService == nil {
				t.Fatal("formal ignore service missing")
			}
			helper, ok := ownedLifetime.ignoreService.backend.(*process.IgnoreRunner)
			if !ok {
				t.Fatal("formal native helper missing")
			}
			deadline := time.Now().Add(5 * time.Second)
			for helper.Stats().Started < 2 || helper.Stats().Active != 1 {
				if time.Now().After(deadline) {
					t.Fatal("scan child was not observed active")
				}
				time.Sleep(time.Millisecond)
			}
			if cancelActive {
				started := time.Now()
				if status, _ := request("POST", "/api/v1/jobs/"+job.Data.ID+"/cancel", "{}", "", grant.Data.Token); status != 200 {
					t.Fatal("active scan HTTP cancellation", status)
				}
				deadline := time.Now().Add(15 * time.Second)
				for {
					var current domain.Job
					if err := store.Pool.QueryRow(ctx, `SELECT state,missing FROM jobs WHERE id=$1::uuid`, job.Data.ID).Scan(&current.State, &current.Missing); err != nil {
						t.Fatal("cancelled job query")
					}
					if current.State == domain.JobCancelled {
						if current.Missing != 0 {
							t.Fatal("cancelled scan reported missing")
						}
						break
					}
					if current.State == domain.JobFailed || current.State == domain.JobSucceeded || time.Now().After(deadline) {
						t.Fatal("HTTP cancellation did not finish safely", current.State)
					}
					time.Sleep(10 * time.Millisecond)
				}
				stats := helper.Stats()
				if stats.Active != 0 || stats.Cancelled != 1 || stats.TimedOut != 0 || !ownedLifetime.ignoreService.Available() {
					t.Fatal("HTTP user cancellation did not interrupt child while preserving service", stats)
				}
				var baseline, owners int64
				if err := store.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM library_inventory_baseline),(SELECT count(*) FROM jobs WHERE owner IS NOT NULL)`).Scan(&baseline, &owners); err != nil || baseline != 0 || owners != 0 {
					t.Fatal("HTTP cancellation published baseline or retained lease")
				}
				fmt.Printf("{\"httpCancellation\":true,\"milliseconds\":%d,\"cancelStops\":%d,\"timeoutStops\":%d,\"childStarts\":%d}\n", time.Since(started).Milliseconds(), stats.Cancelled, stats.TimedOut, stats.Started)
			}
			stopCtx, cancelStop := context.WithTimeout(context.Background(), 5*time.Second)
			err := application.Stop(stopCtx)
			cancelStop()
			stopped = true
			if err != nil || helper.Stats().Active != 0 || ownedLifetime.ignoreService.Available() || ownedLifetime.ignoreService.Close() != nil {
				t.Fatal("active helper runtime stop did not join and close", err)
			}
			var baseline, owners int64
			var state string
			if err := store.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM library_inventory_baseline),(SELECT count(*) FROM jobs WHERE owner IS NOT NULL),state FROM jobs WHERE id=$1::uuid`, job.Data.ID).Scan(&baseline, &owners, &state); err != nil || baseline != 0 || owners != 0 || state != domain.JobQueued && (!cancelActive || state != domain.JobCancelled) {
				t.Fatal("stopped scan published or retained owner", err, state)
			}
			if entries, err := os.ReadDir(scratch); err != nil || len(entries) != 0 {
				t.Fatal("active helper stop left temporary input")
			}
			for name, data := range files {
				actual, err := os.ReadFile(filepath.Join(mediaRoot, filepath.FromSlash(name)))
				if err != nil || string(actual) != data {
					t.Fatal("active helper stop modified original files")
				}
			}
			response, err := client.Get("http://" + address + "/readyz")
			if err == nil {
				response.Body.Close()
				t.Fatal("stopped HTTP listener remained open")
			}
			return
		}
		if status, _ := request("POST", path, body, "family-runtime", grant.Data.Token); status != 200 {
			t.Fatal("family HTTP replay", status)
		}
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			status, raw = request("GET", "/api/v1/jobs/"+job.Data.ID, "", "", grant.Data.Token)
			var current struct {
				Data domain.Job `json:"data"`
			}
			if status != 200 || json.Unmarshal(raw, &current) != nil {
				t.Fatal("runtime job query", status)
			}
			if current.Data.State == domain.JobSucceeded {
				if current.Data.ReviewRequired || current.Data.Missing != 0 {
					t.Fatal("unexpected review")
				}
				break
			}
			if current.Data.State == domain.JobFailed || current.Data.State == domain.JobCancelled {
				t.Fatal("family runtime worker failed", current.Data.ErrorCode)
			}
			select {
			case <-ctx.Done():
				t.Fatal("family runtime completion timed out")
			case <-ticker.C:
			}
		}
		status, raw = request("GET", "/api/v1/jobs/"+job.Data.ID+"/ignore?limit=100", "", "", grant.Data.Token)
		var report struct {
			Data domain.IgnoreReport `json:"data"`
		}
		if status != 200 || json.Unmarshal(raw, &report) != nil || !report.Data.Enabled || report.Data.ExcludedFiles != 2 || report.Data.ExcludedDirectories != 1 || len(report.Data.Entries) != 3 || strings.Contains(string(raw), mediaRoot) {
			t.Fatal("family runtime report", status)
		}
		custom, legacy := 0, 0
		for _, entry := range report.Data.Entries {
			switch entry.Family {
			case domain.IgnoreFamilyCustom:
				custom++
			case domain.IgnoreFamilyLegacy:
				legacy++
			}
		}
		if custom != 1 || legacy != 2 {
			t.Fatal("runtime rule provenance missing")
		}
		var kept int
		if err = store.Pool.QueryRow(ctx, `SELECT count(*) FROM library_inventory_baseline WHERE library_id=$1::uuid AND path='keep.mkv'`, registration.Library.ID).Scan(&kept); err != nil || kept != 1 {
			t.Fatal("filtered runtime baseline")
		}
	}
	stopCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	if err = application.Stop(stopCtx); err != nil {
		t.Fatal("runtime shutdown")
	}
	stopped = true
	if enabled {
		// A service restart with the feature off still reads authorized retained
		// requests, while new family jobs have no helper authority.
		cfg.EnableFamilyIgnore = false
		application = New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if application.Err() != nil || application.Start(ctx) != nil {
			t.Fatal("disabled runtime restart")
		}
		stopped = false
		if status, _ := request("POST", path, body, "family-runtime", grant.Data.Token); status != 200 {
			t.Fatal("retained replay lost after feature disable", status)
		}
		if status, _ := request("POST", path, body, "new-disabled", grant.Data.Token); status != 503 {
			t.Fatal("new admission after feature disable", status)
		}
		if err = application.Stop(stopCtx); err != nil {
			t.Fatal("restarted runtime shutdown")
		}
		stopped = true
	}
	if entries, err := os.ReadDir(scratch); err != nil || len(entries) != 0 {
		t.Fatal("runtime temporary cleanup failed")
	}
	for name, data := range files {
		actual, err := os.ReadFile(filepath.Join(mediaRoot, filepath.FromSlash(name)))
		if err != nil || string(actual) != data {
			t.Fatal("runtime modified fixture media")
		}
	}
}
