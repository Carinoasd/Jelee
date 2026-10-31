package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
)

func TestFirstRangeStart(t *testing.T) {
	for _, tc := range []struct {
		header string
		start  int64
		ok     bool
	}{
		{"", 0, true},
		{"bytes=0-", 0, true},
		{"bytes=100-199", 100, true},
		{"bytes= 100-199", 100, true},
		{"bytes=100-199,300-399", 100, true},
		{"bytes=-50", 950, true},
		{"bytes=-5000", 0, true},
		{"bytes=1000-", 0, false}, // beyond the end: ServeContent answers 416
		{"bytes=-0", 0, false},
		{"bytes=abc-", 0, false},
		{"bytes=-1-2", 0, false},
		{"items=0-1", 0, false},
		{"bytes=5", 0, false},
	} {
		start, ok := firstRangeStart(tc.header, 1000)
		if start != tc.start || ok != tc.ok {
			t.Errorf("%q: got %d %t", tc.header, start, ok)
		}
	}
}

type advice struct{ offset, length int64 }

// prefetchHandler records the read-ahead advice instead of giving it.
func prefetchHandler(t *testing.T, resolver Resolver, prefetch int64) (*Handler, func() []advice) {
	t.Helper()
	handler, err := NewHandler(resolver, Options{MaxConcurrent: 4, WriteTimeout: time.Second, WriteError: testWriteError, PrefetchBytes: prefetch})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var calls []advice
	handler.advise = func(_ *os.File, offset, length int64) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, advice{offset, length})
	}
	return handler, func() []advice {
		mu.Lock()
		defer mu.Unlock()
		out := calls
		calls = nil
		return out
	}
}

// TestPrefetchAdviceKeepsBytes checks which ranges are advised and that
// advice never changes a response (G10.6) or the original file (G10.10).
func TestPrefetchAdviceKeepsBytes(t *testing.T) {
	source, content := fixture(t)
	path := filepath.Join(source.Root, source.RelativePath)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	before := sha256.Sum256(content)
	size := int64(len(content))
	source.SeekIndex = SeekIndex{Offset: size - 4096, Length: 4096, Size: size, ModifiedUnixNano: info.ModTime().UnixNano()}
	current := source
	handler, calls := prefetchHandler(t, resolveFunc(func(context.Context, access.Principal, string) (Source, error) { return current, nil }), 64<<10)
	get := func(method, rangeHeader string) *httptest.ResponseRecorder {
		r := nativeRequest(method, "/stream")
		if rangeHeader != "" {
			r.Header.Set("Range", rangeHeader)
		}
		w := httptest.NewRecorder()
		handler.ServeSource(w, r, "source")
		return w
	}
	for _, tc := range []struct {
		name, header string
		status       int
		body         []byte
		want         []advice
	}{
		{"whole file", "", http.StatusOK, content, []advice{{0, 64 << 10}, {size - 4096, 4096}}},
		{"from start", "bytes=0-99", http.StatusPartialContent, content[:100], []advice{{0, 64 << 10}, {size - 4096, 4096}}},
		{"seek", "bytes=100000-100099", http.StatusPartialContent, content[100000:100100], []advice{{100000, 64 << 10}}},
		{"near end", "bytes=-10", http.StatusPartialContent, content[size-10:], []advice{{size - 10, 10}}},
		{"beyond end", "bytes=99999999-", http.StatusRequestedRangeNotSatisfiable, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := get(http.MethodGet, tc.header)
			if w.Code != tc.status || tc.body != nil && !bytes.Equal(w.Body.Bytes(), tc.body) {
				t.Fatalf("status %d, %d bytes", w.Code, w.Body.Len())
			}
			if got := calls(); !equalAdvice(got, tc.want) {
				t.Fatalf("advice %v, want %v", got, tc.want)
			}
		})
	}
	// Multipart ranges are unchanged; advice covers the first range only.
	w := get(http.MethodGet, "bytes=0-9,20-29")
	if w.Code != http.StatusPartialContent || !bytes.Contains(w.Body.Bytes(), content[20:30]) {
		t.Fatal("multipart range changed")
	}
	if got := calls(); len(got) != 2 || got[0] != (advice{0, 64 << 10}) {
		t.Fatalf("multipart advice %v", got)
	}
	// HEAD reads nothing and is not advised.
	if w := get(http.MethodHead, ""); w.Code != http.StatusOK || len(calls()) != 0 {
		t.Fatal("HEAD was advised")
	}
	// A recorded index for another stamp (the file changed) is ignored.
	current.SeekIndex.ModifiedUnixNano++
	get(http.MethodGet, "")
	if got := calls(); !equalAdvice(got, []advice{{0, 64 << 10}}) {
		t.Fatalf("stale index advised: %v", got)
	}
	current.SeekIndex = SeekIndex{Offset: size, Length: 10, Size: size, ModifiedUnixNano: info.ModTime().UnixNano()}
	get(http.MethodGet, "")
	if got := calls(); !equalAdvice(got, []advice{{0, 64 << 10}}) {
		t.Fatalf("index outside the file advised: %v", got)
	}
	after, err := os.ReadFile(path)
	if err != nil || sha256.Sum256(after) != before {
		t.Fatal("delivery changed the original file")
	}
}

