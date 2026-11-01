package testutil

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordingTB captures failures so a leak can be asserted without failing
// the test that provoked it.
type recordingTB struct {
	testing.TB
	mu       sync.Mutex
	failures []string
}

func (r *recordingTB) Helper() {}

func (r *recordingTB) Errorf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failures = append(r.failures, fmt.Sprintf(format, args...))
}

//go:noinline
func parkUntilClosed(release <-chan struct{}) { <-release }

func TestGoroutineLeakIsReportedWithItsStack(t *testing.T) {
	baseline := SnapshotGoroutines()
	release := make(chan struct{})
	defer close(release)
	started := make(chan struct{})
	go func() {
		close(started)
		parkUntilClosed(release)
	}()
	<-started
	leaked := baseline.Leaks(20 * time.Millisecond)
	if len(leaked) != 1 || !strings.Contains(leaked[0].Stack, "parkUntilClosed") {
		t.Fatalf("leaks = %+v", leaked)
	}
	found := false
	for _, function := range leaked[0].Functions {
		found = found || strings.HasSuffix(function, "testutil.parkUntilClosed")
	}
	if !found {
		t.Fatalf("functions %q lack the parked frame", leaked[0].Functions)
	}

	recorder := &recordingTB{TB: t}
	start := time.Now()
	baseline.Check(recorder)
	if len(recorder.failures) != 1 || !strings.Contains(recorder.failures[0], "1 goroutine(s) leaked") || !strings.Contains(recorder.failures[0], "parkUntilClosed") {
		t.Fatalf("failures = %q", recorder.failures)
	}
	if waited := time.Since(start); waited < GoroutineLeakGrace {
		t.Fatalf("reported after %s, before the %s grace", waited, GoroutineLeakGrace)
	}
}

func TestGoroutineIgnorePrefixAndGrace(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	baseline := SnapshotGoroutines("github.com/MoYuanCN/Jelee/internal/platform/testutil.parkUntilClosed")
	go parkUntilClosed(release)
	if leaked := baseline.Leaks(20 * time.Millisecond); len(leaked) != 0 {
		t.Fatalf("ignored goroutine reported: %+v", leaked)
	}

	// A goroutine that finishes inside the grace is not a leak.
	baseline = SnapshotGoroutines()
	go time.Sleep(30 * time.Millisecond)
	if leaked := baseline.Leaks(time.Second); len(leaked) != 0 {
		t.Fatalf("finished goroutine reported: %+v", leaked)
	}

	// Goroutines running before the snapshot are never reported.
	before := make(chan struct{})
	defer close(before)
	go parkUntilClosed(before)
	baseline = SnapshotGoroutines()
	if leaked := baseline.Leaks(0); len(leaked) != 0 {
		t.Fatalf("pre-existing goroutine reported: %+v", leaked)
	}
}

func TestTrackGoroutinesPassesAfterJoin(t *testing.T) {
	TrackGoroutines(t)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() { time.Sleep(time.Millisecond) })
	}
	wg.Wait()
	// Parallel subtests run in testing.tRunner goroutines and are ignored.
	t.Run("parallel", func(t *testing.T) {
		t.Parallel()
	})
}

func TestTrackGoroutinesReportsAtCleanup(t *testing.T) {
	recorder := &recordingTB{TB: t}
	release := make(chan struct{})
	t.Cleanup(func() {
		recorder.mu.Lock()
		failures := recorder.failures
		recorder.mu.Unlock()
		close(release)
		if len(failures) != 1 || !strings.Contains(failures[0], "parkUntilClosed") {
			t.Errorf("failures = %q", failures)
		}
	})
	// The check registered on recorder runs through t.Cleanup, before the
	// assertion above (cleanups run last-in first-out).
	TrackGoroutines(recorder)
	go parkUntilClosed(release)
}

func TestParseGoroutines(t *testing.T) {
	dump := "goroutine 7 [chan receive, 3 minutes]:\n" +
		"net/http.(*Server).Serve(0xc000123456, {0x1, 0x2})\n\t/go/src/net/http/server.go:3300 +0x1a\n" +
		"main.run[...](0x0)\n\t/src/main.go:10 +0x2\n" +
		"...additional frames elided...\n" +
		"created by main.main in goroutine 1\n\t/src/main.go:5 +0x3\n\n" +
		"not a goroutine\n\n" +
		"goroutine x [running]:\nmain.main()\n"
	got := parseGoroutines([]byte(dump))
	if len(got) != 1 || got[0].ID != 7 || strings.Join(got[0].Functions, "|") != "net/http.(*Server).Serve|main.run[...]" {
		t.Fatalf("parsed %+v", got)
	}
	if currentGoroutineID() == 0 {
		t.Fatal("current goroutine has no ID")
	}
}
