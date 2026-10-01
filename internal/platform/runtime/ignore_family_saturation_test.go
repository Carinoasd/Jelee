//go:build !race && (linux || windows)

package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/legacyignore"
	"github.com/MoYuanCN/Jelee/internal/platform/process"
)

func TestFamilyIgnoreServiceNativeSaturationAndRepeatedReuse(t *testing.T) {
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
	const rounds = 8
	const rejectedPerRound = 32
	for round := 0; round < rounds; round++ {
		before := helper.Stats()
		ctx, cancel := context.WithCancel(context.Background())
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
		// Register cleanup before any assertion so failure cannot retain children.
		t.Cleanup(cancel)
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
	}
	stats := helper.Stats()
	if stats.Started != 1+rounds*3 || stats.Peak != 2 || stats.Active != 0 || stats.Cancelled != rounds*2 || stats.TimedOut != 0 {
		t.Fatal("unexpected aggregate counts", stats)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 || s.Available() {
		t.Fatal("final close retained temporary tree", err)
	}
	t.Logf("native saturation: rounds=%d rejected=%d started=%d peak=%d cancelled=%d timedOut=%d active=%d", rounds, rounds*rejectedPerRound, stats.Started, stats.Peak, stats.Cancelled, stats.TimedOut, stats.Active)
}
