package resources

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// Pressure sources reported in logs, metrics and EffectiveLimits.
const (
	PressureLoad           = "load"
	PressureCPUThrottle    = "cpu_throttle"
	PressureMemoryPressure = "memory_pressure"
	PressureMemoryUsage    = "memory_usage"
)

// Reading is one sample of system pressure. A signal whose Has flag is false
// was unavailable and takes no part in the decision.
type Reading struct {
	// Load1 is the one-minute load average; CPUs is the CPU capacity it is
	// compared with (the cgroup quota when one applies).
	Load1   float64
	CPUs    float64
	HasLoad bool
	// Throttled is the fraction of CFS periods throttled since the previous
	// reading (cgroup v2 cpu.stat).
	Throttled   float64
	HasThrottle bool
	// MemoryPressure is the PSI "some avg10" percentage.
	MemoryPressure    float64
	HasMemoryPressure bool
	// MemoryUsage is the working set divided by the cgroup memory limit.
	MemoryUsage    float64
	HasMemoryUsage bool
}

// PressureSource reads system pressure. Implementations must bound their
// reads; an error makes the controller keep the current limits.
type PressureSource interface {
	Read(context.Context) (Reading, error)
}

// Thresholds is one hysteresis band: at or above High the signal is under
// pressure, at or below Low it is calm, in between it holds.
type Thresholds struct{ High, Low float64 }

func (t Thresholds) valid(ceiling float64) bool {
	return t.Low > 0 && t.Low < t.High && t.High <= ceiling
}

// AdaptiveOptions configures the G41.7 controller. Zero values select the
// defaults below.
type AdaptiveOptions struct {
	Source PressureSource
	// Interval between readings (default 10s).
	Interval time.Duration
	// Dwell is the shortest time between two changes (default 30s), so one
	// spike lowers the limits by one step only.
	Dwell time.Duration
	// Cooldown is how long every signal must stay calm before each
	// recovery step (default 2m).
	Cooldown time.Duration
	// MinPercent is the floor of the effective limits (default 25);
	// StepPercent is the size of one change (default 25).
	MinPercent  int
	StepPercent int
	// Load is per CPU (default 1.5/0.9), Throttle a fraction of periods
	// (0.3/0.05), MemoryPressure a PSI percentage (20/5) and MemoryUsage a
	// fraction of the limit (0.92/0.8).
	Load           Thresholds
	Throttle       Thresholds
	MemoryPressure Thresholds
	MemoryUsage    Thresholds
	Now            func() time.Time
	Logger         *slog.Logger
}

func (o *AdaptiveOptions) defaults() {
	if o.Interval == 0 {
		o.Interval = 10 * time.Second
	}
	if o.Dwell == 0 {
		o.Dwell = 30 * time.Second
	}
	if o.Cooldown == 0 {
		o.Cooldown = 2 * time.Minute
	}
	if o.MinPercent == 0 {
		o.MinPercent = 25
	}
	if o.StepPercent == 0 {
		o.StepPercent = 25
	}
	if o.Load == (Thresholds{}) {
		o.Load = Thresholds{High: 1.5, Low: 0.9}
	}
	if o.Throttle == (Thresholds{}) {
		o.Throttle = Thresholds{High: 0.3, Low: 0.05}
	}
	if o.MemoryPressure == (Thresholds{}) {
		o.MemoryPressure = Thresholds{High: 20, Low: 5}
	}
	if o.MemoryUsage == (Thresholds{}) {
		o.MemoryUsage = Thresholds{High: 0.92, Low: 0.8}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
}

// AdaptiveStatus is a snapshot of the controller.
type AdaptiveStatus struct {
	Percent      int
	Source       string
	ReadFailures uint64
}

// Adaptive lowers the effective limits of a Budget under system pressure and
// restores them once the system is calm (G41.7). It only ever lowers: the
// configured limits are the ceiling. Changes follow a hysteresis band per
// signal, a minimum dwell between changes and a recovery cooldown, so a
// signal oscillating around one threshold cannot make the limits flap.
type Adaptive struct {
	budget *Budget
	opts   AdaptiveOptions

	mu         sync.Mutex
	percent    int
	source     string
	lastChange time.Time
	calmSince  time.Time
	failures   uint64
	lastWarn   time.Time
}

// ErrPressureUnsupported reports a platform without pressure readings.
var ErrPressureUnsupported = errors.New("system pressure readings are unavailable on this platform")

// NewAdaptive validates o (zero values take the defaults) and returns a
// controller at 100% of the configured limits.
func NewAdaptive(b *Budget, o AdaptiveOptions) (*Adaptive, error) {
	o.defaults()
	switch {
	case b == nil || o.Source == nil:
		return nil, errors.New("adaptive concurrency requires a budget and a pressure source")
	case o.Interval < time.Second || o.Interval > time.Hour || o.Dwell < 0 || o.Dwell > 24*time.Hour || o.Cooldown < 0 || o.Cooldown > 24*time.Hour:
		return nil, errors.New("adaptive concurrency timing is outside the supported range")
	case o.MinPercent < 1 || o.MinPercent > 100 || o.StepPercent < 1 || o.StepPercent > 100:
		return nil, errors.New("adaptive concurrency percentages are outside the supported range")
	case !o.Load.valid(1024) || !o.Throttle.valid(1) || !o.MemoryPressure.valid(100) || !o.MemoryUsage.valid(1):
		return nil, errors.New("adaptive concurrency thresholds are invalid")
	}
	return &Adaptive{budget: b, opts: o, percent: 100}, nil
}

// Status returns the current factor, pressure source and read failures.
func (a *Adaptive) Status() AdaptiveStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	return AdaptiveStatus{Percent: a.percent, Source: a.source, ReadFailures: a.failures}
}

