package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
)

type resolveFunc func(context.Context, access.Principal, string) (Source, error)

func (f resolveFunc) Resolve(ctx context.Context, principal access.Principal, id string) (Source, error) {
	return f(ctx, principal, id)
}

func testWriteError(w http.ResponseWriter, _ *http.Request, err error) {
	status := 500
	for candidate, code := range map[error]int{ErrUnauthenticated: 401, ErrPlaybackDenied: 403, ErrNotFound: 404, ErrTranscodeDisabled: 409, ErrInvalidRequest: 400, ErrBusy: 429, ErrMethodNotAllowed: 405, ErrInvalidRange: 416, ErrPreconditionFailed: 412, ErrBodyTooLarge: 413, ErrUnsupportedMediaType: 415, ErrLookupTimeout: 504} {
		if errors.Is(err, candidate) {
			status = code
		}
	}
	http.Error(w, http.StatusText(status), status)
}

func testHandler(t *testing.T, resolver Resolver, limit int) *Handler {
	t.Helper()
	handler, err := NewHandler(resolver, Options{MaxConcurrent: limit, WriteTimeout: time.Second, WriteError: testWriteError})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func nativeRequest(method, target string) *http.Request {
	r := httptest.NewRequest(method, target, nil)
	return r.WithContext(access.WithPrincipal(r.Context(), access.Principal{UserID: "u", SessionID: "s", Kind: access.ClientNative}))
}

func fixture(t *testing.T) (Source, []byte) {
	t.Helper()
	root := t.TempDir()
	content := bytes.Repeat([]byte("0123456789abcdef"), 32768)
	if err := os.WriteFile(filepath.Join(root, "original.mkv"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(root, "original.mkv"), stamp, stamp); err != nil {
		t.Fatal(err)
	}
	return Source{Root: root, RelativePath: "original.mkv", ContentType: "video/x-matroska", ETag: `"content-version-1"`}, content
}

func TestDirectDeliveryRangeAndSourceUnchanged(t *testing.T) {
	source, content := fixture(t)
	before := sha256.Sum256(content)
	resolver := resolveFunc(func(_ context.Context, p access.Principal, id string) (Source, error) {
		if p.UserID != "u" || id != "source" {
			t.Error("identity or source ID lost")
		}
		return source, nil
	})
	handler := testHandler(t, resolver, 2)
	cases := []struct {
		name, method, ranges, ifRange string
		status                        int
		want                          []byte
	}{
		{"complete", "GET", "", "", 200, content},
		{"head", "HEAD", "", "", 200, nil},
		{"first", "GET", "bytes=0-9", "", 206, content[:10]},
		{"suffix", "GET", "bytes=-7", "", 206, content[len(content)-7:]},
		{"open_end", "GET", "bytes=500000-", "", 206, content[500000:]},
		{"etag_match", "GET", "bytes=0-9", source.ETag, 206, content[:10]},
		{"etag_mismatch", "GET", "bytes=0-9", `"older"`, 200, content},
		{"weak_etag", "GET", "bytes=0-9", `W/"content-version-1"`, 200, content},
		{"date_match", "GET", "bytes=0-9", "Thu, 02 Jan 2025 03:04:05 GMT", 206, content[:10]},
		{"date_mismatch", "GET", "bytes=0-9", "Wed, 01 Jan 2025 03:04:05 GMT", 200, content},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := nativeRequest(tc.method, "/stream")
			r.Header.Set("Range", tc.ranges)
			r.Header.Set("If-Range", tc.ifRange)
			w := httptest.NewRecorder()
			w.Header().Set("Content-Disposition", "attachment; filename=original.mkv")
			handler.ServeSource(w, r, "source")
			if w.Code != tc.status || !bytes.Equal(w.Body.Bytes(), tc.want) {
				t.Fatalf("status=%d bytes=%d; want status=%d bytes=%d", w.Code, w.Body.Len(), tc.status, len(tc.want))
			}
			if w.Header().Get("Content-Disposition") != "" || w.Header().Get("Accept-Ranges") != "bytes" {
				t.Fatalf("unexpected headers: %v", w.Header())
			}
		})
	}
	actual, err := os.ReadFile(filepath.Join(source.Root, source.RelativePath))
	if err != nil || sha256.Sum256(actual) != before {
		t.Fatalf("original resource changed: %v", err)
	}
}

