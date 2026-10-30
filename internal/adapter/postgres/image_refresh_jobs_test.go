package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/images"
	"github.com/MoYuanCN/Jelee/internal/adapter/scan"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/jobs"
	"github.com/MoYuanCN/Jelee/internal/platform/outbound"
)

// refreshOrigin is a local HTTP origin standing in for remote artwork hosts.
// URLs name https://art.example/<path>; the test transport sends them to
// the httptest server instead, so no request leaves the machine. Every path
// "ok-N.png" serves a distinct PNG, "missing.png" answers 404 and a path
// with a hook runs it before answering.
type refreshOrigin struct {
	srv      *httptest.Server
	mu       sync.Mutex
	requests map[string]int
	hooks    map[string]func(*http.Request)
}

func newRefreshOrigin(t *testing.T) *refreshOrigin {
	t.Helper()
	o := &refreshOrigin{requests: map[string]int{}, hooks: map[string]func(*http.Request){}}
	o.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		o.mu.Lock()
		o.requests[name]++
		hook := o.hooks[name]
		o.mu.Unlock()
		if hook != nil {
			hook(r)
		}
		if r.Context().Err() != nil {
			return
		}
		if name == "missing.png" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(refreshPNG(name))
	}))
	t.Cleanup(o.srv.Close)
	return o
}

func (o *refreshOrigin) hook(name string, f func(*http.Request)) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.hooks[name] = f
}

func (o *refreshOrigin) count(name string) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.requests[name]
}

func (o *refreshOrigin) total() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	n := 0
	for _, v := range o.requests {
		n += v
	}
	return n
}

// Stream implements images.FetchTransport over the local server.
func (o *refreshOrigin) Stream(ctx context.Context, target string, _ outbound.StreamOptions) (*outbound.StreamResponse, error) {
	path, ok := strings.CutPrefix(target, "https://art.example/")
	if !ok {
		return nil, outbound.ErrDenied
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, o.srv.URL+"/"+path, nil)
	if err != nil {
		return nil, outbound.ErrDenied
	}
	response, err := o.srv.Client().Do(request) //nolint:bodyclose // the fetcher closes StreamResponse.Body
	if err != nil {
		return nil, outbound.ErrUnavailable
	}
	return &outbound.StreamResponse{Status: response.StatusCode, ContentType: response.Header.Get("Content-Type"), ContentLength: response.ContentLength,
		RetryAfter: response.Header.Get("Retry-After"), Body: response.Body}, nil
}

func refreshPNG(name string) []byte {
	sum := sha256.Sum256([]byte(name))
	picture := image.NewNRGBA(image.Rect(0, 0, 12, 8))
	for y := range 8 {
		for x := range 12 {
			picture.Set(x, y, color.NRGBA{R: sum[x], G: sum[y+12], B: uint8(x * y), A: 255})
		}
	}
	var encoded bytes.Buffer
	_ = png.Encode(&encoded, picture)
	return encoded.Bytes()
}

func refreshURL(name string) string { return "https://art.example/" + name }

// refreshWorld is a job fixture with an image store and a refresher that
// fetches from the local origin.
type refreshWorld struct {
	jobFixture
	origin    *refreshOrigin
	store     *images.Store
	refresher *images.ItemImageRefresher
}

func newRefreshWorld(t *testing.T) refreshWorld {
	t.Helper()
	f := newJobFixtureWithin(t, 3*time.Minute)
	base := t.TempDir()
	temp, root := filepath.Join(base, "temp"), filepath.Join(base, "store")
	for _, dir := range []string{temp, root} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	pictures, err := images.OpenStore(context.Background(), images.StoreOptions{Root: root, OriginalBytes: 64 << 20, VariantBytes: 16 << 20, MaxEntries: 1024})
	if err != nil {
		t.Fatal("open image store", err)
	}
	processor, err := images.New(context.Background(), images.Options{TempRoot: temp, MaxConcurrent: 2, MaxImageBytes: 96 << 20, MaxSourceBytes: 16 << 20,
		MaxOutputBytes: 2 << 20, CacheBytes: 32 << 20, MaxOutputDimension: 1024, CacheEntries: 128, DefaultQuality: 85, Timeout: 15 * time.Second,
		CacheTTL: 300 * time.Second, Store: pictures, Index: f.s})
	if err != nil {
		t.Fatal("create image processor", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = processor.Shutdown(ctx)
		_ = pictures.Close(ctx)
	})
	origin := newRefreshOrigin(t)
	fetcher, err := images.NewRemoteFetcher(origin, images.FetchOptions{HostInterval: time.Millisecond, MaxAttempts: 1, Timeout: 20 * time.Second, AttemptTimeout: 10 * time.Second, MaxBytes: processor.ItemImageLimit()})
	if err != nil {
		t.Fatal(err)
	}
	refresher, err := images.NewItemImageRefresher(fetcher, processor)
	if err != nil {
		t.Fatal(err)
	}
	return refreshWorld{jobFixture: f, origin: origin, store: pictures, refresher: refresher}
}

