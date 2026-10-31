package logging

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const sampleRecord = `{"time":"2026-10-31T08:09:10.123456789Z","level":"INFO","msg":"request completed","component":"http","status":200}`

// fastCore makes retries quick; it must run before the first record.
func fastCore(t *testing.T, f Forwarder) *forwardCore {
	t.Helper()
	c, ok := f.(*forwardCore)
	if !ok {
		t.Fatalf("unexpected forwarder %T", f)
	}
	c.backoffMin, c.backoffMax = 10*time.Millisecond, 40*time.Millisecond
	c.notify = nil
	return c
}

func mustForwarder(t *testing.T, o ForwarderOptions) *forwardCore {
	t.Helper()
	f, err := NewForwarder(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return fastCore(t, f)
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func record(level, component, msg string) string {
	return fmt.Sprintf(`{"time":"2026-10-31T08:09:10Z","level":%q,"msg":%q,"component":%q}`+"\n", level, msg, component)
}

// --- syslog ---------------------------------------------------------------

var syslogHeader = regexp.MustCompile(`^<(\d+)>1 (\S+) (\S+) (\S+) (\d+) (\S+) - \x{FEFF}(.*)$`)

func TestSyslogUDPMessageFormat(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pc.Close() }()
	f := mustForwarder(t, ForwarderOptions{Kind: ForwardSyslog, Syslog: SyslogOptions{Address: pc.LocalAddr().String(), Hostname: "media-1", AppName: "jelee-test"}})
	for _, line := range []string{sampleRecord + "\n", record("ERROR", "security", "boom"), record("DEBUG", "", "nocomp"), record("WARN", "bad component!", "w")} {
		if _, err := f.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []struct{ pri, msgID string }{{"134", "http"}, {"131", "security"}, {"135", "-"}, {"132", "badcomponent!"}}
	buf := make([]byte, 65536)
	for i, w := range want {
		_ = pc.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, _, err := pc.ReadFrom(buf)
		if err != nil {
			t.Fatalf("datagram %d: %v", i, err)
		}
		m := syslogHeader.FindStringSubmatch(string(buf[:n]))
		if m == nil {
			t.Fatalf("datagram %d is not RFC 5424: %q", i, buf[:n])
		}
		if m[1] != w.pri || m[3] != "media-1" || m[4] != "jelee-test" || m[6] != w.msgID {
			t.Fatalf("datagram %d header %v, want pri %s msgid %s", i, m[1:7], w.pri, w.msgID)
		}
		if i == 0 && m[2] != "2026-10-31T08:09:10.123456Z" {
			t.Fatalf("timestamp %q", m[2])
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(m[7]), &rec); err != nil {
			t.Fatalf("MSG is not the JSON record: %q", m[7])
		}
	}
	if st := f.ForwardStat(); st.Sent != 4 || st.Dropped != 0 {
		t.Fatalf("stat %+v", st)
	}
}

// tcpCollector accepts connections and delivers raw bytes.
func tcpCollector(t *testing.T) (addr string, conns <-chan net.Conn) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	ch := make(chan net.Conn, 8)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			ch <- c
		}
	}()
	return l.Addr().String(), ch
}

func TestSyslogTCPFramingAndReconnect(t *testing.T) {
	addr, conns := tcpCollector(t)
	f := mustForwarder(t, ForwarderOptions{Kind: ForwardSyslog, Syslog: SyslogOptions{Network: "tcp", Address: addr, Hostname: "h"}})
	for _, msg := range []string{"a", "bb"} {
		_, _ = f.Write([]byte(record("INFO", "http", msg)))
	}
	if err := f.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	c1 := <-conns
	r := bufio.NewReader(c1)
	for _, msg := range []string{"a", "bb"} {
		var n int
		if _, err := fmt.Fscanf(r, "%d ", &n); err != nil {
			t.Fatal(err)
		}
		body := make([]byte, n)
		if _, err := io.ReadFull(r, body); err != nil {
			t.Fatal(err)
		}
		m := syslogHeader.FindStringSubmatch(string(body))
		if m == nil || !strings.Contains(m[7], `"msg":"`+msg+`"`) {
			t.Fatalf("octet-counted frame %q", body)
		}
	}
	// The collector restarts: the next push reconnects.
	_ = c1.Close()
	var c2 net.Conn
	eventually(t, "reconnect", func() bool {
		_, _ = f.Write([]byte(record("INFO", "http", "after")))
		_ = f.Flush(context.Background())
		select {
		case c2 = <-conns:
			return true
		default:
			return false
		}
	})
	// A write to a connection the peer closed can still succeed once, so a
	// record may be lost to the dead connection (at-most-once on a broken
	// stream); the records sent after the reconnect arrive.
	_ = c2.SetReadDeadline(time.Now().Add(2 * time.Second))
	data := make([]byte, 4096)
	n, err := c2.Read(data)
	if err != nil || !strings.Contains(string(data[:n]), `"msg":"after"`) {
		t.Fatalf("nothing delivered after the reconnect: %q %v", data[:n], err)
	}
}