func TestMultipartRange(t *testing.T) {
	source, content := fixture(t)
	handler := testHandler(t, resolveFunc(func(context.Context, access.Principal, string) (Source, error) { return source, nil }), 1)
	r := nativeRequest("GET", "/stream")
	r.Header.Set("Range", "bytes=0-3,100-106")
	w := httptest.NewRecorder()
	handler.ServeSource(w, r, "source")
	if w.Code != 206 {
		t.Fatalf("status=%d", w.Code)
	}
	kind, params, err := mime.ParseMediaType(w.Header().Get("Content-Type"))
	if err != nil || kind != "multipart/byteranges" {
		t.Fatalf("invalid multipart response: %v", err)
	}
	parts := multipart.NewReader(w.Body, params["boundary"])
	for i, expected := range [][]byte{content[:4], content[100:107]} {
		part, err := parts.NextPart()
		if err != nil {
			t.Fatal(err)
		}
		actual, err := io.ReadAll(part)
		if err != nil || !bytes.Equal(actual, expected) || part.Header.Get("Content-Range") == "" {
			t.Fatalf("part %d incorrect", i)
		}
	}
	if _, err := parts.NextPart(); err != io.EOF {
		t.Fatalf("unexpected extra part: %v", err)
	}
}

func TestAuthorizationBeforeResolution(t *testing.T) {
	var calls atomic.Int32
	handler := testHandler(t, resolveFunc(func(context.Context, access.Principal, string) (Source, error) {
		calls.Add(1)
		return Source{}, ErrNotFound
	}), 1)
	for _, tc := range []struct {
		name      string
		principal access.Principal
		status    int
	}{
		{"anonymous", access.Principal{}, 401},
		{"web", access.Principal{UserID: "u", SessionID: "s", Kind: access.ClientWeb}, 403},
		{"web_admin", access.Principal{UserID: "u", SessionID: "s", Kind: access.ClientWeb, Admin: true}, 403},
		{"unknown", access.Principal{UserID: "u", SessionID: "s", Kind: "unknown"}, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/stream", nil)
			r.Header.Set("User-Agent", "Trusted Native Player")
			r.Header.Set("X-Client-Kind", "native")
			r = r.WithContext(access.WithPrincipal(r.Context(), tc.principal))
			w := httptest.NewRecorder()
			handler.ServeSource(w, r, "source")
			if w.Code != tc.status || calls.Load() != 0 {
				t.Fatalf("status=%d resolver calls=%d", w.Code, calls.Load())
			}
		})
	}
	// A browser-looking UA cannot change an already verified native session kind.
	r := nativeRequest("GET", "/stream")
	r.Header.Set("User-Agent", "Mozilla/5.0")
	w := httptest.NewRecorder()
	handler.ServeSource(w, r, "hidden")
	if w.Code != 404 || calls.Load() != 1 {
		t.Fatalf("native lookup: status=%d calls=%d", w.Code, calls.Load())
	}
}

