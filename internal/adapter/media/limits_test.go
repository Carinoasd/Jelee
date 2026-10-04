package media

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	c        chan time.Time
	at       time.Time
	clock    *fakeClock
	finished bool
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) NewTimer(d time.Duration) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{c: make(chan time.Time, 1), at: c.now.Add(d), clock: c}
	c.timers = append(c.timers, t)
	c.fireLocked()
	return t
}

func (t *fakeTimer) C() <-chan time.Time { return t.c }
func (t *fakeTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	active := !t.finished
	t.finished = true
	return active
}

func (c *fakeClock) fireLocked() {
	pending := c.timers[:0]
	for _, t := range c.timers {
		if !t.finished && !t.at.After(c.now) {
			t.finished = true
			t.c <- c.now
		}
		if !t.finished {
			pending = append(pending, t)
		}
	}
	c.timers = pending
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	c.fireLocked()
}

// waitPending waits in real time until n timers are armed.
func (c *fakeClock) waitPending(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		// Stopped timers stay listed until the next fire; count only armed
		// ones, or a finished request's timer satisfies the wait before the
		// stream under test has armed its own.
		c.mu.Lock()
		count := 0
		for _, timer := range c.timers {
			if !timer.finished {
				count++
			}
		}
		c.mu.Unlock()
		if count >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timers armed: %d, want %d", count, n)
		}
		time.Sleep(time.Millisecond)
	}
}

// advanceToNext moves the clock to the earliest armed timer, if any.
func (c *fakeClock) advanceToNext() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.timers) == 0 {
		return false
	}
	next := c.timers[0].at
	for _, t := range c.timers[1:] {
		if t.at.Before(next) {
			next = t.at
		}
	}
	if next.After(c.now) {
		c.now = next
	}
	c.fireLocked()
	return true
}

func ptr[T any](v T) *T { return &v }

func streamPrincipal(user, session string) access.Principal {
	return access.Principal{UserID: user, SessionID: session, Kind: access.ClientNative}
}

func admitOK(t *testing.T, l *limiter, p access.Principal, source string, s Source) *admission {
	t.Helper()
	a, err := l.admit(p, source, s)
	if err != nil {
		t.Fatalf("admit %s/%s/%s: %v", p.UserID, p.SessionID, source, err)
	}
	return a
}

func TestLimiterCountsDistinctPlaybacksPerUser(t *testing.T) {
	l := newLimiter(Limits{StreamLimit: true, MaxStreamsPerUser: 2}, newFakeClock())
	s1, s2 := streamPrincipal("u", "s1"), streamPrincipal("u", "s2")
	first := admitOK(t, l, s1, "a", Source{})
	// Overlapping Range requests of the same playback count once.
	firstRange := admitOK(t, l, s1, "a", Source{})
	second := admitOK(t, l, s2, "b", Source{})
	if _, err := l.admit(s2, "c", Source{}); !errors.Is(err, ErrUserStreamLimit) {
		t.Fatalf("third playback admitted: %v", err)
	}
	admitOK(t, l, streamPrincipal("other", "s3"), "c", Source{}).release()
	first.release()
	if _, err := l.admit(s2, "c", Source{}); !errors.Is(err, ErrUserStreamLimit) {
		t.Fatal("playback released while one of its requests still runs")
	}
	firstRange.release()
	third := admitOK(t, l, s2, "c", Source{})
	second.release()
	third.release()
	if len(l.users) != 0 || len(l.buckets) != 0 {
		t.Fatalf("limiter leaked state: users=%d buckets=%d", len(l.users), len(l.buckets))
	}
}

