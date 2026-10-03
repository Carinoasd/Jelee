//go:build jelee_probe_tests

package runtime

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	imageadapter "github.com/MoYuanCN/Jelee/internal/adapter/images"
	"github.com/MoYuanCN/Jelee/internal/adapter/postgres"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/password"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const imagesMemoryScratch = "/image-work"

var errImagesMemoryHTTP = errors.New("image acceptance HTTP result invalid")

type imagesMemoryAcceptanceReport struct {
	Version                  int                       `json:"version"`
	Result                   string                    `json:"result"`
	ErrorCode                string                    `json:"errorCode"`
	FixtureItems             int                       `json:"fixtureItems"`
	Configuration            imagesMemoryConfiguration `json:"configuration"`
	Fixtures                 imagesMemoryFixtures      `json:"fixtures"`
	Cold                     imagesMemoryPhaseResult   `json:"cold"`
	Warm                     imagesMemoryPhaseResult   `json:"warm"`
	Negative                 imagesMemoryNegative      `json:"negative"`
	Cancellation             imagesMemoryCancellation  `json:"cancellation"`
	SourceSampleCount        int                       `json:"sourceSampleCount"`
	OriginalSamplesUnchanged bool                      `json:"originalSamplesUnchanged"`
	Shutdown                 imagesMemoryShutdown      `json:"shutdown"`
	MemoryProfile            imagesMemoryReport        `json:"memoryProfile"`
}

type imagesMemoryConfiguration struct {
	MaxConcurrent       int   `json:"maxConcurrent"`
	MaxImageBytes       int64 `json:"maxImageBytes"`
	MaxSourceBytes      int64 `json:"maxSourceBytes"`
	MaxOutputBytes      int64 `json:"maxOutputBytes"`
	MaxOutputDimension  int   `json:"maxOutputDimension"`
	CacheBytes          int64 `json:"cacheBytes"`
	CacheEntries        int   `json:"cacheEntries"`
	DefaultQuality      int   `json:"defaultQuality"`
	TimeoutSeconds      int   `json:"timeoutSeconds"`
	CacheTTLSeconds     int   `json:"cacheTTLSeconds"`
	GOMAXPROCS          int   `json:"gomaxprocs"`
	PasswordMemoryKiB   int   `json:"passwordMemoryKiB"`
	PasswordIterations  int   `json:"passwordIterations"`
	PasswordParallelism int   `json:"passwordParallelism"`
	PasswordConcurrency int   `json:"passwordConcurrency"`
	Jobs                bool  `json:"jobs"`
	Probe               bool  `json:"probe"`
	Ignore              bool  `json:"ignore"`
}

type imagesMemoryFixtures struct {
	JPEG          int   `json:"jpeg"`
	PNG           int   `json:"png"`
	PNG16         int   `json:"png16"`
	Items         int64 `json:"items"`
	MediaSources  int64 `json:"mediaSources"`
	DistinctPaths int64 `json:"distinctPaths"`
}

type imagesMemoryStats struct {
	Active                 int64  `json:"active"`
	ReservedBytes          int64  `json:"reservedBytes"`
	MaxEstimatedImageBytes int64  `json:"maxEstimatedImageBytes"`
	Admitted               uint64 `json:"admitted"`
	Completed              uint64 `json:"completed"`
	Failed                 uint64 `json:"failed"`
	Busy                   uint64 `json:"busy"`
	CacheHits              uint64 `json:"cacheHits"`
	CacheMisses            uint64 `json:"cacheMisses"`
	Decodes                uint64 `json:"decodes"`
	CacheEntries           int    `json:"cacheEntries"`
	CacheBytes             int64  `json:"cacheBytes"`
	CacheEvictions         uint64 `json:"cacheEvictions"`
}

func imagesMemoryStatsValue(value imageadapter.Stats) imagesMemoryStats {
	return imagesMemoryStats{value.Active, value.ReservedBytes, value.MaxEstimatedImageBytes,
		value.Admitted, value.Completed, value.Failed, value.Busy, value.CacheHits, value.CacheMisses,
		value.Decodes, value.CacheEntries, value.CacheBytes, value.CacheEvictions}
}

type imagesMemoryPhaseResult struct {
	StartedNanos   int64             `json:"startedNanos"`
	FinishedNanos  int64             `json:"finishedNanos"`
	ElapsedNanos   int64             `json:"elapsedNanos"`
	Get200         int64             `json:"get200"`
	Head200        int64             `json:"head200"`
	NotModified304 int64             `json:"notModified304"`
	HTTPBytes      int64             `json:"httpBytes"`
	Before         imagesMemoryStats `json:"before"`
	After          imagesMemoryStats `json:"after"`
}

type imagesMemoryNegative struct {
	Unauthenticated401 bool `json:"unauthenticated401"`
	ACLDenied404       bool `json:"aclDenied404"`
	ACLRevoked404      bool `json:"aclRevoked404"`
	Unsupported415     bool `json:"unsupported415"`
	Corrupt404         bool `json:"corrupt404"`
	SourceLimit413     bool `json:"sourceLimit413"`
	DimensionLimit413  bool `json:"dimensionLimit413"`
	NoPreflightDecode  bool `json:"noPreflightDecode"`
	ErrorsRedacted     bool `json:"errorsRedacted"`
	ScratchEmpty       bool `json:"scratchEmpty"`
}

type imagesMemoryCancellation struct {
	BlockedQueries         int  `json:"blockedQueries"`
	Busy503                bool `json:"busy503"`
	RetryAfter             bool `json:"retryAfter"`
	CancelledRequest       bool `json:"cancelledRequest"`
	CancelledQueryReleased bool `json:"cancelledQueryReleased"`
	Recovered200           bool `json:"recovered200"`
}

type imagesMemoryShutdown struct {
	Signal             string `json:"signal"`
	InFlightBeforeStop bool   `json:"inFlightBeforeStop"`
	ActiveImages       int64  `json:"activeImages"`
	CacheEntries       int    `json:"cacheEntries"`
	CacheBytes         int64  `json:"cacheBytes"`
	HTTPClosed         bool   `json:"httpClosed"`
	LifetimeCancelled  bool   `json:"lifetimeCancelled"`
	PoolConnections    int64  `json:"poolConnections"`
	ScratchEmpty       bool   `json:"scratchEmpty"`
	Result             string `json:"result"`
}