func (w refreshWorld) worker(t *testing.T) *app.ImageRefreshWorker {
	t.Helper()
	worker, err := app.NewImageRefreshWorker(w.refresher, w.s, 0)
	if err != nil {
		t.Fatal(err)
	}
	return worker
}

// start runs a job runner that claims image_refresh jobs.
func (w refreshWorld) start(t *testing.T) func() {
	t.Helper()
	opts := jobs.DefaultOptions()
	opts.Workers, opts.PollInterval = 1, 100*time.Millisecond
	opts.ImageRefresh = &jobs.ImageRefreshOptions{Worker: w.worker(t), Repository: w.s}
	runner, err := jobs.New(w.s, scan.New(), opts, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err = runner.Start(w.ctx); err != nil {
		t.Fatal(err)
	}
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := runner.Stop(ctx); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(stop)
	return stop
}

func (w refreshWorld) submit(t *testing.T, key string) domain.Job {
	t.Helper()
	job, _, err := w.s.SubmitImageRefreshJob(w.ctx, w.a, w.registration.Library.ID, key, domain.JobPriorityManual, w.policy)
	if err != nil {
		t.Fatal("submit image refresh", err)
	}
	return job
}

func (w refreshWorld) wait(t *testing.T, job string) domain.Job {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		value, err := w.s.GetJob(w.ctx, w.a, job)
		if err != nil {
			t.Fatal(err)
		}
		if value.State != domain.JobQueued && value.State != domain.JobRunning {
			return value
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("image refresh did not finish")
	return domain.Job{}
}

func (w refreshWorld) state(t *testing.T, job string) domain.ImageRefreshJobState {
	t.Helper()
	state, err := w.s.GetImageRefreshState(w.ctx, w.a, job)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

// remote inserts one remote row of a new item, the way scan ingestion or a
// person would write it.
func (w refreshWorld) remote(t *testing.T, name string, locked, manual bool) (item, image string) {
	t.Helper()
	item = metadataItem(t, w.jobFixture)
	if err := w.s.Pool.QueryRow(w.ctx, `INSERT INTO item_images(item_id,library_id,image_type,image_index,source_kind,remote_url,locked,manual)
 VALUES($1::uuid,$2::uuid,'Primary',0,'remote',$3,$4,$5) RETURNING id::text`, item, w.registration.Library.ID, refreshURL(name), locked, manual).Scan(&image); err != nil {
		t.Fatal(err)
	}
	return item, image
}

// rows renders every image row of the library, everything a refresh may
// change included.
func (w refreshWorld) rows(t *testing.T) map[string]string {
	t.Helper()
	rows, err := w.s.Pool.Query(w.ctx, `SELECT id::text,to_jsonb(g)::text FROM item_images g WHERE library_id=$1::uuid`, w.registration.Library.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, value string
		if err := rows.Scan(&id, &value); err != nil {
			t.Fatal(err)
		}
		out[id] = value
	}
	return out
}

func (w refreshWorld) content(t *testing.T, image string) []byte {
	t.Helper()
	var digest []byte
	if err := w.s.Pool.QueryRow(w.ctx, `SELECT content_sha256 FROM item_images WHERE id=$1::uuid`, image).Scan(&digest); err != nil {
		t.Fatal(err)
	}
	return digest
}

type refreshFailure struct {
	failures int
	reason   string
	status   *int
	wait     time.Duration
}

func (w refreshWorld) failure(t *testing.T, image string) (refreshFailure, bool) {
	t.Helper()
	var f refreshFailure
	var seconds float64
	err := w.s.Pool.QueryRow(w.ctx, `SELECT failures,reason,http_status,extract(epoch FROM retry_at-attempted_at) FROM item_image_fetch_failures WHERE image_id=$1::uuid`, image).Scan(&f.failures, &f.reason, &f.status, &seconds)
	if errors.Is(storageError(err), domain.ErrNotFound) {
		return f, false
	}
	if err != nil {
		t.Fatal(err)
	}
	f.wait = time.Duration(seconds * float64(time.Second))
	return f, true
}

// TestImageRefreshJobFetchesOnlyUnlockedRemoteRows: the refresh fills every
// unlocked, non-manual remote row without content, records a failure per
// row without affecting the others, leaves locked, manual, local and
// already fetched rows byte for byte alone, and a second run is idempotent.
func TestImageRefreshJobFetchesOnlyUnlockedRemoteRows(t *testing.T) {
	w := newRefreshWorld(t)
	var fetchable []string
	for i := range 6 {
		_, image := w.remote(t, fmt.Sprintf("ok-%d.png", i), false, false)
		fetchable = append(fetchable, image)
	}
	_, missing := w.remote(t, "missing.png", false, false)
	_, locked := w.remote(t, "locked.png", true, true)
	_, manual := w.remote(t, "manual.png", false, true)
	present, presentImage := w.remote(t, "present.png", false, false)
	if _, err := w.s.Pool.Exec(w.ctx, `UPDATE item_images SET content_sha256=$2,format='png',byte_size=1,fetched_at=now() WHERE id=$1::uuid`, presentImage, itemImageDigest("present")); err != nil {
		t.Fatal(err)
	}
	itemImageUpsert(t, w.jobFixture, itemImageLocal(w.jobFixture, present, "poster.jpg"))
	before := w.rows(t)
	stop := w.start(t)

	job := w.wait(t, w.submit(t, "refresh-1").ID)
	state := w.state(t, job.ID)
	if job.State != domain.JobSucceeded || job.Kind != domain.JobImageRefresh || state.Fetched != 6 || state.Failed != 1 || state.Deferred != 0 || state.Skipped != 0 ||
		job.Files != 6 || job.Skipped != 1 || state.CursorImageID == nil {
		t.Fatalf("first run: job=%+v state=%+v", job, state)
	}
	for i, image := range fetchable {
		name := fmt.Sprintf("ok-%d.png", i)
		want := sha256.Sum256(refreshPNG(name))
		if !bytes.Equal(w.content(t, image), want[:]) || w.origin.count(name) != 1 {
			t.Fatal("row not fetched exactly once", name)
		}
		if !w.store.HasOriginal(want) {
			t.Fatal("fetched original not in the store", name)
		}
		var width, height int
		var format string
		var average, dominant *int
		if err := w.s.Pool.QueryRow(w.ctx, `SELECT width,height,format,average_color,dominant_color FROM item_images WHERE id=$1::uuid`, image).Scan(&width, &height, &format, &average, &dominant); err != nil || width != 12 || height != 8 || format != "png" {
			t.Fatal("content columns", width, height, format, err)
		}
		if average == nil || dominant == nil {
			t.Fatal("fetched row lacks its sampled colors", name)
		}
		if _, ok := w.failure(t, image); ok {
			t.Fatal("success left a failure row")
		}
	}
	failure, ok := w.failure(t, missing)
	if !ok || failure.failures != 1 || failure.reason != domain.ImageFetchNotFound || failure.status == nil || *failure.status != 404 || failure.wait != domain.ImageFetchPermanentRetryDelay {
		t.Fatalf("failure row: %+v %t", failure, ok)
	}
	after := w.rows(t)
	for _, image := range []string{missing, locked, manual, presentImage} {
		if after[image] != before[image] {
			t.Fatal("refresh rewrote a row it must not change", image)
		}
	}
	for id, row := range before {
		if strings.Contains(row, `"source_kind": "local"`) && after[id] != row {
			t.Fatal("refresh changed a local row")
		}
	}
	if w.origin.count("locked.png") != 0 || w.origin.count("manual.png") != 0 || w.origin.count("present.png") != 0 {
		t.Fatal("refresh fetched a locked, manual or present row")
	}

	// A second run is idempotent: nothing is fetched again, the failed row
	// waits for its retry time, and no row changes.
	requests := w.origin.total()
	job = w.wait(t, w.submit(t, "refresh-2").ID)
	state = w.state(t, job.ID)
	if job.State != domain.JobSucceeded || state.Fetched != 0 || state.Failed != 0 || state.Deferred != 1 || state.Skipped != 0 || w.origin.total() != requests {
		t.Fatalf("second run: job=%+v state=%+v requests=%d", job, state, w.origin.total()-requests)
	}
	again := w.rows(t)
	for id, row := range after {
		if again[id] != row {
			t.Fatal("idempotent run changed a row", id)
		}
	}

	// Once the retry time passes the row is tried again and the delay
	// doubles; another URL starts a fresh count.
	if _, err := w.s.Pool.Exec(w.ctx, `UPDATE item_image_fetch_failures SET attempted_at=attempted_at-interval '2 days',retry_at=now()-interval '1 second' WHERE image_id=$1::uuid`, missing); err != nil {
		t.Fatal(err)
	}
	job = w.wait(t, w.submit(t, "refresh-3").ID)
	if state = w.state(t, job.ID); state.Failed != 1 || w.origin.count("missing.png") != 2 {
		t.Fatalf("retry run: %+v", state)
	}
	if failure, _ = w.failure(t, missing); failure.failures != 2 || failure.wait != 2*domain.ImageFetchPermanentRetryDelay {
		t.Fatalf("second failure: %+v", failure)
	}
	if _, err := w.s.Pool.Exec(w.ctx, `UPDATE item_images SET remote_url=$2 WHERE id=$1::uuid`, missing, refreshURL("ok-moved.png")); err != nil {
		t.Fatal(err)
	}
	job = w.wait(t, w.submit(t, "refresh-4").ID)
	want := sha256.Sum256(refreshPNG("ok-moved.png"))
	if state = w.state(t, job.ID); state.Fetched != 1 || state.Deferred != 0 || !bytes.Equal(w.content(t, missing), want[:]) {
		t.Fatalf("changed URL: %+v", state)
	}
	if _, ok := w.failure(t, missing); ok {
		t.Fatal("fetched row kept its failure")
	}
	stop()
}

// TestImageRefreshJobLockWinsDuringFetch: a person locking (or writing) the
// row while its image downloads wins; the fetched content is not written.
func TestImageRefreshJobLockWinsDuringFetch(t *testing.T) {
	w := newRefreshWorld(t)
	_, first := w.remote(t, "ok-a.png", false, false)
	_, raced := w.remote(t, "ok-b.png", false, false)
	_, last := w.remote(t, "ok-c.png", false, false)
	w.origin.hook("ok-b.png", func(*http.Request) {
		if _, err := w.s.Pool.Exec(context.Background(), `UPDATE item_images SET locked=true,manual=true WHERE id=$1::uuid`, raced); err != nil {
			t.Error(err)
		}
	})
	w.start(t)
	job := w.wait(t, w.submit(t, "refresh-race").ID)
	state := w.state(t, job.ID)
	if job.State != domain.JobSucceeded || state.Fetched != 2 || state.Skipped != 1 || state.Failed != 0 {
		t.Fatalf("race: %+v %+v", job, state)
	}
	if w.content(t, raced) != nil || w.content(t, first) == nil || w.content(t, last) == nil {
		t.Fatal("a row locked during its fetch received content")
	}
}

// TestImageRefreshJobCancelAndResume: cancelling stops during a fetch and
// keeps every recorded row; a job whose lease is lost resumes after its
// cursor without fetching a recorded row again.
func TestImageRefreshJobCancelAndResume(t *testing.T) {
	w := newRefreshWorld(t)
	names := make([]string, 8)
	ids := make([]string, 8)
	for i := range names {
		names[i] = fmt.Sprintf("ok-%d.png", i)
		_, ids[i] = w.remote(t, names[i], false, false)
	}
	// Rows are visited in ID order; name the fetch order.
	var order []string
	rows, err := w.s.Pool.Query(w.ctx, `SELECT remote_url FROM item_images WHERE library_id=$1::uuid ORDER BY id`, w.registration.Library.ID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var url string
		if err := rows.Scan(&url); err != nil {
			t.Fatal(err)
		}
		order = append(order, strings.TrimPrefix(url, "https://art.example/"))
	}
	rows.Close()

	// Cancellation: the fourth fetch blocks until the job is cancelled.
	entered := make(chan struct{})
	w.origin.hook(order[3], func(r *http.Request) {
		close(entered)
		<-r.Context().Done()
	})
	stopFirst := w.start(t)
	job := w.submit(t, "refresh-cancel")
	select {
	case <-entered:
	case <-time.After(30 * time.Second):
		t.Fatal("blocking fetch never started")
	}
	if _, err := w.s.CancelJob(w.ctx, w.a, job.ID); err != nil {
		t.Fatal(err)
	}
	cancelled := w.wait(t, job.ID)
	state := w.state(t, job.ID)
	if cancelled.State != domain.JobCancelled || state.Fetched != 3 || state.Failed != 0 {
		t.Fatalf("cancelled: %+v %+v", cancelled, state)
	}
	fetched := 0
	for _, id := range ids {
		if w.content(t, id) != nil {
			fetched++
		}
	}
	if fetched != 3 {
		t.Fatal("cancelled job wrote rows after the cancel", fetched)
	}
	w.origin.hook(order[3], nil)

	// Resume: a worker runs until two more rows are recorded, then loses
	// its lease (as when the process dies); the next claim continues after
	// the cursor.
	stopFirst()
	resumed := w.submit(t, "refresh-resume")
	lease, err := w.s.ClaimJobWithCapabilities(w.ctx, "refresh-crashed-owner", false, time.Minute, domain.ScanCapabilities{ImageRefresh: true})
	if err != nil || lease.Job.ID != resumed.ID {
		t.Fatal("claim", err)
	}
	ctx, stop := context.WithCancel(w.ctx)
	w.origin.hook(order[5], func(*http.Request) { stop() })
	if err := w.worker(t).Run(ctx, lease); !errors.Is(err, context.Canceled) {
		t.Fatal("interrupted run", err)
	}
	w.origin.hook(order[5], nil)
	partial := w.state(t, resumed.ID)
	if partial.Fetched != 2 || partial.CursorImageID == nil {
		t.Fatalf("interrupted state: %+v", partial)
	}
	if _, err := w.s.Pool.Exec(w.ctx, `UPDATE jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, resumed.ID); err != nil {
		t.Fatal(err)
	}
	w.start(t)
	done := w.wait(t, resumed.ID)
	final := w.state(t, resumed.ID)
	if done.State != domain.JobSucceeded || done.Attempts < 2 || final.Fetched != 5 || final.Skipped != 0 {
		t.Fatalf("resumed: %+v %+v", done, final)
	}
	for i, name := range order {
		want := 1
		if i == 3 || i == 5 {
			want = 2 // the cancelled and the interrupted fetch were never recorded
		}
		if w.origin.count(name) != want {
			t.Fatal("fetch count after resume", name, w.origin.count(name), want)
		}
	}
	for _, id := range ids {
		if w.content(t, id) == nil {
			t.Fatal("row left without content after resume")
		}
	}
	// The interrupted worker's lease is gone: it can no longer record.
	if err := w.s.RecordImageRefresh(w.ctx, lease, []domain.ImageRefreshResult{{Candidate: domain.ImageRefreshCandidate{ImageID: ids[0], ItemID: ids[0], URL: refreshURL("x.png")}, Deferred: true}}); !errors.Is(err, domain.ErrJobLeaseLost) {
		t.Fatal("stale lease recorded", err)
	}
}

// TestImageRefreshJobAdmission covers replay, conflicts and busy libraries.
func TestImageRefreshJobAdmission(t *testing.T) {
	f := newJobFixture(t)
	library := f.registration.Library.ID
	job, replay, err := f.s.SubmitImageRefreshJob(f.ctx, f.a, library, "refresh-admit", domain.JobPriorityBackground, f.policy)
	if err != nil || replay || job.Kind != domain.JobImageRefresh || job.State != domain.JobQueued {
		t.Fatal("submit", err)
	}
	again, replay, err := f.s.SubmitImageRefreshJob(f.ctx, f.a, library, "refresh-admit", domain.JobPriorityBackground, f.policy)
	if err != nil || !replay || again.ID != job.ID {
		t.Fatal("replay", err)
	}
	if _, _, err := f.s.SubmitImageRefreshJob(f.ctx, f.a, library, "refresh-admit", domain.JobPriorityManual, f.policy); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("same key, other priority", err)
	}
	if _, _, err := f.s.SubmitImageRefreshJob(f.ctx, f.a, library, "refresh-other", domain.JobPriorityManual, f.policy); !errors.Is(err, domain.ErrJobBusy) {
		t.Fatal("busy library", err)
	}
	if _, _, err := f.s.SubmitImageRefreshJob(f.ctx, f.a, "00000000-0000-4000-8000-000000000999", "refresh-none", domain.JobPriorityManual, f.policy); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("missing library", err)
	}
	state, err := f.s.GetImageRefreshState(f.ctx, f.a, job.ID)
	if err != nil || state.JobID != job.ID || state.LibraryID != library || state.CursorImageID != nil {
		t.Fatal("initial state", err)
	}
	var audits int
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM audit_logs WHERE event='image.refresh_submitted' AND target_id=$1`, job.ID).Scan(&audits); err != nil || audits != 1 {
		t.Fatal("audit", audits, err)
	}
	if _, err := f.s.GetImageRefreshState(f.ctx, f.a, "not-an-id"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal(err)
	}
	cancelled, err := f.s.CancelJob(f.ctx, f.a, job.ID)
	if err != nil || cancelled.State != domain.JobCancelled {
		t.Fatal("cancel queued", err)
	}
}

func TestImageRefreshMigrationRefusesRetainedJobs(t *testing.T) {
	f := newJobFixture(t)
	if _, _, err := f.s.SubmitImageRefreshJob(f.ctx, f.a, f.registration.Library.ID, "refresh-kept", domain.JobPriorityManual, f.policy); err != nil {
		t.Fatal(err)
	}
	refuseRetainedDowngrade(t, f, "image_refresh_jobs", "a retained image refresh job was downgraded")
}

func TestImageRefreshMigrationRoundTrip(t *testing.T) {
	f, item := itemImageFixture(t)
	itemImageUpsert(t, f, itemImageRemote(item, domain.ImageSourceRemote, "https://art.example/a.png"))
	var image string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT id::text FROM item_images WHERE item_id=$1::uuid`, item).Scan(&image); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO item_image_fetch_failures(image_id,remote_url,failures,reason,attempted_at,retry_at) VALUES($1::uuid,'https://art.example/a.png',1,'timeout',now(),now())`, image); err != nil {
		t.Fatal(err)
	}
	want := migrationVersion(t, "image_refresh_jobs")
	jobMetricMigration(t, f, "down", want-1)
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE jobs SET kind='image_refresh' WHERE false`); err != nil {
		t.Fatal(err)
	}
	var exists bool
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT to_regclass('item_image_fetch_failures') IS NOT NULL OR to_regclass('image_refresh_job_state') IS NOT NULL`).Scan(&exists); err != nil || exists {
		t.Fatal("downgrade kept refresh tables", err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO job_metric_totals(kind,priority) VALUES('image_refresh','manual')`); itemImageSQLState(err) != "23514" {
		t.Fatal("schema below refresh accepted the kind", err)
	}
	jobMetricMigration(t, f, "up", SchemaVersion)
	if rows, err := f.s.ListItemImages(f.ctx, f.a, item); err != nil || len(rows) != 1 {
		t.Fatal("round trip lost the image row", err)
	}
	if _, _, err := f.s.SubmitImageRefreshJob(f.ctx, f.a, f.registration.Library.ID, "refresh-after", domain.JobPriorityManual, f.policy); err != nil {
		t.Fatal("submit after round trip", err)
	}
}
