package process

import (
	"context"
	"errors"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/legacyignore"
	"github.com/MoYuanCN/Jelee/internal/platform/legacyignorehelper"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == legacyignorehelper.Command {
		os.Exit(legacyignorehelper.Main())
	}
	os.Exit(m.Run())
}
func TestIgnoreRunnerLifecycle(t *testing.T) {
	if ignoreRaceEnabled {
		t.Skip("production helper address-space cap is incompatible with race runtime reservation; run without -race")
	}
	if runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		t.Skip("native process runner unsupported")
	}
	root := t.TempDir()
	runner, err := NewIgnoreRunner(root, 1, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	batch := legacyignore.Batch{Source: "*.mkv\n!a.mkv", Paths: []string{"/a.mkv", "/b.mkv"}}
	result, err := runner.Evaluate(context.Background(), batch)
	if err != nil || len(result.Decisions) != 2 || result.Decisions[0].Kind != legacyignore.RuleInclude || result.Decisions[1].Kind != legacyignore.RuleExclude {
		t.Fatal("real helper failed", err)
	}
	// Keep the child in a bounded but expensive compile, then cancel after OS
	// creation has been observed. No artificial sleep implementation is used.
	heavy := legacyignore.Batch{Source: strings.Repeat("(a|aa){1,100}b\n", 4000), Paths: []string{"/" + strings.Repeat("a", 40)}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		result, err := runner.Evaluate(ctx, heavy)
		if len(result.Decisions) != 0 {
			done <- errors.New("partial results")
			return
		}
		done <- err
	}()
	deadline := time.Now().Add(3 * time.Second)
	for runner.Stats().Started < 2 {
		if time.Now().After(deadline) {
			t.Fatal("child did not start")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := runner.Evaluate(context.Background(), batch); err != ErrBusy {
		t.Fatal("concurrency bound", err)
	}
	cancel()
	select {
	case err := <-done:
		if err != ErrCancelled {
			t.Fatal("cancel", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancel did not join")
	}
	if runner.Stats().Active != 0 || runner.Stats().Cancelled != 1 || runner.Stats().TimedOut != 0 {
		t.Fatal("child remains active")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("temporary input leaked", err)
	}
	if _, err := runner.Evaluate(context.Background(), batch); err != nil {
		t.Fatal("slot not reusable", err)
	}
	short, err := NewIgnoreRunner(root, 1, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	result, err = short.Evaluate(context.Background(), heavy)
	if !errors.Is(err, ErrTimeout) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("timeout", err)
	}
	if len(result.Decisions) != 0 || short.Stats().Active != 0 || short.Stats().Started != 1 || short.Stats().TimedOut != 1 || short.Stats().Cancelled != 0 {
		t.Fatal("timeout leaked results or process")
	}
	entries, err = os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("timeout left files", err)
	}
}
func TestIgnoreRunnerRejectsConfiguration(t *testing.T) {
	for _, count := range []int{0, 3} {
		if _, err := NewIgnoreRunner(t.TempDir(), count, time.Second); err != ErrInvalid {
			t.Fatal("unbounded concurrency")
		}
	}
	if _, err := NewIgnoreRunner(t.TempDir(), 1, 11*time.Second); err != ErrInvalid {
		t.Fatal("unbounded timeout")
	}
	var empty *IgnoreRunner
	if empty.Stats() != (Stats{}) {
		t.Fatal("nil stats")
	}
	if _, err := empty.Evaluate(context.Background(), legacyignore.Batch{}); err != ErrInvalid {
		t.Fatal("nil runner")
	}
}