func TestImagesMemoryAcceptance(t *testing.T) {
	if os.Getenv("JELEE_IMAGES_MEMORY_ACCEPTANCE") != "true" {
		t.Skip("image memory acceptance requires its controlled container")
	}
	report := imagesMemoryAcceptanceReport{Version: 1, Result: "failed"}
	var profile *imagesMemoryProfile
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
			t.Error("image memory acceptance failed", report.ErrorCode)
		}
		data, err := json.Marshal(map[string]any{"imagesMemoryAcceptance": report})
		if err != nil || len(data) > 2<<20 {
			t.Error("image memory report exceeded its encoding boundary")
			fmt.Println(`{"imagesMemoryAcceptance":{"version":1,"result":"failed","errorCode":"report_encoding_failed"}}`)
			return
		}
		fmt.Println(string(data))
	}()
	count, err := strconv.Atoi(os.Getenv("JELEE_IMAGES_MEMORY_ITEMS"))
	if err != nil || count != 1000 && count != 100000 || goruntime.GOOS != "linux" || os.Getuid() != 65532 {
		report.ErrorCode = "invalid_acceptance_environment"
		return
	}
	report.FixtureItems = count
	profile, err = startImagesMemoryProfile()
	if err != nil {
		report.ErrorCode = "memory_profile_start_failed"
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Minute)
	defer cancel()
	report.ErrorCode = runImagesMemoryAcceptance(ctx, profile, &report)
}