func TestPathBoundariesAndNoDisclosure(t *testing.T) {
	source, _ := fixture(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("private-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	paths := []string{"", ".", "..", "../secret", "folder/../../secret", "/etc/passwd", `C:\private`, `folder\file`, "original.mkv:alternate", "original.mkv\x00", "missing", "folder/../original.mkv"}
	for _, relative := range paths {
		t.Run(relative, func(t *testing.T) {
			bad := source
			bad.RelativePath = relative
			assertSourceHidden(t, bad)
		})
	}
	t.Run("directory", func(t *testing.T) {
		if err := os.Mkdir(filepath.Join(source.Root, "folder"), 0o700); err != nil {
			t.Fatal(err)
		}
		bad := source
		bad.RelativePath = "folder"
		assertSourceHidden(t, bad)
	})
	t.Run("symlink_escape", func(t *testing.T) {
		if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(source.Root, "escape")); err != nil {
			t.Skipf("symlink creation unavailable: %v", err)
		}
		bad := source
		bad.RelativePath = "escape"
		assertSourceHidden(t, bad)
	})
	t.Run("directory_symlink_escape", func(t *testing.T) {
		if err := os.Symlink(outside, filepath.Join(source.Root, "escape-dir")); err != nil {
			t.Skipf("symlink creation unavailable: %v", err)
		}
		bad := source
		bad.RelativePath = "escape-dir/secret"
		assertSourceHidden(t, bad)
	})
}

func assertSourceHidden(t *testing.T, source Source) {
	t.Helper()
	handler := testHandler(t, resolveFunc(func(context.Context, access.Principal, string) (Source, error) { return source, nil }), 1)
	w := httptest.NewRecorder()
	handler.ServeSource(w, nativeRequest("GET", "/stream"), "source")
	if w.Code != 404 || strings.Contains(w.Body.String(), source.Root) || strings.Contains(w.Body.String(), "private-secret") {
		t.Fatalf("unsafe response: status=%d body=%q", w.Code, w.Body.String())
	}
}

func TestErrorsUseInjectedWriter(t *testing.T) {
	source, _ := fixture(t)
	handler := testHandler(t, resolveFunc(func(context.Context, access.Principal, string) (Source, error) { return source, nil }), 1)
	for _, tc := range []struct {
		method, rangeValue, ifMatch string
		status                      int
	}{
		{"POST", "", "", 405}, {"GET", "bytes=9999999-", "", 416}, {"GET", "bytes=no", "", 416}, {"GET", "", `"wrong"`, 412},
	} {
		r := nativeRequest(tc.method, "/stream")
		r.Header.Set("Range", tc.rangeValue)
		r.Header.Set("If-Match", tc.ifMatch)
		w := httptest.NewRecorder()
		handler.ServeSource(w, r, "source")
		if w.Code != tc.status || strings.TrimSpace(w.Body.String()) != http.StatusText(tc.status) {
			t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
		}
	}
}

func TestConcurrencyBudgetAndCancellation(t *testing.T) {
	source, _ := fixture(t)
	entered := make(chan struct{})
	var calls atomic.Int32
	handler := testHandler(t, resolveFunc(func(ctx context.Context, _ access.Principal, _ string) (Source, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-ctx.Done()
			return Source{}, ctx.Err()
		}
		return source, nil
	}), 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := nativeRequest("GET", "/stream")
	r = r.WithContext(access.WithPrincipal(ctx, access.Principal{UserID: "u", SessionID: "s", Kind: access.ClientNative}))
	done := make(chan struct{})
	go func() { defer close(done); handler.ServeSource(httptest.NewRecorder(), r, "source") }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("resolver did not start")
	}
	w := httptest.NewRecorder()
	handler.ServeSource(w, nativeRequest("GET", "/stream"), "source")
	if w.Code != 429 || calls.Load() != 1 {
		t.Fatalf("budget bypass: status=%d calls=%d", w.Code, calls.Load())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled request did not exit")
	}
	w = httptest.NewRecorder()
	handler.ServeSource(w, nativeRequest("HEAD", "/stream"), "source")
	if w.Code != 200 || calls.Load() != 2 {
		t.Fatal("stream slot was not released")
	}
}

type cancellingWriter struct {
	*httptest.ResponseRecorder
	cancel  context.CancelFunc
	written int
}

func (w *cancellingWriter) Write(data []byte) (int, error) {
	w.written += len(data)
	w.cancel()
	return w.ResponseRecorder.Write(data)
}

func TestCancellationStopsStreamAndReleasesSlot(t *testing.T) {
	source, content := fixture(t)
	handler := testHandler(t, resolveFunc(func(context.Context, access.Principal, string) (Source, error) { return source, nil }), 1)
	r := nativeRequest("GET", "/stream")
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	w := &cancellingWriter{ResponseRecorder: httptest.NewRecorder(), cancel: cancel}
	handler.ServeSource(w, r.WithContext(ctx), "source")
	if w.written == 0 || w.written >= len(content) {
		t.Fatalf("cancellation did not stop stream: bytes=%d", w.written)
	}
	next := httptest.NewRecorder()
	handler.ServeSource(next, nativeRequest("HEAD", "/stream"), "source")
	if next.Code != 200 {
		t.Fatalf("slot leaked: status=%d", next.Code)
	}
}

func TestNewHandlerRejectsIncompleteConfiguration(t *testing.T) {
	resolver := resolveFunc(func(context.Context, access.Principal, string) (Source, error) { return Source{}, ErrNotFound })
	for _, options := range []Options{{}, {MaxConcurrent: 1, WriteTimeout: time.Second}, {MaxConcurrent: -1, WriteTimeout: time.Second, WriteError: testWriteError}, {MaxConcurrent: 1, WriteError: testWriteError}} {
		if _, err := NewHandler(resolver, options); err == nil {
			t.Fatal("incomplete configuration accepted")
		}
	}
	if _, err := NewHandler(nil, Options{MaxConcurrent: 1, WriteTimeout: time.Second, WriteError: testWriteError}); err == nil {
		t.Fatal("nil resolver accepted")
	}
}

