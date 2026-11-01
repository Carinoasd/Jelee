package logging

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/testutil"
)

// G29.3: closing the logging pipeline stops every goroutine it started: the
// async writer of each sink, each forwarder's worker and the keep-alive
// connections of its HTTP client or syslog stream.
func TestRouterCloseLeavesNoGoroutines(t *testing.T) {
	testutil.TrackGoroutines(t)
	loki := newLoki(t)
	addr, conns := tcpCollector(t)
	var stdout syncBuffer
	r, err := Open(Options{Output: OutputBoth, File: RotateOptions{Path: filepath.Join(t.TempDir(), "jelee.log")}, Forward: []ForwarderOptions{
		{Kind: ForwardLoki, BatchInterval: 10 * time.Millisecond, Loki: LokiOptions{URL: loki.URL}},
		{Kind: ForwardSyslog, Syslog: SyslogOptions{Network: "tcp", Address: addr}},
	}}, &stdout)
	if err != nil {
		t.Fatal(err)
	}
	for range 20 {
		r.Logger().Info("request completed", "component", "http", "status", 200)
	}
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	collector := <-conns
	t.Cleanup(func() { _ = collector.Close() })
	go func() { _, _ = io.Copy(io.Discard, collector) }()
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if n := len(loki.lines()); n != 20 {
		t.Fatalf("%d of 20 records reached Loki", n)
	}
}

// A refused configuration closes the forwarders it already started.
func TestRouterOpenFailureLeavesNoGoroutines(t *testing.T) {
	testutil.TrackGoroutines(t)
	if _, err := Open(Options{Forward: []ForwarderOptions{
		{Kind: ForwardLoki, Loki: LokiOptions{URL: "http://127.0.0.1:1"}},
		{Kind: ForwardSyslog, Syslog: SyslogOptions{Network: "udp", Address: "127.0.0.1:1"}},
		{Kind: ForwardLoki, Loki: LokiOptions{URL: "ftp://x"}},
	}}, &syncBuffer{}); err == nil {
		t.Fatal("bad forwarder accepted")
	}
}

// Closing a forwarder whose collector hangs on a push in flight returns after
// the flush timeout, not the request timeout, and ends that request: no
// worker and no client connection goroutine waits for the collector.
func TestForwarderCloseWithHungCollectorLeavesNoGoroutines(t *testing.T) {
	hang := make(chan struct{})
	entered := make(chan struct{}, 1)
	collector := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-hang
	}))
	defer collector.Close()
	defer close(hang)
	// The collector's own connection goroutines are the fake's, not the
	// forwarder's.
	baseline := testutil.SnapshotGoroutines("net/http.(*conn).serve", "net/http.(*connReader).backgroundRead")
	f, err := NewForwarder(ForwarderOptions{Kind: ForwardLoki, BatchSize: 1, Timeout: time.Minute, Loki: LokiOptions{URL: collector.URL}})
	if err != nil {
		t.Fatal(err)
	}
	c := fastCore(t, f)
	c.flushTimeout = 100 * time.Millisecond
	_, _ = c.Write([]byte(sampleRecord))
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	closed := make(chan error, 1)
	go func() { closed <- c.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
		if waited := time.Since(start); waited > 2*time.Second {
			t.Fatalf("Close waited %v for the hung collector", waited)
		}
		if st := c.ForwardStat(); st.Dropped != 1 || st.Sent != 0 {
			t.Fatalf("stat %+v", st)
		}
	case <-ctx.Done():
		t.Fatal("Close waited for the hung collector")
	}
	baseline.Check(t)
}