func runImagesMemoryAcceptance(ctx context.Context, profile *imagesMemoryProfile, report *imagesMemoryAcceptanceReport) (failure string) {
	cleanupFailure := func(code string) {
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
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if admin.Close(c) != nil {
			cleanupFailure("database_close_failed")
		}
	}()
	quotedSchema := pgx.Identifier{schemaName}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		return "owned_schema_creation_failed"
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if _, err := admin.Exec(c, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			cleanupFailure("owned_schema_cleanup_failed")
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
	applicationName := schemaName + "_runtime"
	query.Set("application_name", applicationName)
	u.RawQuery = query.Encode()
	values := map[string]string{"JELEE_DATABASE_URL": u.String(), "JELEE_ENABLE_ACCOUNTS": "true", "JELEE_ENABLE_CATALOG": "true", "JELEE_ENABLE_IMAGES": "true", "JELEE_IMAGE_TEMP_ROOT": imagesMemoryScratch, "JELEE_MAX_CONNECTIONS": "8"}
	cfg, err := config.LoadWith(func(key string) (string, bool) { value, ok := values[key]; return value, ok })
	if err != nil || cfg.Images != (func() config.ImagesConfig {
		v := config.DefaultImagesConfig()
		v.TempRoot = imagesMemoryScratch
		return v
	})() || cfg.Accounts != config.DefaultAccountsConfig() || cfg.EnableJobs || cfg.EnableProbe || cfg.EnableFamilyIgnore || goruntime.GOMAXPROCS(0) != 2 {
		return "runtime_configuration_failed"
	}
	p, a := cfg.Images, cfg.Accounts
	report.Configuration = imagesMemoryConfiguration{p.MaxConcurrent, p.MaxImageBytes, p.MaxSourceBytes, p.MaxOutputBytes, p.MaxOutputDimension, p.CacheBytes, p.CacheEntries, p.DefaultQuality, p.TimeoutSeconds, p.CacheTTLSeconds, goruntime.GOMAXPROCS(0), a.PasswordMemoryKiB, a.PasswordIterations, a.PasswordParallelism, a.PasswordConcurrency, cfg.EnableJobs, cfg.EnableProbe, cfg.EnableFamilyIgnore}
	hasher, err := password.New(password.DefaultConfig())
	if err != nil {
		return "password_configuration_failed"
	}
	var entropy [16]byte
	if _, err = rand.Read(entropy[:]); err != nil {
		return "account_entropy_failed"
	}
	secret := hex.EncodeToString(entropy[:])
	hash, err := hasher.Hash(ctx, secret)
	if err != nil {
		return "account_hash_failed"
	}
	if _, err = store.BootstrapAdmin(ctx, domain.UserInput{Name: "image-memory-admin", DisplayName: "Image acceptance", Locale: "en-US", PasswordHash: hash}); err != nil {
		return "account_bootstrap_failed"
	}
	registration, err := store.RegisterLibrary(ctx, "image memory acceptance", "/media")
	if err != nil {
		return "fixture_registration_failed"
	}
	if err := seedImagesMemoryItems(ctx, store, registration, report.FixtureItems); err != nil {
		return "fixture_seed_failed"
	}
	boundary := max(100, report.FixtureItems/20)
	report.Fixtures.JPEG, report.Fixtures.PNG, report.Fixtures.PNG16 = report.FixtureItems-boundary, boundary-64, 64
	if err = store.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM items WHERE library_id=$1::uuid),count(*),count(DISTINCT relative_path) FROM media_sources WHERE library_id=$1::uuid`, registration.Library.ID).Scan(&report.Fixtures.Items, &report.Fixtures.MediaSources, &report.Fixtures.DistinctPaths); err != nil || report.Fixtures.Items != int64(report.FixtureItems) || report.Fixtures.MediaSources != int64(report.FixtureItems) || report.Fixtures.DistinctPaths != int64(report.FixtureItems) {
		return "fixture_seed_mismatch"
	}
	samples, err := snapshotImagesMemoryFixtures(report.FixtureItems)
	if err != nil {
		return "fixture_samples_failed"
	}
	report.SourceSampleCount = len(samples)
	if profile.changePhase("login") != nil {
		return "memory_phase_failed"
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	life := newLifetime(logger)
	address := ""
	life.listen = func(c context.Context, network, _ string) (net.Listener, error) {
		l, e := (&net.ListenConfig{}).Listen(c, network, "127.0.0.1:0")
		if e == nil {
			address = "http://" + l.Addr().String()
		}
		return l, e
	}
	application := newWithLifetime(cfg, logger, life)
	if application.Err() != nil || life.imageStats == nil || profile.observeProcessor(life.imageStats) != nil {
		return "runtime_build_failed"
	}
	stopped := false
	defer func() {
		if !stopped {
			c, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if application.Stop(c) != nil {
				cleanupFailure("runtime_cleanup_failed")
			}
		}
	}()
	signals := application.Wait()
	startCtx, stopStart := context.WithTimeout(ctx, 20*time.Second)
	err = application.Start(startCtx)
	stopStart()
	if err != nil || address == "" || life.worker != nil || life.closeImages == nil {
		return "runtime_start_failed"
	}
	// Reuse exactly two HTTP/1 connections for the two cold workers. The
	// server finishes a prior handler (including Body.Close/admission release)
	// before processing its next request on that connection. A separate,
	// bounded connection below exercises deliberate third-request rejection.
	transport := &http.Transport{MaxIdleConns: 2, MaxIdleConnsPerHost: 2, MaxConnsPerHost: 2, DisableCompression: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	login := func(name string) (domain.SessionGrant, error) {
		body, _ := json.Marshal(map[string]string{"name": name, "password": secret, "deviceName": "image acceptance"})
		var grant domain.SessionGrant
		e := imagesMemoryAPI(ctx, client, address, "POST", "/api/v1/auth/login", body, "", "", 200, &grant)
		if e == nil && (grant.Token == "" || !domain.ValidID(grant.User.ID)) {
			e = errImagesMemoryHTTP
		}
		return grant, e
	}
	administrator, err := login("image-memory-admin")
	if err != nil || !administrator.User.Admin {
		return "http_admin_login_failed"
	}
	userBody, _ := json.Marshal(map[string]any{"name": "image-memory-viewer", "displayName": "Image viewer", "locale": "en-US", "password": secret})
	var viewerUser domain.User
	if imagesMemoryAPI(ctx, client, address, "POST", "/api/v1/users", userBody, administrator.Token, "image-memory-viewer", 201, &viewerUser) != nil {
		return "http_viewer_create_failed"
	}
	viewer, err := login("image-memory-viewer")
	if err != nil || viewer.User.Admin || viewer.User.ID != viewerUser.ID {
		return "http_viewer_login_failed"
	}
	access := func(allow bool) error {
		ids := []string{}
		if allow {
			ids = append(ids, registration.Library.ID)
		}
		body, _ := json.Marshal(map[string]any{"libraryIds": ids})
		return imagesMemoryAPI(ctx, client, address, "PUT", "/api/v1/users/"+viewer.User.ID+"/libraries", body, administrator.Token, "", 204, nil)
	}
	if access(true) != nil {
		return "http_acl_grant_failed"
	}
	var warmTags [64]string
	if profile.changePhase("cold") != nil {
		return "memory_phase_failed"
	}
	if code := runImagesMemoryCold(ctx, profile, life, client, address, viewer.Token, report.FixtureItems, &report.Cold, &warmTags); code != "" {
		return code
	}
	if profile.changePhase("warm") != nil {
		return "memory_phase_failed"
	}
	if code := runImagesMemoryWarm(ctx, profile, life, client, address, viewer.Token, report.FixtureItems, &report.Warm, warmTags); code != "" {
		return code
	}
	if profile.changePhase("negative") != nil {
		return "memory_phase_failed"
	}
	if code := runImagesMemoryNegative(ctx, store, registration, life, client, address, viewer.Token, report.FixtureItems, warmTags[63], access, &report.Negative); code != "" {
		return code
	}
	if profile.changePhase("cancellation") != nil {
		return "memory_phase_failed"
	}
	if code := runImagesMemoryCancellation(ctx, store, applicationName, life, client, address, viewer.Token, report.FixtureItems, &report.Cancellation); code != "" {
		return code
	}
	if err := verifyImagesMemoryFixtures(samples); err != nil {
		return "original_fixture_changed"
	}
	report.OriginalSamplesUnchanged = true
	// Keep one real source lookup waiting, without replacing the repository.
	lock, err := store.Pool.Begin(ctx)
	if err != nil {
		return "shutdown_lock_failed"
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = lock.Rollback(c)
	}()
	if _, err = lock.Exec(ctx, `LOCK TABLE media_sources IN ACCESS EXCLUSIVE MODE`); err != nil {
		return "shutdown_lock_failed"
	}
	loopCtx, stopLoop := context.WithCancel(ctx)
	defer stopLoop()
	var stopNew atomic.Bool
	loopDone := make(chan error, 1)
	go func() {
		for attempts := 0; attempts < 32 && !stopNew.Load(); attempts++ {
			result, e := imagesMemoryGET(loopCtx, client, address, viewer.Token, report.FixtureItems-1, report.FixtureItems, "GET", "", 200)
			if loopCtx.Err() != nil {
				loopDone <- nil
				return
			}
			if e == nil || result.status != 408 && result.status != 503 {
				loopDone <- errImagesMemoryHTTP
				return
			}
		}
		loopDone <- nil
	}()
	loopJoined := false
	defer func() {
		stopNew.Store(true)
		stopLoop()
		if !loopJoined {
			select {
			case <-loopDone:
			case <-time.After(3 * time.Second):
				cleanupFailure("shutdown_client_join_failed")
			}
		}
	}()
	if waitImagesMemoryBlocked(ctx, store, applicationName, 1) != nil {
		return "shutdown_inflight_not_observed"
	}
	fmt.Println(`{"imagesMemoryReadyForSIGTERM":true}`)
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
	case <-loopDone:
		loopJoined = true
		return "shutdown_client_ended_early"
	}
	if waitImagesMemoryBlocked(ctx, store, applicationName, 1) != nil {
		return "shutdown_inflight_not_observed"
	}
	report.Shutdown.InFlightBeforeStop = true
	stopNew.Store(true)
	if profile.changePhase("shutdown") != nil {
		return "memory_phase_failed"
	}
	stopCtx, cancelStop := context.WithTimeout(context.Background(), 15*time.Second)
	err = application.Stop(stopCtx)
	cancelStop()
	stopped = true
	if err != nil {
		return "runtime_stop_failed"
	}
	stopLoop()
	select {
	case e := <-loopDone:
		loopJoined = true
		if e != nil {
			return "shutdown_client_failed"
		}
	case <-time.After(3 * time.Second):
		return "shutdown_client_join_failed"
	}
	if lock.Rollback(ctx) != nil {
		return "shutdown_lock_cleanup_failed"
	}
	select {
	case <-life.stopped:
	default:
		return "runtime_join_incomplete"
	}
	stats := life.imageStats()
	report.Shutdown.ActiveImages, report.Shutdown.CacheEntries, report.Shutdown.CacheBytes = stats.Active, stats.CacheEntries, stats.CacheBytes
	if stats.Active != 0 || stats.ReservedBytes != 0 || stats.CacheEntries != 0 || stats.CacheBytes != 0 {
		return "image_resources_retained"
	}
	report.Shutdown.LifetimeCancelled = life.ctx.Err() != nil
	if !report.Shutdown.LifetimeCancelled {
		return "runtime_lifetime_not_cancelled"
	}
	response, requestErr := client.Get(address + "/healthz")
	if requestErr == nil {
		response.Body.Close()
		return "http_listener_not_closed"
	}
	report.Shutdown.HTTPClosed = true
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
		case <-time.After(10 * time.Millisecond):
		}
	}
	report.Shutdown.ScratchEmpty = imagesMemoryScratchEmpty()
	if !report.Shutdown.ScratchEmpty {
		return "image_scratch_retained"
	}
	if profile.changePhase("stopped") != nil {
		return "memory_phase_failed"
	}
	if report.MemoryProfile, err = profile.finish(); err != nil {
		return "memory_profile_failed"
	}
	report.Shutdown.Result = "passed"
	return ""
}

func imagesMemoryItem(index int) string { return fmt.Sprintf("64000000-0000-4000-8000-%012x", index+1) }
func imagesMemorySource(index int) string {
	return fmt.Sprintf("65000000-0000-4000-8000-%012x", index+1)
}
func imagesMemoryMediaPath(index int) string {
	return fmt.Sprintf("dir-%05d/clip-%06d.mkv", index/10, index)
}
func imagesMemoryPosterPath(index, total int) string {
	ext := "jpg"
	if index < max(100, total/20) {
		ext = "png"
	}
	return fmt.Sprintf("dir-%05d/clip-%06d-poster.%s", index/10, index, ext)
}

func seedImagesMemoryItems(ctx context.Context, store *postgres.Store, registration domain.LibraryRegistration, total int) error {
	var libraryID, rootID pgtype.UUID
	if libraryID.Scan(registration.Library.ID) != nil || rootID.Scan(registration.RootID) != nil {
		return errImagesMemoryHTTP
	}
	for start := 0; start < total; start += 1000 {
		end := min(start+1000, total)
		items, sources := make([][]any, 0, end-start), make([][]any, 0, end-start)
		for i := start; i < end; i++ {
			var itemID, sourceID pgtype.UUID
			if itemID.Scan(imagesMemoryItem(i)) != nil || sourceID.Scan(imagesMemorySource(i)) != nil {
				return errImagesMemoryHTTP
			}
			items = append(items, []any{itemID, libraryID, "Image fixture", "Movie"})
			sources = append(sources, []any{sourceID, itemID, libraryID, rootID, imagesMemoryMediaPath(i), "video/x-matroska"})
		}
		tx, err := store.Pool.Begin(ctx)
		if err != nil {
			return errImagesMemoryHTTP
		}
		count, err := tx.CopyFrom(ctx, pgx.Identifier{"items"}, []string{"id", "library_id", "title", "kind"}, pgx.CopyFromRows(items))
		if err == nil && count == int64(end-start) {
			count, err = tx.CopyFrom(ctx, pgx.Identifier{"media_sources"}, []string{"id", "item_id", "library_id", "root_id", "relative_path", "content_type"}, pgx.CopyFromRows(sources))
		}
		if err != nil || count != int64(end-start) {
			c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = tx.Rollback(c)
			cancel()
			return errImagesMemoryHTTP
		}
		if tx.Commit(ctx) != nil {
			return errImagesMemoryHTTP
		}
	}
	return nil
}

func imagesMemoryAPI(ctx context.Context, client *http.Client, address, method, path string, body []byte, token, key string, status int, result any) error {
	request, err := http.NewRequestWithContext(ctx, method, address+path, bytes.NewReader(body))
	if err != nil {
		return errImagesMemoryHTTP
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if len(body) != 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	response, err := client.Do(request)
	if err != nil {
		return errImagesMemoryHTTP
	}
	data, readErr := io.ReadAll(io.LimitReader(response.Body, (128<<10)+1))
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil || len(data) > 128<<10 || response.StatusCode != status {
		return errImagesMemoryHTTP
	}
	if status == 204 {
		if len(data) != 0 {
			return errImagesMemoryHTTP
		}
		return nil
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(data, &envelope) != nil || len(envelope.Data) == 0 || result == nil || json.Unmarshal(envelope.Data, result) != nil {
		return errImagesMemoryHTTP
	}
	return nil
}

type imagesMemoryHTTPResult struct {
	status int
	bytes  int64
	etag   string
}

func imagesMemoryGET(ctx context.Context, client *http.Client, address, token string, index, total int, method, etag string, status int) (result imagesMemoryHTTPResult, err error) {
	width, height := 160, 240
	if index < 64 {
		width, height = 1024, 1024
	}
	request, err := http.NewRequestWithContext(ctx, method, address+"/images/Primary/"+imagesMemoryItem(index)+fmt.Sprintf("?width=%d&height=%d&quality=85&format=jpeg", width, height), nil)
	if err != nil {
		return result, errImagesMemoryHTTP
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if etag != "" {
		request.Header.Set("If-None-Match", etag)
	}
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, errImagesMemoryHTTP
	}
	result.status = response.StatusCode
	defer func() {
		if response.Body.Close() != nil {
			err = errImagesMemoryHTTP
		}
	}()
	if response.StatusCode != status {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4097))
		return result, errImagesMemoryHTTP
	}
	result.etag = response.Header.Get("ETag")
	if !imagesMemoryValidETag(result.etag) || etag != "" && etag != result.etag || !strings.Contains(response.Header.Get("Cache-Control"), "private") {
		return result, errImagesMemoryHTTP
	}
	if method == "HEAD" || status == 304 {
		body, e := io.ReadAll(io.LimitReader(response.Body, 1))
		if e != nil || len(body) != 0 {
			return result, errImagesMemoryHTTP
		}
		return result, nil
	}
	if response.Header.Get("Content-Type") != "image/jpeg" || response.ContentLength <= 0 || response.ContentLength > 2<<20 {
		return result, errImagesMemoryHTTP
	}
	digest := sha256.New()
	limited := &io.LimitedReader{R: response.Body, N: (2 << 20) + 1}
	reader := bufio.NewReaderSize(io.TeeReader(limited, digest), 16<<10)
	configuration, e := jpeg.DecodeConfig(reader)
	wantW, wantH := 160, 240
	if index < 64 {
		wantW, wantH = 1024, 640
	}
	if e != nil || configuration.Width != wantW || configuration.Height != wantH {
		return result, errImagesMemoryHTTP
	}
	var buffer [32 << 10]byte
	if _, e = io.CopyBuffer(io.Discard, reader, buffer[:]); e != nil {
		return result, errImagesMemoryHTTP
	}
	result.bytes = (2 << 20) + 1 - limited.N
	if result.bytes != response.ContentLength || result.bytes > 2<<20 || result.etag != `"`+hex.EncodeToString(digest.Sum(nil))+`"` {
		return result, errImagesMemoryHTTP
	}
	return result, nil
}

func imagesMemoryValidETag(value string) bool {
	if len(value) != 66 || value[0] != '"' || value[65] != '"' {
		return false
	}
	for _, c := range value[1:65] {
		if !(c >= 'a' && c <= 'f' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

func imagesMemoryIdle(ctx context.Context, life *lifetime) (imagesMemoryStats, error) {
	c, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	for {
		stats := imagesMemoryStatsValue(life.imageStats())
		if stats.Active == 0 && stats.ReservedBytes == 0 {
			return stats, nil
		}
		select {
		case <-c.Done():
			return stats, errImagesMemoryHTTP
		case <-time.After(time.Millisecond):
		}
	}
}

func imagesMemoryPhaseValid(phase imagesMemoryPhaseResult, cold int64, warm bool) bool {
	b, a := phase.Before, phase.After
	if b.Active != 0 || a.Active != 0 || b.ReservedBytes != 0 || a.ReservedBytes != 0 || a.Failed != b.Failed || a.Busy != b.Busy || a.CacheEntries < 1 || a.CacheEntries > 128 || a.CacheBytes <= 0 || a.CacheBytes > 32<<20 || phase.FinishedNanos <= phase.StartedNanos || phase.ElapsedNanos != phase.FinishedNanos-phase.StartedNanos {
		return false
	}
	if warm {
		return phase.Get200 == 64 && phase.Head200 == 64 && phase.NotModified304 == 64 && a.Admitted == b.Admitted+192 && a.Completed == b.Completed+192 && a.CacheHits == b.CacheHits+192 && a.CacheMisses == b.CacheMisses && a.Decodes == b.Decodes
	}
	return phase.Get200 == cold && phase.Head200 == 0 && phase.NotModified304 == 0 && a.Admitted == b.Admitted+uint64(cold) && a.Completed == b.Completed+uint64(cold) && a.CacheMisses == b.CacheMisses+uint64(cold) && a.Decodes == b.Decodes+uint64(cold) && a.CacheHits == b.CacheHits
}

func runImagesMemoryCold(ctx context.Context, profile *imagesMemoryProfile, life *lifetime, client *http.Client, address, token string, total int, report *imagesMemoryPhaseResult, tags *[64]string) string {
	return runImagesColdSince(ctx, profile.started, life, client, address, token, total, report, tags)
}

// A shared monotonic origin lets the soak repeat the same real HTTP workload
// without constructing or resetting the one-hour memory sampler.
func runImagesColdSince(ctx context.Context, started time.Time, life *lifetime, client *http.Client, address, token string, total int, report *imagesMemoryPhaseResult, tags *[64]string) string {
	var err error
	report.Before, err = imagesMemoryIdle(ctx, life)
	if err != nil {
		return "cold_not_idle"
	}
	report.StartedNanos = time.Since(started).Nanoseconds()
	c, cancel := context.WithCancel(ctx)
	defer cancel()
	work := make(chan int, 32)
	var joined sync.WaitGroup
	var completed, transferred atomic.Int64
	var failed atomic.Bool
	for worker := 0; worker < 2; worker++ {
		joined.Add(1)
		go func() {
			defer joined.Done()
			for index := range work {
				if c.Err() != nil {
					return
				}
				result, e := imagesMemoryGET(c, client, address, token, index, total, "GET", "", 200)
				if e != nil {
					failed.Store(true)
					cancel()
					return
				}
				if index >= total-64 {
					tags[index-(total-64)] = result.etag
				}
				completed.Add(1)
				transferred.Add(result.bytes)
			}
		}()
	}
produce:
	for i := 0; i < total; i++ {
		select {
		case work <- i:
		case <-c.Done():
			break produce
		}
	}
	close(work)
	joined.Wait()
	report.Get200, report.HTTPBytes = completed.Load(), transferred.Load()
	report.FinishedNanos = time.Since(started).Nanoseconds()
	report.ElapsedNanos = report.FinishedNanos - report.StartedNanos
	report.After, err = imagesMemoryIdle(ctx, life)
	if failed.Load() || ctx.Err() != nil || err != nil || !imagesMemoryPhaseValid(*report, int64(total), false) {
		return "cold_image_processing_failed"
	}
	return ""
}

func runImagesMemoryWarm(ctx context.Context, profile *imagesMemoryProfile, life *lifetime, client *http.Client, address, token string, total int, report *imagesMemoryPhaseResult, tags [64]string) string {
	return runImagesWarmSince(ctx, profile.started, life, client, address, token, total, report, tags)
}

func runImagesWarmSince(ctx context.Context, started time.Time, life *lifetime, client *http.Client, address, token string, total int, report *imagesMemoryPhaseResult, tags [64]string) string {
	var err error
	report.Before, err = imagesMemoryIdle(ctx, life)
	if err != nil {
		return "warm_not_idle"
	}
	report.StartedNanos = time.Since(started).Nanoseconds()
	for offset, etag := range tags {
		if !imagesMemoryValidETag(etag) {
			return "warm_etag_missing"
		}
		index := total - 64 + offset
		for _, step := range []struct {
			method, conditional string
			status              int
		}{{"GET", "", 200}, {"HEAD", "", 200}, {"GET", etag, 304}} {
			result, e := imagesMemoryGET(ctx, client, address, token, index, total, step.method, step.conditional, step.status)
			if e != nil || result.etag != etag {
				return "warm_image_processing_failed"
			}
			report.HTTPBytes += result.bytes
			if step.status == 304 {
				report.NotModified304++
			} else if step.method == "HEAD" {
				report.Head200++
			} else {
				report.Get200++
			}
		}
	}
	report.FinishedNanos = time.Since(started).Nanoseconds()
	report.ElapsedNanos = report.FinishedNanos - report.StartedNanos
	report.After, err = imagesMemoryIdle(ctx, life)
	if err != nil || !imagesMemoryPhaseValid(*report, 0, true) {
		return "warm_cache_result_mismatch"
	}
	return ""
}

func imagesMemoryErrorResponse(ctx context.Context, client *http.Client, address, token string, index int, etag string, status int, code string) (http.Header, error) {
	want := map[string]string{"authentication_required": "Authentication is required.", "not_found": "Resource was not found.", "image_busy": "Image processing is busy. Try again later.", "image_unavailable": "Image is unavailable.", "image_too_large": "Image exceeds the processing limit.", "image_unsupported": "Image format is not supported."}[code]
	if want == "" {
		return nil, errImagesMemoryHTTP
	}
	request, err := http.NewRequestWithContext(ctx, "GET", address+"/images/Primary/"+imagesMemoryItem(index)+"?width=160&height=240&format=jpeg", nil)
	if err != nil {
		return nil, errImagesMemoryHTTP
	}
	request.Header.Set("Accept-Language", "en-US")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if etag != "" {
		request.Header.Set("If-None-Match", etag)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, errImagesMemoryHTTP
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 4097))
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil || len(body) > 4096 || response.StatusCode != status {
		return nil, errImagesMemoryHTTP
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(body, &envelope) != nil || len(envelope) != 1 {
		return nil, errImagesMemoryHTTP
	}
	var problem map[string]json.RawMessage
	if json.Unmarshal(envelope["error"], &problem) != nil || len(problem) != 4 {
		return nil, errImagesMemoryHTTP
	}
	var gotCode, message, trace string
	var details map[string]any
	if json.Unmarshal(problem["code"], &gotCode) != nil || gotCode != code || json.Unmarshal(problem["message"], &message) != nil || message != want || json.Unmarshal(problem["details"], &details) != nil || details == nil || len(details) != 0 || json.Unmarshal(problem["traceId"], &trace) != nil || len(trace) < 1 || len(trace) > 64 {
		return nil, errImagesMemoryHTTP
	}
	for _, c := range trace {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '-') {
			return nil, errImagesMemoryHTTP
		}
	}
	return response.Header, nil
}

func runImagesMemoryNegative(ctx context.Context, store *postgres.Store, registration domain.LibraryRegistration, life *lifetime, client *http.Client, address, token string, total int, tag string, access func(bool) error, report *imagesMemoryNegative) string {
	if _, err := imagesMemoryErrorResponse(ctx, client, address, "", total-1, "", 401, "authentication_required"); err != nil {
		return "unauthenticated_image_failed"
	}
	report.Unauthenticated401 = true
	if access(false) != nil {
		return "http_acl_revoke_failed"
	}
	if _, err := imagesMemoryErrorResponse(ctx, client, address, token, total-1, "", 404, "not_found"); err != nil {
		return "image_acl_denial_failed"
	}
	report.ACLDenied404 = true
	if _, err := imagesMemoryErrorResponse(ctx, client, address, token, total-1, tag, 404, "not_found"); err != nil {
		return "warm_image_acl_denial_failed"
	}
	report.ACLRevoked404 = true
	if access(true) != nil {
		return "http_acl_restore_failed"
	}
	cases := []struct {
		name     string
		status   int
		code     string
		observed *bool
	}{
		{"unsupported", 415, "image_unsupported", &report.Unsupported415},
		{"corrupt", 404, "image_unavailable", &report.Corrupt404},
		{"oversized-source", 413, "image_too_large", &report.SourceLimit413},
		{"oversized-dimensions", 413, "image_too_large", &report.DimensionLimit413},
	}
	for offset, tc := range cases {
		index := total + offset
		if _, err := store.Pool.Exec(ctx, `INSERT INTO items(id,library_id,title,kind) VALUES($1::uuid,$2::uuid,'Image negative fixture','Movie')`, imagesMemoryItem(index), registration.Library.ID); err != nil {
			return "negative_fixture_seed_failed"
		}
		if _, err := store.Pool.Exec(ctx, `INSERT INTO media_sources(id,item_id,library_id,root_id,relative_path,content_type) VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5,'video/x-matroska')`, imagesMemorySource(index), imagesMemoryItem(index), registration.Library.ID, registration.RootID, "negative/"+tc.name+"/clip.mkv"); err != nil {
			return "negative_fixture_seed_failed"
		}
		before, err := imagesMemoryIdle(ctx, life)
		if err != nil {
			return "negative_not_idle"
		}
		if _, err := imagesMemoryErrorResponse(ctx, client, address, token, index, "", tc.status, tc.code); err != nil {
			return "negative_image_response_failed"
		}
		after, err := imagesMemoryIdle(ctx, life)
		if err != nil || after.Decodes != before.Decodes || after.Completed != before.Completed || after.Failed != before.Failed+1 {
			return "negative_preflight_decode_failed"
		}
		*tc.observed = true
		if !imagesMemoryScratchEmpty() {
			return "negative_scratch_retained"
		}
	}
	report.NoPreflightDecode, report.ErrorsRedacted, report.ScratchEmpty = true, true, true
	return ""
}

func waitImagesMemoryBlocked(ctx context.Context, store *postgres.Store, applicationName string, count int) error {
	c, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	for {
		var observed int
		if err := store.Pool.QueryRow(c, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND application_name=$1 AND state='active' AND wait_event_type='Lock' AND query LIKE '%JOIN media_sources m%'`, applicationName).Scan(&observed); err != nil {
			return errImagesMemoryHTTP
		}
		if observed == count {
			return nil
		}
		select {
		case <-c.Done():
			return errImagesMemoryHTTP
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func runImagesMemoryCancellation(ctx context.Context, store *postgres.Store, applicationName string, life *lifetime, client *http.Client, address, token string, total int, report *imagesMemoryCancellation) string {
	lock, err := store.Pool.Begin(ctx)
	if err != nil {
		return "cancellation_lock_failed"
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = lock.Rollback(c)
	}()
	if _, err = lock.Exec(ctx, `LOCK TABLE media_sources IN ACCESS EXCLUSIVE MODE`); err != nil {
		return "cancellation_lock_failed"
	}
	first, cancelFirst := context.WithCancel(ctx)
	defer cancelFirst()
	second, cancelSecond := context.WithCancel(ctx)
	defer cancelSecond()
	firstDone, secondDone := make(chan error, 1), make(chan error, 1)
	go func() {
		_, e := imagesMemoryGET(first, client, address, token, total-1, total, "GET", "", 200)
		firstDone <- e
	}()
	go func() {
		_, e := imagesMemoryGET(second, client, address, token, total-2, total, "GET", "", 200)
		secondDone <- e
	}()
	firstJoined, secondJoined := false, false
	defer func() {
		cancelFirst()
		cancelSecond()
		for _, value := range []struct {
			done   chan error
			joined bool
		}{{firstDone, firstJoined}, {secondDone, secondJoined}} {
			if !value.joined {
				select {
				case <-value.done:
				case <-time.After(3 * time.Second):
				}
			}
		}
	}()
	if waitImagesMemoryBlocked(ctx, store, applicationName, 2) != nil {
		return "two_http_slots_not_observed"
	}
	report.BlockedQueries = 2
	before := life.imageStats()
	busyTransport := &http.Transport{MaxConnsPerHost: 1, DisableCompression: true}
	defer busyTransport.CloseIdleConnections()
	busyClient := &http.Client{Transport: busyTransport, Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	header, err := imagesMemoryErrorResponse(ctx, busyClient, address, token, total-3, "", 503, "image_busy")
	if err != nil || header.Get("Retry-After") != "1" || life.imageStats().Admitted != before.Admitted {
		return "http_busy_admission_failed"
	}
	report.Busy503, report.RetryAfter = true, true
	cancelFirst()
	select {
	case e := <-firstDone:
		firstJoined = true
		if !errors.Is(e, context.Canceled) {
			return "client_cancellation_failed"
		}
	case <-time.After(time.Second):
		return "client_cancellation_not_joined"
	}
	report.CancelledRequest = true
	if waitImagesMemoryBlocked(ctx, store, applicationName, 1) != nil {
		return "cancelled_query_not_released"
	}
	report.CancelledQueryReleased = true
	if lock.Rollback(ctx) != nil {
		return "cancellation_lock_cleanup_failed"
	}
	select {
	case e := <-secondDone:
		secondJoined = true
		if e != nil {
			return "remaining_image_failed"
		}
	case <-time.After(3 * time.Second):
		return "remaining_image_not_joined"
	}
	if _, err := imagesMemoryGET(ctx, client, address, token, total-1, total, "GET", "", 200); err != nil {
		return "image_recovery_failed"
	}
	if _, err := imagesMemoryIdle(ctx, life); err != nil || !imagesMemoryScratchEmpty() {
		return "cancellation_image_resources_retained"
	}
	report.Recovered200 = true
	return ""
}

func imagesMemoryScratchEmpty() bool {
	file, err := os.Open(imagesMemoryScratch)
	if err != nil {
		return false
	}
	entries, readErr := file.ReadDir(1)
	closeErr := file.Close()
	return len(entries) == 0 && readErr == io.EOF && closeErr == nil
}

type imagesMemoryFixtureSample struct {
	path   string
	info   os.FileInfo
	digest [32]byte
}

func readImagesMemoryFixture(path string) (imagesMemoryFixtureSample, error) {
	value := imagesMemoryFixtureSample{path: path}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 16<<20 {
		return value, errImagesMemoryHTTP
	}
	file, err := os.Open(path)
	if err != nil {
		return value, errImagesMemoryHTTP
	}
	opened, statErr := file.Stat()
	digest := sha256.New()
	var buffer [32 << 10]byte
	count, readErr := io.CopyBuffer(digest, io.LimitReader(file, (16<<20)+1), buffer[:])
	after, afterErr := file.Stat()
	closeErr := file.Close()
	if statErr != nil || afterErr != nil || readErr != nil || closeErr != nil || !os.SameFile(info, opened) || !os.SameFile(opened, after) || count != info.Size() || after.Size() != count || !info.ModTime().Equal(after.ModTime()) {
		return value, errImagesMemoryHTTP
	}
	value.info = after
	copy(value.digest[:], digest.Sum(nil))
	return value, nil
}

func snapshotImagesMemoryFixtures(total int) ([]imagesMemoryFixtureSample, error) {
	boundary := max(100, total/20)
	selected := map[int]bool{0: true, 63: true, 64: true, boundary - 1: true, boundary: true, total - 65: true, total - 64: true, total - 1: true}
	for i := 0; i < 16; i++ {
		selected[i*(total-1)/15] = true
	}
	indices := make([]int, 0, len(selected))
	for index := range selected {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	result := make([]imagesMemoryFixtureSample, 0, len(indices)*2)
	for _, index := range indices {
		for _, relative := range []string{imagesMemoryMediaPath(index), imagesMemoryPosterPath(index, total)} {
			value, err := readImagesMemoryFixture(filepath.Join("/media", filepath.FromSlash(relative)))
			if err != nil {
				return nil, err
			}
			result = append(result, value)
		}
	}
	if len(result) > 64 {
		return nil, errImagesMemoryHTTP
	}
	return result, nil
}

func verifyImagesMemoryFixtures(samples []imagesMemoryFixtureSample) error {
	for _, before := range samples {
		after, err := readImagesMemoryFixture(before.path)
		if err != nil || !os.SameFile(before.info, after.info) || before.info.Size() != after.info.Size() || !before.info.ModTime().Equal(after.info.ModTime()) || before.info.Mode() != after.info.Mode() || before.digest != after.digest {
			return errImagesMemoryHTTP
		}
	}
	return nil
}

func TestImagesMemoryFixtureContract(t *testing.T) {
	for _, total := range []int{1000, 100000} {
		var png, jpegCount int
		for index := 0; index < total; index++ {
			if !domain.ValidID(imagesMemoryItem(index)) || !domain.ValidID(imagesMemorySource(index)) || imagesMemoryItem(index) == imagesMemorySource(index) {
				t.Fatal("fixture identity is invalid")
			}
			media, poster := imagesMemoryMediaPath(index), imagesMemoryPosterPath(index, total)
			posterBase := strings.TrimSuffix(strings.TrimSuffix(poster, ".png"), ".jpg")
			if !strings.HasPrefix(media, fmt.Sprintf("dir-%05d/", index/10)) || posterBase != strings.TrimSuffix(media, ".mkv")+"-poster" {
				t.Fatal("fixture path relationship differs")
			}
			if strings.HasSuffix(poster, ".png") {
				png++
			} else {
				jpegCount++
			}
			if index >= total-64 && !strings.HasSuffix(poster, ".jpg") {
				t.Fatal("warm tail is not JPEG")
			}
		}
		if png != max(100, total/20) || jpegCount != total-png {
			t.Fatal("fixture format count differs")
		}
	}
}

func TestImagesMemoryHTTPStreamContract(t *testing.T) {
	var encoded bytes.Buffer
	if jpeg.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 160, 240)), nil) != nil {
		t.Fatal("encode small response fixture")
	}
	original := append([]byte(nil), encoded.Bytes()...)
	sum := sha256.Sum256(original)
	tag := `"` + hex.EncodeToString(sum[:]) + `"`
	for _, mode := range []string{"valid", "etag", "truncated", "wrong-dimension", "oversized", "status"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				data := original
				header := tag
				if mode == "etag" {
					header = `"` + strings.Repeat("0", 64) + `"`
				}
				if mode == "wrong-dimension" {
					var b bytes.Buffer
					_ = jpeg.Encode(&b, image.NewRGBA(image.Rect(0, 0, 16, 24)), nil)
					data = b.Bytes()
					s := sha256.Sum256(data)
					header = `"` + hex.EncodeToString(s[:]) + `"`
				}
				w.Header().Set("Content-Type", "image/jpeg")
				w.Header().Set("Cache-Control", "private, no-cache")
				w.Header().Set("ETag", header)
				w.Header().Set("Content-Length", strconv.Itoa(len(data)))
				if mode == "oversized" {
					w.Header().Set("Content-Length", strconv.Itoa((2<<20)+1))
				}
				if mode == "status" {
					w.WriteHeader(503)
				}
				if mode == "truncated" {
					data = data[:len(data)-5]
				}
				_, _ = w.Write(data)
			}))
			defer server.Close()
			result, err := imagesMemoryGET(context.Background(), server.Client(), server.URL, "", 999, 1000, "GET", "", 200)
			if mode == "valid" {
				if err != nil || result.bytes != int64(len(original)) || result.etag != tag {
					t.Fatal("valid streamed response rejected")
				}
			} else if !errors.Is(err, errImagesMemoryHTTP) {
				t.Fatal("invalid streamed response accepted")
			}
		})
	}
}

