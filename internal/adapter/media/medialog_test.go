package media

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/platform/logging"
)

type logBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// mediaLog is the production log router at DEBUG, so the records pass the
// real whitelist.
func mediaLog(t *testing.T) (*logging.Router, *logBuffer) {
	t.Helper()
	out := &logBuffer{}
	router, err := logging.Open(logging.Options{Level: slog.LevelDebug}, out)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = router.Close() })
	return router, out
}

// mediaRecords returns the media scope records written so far.
func mediaRecords(t *testing.T, router *logging.Router, out *logBuffer) []map[string]any {
	t.Helper()
	if err := router.Flush(); err != nil {
		t.Fatal(err)
	}
	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("not JSON: %s", line)
		}
		if record["component"] == "media" {
			records = append(records, record)
		}
	}
	return records
}

const loggedSourceID = "00000000-0000-4000-8000-0000000000a1"

// privateFixture is a source below a directory whose name must never reach
// the log.
func privateFixture(t *testing.T) (Source, []byte) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "Private Collection"), 0o700); err != nil {
		t.Fatal(err)
	}
	content := bytes.Repeat([]byte("0123456789abcdef"), 4096)
	if err := os.WriteFile(filepath.Join(root, "Private Collection", "secret-movie.mkv"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	return Source{Root: root, RelativePath: "Private Collection/secret-movie.mkv", ContentType: "video/x-matroska", ETag: `"content-version-1"`}, content
}

// G46.3 media log: seeks (Range answered 206), the first requested offset,
// If-Range hit or miss, refusals with their code, and no path, root, file
// name or error text in any record.
func TestMediaLogSeekIfRangeAndRefusals(t *testing.T) {
	source, content := privateFixture(t)
	router, out := mediaLog(t)
	failure := errors.New("open " + source.Root + "/Private Collection/secret-movie.mkv: permission denied")
	resolver := resolveFunc(func(_ context.Context, _ access.Principal, id string) (Source, error) {
		switch id {
		case "00000000-0000-4000-8000-0000000000a2":
			return Source{}, ErrNotFound
		case "00000000-0000-4000-8000-0000000000a3":
			return Source{}, failure
		}
		return source, nil
	})
	handler, err := NewHandler(resolver, Options{MaxConcurrent: 2, WriteTimeout: time.Second, WriteError: testWriteError, Logger: router.Logger()})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, method, source, ranges, ifRange string
		status                                int
		want                                  map[string]any
		level                                 string
	}{
		{"complete", "GET", loggedSourceID, "", "", 200, map[string]any{"outcome": "completed", "status": float64(200), "bytes": float64(len(content))}, "DEBUG"},
		{"head", "HEAD", loggedSourceID, "", "", 200, map[string]any{"outcome": "completed", "method": "HEAD", "bytes": float64(0)}, "DEBUG"},
		{"seek", "GET", loggedSourceID, "bytes=1000-", "", 206, map[string]any{"seek": true, "offset": float64(1000), "status": float64(206), "bytes": float64(len(content) - 1000)}, "DEBUG"},
		{"if_range_hit", "GET", loggedSourceID, "bytes=4096-8191", source.ETag, 206, map[string]any{"seek": true, "offset": float64(4096), "ifRange": "hit", "bytes": float64(4096)}, "DEBUG"},
		{"if_range_miss", "GET", loggedSourceID, "bytes=10-20", `"older"`, 200, map[string]any{"seek": false, "offset": float64(10), "ifRange": "miss", "status": float64(200)}, "DEBUG"},
		{"suffix", "GET", loggedSourceID, "bytes=-7", "", 206, map[string]any{"seek": true, "offset": float64(len(content) - 7)}, "DEBUG"},
		{"unsatisfiable", "GET", loggedSourceID, "bytes=99999999-", "", 416, map[string]any{"outcome": "rejected", "code": "invalid_range"}, "DEBUG"},
		{"missing", "GET", "00000000-0000-4000-8000-0000000000a2", "", "", 404, map[string]any{"outcome": "rejected", "code": "media_not_found"}, "DEBUG"},
		{"failed", "GET", "00000000-0000-4000-8000-0000000000a3", "", "", 500, map[string]any{"outcome": "rejected", "code": "media_failed"}, "WARN"},
	}
	for _, tc := range cases {
		r := nativeRequest(tc.method, "/stream")
		if tc.ranges != "" {
			r.Header.Set("Range", tc.ranges)
		}
		if tc.ifRange != "" {
			r.Header.Set("If-Range", tc.ifRange)
		}
		w := httptest.NewRecorder()
		handler.ServeSource(w, r, tc.source)
		if w.Code != tc.status {
			t.Fatalf("%s: status %d, want %d", tc.name, w.Code, tc.status)
		}
	}
	records := mediaRecords(t, router, out)
	if len(records) != len(cases) {
		t.Fatalf("records %d, want %d: %v", len(records), len(cases), records)
	}
	for i, tc := range cases {
		record := records[i]
		if record["level"] != tc.level || record["sourceId"] != tc.source || record["durationMs"] == nil {
			t.Errorf("%s: record %v", tc.name, record)
		}
		for key, want := range tc.want {
			if record[key] != want {
				t.Errorf("%s: %s = %v, want %v", tc.name, key, record[key], want)
			}
		}
		_, seek := record["seek"]
		if seek != (tc.ranges != "" && tc.status < 400) {
			t.Errorf("%s: seek present=%v", tc.name, seek)
		}
	}
	for _, needle := range []string{source.Root, "Private Collection", "secret-movie", "permission denied", "content-version-1", "older"} {
		if strings.Contains(out.String(), needle) {
			t.Fatalf("media log leaked %q:\n%s", needle, out.String())
		}
	}
}