type blockedNetworkWriter struct {
	header  http.Header
	started chan struct{}
	expired chan struct{}
	once    sync.Once
}

func (w *blockedNetworkWriter) Header() http.Header { return w.header }
func (w *blockedNetworkWriter) WriteHeader(int)     {}
func (w *blockedNetworkWriter) Write([]byte) (int, error) {
	close(w.started)
	<-w.expired
	return 0, context.DeadlineExceeded
}
func (w *blockedNetworkWriter) SetWriteDeadline(deadline time.Time) error {
	if !deadline.IsZero() && !deadline.After(time.Now()) {
		w.once.Do(func() { close(w.expired) })
	}
	return nil
}

func TestCancellationInterruptsBlockedNetworkWrite(t *testing.T) {
	source, _ := fixture(t)
	handler := testHandler(t, resolveFunc(func(context.Context, access.Principal, string) (Source, error) { return source, nil }), 1)
	r := nativeRequest("GET", "/stream")
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	w := &blockedNetworkWriter{header: make(http.Header), started: make(chan struct{}), expired: make(chan struct{})}
	done := make(chan struct{})
	go func() { defer close(done); handler.ServeSource(w, r.WithContext(ctx), "source") }()
	select {
	case <-w.started:
	case <-time.After(time.Second):
		t.Fatal("write did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("context cancellation did not interrupt blocked write")
	}
	w2 := httptest.NewRecorder()
	handler.ServeSource(w2, nativeRequest("HEAD", "/stream"), "source")
	if w2.Code != 200 {
		t.Fatal("blocked stream leaked its concurrency slot")
	}
}

func BenchmarkOriginalRange(b *testing.B) {
	root := b.TempDir()
	if err := os.WriteFile(filepath.Join(root, "original.mkv"), bytes.Repeat([]byte("0123456789abcdef"), 4096), 0o600); err != nil {
		b.Fatal(err)
	}
	source := Source{Root: root, RelativePath: "original.mkv", ContentType: "video/x-matroska"}
	handler, err := NewHandler(resolveFunc(func(context.Context, access.Principal, string) (Source, error) { return source, nil }), Options{MaxConcurrent: 1, WriteTimeout: time.Second, WriteError: testWriteError})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.SetBytes(16384)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r := nativeRequest("GET", "/stream")
		r.Header.Set("Range", "bytes=0-16383")
		w := httptest.NewRecorder()
		handler.ServeSource(w, r, "source")
		if w.Code != 206 || w.Body.Len() != 16384 {
			b.Fatal("incorrect range response")
		}
	}
}

func TestRangeBudgetRejectsAbuseBeforeLookup(t *testing.T) {
	var calls atomic.Int32
	handler := testHandler(t, resolveFunc(func(context.Context, access.Principal, string) (Source, error) {
		calls.Add(1)
		return Source{}, ErrNotFound
	}), 1)
	for _, value := range []string{"bytes=" + strings.Repeat("0-1,", 16) + "0-1", "bytes=" + strings.Repeat("9", 4096) + "-"} {
		r := nativeRequest("GET", "/stream")
		r.Header.Set("Range", value)
		w := httptest.NewRecorder()
		handler.ServeSource(w, r, "source")
		if w.Code != 416 || calls.Load() != 0 {
			t.Fatalf("range budget bypass: status=%d calls=%d", w.Code, calls.Load())
		}
	}
}

func TestSourceLookupHasIndependentDeadline(t *testing.T) {
	handler, err := NewHandler(resolveFunc(func(ctx context.Context, _ access.Principal, _ string) (Source, error) {
		<-ctx.Done()
		return Source{}, ctx.Err()
	}), Options{MaxConcurrent: 1, LookupTimeout: time.Millisecond, WriteTimeout: time.Second, WriteError: testWriteError})
	if err != nil {
		t.Fatal(err)
	}
	r := nativeRequest("GET", "/stream")
	w := httptest.NewRecorder()
	handler.ServeSource(w, r, "source")
	if w.Code != 504 || r.Context().Err() != nil {
		t.Fatalf("lookup timeout leaked into stream context: status=%d err=%v", w.Code, r.Context().Err())
	}
}

type cancelOnDeadlineWriter struct {
	*httptest.ResponseRecorder
	cancel context.CancelFunc
}

func (w *cancelOnDeadlineWriter) SetWriteDeadline(deadline time.Time) error {
	if deadline.After(time.Now()) {
		w.cancel()
	}
	return nil
}

