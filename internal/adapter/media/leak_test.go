package media

import (
	"bufio"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
)

// Disconnect leak regression (G10.7, G29.5): clients that vanish mid-stream
// through every route the server knows (closed connection, revoked session,
// stalled reader past the write timeout) must leave no open media file, no
// watchdog or session watcher goroutine, no held playback slot and no
// zero-copy state behind, on both the sendfile and the buffered path.

const (
	leakClients = 6
	leakSize    = 48 << 20 // larger than the loopback socket buffers
	leakHead    = 64 << 10
)

// bufferedOnlyWriter hides the connection's io.ReaderFrom so the stream takes
// the buffered copy path, the one Windows and TLS connections use.
type bufferedOnlyWriter struct{ http.ResponseWriter }

func (w bufferedOnlyWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// leakHarness counts running handlers of a loopback server.
type leakHarness struct {
	server   *httptest.Server
	handler  *Handler
	inflight atomic.Int32
	started  chan struct{}
	conns    []net.Conn
}

func newLeakHarness(t *testing.T, source Source, buffered bool, options Options) *leakHarness {
	t.Helper()
	h := &leakHarness{started: make(chan struct{}, 4*leakClients)}
	h.handler = zeroCopyHandler(t, source, options)
	h.handler.slots = make(chan struct{}, leakClients)
	h.server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.inflight.Add(1)
		defer h.inflight.Add(-1)
		h.started <- struct{}{}
		if buffered {
			w = bufferedOnlyWriter{w}
		}
		h.handler.ServeSource(w, r.WithContext(access.WithPrincipal(r.Context(), streamPrincipal("u", "s"))), "source")
	}))
	h.server.Start()
	t.Cleanup(h.server.Close)
	return h
}

// dial opens a raw connection, requests the whole file and reads the header
// plus leakHead body bytes, so the server is inside the transfer.
func (h *leakHarness) dial(t *testing.T) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", h.server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	h.conns = append(h.conns, conn)
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := fmt.Fprint(conn, "GET /stream HTTP/1.1\r\nHost: media\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	// Closing the body would drain the rest of the file; the raw connection is
	// closed by the test instead.
	response, err := http.ReadResponse(reader, nil) //nolint:bodyclose // see above
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("response: %v %v", response, err)
	}
	if _, err := io.CopyN(io.Discard, response.Body, leakHead); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetDeadline(time.Time{})
	return conn
}

// waitIdle waits until no handler runs and fails with the stacks otherwise.
func (h *leakHarness) waitIdle(t *testing.T, limit time.Duration) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for h.inflight.Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("%d handlers still running after %v:\n%s", h.inflight.Load(), limit, goroutineDump())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func goroutineDump() string {
	buf := make([]byte, 1<<20)
	return string(buf[:runtime.Stack(buf, true)])
}

// delivery goroutines that must not outlive their stream.
var streamGoroutines = []string{"media.(*streamWriter).watchProgress", "media.(*Handler).watchSession", "media.(*Handler).deliver"}

type resourceMark struct {
	goroutines int
	fds        int
}

func markResources() resourceMark {
	// No runtime.GC here: os.File finalizers would hide an unclosed file.
	return resourceMark{goroutines: runtime.NumGoroutine(), fds: openDescriptors()}
}