func TestSyslogNewlineFraming(t *testing.T) {
	addr, conns := tcpCollector(t)
	f := mustForwarder(t, ForwarderOptions{Kind: ForwardSyslog, Syslog: SyslogOptions{Network: "tcp", Address: addr, Framing: "newline"}})
	_, _ = f.Write([]byte(record("INFO", "http", "one")))
	_, _ = f.Write([]byte(record("INFO", "http", "two")))
	if err := f.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(<-conns)
	for _, msg := range []string{"one", "two"} {
		line, err := r.ReadString('\n')
		if err != nil || !strings.HasPrefix(line, "<134>1 ") || !strings.Contains(line, `"msg":"`+msg+`"`) {
			t.Fatalf("line %q err %v", line, err)
		}
	}
}

func TestSyslogUnixSockets(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are refused on Windows")
	}
	dir, err := os.MkdirTemp("", "jl")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	for _, network := range []string{"unix", "unixgram"} {
		path := filepath.Join(dir, network+".sock")
		got := make(chan string, 1)
		if network == "unixgram" {
			pc, err := net.ListenPacket("unixgram", path)
			if err != nil {
				t.Skipf("unixgram unavailable: %v", err)
			}
			defer func() { _ = pc.Close() }()
			go func() {
				b := make([]byte, 4096)
				n, _, _ := pc.ReadFrom(b)
				got <- string(b[:n])
			}()
		} else {
			l, err := net.Listen("unix", path)
			if err != nil {
				t.Skipf("unix unavailable: %v", err)
			}
			defer func() { _ = l.Close() }()
			go func() {
				c, err := l.Accept()
				if err != nil {
					return
				}
				b := make([]byte, 4096)
				n, _ := c.Read(b)
				got <- string(b[:n])
			}()
		}
		f := mustForwarder(t, ForwarderOptions{Kind: ForwardSyslog, Syslog: SyslogOptions{Network: network, Address: path}})
		_, _ = f.Write([]byte(sampleRecord))
		if err := f.Flush(context.Background()); err != nil {
			t.Fatal(network, err)
		}
		select {
		case msg := <-got:
			if !strings.Contains(msg, "<134>1 2026-10-31T08:09:10.123456Z") {
				t.Fatalf("%s message %q", network, msg)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: nothing received", network)
		}
	}
}

func TestSyslogRefusesUnixSocketsOnWindows(t *testing.T) {
	for _, network := range []string{"unix", "unixgram"} {
		o := ForwarderOptions{Kind: ForwardSyslog, Syslog: SyslogOptions{Network: network, Address: "/run/log.sock"}}
		if err := o.validate("windows"); err == nil || !strings.Contains(err.Error(), "Windows") {
			t.Errorf("%s on Windows: %v", network, err)
		}
		if err := o.validate("linux"); err != nil {
			t.Errorf("%s on Linux: %v", network, err)
		}
	}
	// Network sockets work everywhere.
	if err := (ForwarderOptions{Kind: ForwardSyslog, Syslog: SyslogOptions{Network: "tcp", Address: "log.example:514"}}).validate("windows"); err != nil {
		t.Fatal(err)
	}
}

// --- loki -----------------------------------------------------------------

type lokiCollector struct {
	*httptest.Server
	mu       sync.Mutex
	bodies   []lokiPush
	headers  []http.Header
	paths    []string
	attempts atomic.Int64
	// statuses is consumed one per request; the last one repeats.
	statuses []int
	extra    http.Header
}

func newLoki(t *testing.T, statuses ...int) *lokiCollector {
	t.Helper()
	c := &lokiCollector{statuses: statuses}
	if len(statuses) == 0 {
		c.statuses = []int{http.StatusNoContent}
	}
	c.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(c.attempts.Add(1))
		status := c.statuses[min(n-1, len(c.statuses)-1)]
		var push lokiPush
		_ = json.NewDecoder(r.Body).Decode(&push)
		c.mu.Lock()
		c.headers = append(c.headers, r.Header.Clone())
		c.paths = append(c.paths, r.Method+" "+r.URL.RequestURI())
		if status < 300 {
			c.bodies = append(c.bodies, push)
		}
		c.mu.Unlock()
		for k, v := range c.extra {
			w.Header()[k] = v
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(c.Close)
	return c
}

func (c *lokiCollector) lines() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, b := range c.bodies {
		for _, s := range b.Streams {
			for _, v := range s.Values {
				out = append(out, v[1])
			}
		}
	}
	return out
}