func TestLimiterOverridesAndSwitches(t *testing.T) {
	p := streamPrincipal("u", "s")
	l := newLimiter(Limits{StreamLimit: true, MaxStreamsPerUser: 4}, newFakeClock())
	one := Source{Limits: domain.DeliveryLimits{MaxStreams: ptr(1)}}
	held := admitOK(t, l, p, "a", one)
	if _, err := l.admit(p, "b", one); !errors.Is(err, ErrUserStreamLimit) {
		t.Fatal("per-user override ignored", err)
	}
	held.release()
	exempt := Source{Limits: domain.DeliveryLimits{MaxStreams: ptr(0)}}
	var all []*admission
	for _, id := range []string{"a", "b", "c", "d", "e", "f"} {
		all = append(all, admitOK(t, l, p, id, exempt))
	}
	for _, a := range all {
		a.release()
	}

	// A disabled limit ignores the server-wide value and every override.
	off := newLimiter(Limits{StreamLimit: false, MaxStreamsPerUser: 1, MaxStreamsPerDevice: 1, BandwidthLimit: false, MaxKbpsPerUser: 8}, newFakeClock())
	limited := Source{Limits: domain.DeliveryLimits{MaxStreams: ptr(1), MaxKbps: ptr(int64(8))}}
	for _, id := range []string{"a", "b", "c"} {
		if a := admitOK(t, off, p, id, limited); a != nil {
			t.Fatal("disabled limits still admitted with a limit")
		}
	}
	// The bandwidth switch alone does not count streams.
	bandwidth := newLimiter(Limits{BandwidthLimit: true, MaxStreamsPerUser: 1}, newFakeClock())
	a := admitOK(t, bandwidth, p, "a", limited)
	b := admitOK(t, bandwidth, p, "b", limited)
	if a.throttle() == nil || a.bucket != b.bucket || len(bandwidth.users) != 0 {
		t.Fatal("bandwidth limit did not share one user bucket or counted streams")
	}
	a.release()
	b.release()
	if len(bandwidth.buckets) != 0 {
		t.Fatal("bucket leaked")
	}
	// Zero override exempts the user from a server-wide rate.
	rate := newLimiter(Limits{BandwidthLimit: true, MaxKbpsPerUser: 1000}, newFakeClock())
	if a := admitOK(t, rate, p, "a", Source{Limits: domain.DeliveryLimits{MaxKbps: ptr(int64(0))}}); a.throttle() != nil {
		t.Fatal("exempt user throttled")
	}
	if a := admitOK(t, rate, p, "a", Source{}); a.throttle() == nil {
		t.Fatal("server-wide rate not applied")
	}
}

func TestLimiterDeviceLimit(t *testing.T) {
	l := newLimiter(Limits{StreamLimit: true, MaxStreamsPerDevice: 1, BandwidthLimit: true, MaxKbpsPerUser: 8000, BandwidthPerDevice: true}, newFakeClock())
	tv := Source{DeviceID: "tv"}
	a := admitOK(t, l, streamPrincipal("u", "s1"), "a", tv)
	if _, err := l.admit(streamPrincipal("u", "s2"), "b", tv); !errors.Is(err, ErrDeviceStreamLimit) {
		t.Fatal("second playback on one device admitted", err)
	}
	phone := admitOK(t, l, streamPrincipal("u", "s2"), "b", Source{DeviceID: "phone"})
	// Sessions without a device ID are their own device.
	bare := admitOK(t, l, streamPrincipal("u", "s3"), "c", Source{})
	if a.bucket == phone.bucket || phone.bucket == bare.bucket {
		t.Fatal("per-device bandwidth shared a bucket across devices")
	}
	for _, x := range []*admission{a, phone, bare} {
		x.release()
	}
	if len(l.users) != 0 || len(l.buckets) != 0 {
		t.Fatal("device state leaked")
	}
}

func TestTokenBucketReservations(t *testing.T) {
	clock := newFakeClock()
	b := newTokenBucket(8000, clock.Now()) // 1,000,000 bytes/s, 250,000 burst
	if d := b.reserve(250_000, clock.Now()); d != 0 {
		t.Fatal("initial burst throttled", d)
	}
	if d := b.reserve(100_000, clock.Now()); d != 100*time.Millisecond {
		t.Fatal("deficit delay", d)
	}
	clock.Advance(100 * time.Millisecond)
	if d := b.reserve(50_000, clock.Now()); d != 50*time.Millisecond {
		t.Fatal("refill after wait", d)
	}
	// Idle time refills only up to the burst.
	clock.Advance(time.Hour)
	if d := b.reserve(250_000, clock.Now()); d != 0 {
		t.Fatal("burst after idle", d)
	}
	if d := b.reserve(1, clock.Now()); d <= 0 {
		t.Fatal("bucket refilled beyond its burst")
	}
	b.setRate(16000, clock.Now())
	if b.rate != 2_000_000 || b.burst != 500_000 {
		t.Fatal("rate change", b.rate, b.burst)
	}
}

