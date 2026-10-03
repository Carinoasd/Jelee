//go:build jelee_probe_tests

package runtime

import (
	"context"
	"testing"
	"time"
)

func TestImagesSoakRoundScopeAndMissingHooksFailBeforeIO(t *testing.T) {
	for _, scope := range []string{"", "accelerated", "formal", "smoke"} {
		base := imagesMemoryAcceptanceReport{FixtureItems: 1000}
		var report imagesSoakWorkReport
		code := runImagesSoakRounds(context.Background(), scope, 1, imagesSoakRoundHooks{}, imagesAcceptanceSession{}, &base, &report)
		if code == "" || len(report.Rounds) != 0 {
			t.Fatal("invalid configuration reached workload")
		}
	}
}

func TestImagesSoakWaitDoesNotIgnoreCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if waitImagesSoakSlot(ctx, time.Now().Add(time.Hour)) == nil {
		t.Fatal("cancelled wait continued")
	}
}

func TestImagesSoakMedianUsesAllTwelveCheckpoints(t *testing.T) {
	values := [12]uint64{100, 1, 99, 2, 98, 3, 97, 4, 96, 5, 95, 6}
	if got := imagesSoakMedianTwice(values); got != 101 {
		t.Fatalf("median twice=%d", got)
	}
	if values[0] != 100 {
		t.Fatal("median reordered caller checkpoints")
	}
}