func TestLokiPushFormatLabelsAndAuth(t *testing.T) {
	c := newLoki(t)
	f := mustForwarder(t, ForwarderOptions{Kind: ForwardLoki, Loki: LokiOptions{URL: c.URL, Username: "u", Password: "p", TenantID: "tenant-1", Service: "jelee-prod"}})
	_, _ = f.Write([]byte(sampleRecord + "\n"))
	_, _ = f.Write([]byte(record("ERROR", "security", "denied")))
	_, _ = f.Write([]byte(record("INFO", "weird component", "x")))
	_, _ = f.Write([]byte(record("TRACE", "http", "oddlevel")))
	if err := f.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.paths) != 1 || c.paths[0] != "POST /loki/api/v1/push" {
		t.Fatalf("requests %v", c.paths)
	}
	h := c.headers[0]
	if h.Get("Content-Type") != "application/json" || h.Get("X-Scope-OrgID") != "tenant-1" {
		t.Fatalf("headers %v", h)
	}
	if user, pass, ok := (&http.Request{Header: h}).BasicAuth(); !ok || user != "u" || pass != "p" {
		t.Fatalf("basic auth missing: %v", h.Get("Authorization"))
	}
	got := map[string]string{}
	for _, s := range c.bodies[0].Streams {
		if len(s.Stream) != 3 || s.Stream["service"] != "jelee-prod" {
			t.Fatalf("labels %v", s.Stream)
		}
		for _, v := range s.Values {
			got[s.Stream["component"]+"/"+s.Stream["level"]] = v[0] + " " + v[1]
		}
	}
	want := map[string]string{"http/info": "1793347750123456789 " + sampleRecord, "security/error": "", "unknown/info": ""}
	// http/info holds both the sample and the record with the unknown level.
	for k, v := range want {
		if _, ok := got[k]; !ok {
			t.Fatalf("stream %s missing in %v", k, got)
		}
		if v != "" && got[k] == "" {
			t.Fatalf("stream %s empty", k)
		}
	}
	if ts := strings.SplitN(got["http/info"], " ", 2)[0]; ts == "" {
		t.Fatal("no timestamp")
	}
	if got := strings.Fields(got["security/error"])[0]; got != strconv.FormatInt(time.Date(2026, 10, 31, 8, 9, 10, 0, time.UTC).UnixNano(), 10) {
		t.Fatalf("timestamp %s", got)
	}
}

func TestLokiBearerAndURLPath(t *testing.T) {
	c := newLoki(t)
	f := mustForwarder(t, ForwarderOptions{Kind: ForwardLoki, Loki: LokiOptions{URL: c.URL + "/prefix/", BearerToken: "tok-123"}})
	_, _ = f.Write([]byte(sampleRecord))
	if err := f.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.paths[0] != "POST /prefix/loki/api/v1/push" || c.headers[0].Get("Authorization") != "Bearer tok-123" {
		t.Fatalf("%v %v", c.paths, c.headers[0])
	}
	for in, want := range map[string]string{
		"http://loki:3100":                    "http://loki:3100/loki/api/v1/push",
		"http://loki:3100/":                   "http://loki:3100/loki/api/v1/push",
		"https://x.example/loki/api/v1/push":  "https://x.example/loki/api/v1/push",
		"https://x.example/loki/api/v1/push/": "https://x.example/loki/api/v1/push",
		"https://x.example/grafana/loki/":     "https://x.example/grafana/loki/loki/api/v1/push",
	} {
		u, err := (LokiOptions{URL: in}).parseURL()
		if err != nil || u.String() != want {
			t.Errorf("%s -> %v %v, want %s", in, u, err, want)
		}
	}
}

func TestLokiBatchesBySizeAndInterval(t *testing.T) {
	c := newLoki(t)
	f := mustForwarder(t, ForwarderOptions{Kind: ForwardLoki, BatchSize: 3, BatchInterval: 40 * time.Millisecond, Loki: LokiOptions{URL: c.URL}})
	for i := range 7 {
		_, _ = f.Write([]byte(record("INFO", "http", fmt.Sprint("m", i))))
	}
	// 7 records: two full batches of 3 right away, the last one by interval.
	eventually(t, "all lines", func() bool { return len(c.lines()) == 7 })
	if n := c.attempts.Load(); n != 3 {
		t.Fatalf("%d pushes, want 3", n)
	}
}

func TestLokiRetriesWithBackoffThenSucceeds(t *testing.T) {
	c := newLoki(t, 503, 429, 500, 204)
	f := mustForwarder(t, ForwarderOptions{Kind: ForwardLoki, BatchInterval: 10 * time.Millisecond, Loki: LokiOptions{URL: c.URL}})
	start := time.Now()
	_, _ = f.Write([]byte(sampleRecord))
	eventually(t, "delivery", func() bool { return len(c.lines()) == 1 })
	// Waits are at least half of 10, 20, 40 ms.
	if elapsed := time.Since(start); elapsed < 35*time.Millisecond {
		t.Fatalf("retries were not spaced: %v", elapsed)
	}
	st := f.ForwardStat()
	if c.attempts.Load() != 4 || st.Failures != 3 || st.Sent != 1 || st.Dropped != 0 {
		t.Fatalf("attempts %d stat %+v", c.attempts.Load(), st)
	}
}