// throttledCopy streams payload through a rate-limited writer driven by the
// fake clock and returns the simulated duration.
func throttledCopy(t *testing.T, clock *fakeClock, bucket *tokenBucket, payloads ...[]byte) time.Duration {
	t.Helper()
	start := clock.Now()
	var wg sync.WaitGroup
	for _, payload := range payloads {
		wg.Go(func() {
			recorder := httptest.NewRecorder()
			writer := &streamWriter{ResponseWriter: recorder, request: nativeRequest("GET", "/stream"), controller: http.NewResponseController(recorder), timeout: time.Second,
				buffers: &sync.Pool{New: func() any { return new([32 << 10]byte) }}, throttle: &throttle{bucket: bucket, clock: clock}}
			if n, err := writer.ReadFrom(bytes.NewReader(payload)); err != nil || n != int64(len(payload)) || !bytes.Equal(recorder.Body.Bytes(), payload) {
				t.Errorf("throttled copy: n=%d err=%v", n, err)
			}
		})
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	for {
		select {
		case <-done:
			return clock.Now().Sub(start)
		default:
		}
		if !clock.advanceToNext() {
			time.Sleep(50 * time.Microsecond)
		}
	}
}

func TestThrottledWriterHoldsRate(t *testing.T) {
	clock := newFakeClock()
	payload := bytes.Repeat([]byte("x"), 2_250_000)
	// 1,000,000 bytes/s with a 250,000 byte burst: the rest takes 2 seconds.
	elapsed := throttledCopy(t, clock, newTokenBucket(8000, clock.Now()), payload)
	if elapsed < 1999*time.Millisecond || elapsed > 2001*time.Millisecond {
		t.Fatalf("single stream took %v of simulated time, want 2s", elapsed)
	}
	// Two streams share one user bucket: twice the data takes twice as long.
	clock = newFakeClock()
	elapsed = throttledCopy(t, clock, newTokenBucket(8000, clock.Now()), payload, payload)
	if elapsed < 4249*time.Millisecond || elapsed > 4251*time.Millisecond {
		t.Fatalf("shared bucket took %v of simulated time, want 4.25s", elapsed)
	}
}

type fakeSessions struct {
	active atomic.Bool
	fail   atomic.Bool
	calls  atomic.Int32
}

func (f *fakeSessions) SessionActive(_ context.Context, user, session string) (bool, error) {
	f.calls.Add(1)
	if user != "u" || session != "s" {
		return false, nil
	}
	if f.fail.Load() {
		return false, errors.New("database unavailable")
	}
	return f.active.Load(), nil
}

func limitWriteError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, ErrUserStreamLimit) || errors.Is(err, ErrDeviceStreamLimit) {
		http.Error(w, err.Error(), http.StatusTooManyRequests)
		return
	}
	testWriteError(w, r, err)
}

func watchedHandler(t *testing.T, source Source, sessions SessionChecker, clock Clock, limits Limits) *Handler {
	t.Helper()
	handler, err := NewHandler(resolveFunc(func(context.Context, access.Principal, string) (Source, error) { return source, nil }),
		Options{MaxConcurrent: 4, WriteTimeout: time.Second, WriteError: limitWriteError, Sessions: sessions, SessionCheckInterval: 5 * time.Second, Clock: clock, Limits: limits})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

// startBlockedStream starts a GET whose first network write blocks until the
// write deadline expires, like a client that stopped reading.
func startBlockedStream(t *testing.T, handler *Handler, r *http.Request, sourceID string) (*blockedNetworkWriter, chan struct{}) {
	t.Helper()
	w := &blockedNetworkWriter{header: make(http.Header), started: make(chan struct{}), expired: make(chan struct{})}
	done := make(chan struct{})
	go func() { defer close(done); handler.ServeSource(w, r, sourceID) }()
	select {
	case <-w.started:
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not start")
	}
	return w, done
}

func expectRunning(t *testing.T, done chan struct{}) {
	t.Helper()
	select {
	case <-done:
		t.Fatal("stream ended while its session was active")
	case <-time.After(20 * time.Millisecond):
	}
}

func expectEnded(t *testing.T, done chan struct{}, why string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal(why)
	}
}

