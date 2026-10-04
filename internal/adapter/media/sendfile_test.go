package media

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
)

// countZeroCopy counts streams handed to the connection's ReaderFrom.
func countZeroCopy(t testing.TB) *atomic.Int32 {
	t.Helper()
	var calls atomic.Int32
	previous := testHookZeroCopy
	testHookZeroCopy = func() { calls.Add(1) }
	t.Cleanup(func() { testHookZeroCopy = previous })
	return &calls
}

// trackedServer serves the handler on loopback TCP. Each request context can
// be cancelled through cancels without closing the client connection, and
// done receives when ServeSource returns.
type trackedServer struct {
	*httptest.Server
	cancels chan context.CancelFunc
	done    chan time.Time
}

func newTrackedServer(t *testing.T, handler *Handler) *trackedServer {
	t.Helper()
	s := &trackedServer{cancels: make(chan context.CancelFunc, 4), done: make(chan time.Time, 4)}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithCancel(access.WithPrincipal(r.Context(), streamPrincipal("u", "s")))
		defer cancel()
		select {
		case s.cancels <- cancel:
		default:
		}
		handler.ServeSource(w, r.WithContext(ctx), "source")
		select {
		case s.done <- time.Now():
		default:
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *trackedServer) waitDone(t *testing.T, limit time.Duration) time.Time {
	t.Helper()
	select {
	case at := <-s.done:
		return at
	case <-time.After(limit):
		t.Fatalf("stream still running after %v", limit)
		return time.Time{}
	}
}

func zeroCopyHandler(t testing.TB, source Source, options Options) *Handler {
	t.Helper()
	options.MaxConcurrent = 2
	options.WriteError = testWriteError
	if options.WriteTimeout == 0 {
		options.WriteTimeout = 5 * time.Second
	}
	handler, err := NewHandler(resolveFunc(func(context.Context, access.Principal, string) (Source, error) { return source, nil }), options)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func TestZeroCopyServesFullAndSingleRangeUnchanged(t *testing.T) {
	calls := countZeroCopy(t)
	source, content := largeFixture(t, 3<<20+123)
	source.ETag = `"rev-1"`
	server := newTrackedServer(t, zeroCopyHandler(t, source, Options{}))
	cases := []struct {
		name, rangeHeader string
		status            int
		want              []byte
		contentRange      string
	}{
		{"full", "", 200, content, ""},
		{"range", "bytes=1000-2099999", 206, content[1000:2100000], fmt.Sprintf("bytes 1000-2099999/%d", len(content))},
		{"suffix", "bytes=-70000", 206, content[len(content)-70000:], fmt.Sprintf("bytes %d-%d/%d", len(content)-70000, len(content)-1, len(content))},
		{"tiny", "bytes=5-9", 206, content[5:10], fmt.Sprintf("bytes 5-9/%d", len(content))},
	}
	for _, tc := range cases {
		request, _ := http.NewRequest("GET", server.URL+"/stream", nil)
		if tc.rangeHeader != "" {
			request.Header.Set("Range", tc.rangeHeader)
		}
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		server.waitDone(t, 5*time.Second)
		if err != nil || response.StatusCode != tc.status || !bytes.Equal(body, tc.want) || response.ContentLength != int64(len(tc.want)) {
			t.Fatalf("%s: status=%d length=%d bytes=%d err=%v", tc.name, response.StatusCode, response.ContentLength, len(body), err)
		}
		if response.Header.Get("Content-Range") != tc.contentRange || response.Header.Get("ETag") != `"rev-1"` ||
			response.Header.Get("Content-Security-Policy") != mediaContentSecurityPolicy || response.Header.Get("Accept-Ranges") != "bytes" {
			t.Fatalf("%s: headers changed: %v", tc.name, response.Header)
		}
	}
	if want := int32(len(cases)); zeroCopySupported && calls.Load() != want {
		t.Fatalf("zero-copy path used %d times, want %d", calls.Load(), want)
	} else if !zeroCopySupported && calls.Load() != 0 {
		t.Fatal("zero-copy path used on a platform without progress tracking")
	}
	// HEAD sends no body and never reaches the copy.
	response, err := server.Client().Head(server.URL + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	server.waitDone(t, 5*time.Second)
	if response.StatusCode != 200 || response.ContentLength != int64(len(content)) || calls.Load() != int32(len(cases)) && zeroCopySupported {
		t.Fatalf("HEAD: status=%d length=%d calls=%d", response.StatusCode, response.ContentLength, calls.Load())
	}
}

func TestZeroCopySkipsMultipartAndThrottledStreams(t *testing.T) {
	calls := countZeroCopy(t)
	source, content := largeFixture(t, 1<<20)
	server := newTrackedServer(t, zeroCopyHandler(t, source, Options{}))
	request, _ := http.NewRequest("GET", server.URL+"/stream", nil)
	request.Header.Set("Range", "bytes=0-99,500000-500099")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	mediaType, params, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || response.StatusCode != 206 || mediaType != "multipart/byteranges" {
		t.Fatalf("multipart response: status=%d type=%q err=%v", response.StatusCode, mediaType, err)
	}
	reader := multipart.NewReader(response.Body, params["boundary"])
	for _, want := range [][]byte{content[:100], content[500000:500100]} {
		part, err := reader.NextPart()
		if err != nil {
			t.Fatal(err)
		}
		got, _ := io.ReadAll(part)
		if !bytes.Equal(got, want) {
			t.Fatal("multipart part corrupted")
		}
	}
	response.Body.Close()
	server.waitDone(t, 5*time.Second)
	throttled := newTrackedServer(t, zeroCopyHandler(t, source, Options{Limits: Limits{BandwidthLimit: true, MaxKbpsPerUser: 400_000}}))
	response, err = throttled.Client().Get(throttled.URL + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	throttled.waitDone(t, 5*time.Second)
	if err != nil || !bytes.Equal(body, content) {
		t.Fatal("throttled download corrupted", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("multipart or throttled stream used the zero-copy path %d times", calls.Load())
	}
}

// stalledDownload starts a full download, reads the first bytes and then
// stops reading so the server blocks on a full socket buffer.
func stalledDownload(t *testing.T, server *trackedServer, content []byte) (*http.Response, context.CancelFunc) {
	t.Helper()
	response, err := server.Client().Get(server.URL + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { response.Body.Close() })
	if response.StatusCode != 200 || response.ContentLength != int64(len(content)) {
		t.Fatal("unexpected response", response.StatusCode, response.ContentLength)
	}
	head := make([]byte, 256<<10)
	if _, err = io.ReadFull(response.Body, head); err != nil || !bytes.Equal(head, content[:len(head)]) {
		t.Fatal("initial bytes", err)
	}
	var cancel context.CancelFunc
	select {
	case cancel = <-server.cancels:
	case <-time.After(time.Second):
		t.Fatal("request context not registered")
	}
	// Let the server fill the socket buffers and block in the transfer.
	time.Sleep(300 * time.Millisecond)
	return response, cancel
}

// assertCutShort checks that the client got a prefix of the media and then an
// error instead of the complete file.
func assertCutShort(t *testing.T, response *http.Response, content []byte) {
	t.Helper()
	rest, err := io.ReadAll(response.Body)
	delivered := 256<<10 + len(rest)
	if err == nil || delivered >= len(content) {
		t.Fatalf("download survived: bytes=%d err=%v", delivered, err)
	}
	if !bytes.Equal(rest, content[256<<10:delivered]) {
		t.Fatal("bytes before the cut were corrupted")
	}
	t.Logf("client received %d of %d bytes before the cut", delivered, len(content))
}

// The zero-copy fast path must stop as promptly as the buffered path when
// the request ends, the session is revoked or the client stops reading.
func TestZeroCopyLoopbackCancellationRevocationAndTimeout(t *testing.T) {
	const size = 96 << 20 // larger than loopback socket buffers
	source, content := largeFixture(t, size)
	t.Run("request cancelled", func(t *testing.T) {
		calls := countZeroCopy(t)
		server := newTrackedServer(t, zeroCopyHandler(t, source, Options{}))
		response, cancel := stalledDownload(t, server, content)
		cancelled := time.Now()
		cancel()
		cut := server.waitDone(t, 2*time.Second).Sub(cancelled)
		t.Logf("stream ended %v after cancellation", cut)
		if cut > 500*time.Millisecond {
			t.Fatalf("cancelled stream ended after %v", cut)
		}
		assertCutShort(t, response, content)
		if zeroCopySupported && calls.Load() != 1 {
			t.Fatal("download did not use the zero-copy path")
		}
	})
	t.Run("session revoked", func(t *testing.T) {
		calls := countZeroCopy(t)
		sessions := &fakeSessions{}
		sessions.active.Store(true)
		server := newTrackedServer(t, zeroCopyHandler(t, source, Options{Sessions: sessions, SessionCheckInterval: 200 * time.Millisecond}))
		response, _ := stalledDownload(t, server, content)
		sessions.active.Store(false)
		revoked := time.Now()
		cut := server.waitDone(t, 3*time.Second).Sub(revoked)
		t.Logf("stream ended %v after revocation", cut)
		// One check interval plus scheduling slack.
		if cut > 700*time.Millisecond {
			t.Fatalf("revoked stream ended after %v", cut)
		}
		assertCutShort(t, response, content)
		if zeroCopySupported && calls.Load() != 1 {
			t.Fatal("download did not use the zero-copy path")
		}
	})
	t.Run("client stalled past write timeout", func(t *testing.T) {
		server := newTrackedServer(t, zeroCopyHandler(t, source, Options{WriteTimeout: 600 * time.Millisecond}))
		started := time.Now()
		response, _ := stalledDownload(t, server, content)
		elapsed := server.waitDone(t, 3*time.Second).Sub(started)
		t.Logf("stalled stream ended %v after start", elapsed)
		if elapsed < 600*time.Millisecond {
			t.Fatalf("stalled stream ended before the write timeout: %v", elapsed)
		}
		assertCutShort(t, response, content)
	})
}

// A client that keeps reading slowly must not be cut by the write timeout,
// even though one sendfile call outlasts it: progress extends the deadline.
// The offset only moves when the kernel wakes the writer, after about half
// the socket send buffer has drained (200-300 ms at this rate on loopback),
// so the timeout leaves room for that granularity.
func TestZeroCopySlowReaderOutlivesWriteTimeout(t *testing.T) {
	calls := countZeroCopy(t)
	const size = 96 << 20
	source, content := largeFixture(t, size)
	server := newTrackedServer(t, zeroCopyHandler(t, source, Options{WriteTimeout: 1500 * time.Millisecond}))
	response, err := server.Client().Get(server.URL + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var body bytes.Buffer
	chunk := make([]byte, 64<<10)
	// About 3.2 seconds at up to 16 MiB/s, more than two write timeouts.
	deadline := time.Now().Add(3200 * time.Millisecond)
	for time.Now().Before(deadline) {
		n, err := io.ReadFull(response.Body, chunk)
		body.Write(chunk[:n])
		if err != nil {
			t.Fatalf("slow reader cut after %d bytes: %v", body.Len(), err)
		}
		time.Sleep(4 * time.Millisecond)
	}
	rest, err := io.ReadAll(response.Body)
	body.Write(rest)
	if err != nil || !bytes.Equal(body.Bytes(), content) {
		t.Fatalf("slow download incomplete: bytes=%d err=%v", body.Len(), err)
	}
	server.waitDone(t, 2*time.Second)
	if zeroCopySupported && calls.Load() != 1 {
		t.Fatal("download did not use the zero-copy path")
	}
}

// BenchmarkLoopbackDownload measures a full download and a single Range over
// loopback TCP, client included. On Linux it also reports process CPU time:
//
//	go test ./internal/adapter/media -run '^$' -bench LoopbackDownload -benchmem
func BenchmarkLoopbackDownload(b *testing.B) {
	root := b.TempDir()
	const size = 64 << 20
	content := bytes.Repeat([]byte("0123456789abcdef"), size/16)
	if err := os.WriteFile(filepath.Join(root, "large.mkv"), content, 0o600); err != nil {
		b.Fatal(err)
	}
	source := Source{Root: root, RelativePath: "large.mkv", ContentType: "video/x-matroska"}
	handler := zeroCopyHandler(b, source, Options{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeSource(w, r.WithContext(access.WithPrincipal(r.Context(), streamPrincipal("u", "s"))), "source")
	}))
	b.Cleanup(server.Close)
	client := server.Client()
	for _, bc := range []struct {
		name, rangeHeader string
		length            int64
	}{{"full", "", size}, {"range", "bytes=1048576-34603007", 32 << 20}} {
		b.Run(bc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(bc.length)
			cpuStart, cpuOK := processCPU()
			for b.Loop() {
				request, _ := http.NewRequest("GET", server.URL+"/stream", nil)
				if bc.rangeHeader != "" {
					request.Header.Set("Range", bc.rangeHeader)
				}
				response, err := client.Do(request)
				if err != nil {
					b.Fatal(err)
				}
				n, err := io.Copy(io.Discard, response.Body)
				response.Body.Close()
				if err != nil || n != bc.length {
					b.Fatalf("downloaded %d bytes: %v", n, err)
				}
			}
			// Process CPU covers client and server; the client side is the
			// same on both paths, so differences come from the server.
			if cpuEnd, ok := processCPU(); ok && cpuOK {
				b.ReportMetric(float64(cpuEnd-cpuStart)/float64(b.N), "cpu-ns/op")
			}
		})
	}
}