// assertReleased fails unless every resource of the streams is back: the
// handlers returned, the media file has no open descriptor, no stream
// goroutine remains, the playback slots are free and the process is back to
// the mark. Closing idle client connections is part of the check because the
// test server keeps a goroutine per accepted connection.
func (h *leakHarness) assertReleased(t *testing.T, path string, before resourceMark, wantZeroCopy int, calls *atomic.Int32) {
	t.Helper()
	h.waitIdle(t, 5*time.Second)
	// The handler closes the file before it returns, without help from the
	// garbage collector, so this holds the moment the handlers are done.
	if n := openDescriptorsOf(path); n != 0 {
		t.Fatalf("%d descriptors of the media file still open after the handlers returned", n)
	}
	// Stream goroutines are joined before the handler returns (the watchdog
	// and the session watcher are waited for), so only scheduler slack is
	// allowed; a stray watchdog would otherwise linger until its next tick.
	settle := time.Now().Add(200 * time.Millisecond)
	for {
		stacks := goroutineDump()
		leaked := ""
		for _, name := range streamGoroutines {
			if strings.Contains(stacks, name) {
				leaked = name
			}
		}
		if leaked == "" {
			break
		}
		if time.Now().After(settle) {
			t.Fatalf("goroutine %s outlived its stream:\n%s", leaked, stacks)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := len(h.handler.slots); got != 0 {
		t.Fatalf("%d playback slots still held", got)
	}
	if zeroCopySupported {
		if got := int(calls.Load()); got != wantZeroCopy {
			t.Fatalf("zero-copy streams = %d, want %d", got, wantZeroCopy)
		}
	}
	for _, conn := range h.conns {
		_ = conn.Close()
	}
	h.server.CloseClientConnections()
	h.server.Close()
	http.DefaultClient.CloseIdleConnections()
	var after resourceMark
	var stacks string
	for deadline := time.Now().Add(5 * time.Second); ; {
		after = markResources()
		stacks = goroutineDump()
		leaked := ""
		for _, name := range streamGoroutines {
			if strings.Contains(stacks, name) {
				leaked = name
			}
		}
		if leaked == "" && after.goroutines <= before.goroutines && after.fds <= before.fds && openDescriptorsOf(path) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("resources not released: goroutines %d -> %d, fds %d -> %d, media file fds %d, leaked goroutine %q\n%s",
				before.goroutines, after.goroutines, before.fds, after.fds, openDescriptorsOf(path), leaked, stacks)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func fileDigest(t *testing.T, path string) [sha256.Size]byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(data)
}

func TestDisconnectReleasesEveryResource(t *testing.T) {
	for _, path := range []struct {
		name     string
		buffered bool
	}{{"sendfile", false}, {"buffered", true}} {
		t.Run(path.name, func(t *testing.T) {
			source, _ := largeFixture(t, leakSize)
			file := source.Root + "/" + source.RelativePath
			digest := fileDigest(t, file)
			wantZeroCopy := func(n int) int {
				if path.buffered {
					return 0
				}
				return n
			}

			t.Run("client closes mid-stream", func(t *testing.T) {
				calls := countZeroCopy(t)
				before := markResources()
				h := newLeakHarness(t, source, path.buffered, Options{})
				conns := make([]net.Conn, leakClients)
				for i := range conns {
					conns[i] = h.dial(t)
				}
				if fdCountingSupported && openDescriptorsOf(file) == 0 {
					t.Fatal("no open media descriptor while streaming: the probe is blind")
				}
				for _, conn := range conns {
					_ = conn.Close()
				}
				h.assertReleased(t, file, before, wantZeroCopy(leakClients), calls)
			})

			t.Run("client resets mid-stream", func(t *testing.T) {
				calls := countZeroCopy(t)
				before := markResources()
				h := newLeakHarness(t, source, path.buffered, Options{})
				for range leakClients {
					conn := h.dial(t)
					_ = conn.(*net.TCPConn).SetLinger(0)
					_ = conn.Close()
				}
				h.assertReleased(t, file, before, wantZeroCopy(leakClients), calls)
			})

			t.Run("session revoked", func(t *testing.T) {
				calls := countZeroCopy(t)
				sessions := &fakeSessions{}
				sessions.active.Store(true)
				before := markResources()
				h := newLeakHarness(t, source, path.buffered, Options{Sessions: sessions, SessionCheckInterval: 50 * time.Millisecond})
				for range leakClients {
					h.dial(t) // stays connected but stops reading
				}
				sessions.active.Store(false)
				h.waitIdle(t, 3*time.Second)
				h.assertReleased(t, file, before, wantZeroCopy(leakClients), calls)
			})

			t.Run("stalled reader past write timeout", func(t *testing.T) {
				calls := countZeroCopy(t)
				before := markResources()
				h := newLeakHarness(t, source, path.buffered, Options{WriteTimeout: 300 * time.Millisecond})
				for range leakClients {
					h.dial(t)
				}
				h.waitIdle(t, 5*time.Second)
				h.assertReleased(t, file, before, wantZeroCopy(leakClients), calls)
			})

			t.Run("request context cancelled", func(t *testing.T) {
				calls := countZeroCopy(t)
				before := markResources()
				handler := zeroCopyHandler(t, source, Options{})
				var inflight atomic.Int32
				var cancels sync.Map
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					inflight.Add(1)
					defer inflight.Add(-1)
					ctx, cancel := context.WithCancel(access.WithPrincipal(r.Context(), streamPrincipal("u", "s")))
					defer cancel()
					cancels.Store(r.RemoteAddr, cancel)
					if path.buffered {
						w = bufferedOnlyWriter{w}
					}
					handler.ServeSource(w, r.WithContext(ctx), "source")
				}))
				defer server.Close()
				h := &leakHarness{server: server, handler: handler, started: make(chan struct{})}
				for range 2 {
					h.dial(t)
				}
				cancels.Range(func(_, cancel any) bool { cancel.(context.CancelFunc)(); return true })
				for deadline := time.Now().Add(3 * time.Second); inflight.Load() != 0; time.Sleep(5 * time.Millisecond) {
					if time.Now().After(deadline) {
						t.Fatalf("cancelled streams still running:\n%s", goroutineDump())
					}
				}
				h.assertReleased(t, file, before, wantZeroCopy(2), calls)
			})

			if fileDigest(t, file) != digest {
				t.Fatal("original file changed during playback")
			}
		})
	}
}
