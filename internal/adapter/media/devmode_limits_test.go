package media

import (
	"errors"
	"sync/atomic"
	"testing"
)

// G45.4: the developer toggles suspend the concurrency and bandwidth limits
// one by one for new admissions, and the limits return with the next
// admission after the toggles end.
func TestLimiterDeveloperRelaxations(t *testing.T) {
	var streams, bandwidth atomic.Bool
	l := newLimiter(Limits{StreamLimit: true, MaxStreamsPerUser: 1, MaxStreamsPerDevice: 1, BandwidthLimit: true, MaxKbpsPerUser: 8,
		RelaxStreams: streams.Load, RelaxBandwidth: bandwidth.Load}, newFakeClock())
	p := streamPrincipal("u", "s")
	held := admitOK(t, l, p, "a", Source{})
	if _, err := l.admit(p, "b", Source{}); !errors.Is(err, ErrUserStreamLimit) {
		t.Fatalf("limit before relaxation: %v", err)
	}
	streams.Store(true)
	relaxed := admitOK(t, l, p, "b", Source{})
	if relaxed.counted || relaxed.throttle() == nil {
		t.Fatal("stream relaxation must not count, and must keep the bandwidth limit")
	}
	bandwidth.Store(true)
	if a := admitOK(t, l, p, "c", Source{}); a != nil {
		t.Fatal("both relaxations still admitted with a limit")
	}
	streams.Store(false)
	bandwidth.Store(false)
	if _, err := l.admit(p, "d", Source{}); !errors.Is(err, ErrUserStreamLimit) {
		t.Fatalf("limit after relaxation: %v", err)
	}
	relaxed.release()
	held.release()
	if len(l.users) != 0 || len(l.buckets) != 0 {
		t.Fatalf("limiter leaked state: users=%d buckets=%d", len(l.users), len(l.buckets))
	}
}