func TestImagesMemoryCountersCannotReplaceTransferEvidence(t *testing.T) {
	phase := imagesMemoryPhaseResult{StartedNanos: 1, FinishedNanos: 3, ElapsedNanos: 2, Get200: 1000, After: imagesMemoryStats{Admitted: 1000, Completed: 1000, CacheMisses: 1000, Decodes: 1000, CacheEntries: 128, CacheBytes: 1024}}
	if !imagesMemoryPhaseValid(phase, 1000, false) {
		t.Fatal("complete cold phase rejected")
	}
	for _, mode := range []string{"not-delivered", "not-prepared", "active", "hidden-hit", "failure"} {
		t.Run(mode, func(t *testing.T) {
			bad := phase
			switch mode {
			case "not-delivered":
				bad.Get200--
			case "not-prepared":
				bad.After.Completed--
			case "active":
				bad.After.Active = 1
			case "hidden-hit":
				bad.After.CacheHits = 1
			case "failure":
				bad.After.Failed = 1
			}
			if imagesMemoryPhaseValid(bad, 1000, false) {
				t.Fatal("partial cold evidence accepted")
			}
		})
	}
}

func TestImagesMemoryErrorEvidenceRejectsPrivateData(t *testing.T) {
	for _, mode := range []string{"fixed", "message", "details", "extra-field"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				problem := map[string]any{"code": "image_unavailable", "message": "Image is unavailable.", "details": map[string]any{}, "traceId": "fixture-trace-123"}
				switch mode {
				case "message":
					problem["message"] = "PRIVATE_SOURCE_PATH"
				case "details":
					problem["details"] = map[string]any{"source": "PRIVATE_SOURCE_PATH"}
				case "extra-field":
					problem["source"] = "PRIVATE_SOURCE_PATH"
				}
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": problem})
			}))
			defer server.Close()
			_, err := imagesMemoryErrorResponse(context.Background(), server.Client(), server.URL, "", 0, "", 404, "image_unavailable")
			if mode == "fixed" && err != nil || mode != "fixed" && !errors.Is(err, errImagesMemoryHTTP) {
				t.Fatal("unsafe error evidence decision")
			}
			if err != nil && strings.Contains(err.Error(), "PRIVATE") {
				t.Fatal("acceptance error exposed response content")
			}
		})
	}
}
