package devmode

import (
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

type recorder struct {
	mu     sync.Mutex
	events []Event
}

func (r *recorder) Observe(e Event) {
	r.mu.Lock()
	r.events = append(r.events, e)
	r.mu.Unlock()
}

func (r *recorder) kinds() []EventKind {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]EventKind, len(r.events))
	for i, e := range r.events {
		out[i] = e.Kind
	}
	return out
}

func (r *recorder) last() Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.events[len(r.events)-1]
}

func allOpen() Inputs {
	return Inputs{EnvFlag: true, ConfigEnabled: true, TokenVerified: true, LoopbackEntry: true}
}

func newState(t *testing.T, ttl time.Duration) (*State, *fakeClock, *recorder) {
	t.Helper()
	clock, rec := newFakeClock(), &recorder{}
	s, err := New(Options{Clock: clock, Observer: rec, TTL: ttl})
	if err != nil {
		t.Fatal(err)
	}
	return s, clock, rec
}

func TestGateMatrix(t *testing.T) {
	for mask := 0; mask < 16; mask++ {
		in := Inputs{EnvFlag: mask&1 != 0, ConfigEnabled: mask&2 != 0, TokenVerified: mask&4 != 0, LoopbackEntry: mask&8 != 0}
		d := Evaluate(in)
		var want []Requirement
		for i, r := range Requirements() {
			if mask&(1<<i) == 0 {
				want = append(want, r)
			}
		}
		if d.Allowed != (mask == 15) || d.Production || d.Alert || !reflect.DeepEqual(d.Missing, want) {
			t.Fatalf("mask %04b: decision %+v, want missing %v", mask, d, want)
		}

		s, _, rec := newState(t, 0)
		_, err := s.Enable(EnableRequest{Inputs: in, Source: "test"})
		if mask == 15 {
			if err != nil || !s.Active() || rec.last().Kind != EventEnabled {
				t.Fatalf("mask %04b: err=%v active=%v", mask, err, s.Active())
			}
			continue
		}
		if !errors.Is(err, ErrDenied) || s.Active() {
			t.Fatalf("mask %04b: err=%v active=%v", mask, err, s.Active())
		}
		if e := rec.last(); e.Kind != EventDenied || e.Alert || !reflect.DeepEqual(e.Missing, want) {
			t.Fatalf("mask %04b: event %+v", mask, e)
		}
	}
}

func TestProductionAlwaysDenied(t *testing.T) {
	cases := []Inputs{
		{Environment: "production"},
		{Environment: " Production "},
		{ProductionImage: true},
	}
	for _, base := range cases {
		for mask := 0; mask < 16; mask++ {
			in := base
			in.EnvFlag, in.ConfigEnabled, in.TokenVerified, in.LoopbackEntry = mask&1 != 0, mask&2 != 0, mask&4 != 0, mask&8 != 0
			d := Evaluate(in)
			if d.Allowed || !d.Production || !d.Alert || len(d.Missing) != 0 {
				t.Fatalf("%+v: decision %+v", in, d)
			}
			s, _, rec := newState(t, 0)
			if _, err := s.Enable(EnableRequest{Inputs: in, Source: "cli"}); !errors.Is(err, ErrProduction) {
				t.Fatalf("%+v: err=%v", in, err)
			}
			if e := rec.last(); e.Kind != EventDenied || !e.Alert || s.Active() {
				t.Fatalf("%+v: event %+v", in, e)
			}
		}
	}
	if Evaluate(Inputs{Environment: "staging", EnvFlag: true, ConfigEnabled: true, TokenVerified: true, LoopbackEntry: true}).Allowed != true {
		t.Fatal("non-production environment must still pass")
	}
}

