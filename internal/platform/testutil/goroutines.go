// Package testutil holds helpers shared by tests of several packages. It
// imports only the standard library so any layer's tests may use it.
package testutil

import (
	"bytes"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// GoroutineLeakGrace is how long a check waits for goroutines started during
// the test to finish before it reports them (G29.3). Shutdown paths join
// their goroutines, but a goroutine may still be unwinding its last deferred
// calls when Stop returns.
const GoroutineLeakGrace = 2 * time.Second

// defaultGoroutineIgnores name goroutines that are not owned by the code
// under test: other tests of the binary, the test framework itself and
// runtime helpers started lazily by the standard library. A goroutine is
// ignored when any of its frames starts with one of these prefixes.
var defaultGoroutineIgnores = []string{
	"testing.tRunner",
	"testing.(*T).Run",
	"testing.(*M).",
	"testing.runTests",
	"testing.runFuzzing",
	"runtime.runfinq",
	"runtime.runCleanups",
	"os/signal.signal_recv",
	"os/signal.loop",
}

// Goroutine is one goroutine of a stack dump.
type Goroutine struct {
	ID uint64
	// Functions are the frames from the innermost call outwards, without
	// arguments; the "created by" line is not included.
	Functions []string
	// Stack is the goroutine's full dump, for failure messages.
	Stack string
}

// GoroutineBaseline is the set of goroutines alive when it was taken.
type GoroutineBaseline struct {
	known  map[uint64]bool
	ignore []string
}

// SnapshotGoroutines records the goroutines running now. Goroutines started
// later and whose frames match none of the default ignores and none of the
// ignore prefixes count as leaks if they are still running at a check.
// Pass a prefix only for a goroutine that is meant to outlive the test, such
// as a lazily started process-wide worker of a dependency; never to hide a
// goroutine the code under test should have joined.
func SnapshotGoroutines(ignore ...string) *GoroutineBaseline {
	b := &GoroutineBaseline{known: map[uint64]bool{}, ignore: slices.Concat(defaultGoroutineIgnores, ignore)}
	for _, g := range Goroutines() {
		b.known[g.ID] = true
	}
	return b
}

// Leaks returns the goroutines started since the snapshot that are still
// running after waiting up to grace for them to finish. The calling
// goroutine is never reported.
func (b *GoroutineBaseline) Leaks(grace time.Duration) []Goroutine {
	self := currentGoroutineID()
	deadline := time.Now().Add(grace)
	wait := time.Millisecond
	for {
		var leaked []Goroutine
		for _, g := range Goroutines() {
			if g.ID != self && !b.known[g.ID] && !b.ignored(g) {
				leaked = append(leaked, g)
			}
		}
		if len(leaked) == 0 || !time.Now().Before(deadline) {
			return leaked
		}
		time.Sleep(min(wait, time.Until(deadline)))
		wait = min(2*wait, 50*time.Millisecond)
	}
}

// Check fails tb with the stack of every goroutine still running
// GoroutineLeakGrace after the call that was not running at the snapshot.
func (b *GoroutineBaseline) Check(tb testing.TB) {
	tb.Helper()
	leaked := b.Leaks(GoroutineLeakGrace)
	if len(leaked) == 0 {
		return
	}
	var dump strings.Builder
	for _, g := range leaked {
		dump.WriteString("\n\n")
		dump.WriteString(g.Stack)
	}
	tb.Errorf("%d goroutine(s) leaked; still running %s after the test:%s", len(leaked), GoroutineLeakGrace, dump.String())
}

func (b *GoroutineBaseline) ignored(g Goroutine) bool {
	for _, function := range g.Functions {
		for _, prefix := range b.ignore {
			if strings.HasPrefix(function, prefix) {
				return true
			}
		}
	}
	return false
}

// TrackGoroutines snapshots the goroutines now and checks for leaks when tb
// and its cleanups finish. Call it first in the test, before anything that
// registers its own cleanup, so the shutdown cleanups run before the check.
func TrackGoroutines(tb testing.TB, ignore ...string) {
	tb.Helper()
	baseline := SnapshotGoroutines(ignore...)
	tb.Cleanup(func() { baseline.Check(tb) })
}

// Goroutines parses a dump of every goroutine of the process.
func Goroutines() []Goroutine {
	return parseGoroutines(stackDump(true))
}

func stackDump(all bool) []byte {
	buffer := make([]byte, 64<<10)
	for {
		n := runtime.Stack(buffer, all)
		if n < len(buffer) {
			return buffer[:n]
		}
		buffer = make([]byte, 2*len(buffer))
	}
}

func currentGoroutineID() uint64 {
	if goroutines := parseGoroutines(stackDump(false)); len(goroutines) == 1 {
		return goroutines[0].ID
	}
	return 0
}

// parseGoroutines reads the runtime.Stack format: blocks separated by a blank
// line, each starting with "goroutine N [state]:" followed by a function line
// and an indented file line per frame.
func parseGoroutines(dump []byte) []Goroutine {
	var goroutines []Goroutine
	for block := range bytes.SplitSeq(bytes.TrimSpace(dump), []byte("\n\n")) {
		lines := strings.Split(string(block), "\n")
		header, ok := strings.CutPrefix(lines[0], "goroutine ")
		if !ok {
			continue
		}
		idText, _, _ := strings.Cut(header, " ")
		id, err := strconv.ParseUint(idText, 10, 64)
		if err != nil {
			continue
		}
		g := Goroutine{ID: id, Stack: string(block)}
		for _, line := range lines[1:] {
			if line == "" || strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "created by ") || strings.HasPrefix(line, "...") {
				continue
			}
			if open := strings.LastIndexByte(line, '('); open > 0 && strings.HasSuffix(line, ")") {
				line = line[:open]
			}
			g.Functions = append(g.Functions, line)
		}
		goroutines = append(goroutines, g)
	}
	return goroutines
}