func TestPrefetchDisabled(t *testing.T) {
	source, content := fixture(t)
	source.SeekIndex = SeekIndex{Offset: 0, Length: 10, Size: int64(len(content))}
	handler, calls := prefetchHandler(t, resolveFunc(func(context.Context, access.Principal, string) (Source, error) { return source, nil }), 0)
	w := httptest.NewRecorder()
	handler.ServeSource(w, nativeRequest(http.MethodGet, "/stream"), "source")
	if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), content) || len(calls()) != 0 {
		t.Fatal("disabled prefetch advised or changed the response")
	}
}

func TestBufferAndPrefetchOptions(t *testing.T) {
	resolver := resolveFunc(func(context.Context, access.Principal, string) (Source, error) { return Source{}, ErrNotFound })
	for _, tc := range []struct {
		buffer   int
		prefetch int64
		ok       bool
	}{
		{0, 0, true}, {MinBufferSize, 0, true}, {MaxBufferSize, MaxPrefetchBytes, true},
		{MinBufferSize - 1, 0, false}, {MaxBufferSize + 1, 0, false}, {-1, 0, false},
		{0, -1, false}, {0, MaxPrefetchBytes + 1, false},
	} {
		handler, err := NewHandler(resolver, Options{MaxConcurrent: 1, WriteTimeout: time.Second, WriteError: testWriteError, BufferSize: tc.buffer, PrefetchBytes: tc.prefetch})
		if (err == nil) != tc.ok {
			t.Fatalf("buffer %d prefetch %d: %v", tc.buffer, tc.prefetch, err)
		}
		if err != nil && !errors.Is(err, ErrInvalidRequest) {
			t.Fatal(err)
		}
		if err == nil {
			want := tc.buffer
			if want == 0 {
				want = DefaultBufferSize
			}
			if got := len(*handler.buffers.Get().(*[]byte)); got != want {
				t.Fatalf("buffer %d, want %d", got, want)
			}
		}
	}
}

// TestConfiguredBufferCopiesExactly runs the buffered path (a bandwidth
// limit forbids the zero-copy path) with the smallest and largest buffer.
func TestConfiguredBufferCopiesExactly(t *testing.T) {
	source, content := fixture(t)
	for _, size := range []int{MinBufferSize, MaxBufferSize} {
		pool := &sync.Pool{New: func() any { buffer := make([]byte, size); return &buffer }}
		recorder := httptest.NewRecorder()
		writer := &streamWriter{ResponseWriter: recorder, request: nativeRequest("GET", "/stream"), controller: http.NewResponseController(recorder), timeout: time.Second, buffers: pool}
		file, err := os.Open(filepath.Join(source.Root, source.RelativePath))
		if err != nil {
			t.Fatal(err)
		}
		n, err := writer.ReadFrom(file)
		_ = file.Close()
		if err != nil || n != int64(len(content)) || !bytes.Equal(recorder.Body.Bytes(), content) {
			t.Fatalf("buffer %d: n=%d err=%v", size, n, err)
		}
	}
}

func equalAdvice(a, b []advice) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
