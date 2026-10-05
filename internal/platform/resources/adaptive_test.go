package resources

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/platform/logging"
)

// fakeSource returns scripted readings; err makes the next reads fail.
type fakeSource struct {
	mu      sync.Mutex
	reading Reading
	err     error
	reads   int
}

func (f *fakeSource) set(r Reading, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reading, f.err = r, err
}

func (f *fakeSource) Read(ctx context.Context) (Reading, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if err := ctx.Err(); err != nil {
		return Reading{}, err
	}
	return f.reading, f.err
}

func load(perCPU float64) Reading { return Reading{Load1: perCPU * 4, CPUs: 4, HasLoad: true} }

type adaptiveHarness struct {
	t      *testing.T
	budget *Budget
	source *fakeSource
	ctl    *Adaptive
	now    time.Time
	logs   *bytes.Buffer
}

func newHarness(t *testing.T) *adaptiveHarness {
	t.Helper()
	b, err := New(Limits{CPU: 8, IO: 16, Total: 32, Queue: 8})
	if err != nil {
		t.Fatal(err)
	}
	h := &adaptiveHarness{t: t, budget: b, source: &fakeSource{}, now: time.Unix(1_000_000, 0), logs: &bytes.Buffer{}}
	h.ctl, err = NewAdaptive(b, AdaptiveOptions{Source: h.source, Now: func() time.Time { return h.now }, Logger: logging.New(h.logs)})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// step advances the clock by d and takes one reading of r.
func (h *adaptiveHarness) step(d time.Duration, r Reading) int {
	h.t.Helper()
	h.now = h.now.Add(d)
	h.source.set(r, nil)
	h.ctl.Step(context.Background())
	return h.ctl.Status().Percent
}

func (h *adaptiveHarness) effective() Limits {
	l, _ := h.budget.EffectiveLimits()
	return l
}

// TestAdaptiveLowersUnderLoadWithDwell: rising load lowers the effective
// limits one step at a time, no faster than the dwell, down to the floor,
// and never above the configured limits.
func TestAdaptiveLowersUnderLoadWithDwell(t *testing.T) {
	h := newHarness(t)
	if got := h.step(10*time.Second, load(0.5)); got != 100 || h.effective() != h.budget.Limits() {
		t.Fatal("calm system changed the limits")
	}
	if got := h.step(10*time.Second, load(2)); got != 75 {
		t.Fatalf("first overload: %d%%", got)
	}
	if l, source := h.budget.EffectiveLimits(); l != (Limits{CPU: 6, IO: 12, Total: 24, Queue: 8}) || source != PressureLoad {
		t.Fatalf("lowered limits: %+v %s", l, source)
	}
	// Still overloaded but inside the dwell: hold.
	if got := h.step(10*time.Second, load(3)); got != 75 {
		t.Fatalf("changed inside the dwell: %d%%", got)
	}
	if got := h.step(20*time.Second, load(3)); got != 50 {
		t.Fatalf("second step after the dwell: %d%%", got)
	}
	for range 10 {
		h.step(30*time.Second, load(4))
	}
	if st := h.ctl.Status(); st.Percent != 25 || st.Source != PressureLoad || h.effective() != (Limits{CPU: 2, IO: 4, Total: 8, Queue: 8}) {
		t.Fatalf("floor: %+v %+v", st, h.effective())
	}
	out := h.logs.String()
	if !strings.Contains(out, `"msg":"adaptive concurrency lowered"`) || !strings.Contains(out, `"source":"load"`) || !strings.Contains(out, `"cpuLimit":2`) || !strings.Contains(out, `"level":"INFO"`) {
		t.Fatalf("lowering not logged: %s", out)
	}
	if strings.Count(out, "adaptive concurrency lowered") != 3 {
		t.Fatalf("lowering logged per reading instead of per change: %s", out)
	}
}

// TestAdaptiveHysteresisDoesNotFlap: a signal oscillating inside the band
// between the low and high thresholds never changes the limits, and one
// alternating across the whole band only ratchets down (never up) because
// recovery needs a full calm cooldown.
func TestAdaptiveHysteresisDoesNotFlap(t *testing.T) {
	h := newHarness(t)
	for i := range 60 {
		value := 1.0
		if i%2 == 0 {
			value = 1.45
		}
		if got := h.step(10*time.Second, load(value)); got != 100 {
			t.Fatalf("in-band oscillation changed the limits at %d: %d%%", i, got)
		}
	}
	h.step(10*time.Second, load(2))
	changes, last := 0, h.ctl.Status().Percent
	for i := range 60 {
		value := 0.5
		if i%2 == 0 {
			value = 1.6
		}
		got := h.step(10*time.Second, load(value))
		if got > last {
			t.Fatalf("recovered during oscillation at %d", i)
		}
		if got != last {
			changes++
		}
		last = got
	}
	if changes > 2 {
		t.Fatalf("limits changed %d times while oscillating", changes)
	}
}

// TestAdaptiveRecoveryCooldown: restoring needs every signal calm for the
// whole cooldown before each step, and ends at the configured limits.
func TestAdaptiveRecoveryCooldown(t *testing.T) {
	h := newHarness(t)
	h.step(10*time.Second, load(2))
	h.step(30*time.Second, load(2))
	if h.ctl.Status().Percent != 50 {
		t.Fatal("setup")
	}
	for elapsed := time.Duration(0); elapsed < 110*time.Second; elapsed += 10 * time.Second {
		if got := h.step(10*time.Second, load(0.5)); got != 50 {
			t.Fatalf("recovered before the cooldown: %d%%", got)
		}
	}
	// A reading inside the band restarts the calm period.
	h.step(10*time.Second, load(1.2))
	for range 12 {
		if got := h.step(10*time.Second, load(0.5)); got != 50 {
			t.Fatalf("band reading did not restart the cooldown: %d%%", got)
		}
	}
	if got := h.step(10*time.Second, load(0.5)); got != 75 {
		t.Fatalf("first recovery step: %d%%", got)
	}
	if got := h.step(10*time.Second, load(0.5)); got != 75 {
		t.Fatal("second recovery step without a new cooldown")
	}
	h.step(110*time.Second, load(0.5))
	if l, source := h.budget.EffectiveLimits(); h.ctl.Status().Percent != 100 || l != h.budget.Limits() || source != "" {
		t.Fatalf("not restored: %+v %q", l, source)
	}
	out := h.logs.String()
	if !strings.Contains(out, "adaptive concurrency recovering") || !strings.Contains(out, "adaptive concurrency restored") {
		t.Fatalf("recovery not logged: %s", out)
	}
	// Restored: further calm readings change nothing.
	if got := h.step(10*time.Minute, load(0.1)); got != 100 {
		t.Fatal("rose above the configured limits")
	}
}

// TestAdaptiveReadFailuresKeepLimits: failed, cancelled or empty readings
// never change the limits and are counted; the warning is rate limited.
func TestAdaptiveReadFailuresKeepLimits(t *testing.T) {
	h := newHarness(t)
	h.step(10*time.Second, load(2))
	before := h.effective()
	h.source.set(Reading{}, errors.New("read failed"))
	for range 5 {
		h.now = h.now.Add(10 * time.Minute)
		h.ctl.Step(context.Background())
	}
	h.step(time.Minute, Reading{}) // no signal at all
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	h.source.set(load(4), nil)
	h.ctl.Step(cancelled)
	if st := h.ctl.Status(); st.ReadFailures != 7 || st.Percent != 75 || h.effective() != before {
		t.Fatalf("failure changed the limits: %+v", st)
	}
	if n := strings.Count(h.logs.String(), "system pressure reading failed"); n != 5 {
		t.Fatalf("failure warnings: %d", n)
	}
	// A failure also restarts the calm period: no recovery right after it.
	h.step(10*time.Second, load(0.5))
	h.source.set(Reading{}, errors.New("read failed"))
	h.now = h.now.Add(119 * time.Second)
	h.ctl.Step(context.Background())
	if got := h.step(10*time.Second, load(0.5)); got != 75 {
		t.Fatal("recovered across a failed reading")
	}
}

// TestAdaptiveSignalPriority: memory and throttling signals lower the
// limits on their own and are reported as the pressure source.
func TestAdaptiveSignalPriority(t *testing.T) {
	for _, c := range []struct {
		reading Reading
		source  string
	}{
		{Reading{MemoryPressure: 25, HasMemoryPressure: true}, PressureMemoryPressure},
		{Reading{MemoryUsage: 0.95, HasMemoryUsage: true}, PressureMemoryUsage},
		{Reading{Throttled: 0.4, HasThrottle: true}, PressureCPUThrottle},
		{Reading{MemoryPressure: 30, HasMemoryPressure: true, Load1: 40, CPUs: 4, HasLoad: true}, PressureMemoryPressure},
	} {
		h := newHarness(t)
		h.step(10*time.Second, c.reading)
		if _, source := h.budget.EffectiveLimits(); source != c.source || h.ctl.Status().Source != c.source {
			t.Fatalf("%+v: source %q", c.reading, source)
		}
	}
	// Load without a CPU capacity is ignored rather than divided by zero.
	h := newHarness(t)
	if got := h.step(10*time.Second, Reading{Load1: 100, HasLoad: true}); got != 100 || h.ctl.Status().ReadFailures != 1 {
		t.Fatal("load without capacity used")
	}
}

func TestAdaptiveRejectsInvalidOptions(t *testing.T) {
	b, _ := New(Limits{CPU: 1, IO: 1, Total: 1, Queue: 0})
	src := &fakeSource{}
	for name, o := range map[string]AdaptiveOptions{
		"source":    {},
		"interval":  {Source: src, Interval: time.Millisecond},
		"dwell":     {Source: src, Dwell: -time.Second},
		"cooldown":  {Source: src, Cooldown: 48 * time.Hour},
		"min":       {Source: src, MinPercent: 101},
		"step":      {Source: src, StepPercent: -1},
		"load":      {Source: src, Load: Thresholds{High: 1, Low: 2}},
		"throttle":  {Source: src, Throttle: Thresholds{High: 2, Low: 0.1}},
		"memory":    {Source: src, MemoryPressure: Thresholds{High: 10, Low: 0}},
		"memoryUse": {Source: src, MemoryUsage: Thresholds{High: 1.1, Low: 0.5}},
	} {
		if _, err := NewAdaptive(b, o); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := NewAdaptive(nil, AdaptiveOptions{Source: src}); err == nil {
		t.Error("nil budget accepted")
	}
}

// TestAdaptiveRun drives the real loop: an unsupported platform returns at
// once with the configured limits; a supported one applies readings until
// its context ends.
func TestAdaptiveRun(t *testing.T) {
	b, _ := New(Limits{CPU: 4, IO: 4, Total: 4, Queue: 0})
	var logs bytes.Buffer
	unsupported, err := NewAdaptive(b, AdaptiveOptions{Source: unsupportedSource{}, Logger: logging.New(&logs)})
	if err != nil {
		t.Fatal(err)
	}
	unsupported.Run(context.Background())
	if !strings.Contains(logs.String(), "adaptive concurrency unavailable") {
		t.Fatal("unsupported platform not reported")
	}
	src := &fakeSource{reading: load(3)}
	ctl, err := NewAdaptive(b, AdaptiveOptions{Source: src, Interval: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); ctl.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for ctl.Status().Percent == 100 {
		if time.Now().After(deadline) {
			t.Fatal("loop never applied a reading")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if l, _ := b.EffectiveLimits(); l.CPU != 3 {
		t.Fatalf("loop limits: %+v", l)
	}
}

// TestEffectiveLimitsGateAdmission: a lowered ceiling makes new work wait
// without revoking admitted work, and raising it dispatches the waiters.
func TestEffectiveLimitsGateAdmission(t *testing.T) {
	b, _ := New(Limits{CPU: 2, IO: 2, Total: 4, Queue: 4})
	first := take(t, b, app.WorkCPU)
	second := take(t, b, app.WorkCPU)
	if got := b.SetEffective(Limits{CPU: 1, IO: 0, Total: 99}, PressureLoad); got != (Limits{CPU: 1, IO: 1, Total: 4, Queue: 4}) {
		t.Fatalf("clamping: %+v", got)
	}
	if s := b.Stats(); s.CPU != 2 {
		t.Fatal("admitted work revoked")
	}
	second()
	done := make(chan func(), 1)
	go func() { done <- take(t, b, app.WorkCPU) }()
	queued(t, b, 1)
	b.SetEffective(Limits{CPU: 2, IO: 2, Total: 4}, "")
	select {
	case release := <-done:
		release()
	case <-time.After(3 * time.Second):
		t.Fatal("raising the ceiling did not dispatch the waiter")
	}
	first()
	if l, source := b.EffectiveLimits(); l != b.Limits() || source != "" {
		t.Fatalf("restored: %+v %q", l, source)
	}
}

func TestFSSourceCgroupV2(t *testing.T) {
	root := t.TempDir()
	if err := os.CopyFS(root, os.DirFS(filepath.Join("testdata", "cgroup2"))); err != nil {
		t.Fatal(err)
	}
	src := NewFSSource(root, 8)
	r, err := src.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// cpu.max 200000/100000 caps 8 CPUs at 2; the cgroup PSI wins over the
	// host file; usage subtracts inactive_file.
	wantUsage := float64(943718400-100000000) / 1073741824
	if !r.HasLoad || r.Load1 != 3 || r.CPUs != 2 || r.HasThrottle || !r.HasMemoryPressure || r.MemoryPressure != 12.5 || !r.HasMemoryUsage || r.MemoryUsage != wantUsage {
		t.Fatalf("first reading: %+v", r)
	}
	stat := filepath.Join(root, "sys/fs/cgroup/system.slice/jelee.service/cpu.stat")
	if err := os.WriteFile(stat, []byte("nr_periods 300\nnr_throttled 110\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r, err = src.Read(context.Background()); err != nil || !r.HasThrottle || r.Throttled != 0.5 {
		t.Fatalf("throttled delta: %+v %v", r, err)
	}
	// A counter reset (container restart) yields no throttle signal.
	if err := os.WriteFile(stat, []byte("nr_periods 5\nnr_throttled 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r, _ = src.Read(context.Background()); r.HasThrottle {
		t.Fatal("throttle across a counter reset")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := src.Read(cancelled); err == nil {
		t.Fatal("cancelled read succeeded")
	}
}

func TestFSSourceHostFallbackAndFailures(t *testing.T) {
	r, err := NewFSSource(filepath.Join("testdata", "host"), 4).Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !r.HasLoad || r.Load1 != 0.4 || r.CPUs != 4 || !r.HasMemoryPressure || r.MemoryPressure != 1.25 || r.HasMemoryUsage || r.HasThrottle {
		t.Fatalf("host reading: %+v", r)
	}
	if _, err := NewFSSource(t.TempDir(), 0).Read(context.Background()); err == nil {
		t.Fatal("empty tree produced a reading")
	}
	big := t.TempDir()
	if err := os.MkdirAll(filepath.Join(big, "proc"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(big, "proc", "loadavg"), bytes.Repeat([]byte("9"), pressureFileLimit+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFSSource(big, 1).Read(context.Background()); err == nil {
		t.Fatal("oversized file read")
	}
	if _, err := (unsupportedSource{}).Read(context.Background()); !errors.Is(err, ErrPressureUnsupported) {
		t.Fatal("unsupported source")
	}
	_, fs := NewSystemSource().(*FSSource)
	if fs != (goruntime.GOOS == "linux") {
		t.Fatal("system source does not match the platform")
	}
}

func TestPressureParsers(t *testing.T) {
	if parseCgroupPath([]byte("0::/../../etc\n")) != "" || parseCgroupPath([]byte("1:cpu:/x\n")) != "" || parseCgroupPath([]byte("0::/\n")) != "sys/fs/cgroup" {
		t.Fatal("cgroup path")
	}
	for _, bad := range []string{"max 100000", "x 100000", "100000", "0 100000", "100 0"} {
		if _, ok := parseCPUMax([]byte(bad)); ok {
			t.Errorf("cpu.max %q accepted", bad)
		}
	}
	if _, _, ok := parseCPUStat([]byte("nr_periods 1\nnr_throttled x\n")); ok {
		t.Fatal("malformed cpu.stat accepted")
	}
	for _, bad := range []string{"full avg10=1.00", "some avg10=x", "some avg10=101", "some avg60=1"} {
		if _, ok := parsePSISome([]byte(bad)); ok {
			t.Errorf("PSI %q accepted", bad)
		}
	}
	for _, c := range [][3]string{{"1", "max", ""}, {"1", "0", ""}, {"x", "10", ""}, {"1", "y", ""}} {
		if _, ok := memoryUsage([]byte(c[0]), []byte(c[1]), []byte(c[2])); ok {
			t.Errorf("memory usage %v accepted", c)
		}
	}
	if v, ok := memoryUsage([]byte("50"), []byte("100"), []byte("inactive_file 80\n")); !ok || v != 0.5 {
		t.Fatal("inactive_file larger than usage must be ignored")
	}
}