func TestRevokedSessionCutsRunningStream(t *testing.T) {
	source, _ := fixture(t)
	clock := newFakeClock()
	sessions := &fakeSessions{}
	sessions.active.Store(true)
	handler := watchedHandler(t, source, sessions, clock, Limits{StreamLimit: true, MaxStreamsPerUser: 1})
	_, done := startBlockedStream(t, handler, nativeRequest("GET", "/stream"), "source")
	clock.waitPending(t, 1)
	clock.Advance(5 * time.Second)
	clock.waitPending(t, 1)
	expectRunning(t, done)
	if sessions.calls.Load() != 1 {
		t.Fatal("session not rechecked after the interval", sessions.calls.Load())
	}
	sessions.active.Store(false)
	clock.Advance(4 * time.Second)
	expectRunning(t, done)
	clock.Advance(time.Second)
	expectEnded(t, done, "revoked session kept streaming")
	if sessions.calls.Load() != 2 {
		t.Fatal("unexpected session checks", sessions.calls.Load())
	}
	// The cut stream released its playback slot and its admission.
	w := httptest.NewRecorder()
	handler.ServeSource(w, nativeRequest("GET", "/stream"), "other")
	if w.Code != 200 || len(handler.limiter.users) != 0 {
		t.Fatalf("revoked stream leaked its admission: status=%d", w.Code)
	}
}

func TestSessionCheckFailuresCutStreamAfterLimit(t *testing.T) {
	source, _ := fixture(t)
	clock := newFakeClock()
	sessions := &fakeSessions{}
	sessions.active.Store(true)
	sessions.fail.Store(true)
	handler := watchedHandler(t, source, sessions, clock, Limits{})
	// Three consecutive failures end the stream, as documented.
	const tolerated = 3
	_, done := startBlockedStream(t, handler, nativeRequest("GET", "/stream"), "source")
	// check fires one interval and waits until the watcher finished the query.
	check := func() {
		t.Helper()
		calls := sessions.calls.Load()
		clock.waitPending(t, 1)
		clock.Advance(5 * time.Second)
		deadline := time.Now().Add(2 * time.Second)
		for sessions.calls.Load() == calls {
			if time.Now().After(deadline) {
				t.Fatal("session check did not run")
			}
			time.Sleep(time.Millisecond)
		}
	}
	for range tolerated - 1 {
		check()
	}
	// A successful check resets the failure count.
	sessions.fail.Store(false)
	check()
	sessions.fail.Store(true)
	for range tolerated - 1 {
		check()
	}
	clock.waitPending(t, 1)
	expectRunning(t, done)
	check()
	expectEnded(t, done, "unconfirmed session kept streaming")
	if got := sessions.calls.Load(); got != 2*tolerated {
		t.Fatal("unexpected session checks", got)
	}
}

func TestHandlerEnforcesPlaybackLimit(t *testing.T) {
	source, _ := fixture(t)
	sessions := &fakeSessions{}
	sessions.active.Store(true)
	handler := watchedHandler(t, source, sessions, newFakeClock(), Limits{StreamLimit: true, MaxStreamsPerUser: 1})
	r := nativeRequest("GET", "/stream")
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	_, done := startBlockedStream(t, handler, r.WithContext(ctx), "a")
	w := httptest.NewRecorder()
	handler.ServeSource(w, nativeRequest("GET", "/stream"), "b")
	if w.Code != http.StatusTooManyRequests || w.Body.String() != "user_stream_limit\n" {
		t.Fatalf("second playback: %d %q", w.Code, w.Body.String())
	}
	// HEAD carries no media and the same playback may open more ranges.
	for _, check := range []struct{ method, id string }{{"HEAD", "b"}, {"GET", "a"}} {
		w = httptest.NewRecorder()
		handler.ServeSource(w, nativeRequest(check.method, "/stream"), check.id)
		if w.Code != 200 {
			t.Fatalf("%s %s refused: %d", check.method, check.id, w.Code)
		}
	}
	cancel()
	expectEnded(t, done, "cancelled stream did not end")
	w = httptest.NewRecorder()
	handler.ServeSource(w, nativeRequest("GET", "/stream"), "b")
	if w.Code != 200 {
		t.Fatal("limit not released", w.Code)
	}
}

func TestNewHandlerValidatesLimits(t *testing.T) {
	resolver := resolveFunc(func(context.Context, access.Principal, string) (Source, error) { return Source{}, ErrNotFound })
	base := Options{MaxConcurrent: 1, WriteTimeout: time.Second, WriteError: testWriteError}
	for _, change := range []func(*Options){
		func(o *Options) { o.SessionCheckInterval = -time.Second },
		func(o *Options) { o.Limits.MaxStreamsPerUser = -1 },
		func(o *Options) { o.Limits.MaxStreamsPerDevice = domain.MaxDeliveryStreams + 1 },
		func(o *Options) { o.Limits.MaxKbpsPerUser = domain.MaxDeliveryKbps + 1 },
	} {
		options := base
		change(&options)
		if _, err := NewHandler(resolver, options); err == nil {
			t.Fatalf("invalid limits accepted: %+v", options)
		}
	}
}