func TestReadEnvironment(t *testing.T) {
	env := map[string]string{EnvDevMode: "TRUE", EnvEnvironment: "production"}
	in := ReadEnvironment(func(k string) (string, bool) { v, ok := env[k]; return v, ok })
	if !in.EnvFlag || !in.IsProduction() {
		t.Fatalf("%+v", in)
	}
	for _, v := range []string{"1", "yes", "on", "", "false", "truee"} {
		env[EnvDevMode] = v
		if ReadEnvironment(func(k string) (string, bool) { v, ok := env[k]; return v, ok }).EnvFlag {
			t.Fatalf("%q must not enable", v)
		}
	}
	if (ReadEnvironment(nil) != Inputs{}) {
		t.Fatal("nil lookup must be closed")
	}
}

func TestTTLExpiryAndBounds(t *testing.T) {
	if _, err := New(Options{TTL: MaxTTL + time.Second}); !errors.Is(err, ErrInvalidTTL) {
		t.Fatalf("over cap: %v", err)
	}
	if _, err := New(Options{TTL: -time.Second}); !errors.Is(err, ErrInvalidTTL) {
		t.Fatalf("negative: %v", err)
	}
	s, clock, rec := newState(t, 0)
	if _, err := s.Enable(EnableRequest{Inputs: allOpen(), TTL: MaxTTL + 1}); !errors.Is(err, ErrInvalidTTL) || s.Active() {
		t.Fatalf("request over cap: %v", err)
	}
	st, err := s.Enable(EnableRequest{Inputs: allOpen(), Source: "cli", ConfigDiff: "dev.enabled: false -> true"})
	if err != nil || st.ExpiresAt.Sub(st.EnabledAt) != DefaultTTL {
		t.Fatalf("default ttl: %+v %v", st, err)
	}
	if e := rec.last(); e.ConfigDiff == "" || e.Source != "cli" || !e.Alert {
		t.Fatalf("enable event %+v", e)
	}
	clock.Advance(DefaultTTL - time.Nanosecond)
	if !s.Active() {
		t.Fatal("expired early")
	}
	clock.Advance(time.Nanosecond)
	if s.Active() {
		t.Fatal("did not expire at deadline")
	}
	if e := rec.last(); e.Kind != EventExpired || !e.Alert {
		t.Fatalf("expiry event %+v", e)
	}
	s.Sweep()
	if n := len(rec.kinds()); rec.kinds()[n-1] != EventExpired || rec.kinds()[n-2] == EventExpired {
		t.Fatalf("expiry must be emitted once: %v", rec.kinds())
	}

	custom, clock2, _ := newState(t, time.Hour)
	st, _ = custom.Enable(EnableRequest{Inputs: allOpen()})
	if st.ExpiresAt.Sub(st.EnabledAt) != time.Hour {
		t.Fatalf("configured ttl %v", st.ExpiresAt.Sub(st.EnabledAt))
	}
	clock2.Advance(time.Hour)
	custom.Sweep()
	if custom.Active() {
		t.Fatal("sweep did not expire")
	}
}

