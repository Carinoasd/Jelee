//go:build !race && (linux || windows)

package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/legacyignore"
	"github.com/MoYuanCN/Jelee/internal/platform/process"
)

func TestFamilyIgnoreServiceNativeSaturationAndRepeatedReuse(t *testing.T) {
	runFamilyIgnoreNativeSaturation(t, 0)
}

// The dedicated target runs this fixed five-minute contract. Routine tests do
// not silently turn it into a shorter acceptance run.
func TestIgnoreServiceNativeSustainedSaturation(t *testing.T) {
	if os.Getenv("JELEE_IGNORE_SUSTAINED_ACCEPTANCE") != "true" {
		t.Skip("run make ignore-sustained-test for the fixed five-minute acceptance")
	}
	runFamilyIgnoreNativeSaturation(t, 5*time.Minute)
}

func runFamilyIgnoreNativeSaturation(t *testing.T, duration time.Duration) {
	root := t.TempDir()
	t.Setenv("TMPDIR", root)
	t.Setenv("TMP", root)
	t.Setenv("TEMP", root)
	s, err := newFamilyIgnoreService(context.Background(), true, prepareProductionFamilyIgnore)
	if err != nil || !s.Available() {
		t.Fatal("native readiness", err)
	}
	defer s.Close()
	helper, ok := s.backend.(*process.IgnoreRunner)
	if !ok {
		t.Fatal("native helper missing")
	}
	heavy := legacyignore.Batch{Source: strings.Repeat(strings.Repeat("(a|aa){1,100}", 4)+"b\n", 4000), Paths: []string{"/" + strings.Repeat("a", 40)}}
	normal := legacyignore.Batch{Source: "*.mkv\n!keep.mkv", Paths: []string{"/keep.mkv", "/drop.mkv"}}
	const rejectedPerRound = 32
	goruntime.GC()
	var baselineMemory goruntime.MemStats
	goruntime.ReadMemStats(&baselineMemory)
	baselineGoroutines := goruntime.NumGoroutine()
	initial := helper.Stats()
	started := time.Now()
	deadline := started.Add(duration)
	rounds := 0
	peakHeap := baselineMemory.HeapAlloc
	for rounds < 8 || duration > 0 && time.Now().Before(deadline) {
		round := rounds
		func() {
			before := helper.Stats()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 2)
			for i := 0; i < 2; i++ {
				go func() {
					result, err := s.Evaluate(ctx, heavy)
					if len(result.Decisions) != 0 {
						err = errors.New("active cancellation returned partial results")
					}
					done <- err
				}()
			}
			deadline := time.Now().Add(3 * time.Second)
			for helper.Stats().Active != 2 || helper.Stats().Started != before.Started+2 {
				if time.Now().After(deadline) {
					cancel()
					t.Fatal("two native children not observed", round, helper.Stats())
				}
				time.Sleep(time.Millisecond)
			}
			for i := 0; i < rejectedPerRound; i++ {
				result, err := s.Evaluate(context.Background(), normal)
				if !errors.Is(err, process.ErrBusy) || len(result.Decisions) != 0 || !s.Available() {
					cancel()
					t.Fatal("saturation did not reject without disabling service", round, err)
				}
			}
			saturated := helper.Stats()
			if saturated.Started != before.Started+2 || saturated.Active != 2 || saturated.Peak != 2 {
				cancel()
				t.Fatal("rejected calls started extra children", saturated)
			}
			cancel()
			for i := 0; i < 2; i++ {
				select {
				case err := <-done:
					if !errors.Is(err, process.ErrCancelled) && !errors.Is(err, context.Canceled) {
						t.Fatal("cancellation result", round, err)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("cancel did not join native children", round)
				}
			}
			after := helper.Stats()
			if after.Active != 0 || after.Cancelled != before.Cancelled+2 || after.TimedOut != 0 || !s.Available() {
				t.Fatal("cancel did not preserve bounded healthy service", round, after)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 1 {
				t.Fatal("service temporary directory missing", err)
			}
			inputs, err := os.ReadDir(filepath.Join(root, entries[0].Name()))
			if err != nil || len(inputs) != 0 {
				t.Fatal("joined calls retained temporary inputs", round, err)
			}
			result, err := s.Evaluate(context.Background(), normal)
			if err != nil || len(result.Decisions) != 2 || result.Decisions[0] != (legacyignore.Decision{Kind: legacyignore.RuleInclude, Line: 2}) || result.Decisions[1] != (legacyignore.Decision{Kind: legacyignore.RuleExclude, Line: 1}) {
				t.Fatal("reused native service changed rule results", round, err)
			}
		}()
		rounds++
		if rounds%64 == 0 {
			var sample goruntime.MemStats
			goruntime.ReadMemStats(&sample)
			if sample.HeapAlloc > peakHeap {
				peakHeap = sample.HeapAlloc
			}
			if peakHeap > 64<<20 {
				t.Fatal("parent Go heap exceeded sustained 64MiB sample bound", peakHeap)
			}
		}
	}
	elapsed := time.Since(started)
	if duration > 0 && (elapsed < duration || rounds < 100) {
		t.Fatal("sustained acceptance did not cover five minutes and 100 rounds", rounds, elapsed)
	}
	goruntime.GC()
	var finalMemory goruntime.MemStats
	goruntime.ReadMemStats(&finalMemory)
	finalGoroutines := goruntime.NumGoroutine()
	if duration > 0 && (finalMemory.HeapAlloc > baselineMemory.HeapAlloc+16<<20 || finalGoroutines > baselineGoroutines+4) {
		t.Fatal("sustained parent resources accumulated", baselineMemory.HeapAlloc, finalMemory.HeapAlloc, baselineGoroutines, finalGoroutines)
	}
	stats := helper.Stats()
	if stats.Started != initial.Started+uint64(rounds*3) || stats.Peak != 2 || stats.Active != 0 || stats.Cancelled != initial.Cancelled+uint64(rounds*2) || stats.TimedOut != 0 {
		t.Fatal("unexpected aggregate counts", stats)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 || s.Available() {
		t.Fatal("final close retained temporary tree", err)
	}
	t.Logf("native saturation: seconds=%.3f rounds=%d rejected=%d started=%d peak=%d cancelled=%d timedOut=%d active=%d baselineHeap=%d finalHeap=%d peakSampleHeap=%d baselineGoroutines=%d finalGoroutines=%d", elapsed.Seconds(), rounds, rounds*rejectedPerRound, stats.Started, stats.Peak, stats.Cancelled, stats.TimedOut, stats.Active, baselineMemory.HeapAlloc, finalMemory.HeapAlloc, peakHeap, baselineGoroutines, finalGoroutines)
}