func TestLokiGivesUpAfterMaxAttemptsAndCountsDrops(t *testing.T) {
	c := newLoki(t, 503)
	f := mustForwarder(t, ForwarderOptions{Kind: ForwardLoki, BatchSize: 2, BatchInterval: 10 * time.Millisecond, Loki: LokiOptions{URL: c.URL}})
	f.maxAttempts = 3
	_, _ = f.Write([]byte(sampleRecord))
	_, _ = f.Write([]byte(sampleRecord))
	eventually(t, "drop", func() bool { return f.ForwardStat().Dropped == 2 })
	if c.attempts.Load() != 3 || f.ForwardStat().Failures != 3 {
		t.Fatalf("attempts %d stat %+v", c.attempts.Load(), f.ForwardStat())
	}
	// The collector recovers: later records flow again.
	c.mu.Lock()
	c.statuses = []int{204}
	c.attempts.Store(0)
	c.mu.Unlock()
	_, _ = f.Write([]byte(sampleRecord))
	eventually(t, "recovery", func() bool { return len(c.lines()) == 1 })
}

func TestLokiPermanentRejectionIsNotRetried(t *testing.T) {
	c := newLoki(t, 400)
	f := mustForwarder(t, ForwarderOptions{Kind: ForwardLoki, BatchInterval: 10 * time.Millisecond, Loki: LokiOptions{URL: c.URL}})
	_, _ = f.Write([]byte(sampleRecord))
	eventually(t, "drop", func() bool { return f.ForwardStat().Dropped == 1 })
	time.Sleep(60 * time.Millisecond)
	if c.attempts.Load() != 1 {
		t.Fatalf("a 400 was retried %d times", c.attempts.Load())
	}
}

func TestLokiRetryAfterIsHonoured(t *testing.T) {
	c := newLoki(t, 429, 204)
	c.extra = http.Header{"Retry-After": {"1"}}
	f := mustForwarder(t, ForwarderOptions{Kind: ForwardLoki, BatchInterval: 10 * time.Millisecond, Loki: LokiOptions{URL: c.URL}})
	start := time.Now()
	_, _ = f.Write([]byte(sampleRecord))
	eventually(t, "delivery", func() bool { return len(c.lines()) == 1 })
	if time.Since(start) < 900*time.Millisecond {
		t.Fatalf("Retry-After ignored: %v", time.Since(start))
	}
}

func TestLokiDoesNotFollowRedirects(t *testing.T) {
	var hit atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit.Add(1) }))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()
	f := mustForwarder(t, ForwarderOptions{Kind: ForwardLoki, BatchInterval: 10 * time.Millisecond, Loki: LokiOptions{URL: redirector.URL, Username: "u", Password: "p"}})
	_, _ = f.Write([]byte(sampleRecord))
	eventually(t, "drop", func() bool { return f.ForwardStat().Dropped == 1 })
	if hit.Load() != 0 {
		t.Fatal("the redirect target received the request and its credentials")
	}
}

func TestLokiTLSUsesSystemRootsOnly(t *testing.T) {
	var hit atomic.Int64
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hit.Add(1); w.WriteHeader(204) }))
	defer srv.Close()
	f := mustForwarder(t, ForwarderOptions{Kind: ForwardLoki, BatchInterval: 10 * time.Millisecond, Loki: LokiOptions{URL: srv.URL}})
	f.maxAttempts = 2
	_, _ = f.Write([]byte(sampleRecord))
	eventually(t, "tls failure", func() bool { return f.ForwardStat().Dropped == 1 })
	if hit.Load() != 0 || f.ForwardStat().Failures != 2 {
		t.Fatalf("an untrusted certificate was accepted: hits %d stat %+v", hit.Load(), f.ForwardStat())
	}
}

func TestForwardersNeverDisableCertificateChecks(t *testing.T) {
	files, err := filepath.Glob("forward*.go")
	if err != nil || len(files) == 0 {
		t.Fatal("no forwarder sources", err)
	}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, banned := range []string{"InsecureSkipVerify", "RootCAs", "VerifyPeerCertificate", "VerifyConnection"} {
			if strings.Contains(string(data), banned) {
				t.Errorf("%s uses %s", name, banned)
			}
		}
	}
}

// --- queue, drops, shutdown -------------------------------------------------

// gateSender blocks every push until released.
type gateSender struct {
	entered chan struct{}
	release chan struct{}
	got     atomic.Int64
	err     error
}

