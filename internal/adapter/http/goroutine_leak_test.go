package httpapi

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/adapter/media"
	"github.com/MoYuanCN/Jelee/internal/platform/testutil"
)

// G29.3: a real HTTP server in front of the router, with the timeouts the
// service uses, leaves no goroutine behind after Shutdown: not for finished
// requests whose keep-alive connection is idle, a stream whose client
// vanished mid-body, nor the server loop itself. (A connection that never
// sends a request is only closed by Shutdown after five seconds, a net/http
// rule; ReadHeaderTimeout bounds it in service.)
func TestHTTPServerShutdownLeavesNoGoroutines(t *testing.T) {
	testutil.TrackGoroutines(t)
	f := newFixture(t, true, true)
	root := t.TempDir()
	// Larger than the loopback socket buffers, so the abandoned stream is
	// still writing when its client goes away. The file is sparse.
	const size = 48 << 20
	if err := os.WriteFile(filepath.Join(root, "sample.mp4"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(filepath.Join(root, "sample.mp4"), size); err != nil {
		t.Fatal(err)
	}
	f.resolver.resolve = func(context.Context, access.Principal, string) (media.Source, error) {
		return media.Source{Root: root, RelativePath: "sample.mp4", ContentType: "video/mp4"}, nil
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: f.handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	base := "http://" + listener.Addr().String()
	transport := &http.Transport{}
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}

	// Requests run one at a time: the fixture's fakes count calls without
	// locks.
	get := func(path string, want int) {
		t.Helper()
		request, err := http.NewRequest(http.MethodGet, base+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Host = "localhost"
		request.Header.Set("Authorization", "Bearer "+strings.Repeat("n", 43))
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_, copyErr := io.Copy(io.Discard, response.Body)
		if err := errors.Join(copyErr, response.Body.Close()); err != nil || response.StatusCode != want {
			t.Fatalf("GET %s: %d %v", path, response.StatusCode, err)
		}
	}
	get("/healthz", http.StatusOK)
	get("/api/v1/items", http.StatusOK)
	get("/api/v1/sources/"+sourceID+"/stream", http.StatusOK)

	// A client that reads the head of a stream and disconnects.
	abandoned, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintf(abandoned, "GET /api/v1/sources/%s/stream HTTP/1.1\r\nHost: localhost\r\nAuthorization: Bearer %s\r\n\r\n", sourceID, strings.Repeat("n", 43)); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(abandoned)
	status, err := reader.ReadString('\n')
	if err != nil || !strings.Contains(status, " 200 ") {
		t.Fatalf("abandoned stream status %q: %v", status, err)
	}
	if _, err := io.CopyN(io.Discard, reader, 1<<20); err != nil {
		t.Fatal(err)
	}
	if err := abandoned.Close(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-served; !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("serve returned %v", err)
	}
	transport.CloseIdleConnections()
}

// The client control gate's background writer stops with Close.
func TestClientControlCloseLeavesNoGoroutines(t *testing.T) {
	testutil.TrackGoroutines(t)
	store := &gateStore{}
	store.set(1, "allow")
	// newGate registers Close as a cleanup, which runs before the check.
	gate := newGate(t, store, nil, ClientControlOptions{FlushInterval: time.Millisecond})
	if w := gateRequest(gateHandler(store, gate), http.MethodGet, "Leak/1"); w.Code != http.StatusNoContent {
		t.Fatalf("gate request: %d", w.Code)
	}
	time.Sleep(5 * time.Millisecond) // let the writer flush on its ticker
}