// Loopback TCP end-to-end checks with the real clock.

func largeFixture(t *testing.T, size int) (Source, []byte) {
	t.Helper()
	root := t.TempDir()
	content := make([]byte, size)
	for i := range content {
		content[i] = byte(i * 7)
	}
	if err := os.WriteFile(filepath.Join(root, "large.mkv"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	return Source{Root: root, RelativePath: "large.mkv", ContentType: "video/x-matroska"}, content
}

func loopbackServer(t *testing.T, handler *Handler) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(access.WithPrincipal(r.Context(), streamPrincipal("u", "s")))
		handler.ServeSource(w, r, "source")
	}))
	t.Cleanup(server.Close)
	return server
}

func TestLoopbackRevocationDisconnectsDownload(t *testing.T) {
	source, content := largeFixture(t, 8<<20)
	sessions := &fakeSessions{}
	sessions.active.Store(true)
	// 1 MB/s keeps the 8 MiB download running for about eight seconds.
	handler, err := NewHandler(resolveFunc(func(context.Context, access.Principal, string) (Source, error) { return source, nil }),
		Options{MaxConcurrent: 2, WriteTimeout: 5 * time.Second, WriteError: testWriteError, Sessions: sessions, SessionCheckInterval: 200 * time.Millisecond,
			Limits: Limits{BandwidthLimit: true, MaxKbpsPerUser: 8000}})
	if err != nil {
		t.Fatal(err)
	}
	server := loopbackServer(t, handler)
	response, err := server.Client().Get(server.URL + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || response.ContentLength != int64(len(content)) {
		t.Fatal("unexpected response", response.StatusCode, response.ContentLength)
	}
	head := make([]byte, 512<<10)
	if _, err = io.ReadFull(response.Body, head); err != nil || !bytes.Equal(head, content[:len(head)]) {
		t.Fatal("initial bytes", err)
	}
	sessions.active.Store(false)
	revoked := time.Now()
	rest, err := io.ReadAll(response.Body)
	cut := time.Since(revoked)
	if err == nil || len(head)+len(rest) >= len(content) {
		t.Fatalf("download survived revocation: bytes=%d err=%v", len(head)+len(rest), err)
	}
	t.Logf("download cut %v after revocation, %d of %d bytes delivered", cut, len(head)+len(rest), len(content))
	// One check interval plus scheduling and socket buffer slack.
	if cut > 1500*time.Millisecond {
		t.Fatalf("revoked download ended after %v", cut)
	}
	if !bytes.Equal(rest, content[len(head):len(head)+len(rest)]) {
		t.Fatal("bytes before the cut were corrupted")
	}
}

func TestLoopbackBandwidthLimit(t *testing.T) {
	source, content := largeFixture(t, 3<<20)
	download := func(limits Limits) time.Duration {
		t.Helper()
		handler, err := NewHandler(resolveFunc(func(context.Context, access.Principal, string) (Source, error) { return source, nil }),
			Options{MaxConcurrent: 2, WriteTimeout: 5 * time.Second, WriteError: testWriteError, Limits: limits})
		if err != nil {
			t.Fatal(err)
		}
		server := loopbackServer(t, handler)
		start := time.Now()
		response, err := server.Client().Get(server.URL + "/stream")
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil || !bytes.Equal(body, content) {
			t.Fatal("download corrupted", err, len(body))
		}
		return time.Since(start)
	}
	// 16,000 kbps = 2,000,000 bytes/s after a 500,000 byte burst.
	want := time.Duration(float64(len(content)-500_000) / 2_000_000 * float64(time.Second))
	limited := download(Limits{BandwidthLimit: true, MaxKbpsPerUser: 16000})
	t.Logf("limited download took %v, expected %v", limited, want)
	if limited < want*9/10 || limited > want*5/4 {
		t.Fatalf("limited download took %v, want about %v", limited, want)
	}
	// The same download with the switch off is not throttled.
	if unlimited := download(Limits{BandwidthLimit: false, MaxKbpsPerUser: 16000}); unlimited > want/2 {
		t.Fatalf("disabled bandwidth limit still throttled: %v", unlimited)
	}
}