func newGateSender() *gateSender {
	return &gateSender{entered: make(chan struct{}, 64), release: make(chan struct{})}
}

func (g *gateSender) send(ctx context.Context, records [][]byte) error {
	g.entered <- struct{}{}
	select {
	case <-g.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	if g.err != nil {
		return g.err
	}
	g.got.Add(int64(len(records)))
	return nil
}
func (g *gateSender) close() {}

func TestForwarderQueueIsBoundedAndCountsDrops(t *testing.T) {
	g := newGateSender()
	c := newForwardCore(forwardCoreOptions{name: "gate", kind: ForwardLoki, queue: 4, batchSize: 1}, g)
	c.notify = nil
	defer func() { close(g.release); _ = c.Close() }()
	_, _ = c.Write([]byte(sampleRecord))
	<-g.entered // the worker holds one record in flight
	for range 14 {
		_, _ = c.Write([]byte(sampleRecord))
	}
	if st := c.ForwardStat(); st.Dropped != 10 {
		t.Fatalf("dropped %d, want 10 (queue of 4 plus one in flight)", st.Dropped)
	}
	_, _ = c.Write([]byte("\n"))
	if c.ForwardStat().Dropped != 10 {
		t.Fatal("an empty line counted as a record")
	}
}

func TestForwarderWriteNeverBlocksOnHungCollector(t *testing.T) {
	hang := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { <-hang }))
	defer srv.Close()
	defer close(hang)
	f := mustForwarder(t, ForwarderOptions{Kind: ForwardLoki, QueueEntries: 64, BatchSize: 1, Timeout: 5 * time.Second, Loki: LokiOptions{URL: srv.URL}})
	start := time.Now()
	for range 20000 {
		_, _ = f.Write([]byte(sampleRecord))
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("Write blocked behind a hung collector: %v", d)
	}
	if f.ForwardStat().Dropped < 19000 {
		t.Fatalf("stat %+v", f.ForwardStat())
	}
}

func TestForwarderFlushHonoursDeadline(t *testing.T) {
	g := newGateSender()
	c := newForwardCore(forwardCoreOptions{name: "gate", kind: ForwardLoki, queue: 16, batchSize: 100, batchInterval: time.Hour}, g)
	c.notify = nil
	_, _ = c.Write([]byte(sampleRecord))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := c.Flush(ctx); !errors.Is(err, ErrForwardDeadline) {
		t.Fatalf("Flush = %v", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("Flush ignored the deadline: %v", d)
	}
	// A caller without a deadline gets the flush timeout.
	c.flushTimeout = 80 * time.Millisecond
	_, _ = c.Write([]byte(sampleRecord))
	start = time.Now()
	if err := c.Flush(context.Background()); !errors.Is(err, ErrForwardDeadline) || time.Since(start) > time.Second {
		t.Fatalf("Flush without deadline = %v after %v", err, time.Since(start))
	}
	close(g.release)
	_ = c.Close()
	if err := c.Flush(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("Flush after Close = %v", err)
	}
}

func TestForwarderCloseIsBoundedAndCountsUndelivered(t *testing.T) {
	g := newGateSender()
	c := newForwardCore(forwardCoreOptions{name: "gate", kind: ForwardLoki, queue: 16, batchSize: 100, batchInterval: time.Hour}, g)
	c.notify = nil
	c.flushTimeout = 100 * time.Millisecond
	for range 5 {
		_, _ = c.Write([]byte(sampleRecord))
	}
	start := time.Now()
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("Close waited %v for a dead collector", d)
	}
	if st := c.ForwardStat(); st.Dropped != 5 || st.Sent != 0 {
		t.Fatalf("stat %+v", st)
	}
	_, _ = c.Write([]byte(sampleRecord))
	if c.ForwardStat().Dropped != 6 {
		t.Fatal("a write after Close was not counted")
	}
	if err := c.Close(); err != nil {
		t.Fatal("second Close:", err)
	}
}