// Run evaluates every Interval until ctx ends. It returns at once when the
// source reports an unsupported platform, leaving the configured limits.
func (a *Adaptive) Run(ctx context.Context) {
	if _, err := a.read(ctx); errors.Is(err, ErrPressureUnsupported) {
		a.opts.Logger.Info("adaptive concurrency unavailable on this platform; configured limits stay in force", "component", "jobs")
		return
	}
	ticker := time.NewTicker(a.opts.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.Step(ctx)
		}
	}
}

func (a *Adaptive) read(ctx context.Context) (Reading, error) {
	// A reading never outlives a fraction of the interval.
	readCtx, cancel := context.WithTimeout(ctx, min(a.opts.Interval/2, 2*time.Second))
	defer cancel()
	return a.opts.Source.Read(readCtx)
}

// over returns the first signal at or above its high threshold. calm reports
// whether every available signal is at or below its low threshold; none
// reports that no signal was available at all.
func (a *Adaptive) classify(r Reading) (over string, calm, none bool) {
	type signal struct {
		name    string
		has     bool
		value   float64
		between Thresholds
	}
	load := 0.0
	if r.HasLoad && r.CPUs > 0 {
		load = r.Load1 / r.CPUs
	}
	signals := [...]signal{
		{PressureMemoryPressure, r.HasMemoryPressure, r.MemoryPressure, a.opts.MemoryPressure},
		{PressureMemoryUsage, r.HasMemoryUsage, r.MemoryUsage, a.opts.MemoryUsage},
		{PressureCPUThrottle, r.HasThrottle, r.Throttled, a.opts.Throttle},
		{PressureLoad, r.HasLoad && r.CPUs > 0, load, a.opts.Load},
	}
	calm, none = true, true
	for _, s := range signals {
		if !s.has {
			continue
		}
		none = false
		if s.value >= s.between.High && over == "" {
			over = s.name
		}
		if s.value > s.between.Low {
			calm = false
		}
	}
	return over, calm && !none, none
}

// Step takes one reading and applies at most one change. It is exported so
// tests can drive the controller with an injected clock.
func (a *Adaptive) Step(ctx context.Context) {
	reading, err := a.read(ctx)
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.opts.Now()
	over, calm, none := "", false, true
	if err == nil {
		over, calm, none = a.classify(reading)
	}
	if err != nil || none {
		// A failed or empty reading keeps the current limits.
		a.failures++
		a.calmSince = time.Time{}
		if a.lastWarn.IsZero() || now.Sub(a.lastWarn) >= 10*time.Minute {
			a.lastWarn = now
			a.opts.Logger.Warn("system pressure reading failed; limits unchanged", "component", "jobs", "percent", a.percent)
		}
		return
	}
	sinceChange := now.Sub(a.lastChange)
	switch {
	case over != "":
		a.calmSince = time.Time{}
		if a.percent > a.opts.MinPercent && (a.lastChange.IsZero() || sinceChange >= a.opts.Dwell) {
			a.apply(now, max(a.opts.MinPercent, a.percent-a.opts.StepPercent), over)
		}
	case calm:
		if a.calmSince.IsZero() {
			a.calmSince = now
		}
		if a.percent < 100 && now.Sub(a.calmSince) >= a.opts.Cooldown && sinceChange >= a.opts.Cooldown {
			a.apply(now, min(100, a.percent+a.opts.StepPercent), a.source)
			// Each further step needs another full calm cooldown.
			a.calmSince = now
		}
	default:
		// Inside the hysteresis band: hold, and restart the calm period.
		a.calmSince = time.Time{}
	}
}

func scaled(limit, percent int) int {
	return max(1, (limit*percent+99)/100)
}

func (a *Adaptive) apply(now time.Time, percent int, source string) {
	lowered := percent < a.percent
	a.percent, a.lastChange = percent, now
	if percent >= 100 {
		source = ""
	}
	a.source = source
	conf := a.budget.Limits()
	effective := a.budget.SetEffective(Limits{CPU: scaled(conf.CPU, percent), IO: scaled(conf.IO, percent), Total: scaled(conf.Total, percent)}, source)
	attrs := []any{"component", "jobs", "percent", percent, "cpuLimit", effective.CPU, "ioLimit", effective.IO, "totalLimit", effective.Total}
	if source != "" {
		attrs = append(attrs, "source", source)
	}
	switch {
	case lowered:
		a.opts.Logger.Info("adaptive concurrency lowered", attrs...)
	case percent >= 100:
		a.opts.Logger.Info("adaptive concurrency restored", attrs...)
	default:
		a.opts.Logger.Info("adaptive concurrency recovering", attrs...)
	}
}