// Disconnects over loopback TCP: a client that goes away, one that stops
// reading past the write timeout, and a revoked session each end the
// stream with their own outcome.
func TestMediaLogDisconnectReasons(t *testing.T) {
	const size = 64 << 20 // larger than loopback socket buffers
	source, content := largeFixture(t, size)
	cases := []struct {
		name    string
		options func(*fakeSessions) Options
		end     func(*http.Response, *fakeSessions)
		outcome string
		level   string
	}{
		{"client gone", func(*fakeSessions) Options { return Options{} },
			func(response *http.Response, _ *fakeSessions) { _ = response.Body.Close() }, "client_aborted", "DEBUG"},
		{"client stalled", func(*fakeSessions) Options { return Options{WriteTimeout: 400 * time.Millisecond} },
			func(*http.Response, *fakeSessions) {}, "write_timeout", "INFO"},
		{"session revoked", func(s *fakeSessions) Options {
			return Options{Sessions: s, SessionCheckInterval: 100 * time.Millisecond}
		}, func(_ *http.Response, s *fakeSessions) { s.active.Store(false) }, "session_revoked", "INFO"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			router, out := mediaLog(t)
			sessions := &fakeSessions{}
			sessions.active.Store(true)
			options := tc.options(sessions)
			options.Logger = router.Logger()
			server := newTrackedServer(t, zeroCopyHandler(t, source, options))
			response, _ := stalledDownload(t, server, content) //nolint:bodyclose // stalledDownload closes the body in t.Cleanup
			tc.end(response, sessions)
			server.waitDone(t, 5*time.Second)
			records := mediaRecords(t, router, out)
			if len(records) != 1 {
				t.Fatalf("records %v", records)
			}
			record := records[0]
			if record["outcome"] != tc.outcome || record["level"] != tc.level || record["msg"] != "media stream interrupted" || record["status"] != float64(200) {
				t.Fatalf("record %v", record)
			}
			if sent, _ := record["bytes"].(float64); sent < 256<<10 || sent >= size {
				t.Fatalf("bytes %v", record["bytes"])
			}
			if strings.Contains(out.String(), source.Root) || strings.Contains(out.String(), "large.mkv") {
				t.Fatal("media log leaked the path")
			}
		})
	}
}

// finish orders the causes: a revocation, then a read failure of the file,
// then a write deadline the stream itself set, then any other write error
// or the request ending (a disconnect), then a refused response.
func TestStreamRecordClassification(t *testing.T) {
	timeout := errors.Join(errors.New("write tcp"), os.ErrDeadlineExceeded)
	cases := []struct {
		name      string
		revoked   bool
		cancelled bool
		readErr   error
		writeErr  error
		deadline  time.Duration
		rejected  bool
		want      string
	}{
		{"completed", false, false, nil, nil, 0, false, streamCompleted},
		{"revoked wins", true, true, errors.New("read"), timeout, -time.Second, false, streamSessionRevoked},
		{"read error", false, false, errors.New("input/output error"), nil, 0, false, streamReadError},
		{"own deadline passed", false, true, nil, timeout, -time.Millisecond, false, streamWriteTimeout},
		{"deadline cut early", false, true, nil, timeout, time.Hour, false, streamClientAborted},
		{"broken pipe", false, false, nil, errors.New("broken pipe"), 0, false, streamClientAborted},
		{"request ended", false, true, nil, nil, 0, false, streamClientAborted},
		{"refused", false, false, nil, nil, 0, true, streamRejected},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requestCtx, cancelRequest := context.WithCancel(context.Background())
			defer cancelRequest()
			streamCtx, cancelStream := context.WithCancelCause(requestCtx)
			defer cancelStream(nil)
			w := &streamWriter{request: nativeRequest("GET", "/stream"), rejected: tc.rejected}
			f := &contextFile{ctx: streamCtx}
			if tc.readErr != nil {
				f.note(tc.readErr)
			}
			w.noteError(tc.writeErr)
			if tc.deadline != 0 {
				w.noteDeadline(time.Now().Add(tc.deadline))
			}
			if tc.revoked {
				cancelStream(ErrSessionRevoked)
			}
			if tc.cancelled {
				cancelRequest()
			}
			rec := &streamRecord{}
			rec.finish(streamCtx, requestCtx, w, f, 100)
			if rec.outcome != tc.want {
				t.Fatalf("outcome %s, want %s", rec.outcome, tc.want)
			}
		})
	}
	// The end of the stream closing the file is not a read failure.
	ctx, cancel := context.WithCancel(context.Background())
	f := &contextFile{ctx: ctx}
	f.note(os.ErrClosed)
	cancel()
	f.note(errors.New("read after cut"))
	if f.failure() != nil {
		t.Fatal("cut counted as read failure")
	}
	if mediaCode(errors.New("x")) != "media_failed" || mediaCode(context.Canceled) != "media_cancelled" || mediaCode(ErrBusy) != "stream_limit_reached" {
		t.Fatal("media codes")
	}
}