func TestRestartDoesNotInherit(t *testing.T) {
	s, _, _ := newState(t, 0)
	if _, err := s.Enable(EnableRequest{Inputs: allOpen()}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetToggle(RelaxAPIRateLimit, true, Confirmation{}, "cli"); err != nil {
		t.Fatal(err)
	}
	fresh, _, _ := newState(t, 0)
	if fresh.Active() || fresh.Effective(RelaxAPIRateLimit) || !reflect.DeepEqual(fresh.Status(), Status{}) {
		t.Fatal("new state must start closed")
	}
}

func TestToggleCatalogue(t *testing.T) {
	var restrictions, debug int
	seen := map[Toggle]bool{}
	for _, tg := range Toggles() {
		if seen[tg] {
			t.Fatalf("duplicate %s", tg)
		}
		seen[tg] = true
		switch tg.Kind() {
		case KindRestriction:
			restrictions++
		case KindDebug:
			debug++
		default:
			t.Fatalf("%s has no kind", tg)
		}
	}
	if restrictions != 12 || debug != 11 {
		t.Fatalf("G45.4 has 12 restrictions and G45.5 has 11 options, got %d/%d", restrictions, debug)
	}
	if !DebugTranscode.Dangerous() || DebugTranscode != "dev-transcode" {
		t.Fatal("dev-transcode must keep its G10.11 name and be dangerous")
	}
	if Toggle("nope").Known() {
		t.Fatal("unknown toggle")
	}
}

func TestTogglesOnlyWhileActive(t *testing.T) {
	s, clock, _ := newState(t, time.Hour)
	for _, tg := range Toggles() {
		if s.Effective(tg) {
			t.Fatalf("%s effective by default", tg)
		}
	}
	if err := s.SetToggle(RelaxLoginRateLimit, true, Confirmation{IUnderstand: true}, "cli"); !errors.Is(err, ErrInactive) {
		t.Fatalf("inactive toggle: %v", err)
	}
	if err := s.SetToggle("bogus", true, Confirmation{IUnderstand: true}, "cli"); !errors.Is(err, ErrUnknownToggle) {
		t.Fatalf("unknown toggle: %v", err)
	}
	if _, err := s.Enable(EnableRequest{Inputs: allOpen()}); err != nil {
		t.Fatal(err)
	}
	for _, tg := range Toggles() {
		if s.Effective(tg) {
			t.Fatalf("%s effective right after enable", tg)
		}
		if tg.Dangerous() {
			if err := s.SetToggle(tg, true, Confirmation{}, "cli"); !errors.Is(err, ErrConfirmationNeeded) || s.Effective(tg) {
				t.Fatalf("%s without confirmation: %v", tg, err)
			}
		}
	}
	if err := s.SetToggle(RelaxLoginRateLimit, true, Confirmation{}, "cli"); err != nil {
		t.Fatal(err)
	}
	if !s.Effective(RelaxLoginRateLimit) || s.Effective(RelaxAPIRateLimit) {
		t.Fatal("toggles must be independent")
	}
	for _, tg := range Toggles() {
		if err := s.SetToggle(tg, true, Confirmation{IUnderstand: true}, "cli"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetToggle(RelaxSSRFStrict, false, Confirmation{}, "cli"); err != nil || s.Effective(RelaxSSRFStrict) {
		t.Fatalf("restoring a default needs no confirmation: %v", err)
	}
	clock.Advance(time.Hour)
	for _, tg := range Toggles() {
		if s.Effective(tg) {
			t.Fatalf("%s survived expiry", tg)
		}
	}
	if _, err := s.Enable(EnableRequest{Inputs: allOpen()}); err != nil {
		t.Fatal(err)
	}
	if st := s.Status(); len(st.Toggles) != 0 {
		t.Fatalf("re-enable must start from production defaults: %v", st.Toggles)
	}
	_ = s.SetToggle(DebugPprof, true, Confirmation{IUnderstand: true}, "cli")
	if err := s.Disable("cli", "done"); err != nil || s.Effective(DebugPprof) {
		t.Fatalf("disable: %v", err)
	}
	if err := s.Disable("cli", "again"); !errors.Is(err, ErrInactive) {
		t.Fatalf("double disable: %v", err)
	}
}

func TestDangerousOperations(t *testing.T) {
	s, _, rec := newState(t, 0)
	if err := s.ConfirmDangerous(OpClearCache, Confirmation{}, "cli"); !errors.Is(err, ErrConfirmationNeeded) {
		t.Fatalf("missing confirmation: %v", err)
	}
	if err := s.ConfirmDangerous(OpClearCache, Confirmation{IUnderstand: true}, "cli"); err != nil {
		t.Fatal(err)
	}
	if e := rec.last(); e.Kind != EventDangerousConfirmed || e.Operation != OpClearCache {
		t.Fatalf("event %+v", e)
	}
	if err := s.ConfirmDangerous(OpDisableAuth, Confirmation{IUnderstand: true}, "cli"); !errors.Is(err, ErrInactive) {
		t.Fatalf("disable auth outside dev: %v", err)
	}
	if err := s.ConfirmDangerous("nuke", Confirmation{IUnderstand: true}, "cli"); !errors.Is(err, ErrUnknownOperation) {
		t.Fatalf("unknown op: %v", err)
	}
	if _, err := s.Enable(EnableRequest{Inputs: allOpen()}); err != nil {
		t.Fatal(err)
	}
	if err := s.ConfirmDangerous(OpDisableAuth, Confirmation{}, "cli"); !errors.Is(err, ErrConfirmationNeeded) {
		t.Fatalf("disable auth unconfirmed: %v", err)
	}
	if err := s.ConfirmDangerous(OpDisableAuth, Confirmation{IUnderstand: true}, "cli"); err != nil {
		t.Fatal(err)
	}
	if len(DangerousOperations()) != 5 {
		t.Fatal("G45.6 lists five operations")
	}
}

func TestEventOrder(t *testing.T) {
	s, clock, rec := newState(t, time.Hour)
	_, _ = s.Enable(EnableRequest{Inputs: Inputs{EnvFlag: true}})
	_, _ = s.Enable(EnableRequest{Inputs: allOpen(), Source: "loopback"})
	_, _ = s.Enable(EnableRequest{Inputs: allOpen(), Source: "loopback"})
	_ = s.SetToggle(RelaxBandwidthLimit, true, Confirmation{}, "cli")
	_ = s.SetToggle(RelaxBandwidthLimit, true, Confirmation{}, "cli") // no-op, no event
	_ = s.SetToggle(RelaxBandwidthLimit, false, Confirmation{}, "cli")
	_ = s.SetToggle(DebugSQLLogging, true, Confirmation{}, "cli")
	_ = s.Disable("cli", "manual")
	_, _ = s.Enable(EnableRequest{Inputs: allOpen()})
	_ = s.SetToggle(DebugSeedData, true, Confirmation{}, "cli")
	clock.Advance(time.Hour)
	s.Sweep()

	want := []EventKind{
		EventDenied, EventEnabled, EventDenied,
		EventToggleChanged, EventToggleChanged, EventToggleChanged,
		EventDisabled, EventEnabled, EventToggleChanged, EventExpired,
	}
	if got := rec.kinds(); !reflect.DeepEqual(got, want) {
		t.Fatalf("events\n got %v\nwant %v", got, want)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if r := rec.events[6].Restored; !reflect.DeepEqual(r, []Toggle{DebugSQLLogging}) {
		t.Fatalf("disable restored %v", r)
	}
	if r := rec.events[9].Restored; !reflect.DeepEqual(r, []Toggle{DebugSeedData}) {
		t.Fatalf("expiry restored %v", r)
	}
	for i := 1; i < len(rec.events); i++ {
		if rec.events[i].At.Before(rec.events[i-1].At) {
			t.Fatal("event times must be monotonic")
		}
	}
}

func TestConcurrentUse(t *testing.T) {
	s, clock, rec := newState(t, time.Minute)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				tg := Toggles()[(g+i)%len(Toggles())]
				switch i % 6 {
				case 0:
					_, _ = s.Enable(EnableRequest{Inputs: allOpen()})
				case 1:
					_ = s.SetToggle(tg, true, Confirmation{IUnderstand: true}, "race")
				case 2:
					_ = s.Effective(tg)
				case 3:
					_ = s.Status()
				case 4:
					clock.Advance(time.Second)
					s.Sweep()
				case 5:
					if g == 0 {
						_ = s.Disable("race", "stress")
					}
				}
			}
		}(g)
	}
	wg.Wait()
	final := s.Active()

	// Every session must alternate enabled -> (disabled|expired).
	active := false
	for _, k := range rec.kinds() {
		switch k {
		case EventEnabled:
			if active {
				t.Fatal("enabled twice without close")
			}
			active = true
		case EventDisabled, EventExpired:
			if !active {
				t.Fatalf("%s without open session", k)
			}
			active = false
		case EventToggleChanged:
			if !active {
				t.Fatal("toggle change outside session")
			}
		}
	}
	if active != final {
		t.Fatal("event stream disagrees with final state")
	}
}