func TestCancellationBetweenDeadlineRefreshAndWrite(t *testing.T) {
	source, _ := fixture(t)
	handler := testHandler(t, resolveFunc(func(context.Context, access.Principal, string) (Source, error) { return source, nil }), 1)
	r := nativeRequest("GET", "/stream")
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	w := &cancelOnDeadlineWriter{ResponseRecorder: httptest.NewRecorder(), cancel: cancel}
	handler.ServeSource(w, r.WithContext(ctx), "source")
	if w.Body.Len() != 0 {
		t.Fatal("stream wrote after cancellation during deadline refresh")
	}
}

// Concurrent copies use distinct payloads and sizes to detect premature reuse.
func TestStreamCopyConcurrentIsolation(t *testing.T) {
	pool := &sync.Pool{New: func() any { return new([32 << 10]byte) }}
	var wg sync.WaitGroup
	for worker := 0; worker < 16; worker++ {
		wg.Go(func() {
			payload := bytes.Repeat([]byte{byte(worker + 1)}, (32<<10)+worker*137)
			for round := 0; round < 20; round++ {
				recorder := httptest.NewRecorder()
				writer := &streamWriter{ResponseWriter: recorder, request: nativeRequest("GET", "/stream"), controller: http.NewResponseController(recorder), timeout: time.Second, buffers: pool}
				n, err := writer.ReadFrom(bytes.NewReader(payload))
				if err != nil || n != int64(len(payload)) || !bytes.Equal(recorder.Body.Bytes(), payload) {
					t.Errorf("concurrent copy corrupted: worker=%d round=%d bytes=%d err=%v", worker, round, n, err)
					return
				}
			}
		})
	}
	wg.Wait()
}

type failingCopyReader struct{}

func (failingCopyReader) Read(p []byte) (int, error) {
	return copy(p, "private media"), io.ErrUnexpectedEOF
}

func TestStreamCopyClearsBufferAfterReadFailure(t *testing.T) {
	buffer := new([32 << 10]byte)
	pool := &sync.Pool{New: func() any { return buffer }}
	recorder := httptest.NewRecorder()
	writer := &streamWriter{ResponseWriter: recorder, request: nativeRequest("GET", "/stream"), controller: http.NewResponseController(recorder), timeout: time.Second, buffers: pool}
	n, err := writer.ReadFrom(failingCopyReader{})
	if !errors.Is(err, io.ErrUnexpectedEOF) || n != 13 || recorder.Body.String() != "private media" {
		t.Fatalf("copy lost partial read or error: n=%d err=%v", n, err)
	}
	if *buffer != [32 << 10]byte{} {
		t.Fatal("failed copy retained media bytes")
	}
}

// CodeQL reflected XSS defense: media is sandboxed even when the stored type
// is a document type, while error responses keep the policy set before.
func TestMediaResponsesAreSandboxed(t *testing.T) {
	source, _ := fixture(t)
	source.ContentType = "text/html; charset=utf-8"
	const boundaryPolicy = "default-src 'none'; frame-ancestors 'none'; base-uri 'none'"
	handler := testHandler(t, resolveFunc(func(_ context.Context, _ access.Principal, id string) (Source, error) {
		if id == "missing" {
			return Source{}, ErrNotFound
		}
		return source, nil
	}), 2)
	for _, tc := range []struct {
		method, id, ranges string
		status             int
		policy             string
	}{
		{"GET", "source", "", 200, mediaContentSecurityPolicy},
		{"HEAD", "source", "", 200, mediaContentSecurityPolicy},
		{"GET", "source", "bytes=0-9", 206, mediaContentSecurityPolicy},
		{"GET", "source", "bytes=99999999-", 416, boundaryPolicy},
		{"GET", "missing", "", 404, boundaryPolicy},
	} {
		r := nativeRequest(tc.method, "/stream")
		if tc.ranges != "" {
			r.Header.Set("Range", tc.ranges)
		}
		w := httptest.NewRecorder()
		w.Header().Set("Content-Security-Policy", boundaryPolicy)
		handler.ServeSource(w, r, tc.id)
		if w.Code != tc.status || len(w.Header().Values("Content-Security-Policy")) != 1 || w.Header().Get("Content-Security-Policy") != tc.policy {
			t.Fatalf("%s %s %q: status=%d policy=%q", tc.method, tc.id, tc.ranges, w.Code, w.Header().Values("Content-Security-Policy"))
		}
	}
}