func TestForwarderCloseDeliversQueuedRecords(t *testing.T) {
	c := newLoki(t)
	f := mustForwarder(t, ForwarderOptions{Kind: ForwardLoki, BatchSize: 4, BatchInterval: MaxForwardBatchInterval, Loki: LokiOptions{URL: c.URL}})
	for range 10 {
		_, _ = f.Write([]byte(sampleRecord))
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if n := len(c.lines()); n != 10 {
		t.Fatalf("%d of 10 records delivered at Close", n)
	}
}

func TestForwarderInterruptedRetryIsDeliveredAtClose(t *testing.T) {
	c := newLoki(t, 503, 204)
	f := mustForwarder(t, ForwarderOptions{Kind: ForwardLoki, BatchInterval: 10 * time.Millisecond, Loki: LokiOptions{URL: c.URL}})
	f.backoffMin, f.backoffMax = time.Hour, time.Hour // retry sleeps until Close
	_, _ = f.Write([]byte(sampleRecord))
	eventually(t, "first failure", func() bool { return c.attempts.Load() >= 1 })
	time.Sleep(20 * time.Millisecond)
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if len(c.lines()) != 1 {
		t.Fatalf("batch lost at Close: attempts %d stat %+v", c.attempts.Load(), f.ForwardStat())
	}
}

func TestForwarderDropsOversizedRecordsAndUDPLimits(t *testing.T) {
	c := newLoki(t)
	f := mustForwarder(t, ForwarderOptions{Kind: ForwardLoki, Loki: LokiOptions{URL: c.URL}})
	_, _ = f.Write([]byte(strings.Repeat("x", maxForwardRecordBytes+1)))
	if f.ForwardStat().Dropped != 1 {
		t.Fatal("oversized record accepted")
	}
	u := mustForwarder(t, ForwarderOptions{Kind: ForwardSyslog, Syslog: SyslogOptions{Address: "127.0.0.1:9"}})
	_, _ = u.Write([]byte(strings.Repeat("x", maxSyslogDatagram+1)))
	if u.ForwardStat().Dropped != 1 {
		t.Fatal("record too big for a datagram accepted")
	}
}

func TestForwarderReportsStateChangesWithoutContent(t *testing.T) {
	c := newLoki(t, 503, 204)
	f := mustForwarder(t, ForwarderOptions{Kind: ForwardLoki, BatchInterval: 10 * time.Millisecond, Loki: LokiOptions{URL: c.URL, BearerToken: "secret-token"}})
	var mu sync.Mutex
	var notes []string
	f.notify = func(s string) { mu.Lock(); notes = append(notes, s); mu.Unlock() }
	_, _ = f.Write([]byte(sampleRecord))
	eventually(t, "recovery note", func() bool { mu.Lock(); defer mu.Unlock(); return len(notes) == 2 })
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(notes[0], "status 503") || !strings.Contains(notes[1], "recovered") {
		t.Fatalf("notes %q", notes)
	}
	for _, n := range notes {
		if strings.Contains(n, "secret-token") || strings.Contains(n, "request completed") || strings.Contains(n, c.URL) {
			t.Fatalf("note leaks: %q", n)
		}
	}
}

// --- router ---------------------------------------------------------------

func TestRouterForwardsRedactedRecordsAndReportsDestinations(t *testing.T) {
	c := newLoki(t)
	addr, conns := tcpCollector(t)
	var stdout syncBuffer
	r, err := Open(Options{Forward: []ForwarderOptions{
		{Kind: ForwardLoki, Loki: LokiOptions{URL: c.URL}},
		{Kind: ForwardLoki, Name: "loki-b", Loki: LokiOptions{URL: c.URL}},
		{Kind: ForwardLoki, Loki: LokiOptions{URL: c.URL}},
		{Kind: ForwardSyslog, Syslog: SyslogOptions{Network: "tcp", Address: addr}},
	}}, &stdout)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range r.forwarders {
		fastCore(t, f.f)
	}
	r.Logger().Info("request completed", "component", "http", "clientIp", "203.0.113.77", "path", "/srv/media/secret.mkv", "status", 200)
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	lines := c.lines()
	if len(lines) != 3 {
		t.Fatalf("%d lines at the collector, want one per Loki forwarder", len(lines))
	}
	stdoutLine := strings.TrimSpace(stdout.String())
	for _, line := range lines {
		if strings.Contains(line, "203.0.113.77") || strings.Contains(line, "secret.mkv") {
			t.Fatalf("forwarded record is not redacted: %s", line)
		}
		var a, b map[string]any
		if json.Unmarshal([]byte(line), &a) != nil || json.Unmarshal([]byte(stdoutLine), &b) != nil || a["msg"] != b["msg"] || a["status"] != b["status"] {
			t.Fatalf("forwarded record differs from stdout:\n%s\n%s", line, stdoutLine)
		}
	}
	conn := <-conns
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	data, _ := io.ReadAll(io.LimitReader(conn, 4096))
	if strings.Contains(string(data), "203.0.113.77") || !strings.Contains(string(data), "request completed") {
		t.Fatalf("syslog message %q", data)
	}
	var names []string
	for _, st := range r.ForwardStats() {
		names = append(names, st.Destination)
		if st.Sent != 1 || st.Dropped != 0 || st.Failures != 0 {
			t.Fatalf("stat %+v", st)
		}
	}
	if strings.Join(names, ",") != "loki,loki-b,loki-2,syslog" {
		t.Fatalf("destinations %v", names)
	}
}

type blockedForwarder struct {
	release chan struct{}
	entered chan struct{}
	once    sync.Once
}

func (b *blockedForwarder) Kind() ForwarderKind         { return ForwardLoki }
func (b *blockedForwarder) Flush(context.Context) error { return nil }
func (b *blockedForwarder) Close() error                { return nil }
func (b *blockedForwarder) Write(p []byte) (int, error) {
	b.once.Do(func() { close(b.entered) })
	<-b.release
	return len(p), nil
}

// A forwarder that does not count for itself still reports the records the
// router queue in front of it dropped.
func TestRouterQueueDropsCountForTheDestination(t *testing.T) {
	b := &blockedForwarder{release: make(chan struct{}), entered: make(chan struct{})}
	r, err := Open(Options{BufferEntries: 2, Forwarders: []Forwarder{b}}, &syncBuffer{})
	if err != nil {
		t.Fatal(err)
	}
	r.Logger().Info("first")
	<-b.entered
	for range 10 {
		r.Logger().Info("more")
	}
	stats := r.ForwardStats()
	close(b.release)
	_ = r.Close()
	if len(stats) != 1 || stats[0].Destination != "loki" || stats[0].Dropped < 8 {
		t.Fatalf("stats %+v", stats)
	}
}

func TestRouterOpenRefusesBadForwarderWithoutLeaks(t *testing.T) {
	_, err := Open(Options{Forward: []ForwarderOptions{
		{Kind: ForwardLoki, Loki: LokiOptions{URL: "http://127.0.0.1:1"}},
		{Kind: ForwardLoki, Loki: LokiOptions{URL: "ftp://x"}},
	}}, &syncBuffer{})
	if err == nil {
		t.Fatal("bad forwarder accepted")
	}
}

// The sensitive samples of the G46.5 scan must not reach a collector either.
func TestSensitiveSamplesDoNotReachCollectors(t *testing.T) {
	c := newLoki(t)
	addr, conns := tcpCollector(t)
	r, err := Open(Options{Level: -4, BufferEntries: 1 << 16, Forward: []ForwarderOptions{
		{Kind: ForwardLoki, QueueEntries: 1 << 16, Loki: LokiOptions{URL: c.URL}},
		{Kind: ForwardSyslog, QueueEntries: 1 << 16, Syslog: SyslogOptions{Network: "tcp", Address: addr}},
	}}, &syncBuffer{})
	if err != nil {
		t.Fatal(err)
	}
	for _, sample := range sensitiveSamples {
		inject(r.Logger(), sample.value)
	}
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	conn := <-conns
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	syslogData, _ := io.ReadAll(conn)
	outputs := map[string]string{"loki": strings.Join(c.lines(), "\n"), "syslog": string(syslogData)}
	for dest, text := range outputs {
		for _, sample := range sensitiveSamples {
			needles := append(append([]string(nil), sample.needles...), defaultOnlyNeedles...)
			assertNoLeak(t, dest, text, needles)
		}
	}
}

// --- validation ---------------------------------------------------------------

func TestForwarderOptionsValidation(t *testing.T) {
	good := LokiOptions{URL: "https://loki.example.org"}
	cases := map[string]ForwarderOptions{
		"unknown kind":          {Kind: "gelf"},
		"otlp":                  {Kind: ForwardOTLP},
		"bad name":              {Kind: ForwardLoki, Name: "Bad Name", Loki: good},
		"queue":                 {Kind: ForwardLoki, QueueEntries: -1, Loki: good},
		"queue huge":            {Kind: ForwardLoki, QueueEntries: MaxForwardQueueEntries + 1, Loki: good},
		"batch":                 {Kind: ForwardLoki, BatchSize: MaxForwardBatchSize + 1, Loki: good},
		"interval small":        {Kind: ForwardLoki, BatchInterval: time.Millisecond, Loki: good},
		"interval large":        {Kind: ForwardLoki, BatchInterval: time.Hour, Loki: good},
		"timeout":               {Kind: ForwardLoki, Timeout: time.Hour, Loki: good},
		"loki without url":      {Kind: ForwardLoki},
		"loki scheme":           {Kind: ForwardLoki, Loki: LokiOptions{URL: "ftp://loki"}},
		"loki userinfo":         {Kind: ForwardLoki, Loki: LokiOptions{URL: "https://u:p@loki"}},
		"loki query":            {Kind: ForwardLoki, Loki: LokiOptions{URL: "https://loki/?x=1"}},
		"loki fragment":         {Kind: ForwardLoki, Loki: LokiOptions{URL: "https://loki/#x"}},
		"loki port":             {Kind: ForwardLoki, Loki: LokiOptions{URL: "https://loki:99999"}},
		"half basic user":       {Kind: ForwardLoki, Loki: LokiOptions{URL: "https://loki", Username: "u"}},
		"half basic password":   {Kind: ForwardLoki, Loki: LokiOptions{URL: "https://loki", Password: "p"}},
		"basic and bearer":      {Kind: ForwardLoki, Loki: LokiOptions{URL: "https://loki", Username: "u", Password: "p", BearerToken: "t"}},
		"clear text basic":      {Kind: ForwardLoki, Loki: LokiOptions{URL: "http://loki.example.org", Username: "u", Password: "p"}},
		"clear text bearer":     {Kind: ForwardLoki, Loki: LokiOptions{URL: "http://10.0.0.5:3100", BearerToken: "t"}},
		"bearer with space":     {Kind: ForwardLoki, Loki: LokiOptions{URL: "https://loki", BearerToken: "a b"}},
		"tenant":                {Kind: ForwardLoki, Loki: LokiOptions{URL: "https://loki", TenantID: "a b"}},
		"service":               {Kind: ForwardLoki, Loki: LokiOptions{URL: "https://loki", Service: "a b\n"}},
		"loki with syslog opts": {Kind: ForwardLoki, Loki: good, Syslog: SyslogOptions{Address: "x:1"}},
		"syslog without addr":   {Kind: ForwardSyslog},
		"syslog no port":        {Kind: ForwardSyslog, Syslog: SyslogOptions{Address: "log.example"}},
		"syslog bad port":       {Kind: ForwardSyslog, Syslog: SyslogOptions{Address: "log.example:0"}},
		"syslog network":        {Kind: ForwardSyslog, Syslog: SyslogOptions{Network: "sctp", Address: "x:1"}},
		"syslog facility":       {Kind: ForwardSyslog, Syslog: SyslogOptions{Address: "x:1", Facility: "mail2"}},
		"syslog framing":        {Kind: ForwardSyslog, Syslog: SyslogOptions{Address: "x:1", Framing: "gzip"}},
		"syslog app name":       {Kind: ForwardSyslog, Syslog: SyslogOptions{Address: "x:1", AppName: "a b"}},
		"syslog hostname":       {Kind: ForwardSyslog, Syslog: SyslogOptions{Address: "x:1", Hostname: strings.Repeat("h", 256)}},
		"syslog relative sock":  {Kind: ForwardSyslog, Syslog: SyslogOptions{Network: "unix", Address: "log.sock"}},
		"syslog with loki opts": {Kind: ForwardSyslog, Syslog: SyslogOptions{Address: "x:1"}, Loki: LokiOptions{URL: "https://l"}},
	}
	for name, o := range cases {
		err := o.validate("linux")
		if err == nil {
			t.Errorf("%s accepted", name)
			continue
		}
		for _, secret := range []string{"u:p@", "a b", "Bad Name"} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("%s: error repeats the configured value: %v", name, err)
			}
		}
	}
	for name, o := range map[string]ForwarderOptions{
		"loki https auth":   {Kind: ForwardLoki, Loki: LokiOptions{URL: "https://loki.example.org", Username: "u", Password: "p"}},
		"loki loopback":     {Kind: ForwardLoki, Loki: LokiOptions{URL: "http://127.0.0.1:3100", BearerToken: "t"}},
		"loki localhost":    {Kind: ForwardLoki, Loki: LokiOptions{URL: "http://localhost:3100", Username: "u", Password: "p"}},
		"loki ipv6 lo":      {Kind: ForwardLoki, Loki: LokiOptions{URL: "http://[::1]:3100"}},
		"loki plain remote": {Kind: ForwardLoki, Loki: LokiOptions{URL: "http://loki.internal:3100"}},
		"syslog tls":        {Kind: ForwardSyslog, Syslog: SyslogOptions{Network: "tls", Address: "log.example:6514", Facility: "local7"}},
		"syslog v6":         {Kind: ForwardSyslog, Syslog: SyslogOptions{Network: "udp", Address: "[::1]:514", Facility: "daemon", Framing: "newline"}},
	} {
		if err := o.validate("linux"); err != nil {
			t.Errorf("%s rejected: %v", name, err)
		}
	}
}

func TestSyslogMessageHeaderSanitizes(t *testing.T) {
	s := newSyslogSender(SyslogOptions{Address: "x:1", Hostname: "h", Facility: "auth"}, time.Second)
	msg := string(s.message([]byte(`{"time":"bad","level":"ERROR","component":"se curity\n"}`), false))
	m := syslogHeader.FindStringSubmatch(msg)
	if m == nil || m[1] != "35" || m[6] != "security" {
		t.Fatalf("%q", msg)
	}
	if got := sanitizeSyslogField("", 10); got != "-" {
		t.Fatal(got)
	}
	if got := string(newSyslogSender(SyslogOptions{Address: "x:1"}, time.Second).message([]byte("not json"), false)); !strings.HasPrefix(got, "<134>1 ") || !strings.HasSuffix(got, "not json") {
		t.Fatalf("%q", got)
	}
}
