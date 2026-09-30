package process

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestProcessStatisticsDistinguishAdmissionFromCreatedChildren(t *testing.T) {
	var empty *Runner
	var isolated *IsolatedRunner
	if empty.Stats() != (Stats{}) || isolated.Stats() != (Stats{}) {
		t.Fatal("nil counters")
	}
	runner := testRunner(t, 5*time.Second, map[string][]string{"sleep": helperArgs("sleep"), "fail": helperArgs("failure")})
	if _, err := runner.Run(context.Background(), Request{Tool: "unknown", Operation: "sleep"}); err != ErrInvalid || runner.Stats() != (Stats{}) {
		t.Fatal("rejection counted as a child")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := runner.Run(ctx, Request{Tool: "helper", Operation: "sleep"}); done <- err }()
	deadline := time.Now().Add(3 * time.Second)
	for runner.Stats().Started != 1 {
		if time.Now().After(deadline) {
			t.Fatal("child did not start")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := runner.Run(context.Background(), Request{Tool: "helper", Operation: "sleep"}); err != ErrBusy {
		t.Fatal("bound not enforced")
	}
	if stats := runner.Stats(); stats.Started != 1 || stats.Peak != 1 {
		t.Fatal("admission inflated counts", stats)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, ErrCancelled) {
			t.Fatal("child cancellation", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("child did not join")
	}
	if stats := runner.Stats(); stats.Active != 0 || stats.Started != 1 || stats.Peak != 1 {
		t.Fatal("joined child remains active", stats)
	}
	if _, err := runner.Run(context.Background(), Request{Tool: "helper", Operation: "fail"}); err != ErrExit {
		t.Fatal("expected actual exit failure", err)
	}
	if stats := runner.Stats(); stats.Started != 2 || stats.Active != 0 || stats.Peak != 1 {
		t.Fatal("failed child not counted/joined", stats)
	}
	assertClean(t, runner)
}
