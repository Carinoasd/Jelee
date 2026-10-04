package devmode

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

func capableLocal() Inputs { return Inputs{EnvFlag: true, ConfigEnabled: true} }

func newController(t *testing.T, store Store, clock Clock, local Inputs, opts ...func(*ControllerOptions)) *Controller {
	t.Helper()
	o := ControllerOptions{Store: store, Clock: clock, Local: local, Available: Toggles()}
	for _, f := range opts {
		f(&o)
	}
	c, err := NewController(o)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func issue(t *testing.T, c *Controller) string {
	t.Helper()
	token, _, err := c.IssueToken(context.Background(), Actor{IP: "127.0.0.1"}, true)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestControllerEnableNeedsEveryThreshold(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		cli     Inputs
		token   func(server *Controller) string
		want    error
		missing []Requirement
	}{
		{"all thresholds", capableLocal(), func(s *Controller) string { return issue(t, s) }, nil, nil},
		{"no environment flag", Inputs{ConfigEnabled: true}, func(s *Controller) string { return issue(t, s) }, ErrDenied, []Requirement{RequireEnvFlag}},
		{"no configuration switch", Inputs{EnvFlag: true}, func(s *Controller) string { return issue(t, s) }, ErrDenied, []Requirement{RequireConfigEnabled}},
		{"no token", capableLocal(), func(*Controller) string { return "" }, ErrDenied, []Requirement{RequireCLIToken, RequireLoopbackEntry}},
		{"forged token", capableLocal(), func(*Controller) string { tok, _, _ := NewToken(); return tok }, ErrDenied, []Requirement{RequireCLIToken, RequireLoopbackEntry}},
		{"production", Inputs{EnvFlag: true, ConfigEnabled: true, Environment: "Production"}, func(s *Controller) string { return issue(t, s) }, ErrProduction, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, clock := NewMemoryStore(), newFakeClock()
			server := newController(t, store, clock, capableLocal())
			cli := newController(t, store, clock, tc.cli)
			st, err := cli.Enable(ctx, Actor{}, tc.token(server), "cli", "dev.enabled=true", 0)
			if !errors.Is(err, tc.want) || st.Active != (tc.want == nil) {
				t.Fatalf("enable: %+v %v", st, err)
			}
			last := store.Audit()[len(store.Audit())-1].Event
			if tc.want != nil && (last.Kind != EventDenied || !slices.Equal(last.Missing, tc.missing) || last.Alert != errors.Is(tc.want, ErrProduction)) {
				t.Fatalf("denial audit: %+v", last)
			}
			if err = server.Refresh(ctx); err != nil || server.Active() != (tc.want == nil) {
				t.Fatalf("server view: %t %v", server.Active(), err)
			}
		})
	}
}

func TestControllerTokens(t *testing.T) {
	ctx := context.Background()
	store, clock := NewMemoryStore(), newFakeClock()
	server := newController(t, store, clock, capableLocal())
	if _, _, err := server.IssueToken(ctx, Actor{}, false); !errors.Is(err, ErrNotLoopback) {
		t.Fatalf("non-loopback token: %v", err)
	}
	for _, local := range []Inputs{{EnvFlag: true}, {ConfigEnabled: true}, {EnvFlag: true, ConfigEnabled: true, Environment: "production"}} {
		if _, _, err := newController(t, store, clock, local).IssueToken(ctx, Actor{}, true); err == nil {
			t.Fatalf("incapable instance issued a token: %+v", local)
		}
	}
	if _, ok := TokenDigest("jdm_" + "AB"); ok {
		t.Fatal("malformed token accepted")
	}

	t.Run("one use only", func(t *testing.T) {
		token := issue(t, server) //nolint:contextcheck // test helper issues a token with its own background context
		if _, err := server.Enable(ctx, Actor{}, token, "cli", "", 0); err != nil {
			t.Fatal(err)
		}
		if err := server.Disable(ctx, Actor{}, "cli", "test"); err != nil {
			t.Fatal(err)
		}
		if _, err := server.Enable(ctx, Actor{}, token, "cli", "", 0); !errors.Is(err, ErrDenied) {
			t.Fatalf("token reused: %v", err)
		}
	})
	t.Run("expires", func(t *testing.T) {
		token := issue(t, server) //nolint:contextcheck // test helper issues a token with its own background context
		clock.Advance(TokenTTL)
		if _, err := server.Enable(ctx, Actor{}, token, "cli", "", 0); !errors.Is(err, ErrDenied) {
			t.Fatalf("expired token accepted: %v", err)
		}
	})
	t.Run("consumed by a refused attempt", func(t *testing.T) {
		token := issue(t, server) //nolint:contextcheck // test helper issues a token with its own background context
		if _, err := server.Enable(ctx, Actor{}, token, "cli", "", MaxTTL+time.Hour); !errors.Is(err, ErrInvalidTTL) {
			t.Fatalf("invalid ttl: %v", err)
		}
		if _, err := server.Enable(ctx, Actor{}, token, "cli", "", 0); !errors.Is(err, ErrDenied) {
			t.Fatalf("token survived a refused attempt: %v", err)
		}
	})
}

func TestControllerExpiryRestoresProduction(t *testing.T) {
	ctx := context.Background()
	store, clock := NewMemoryStore(), newFakeClock()
	var changes []Status
	server := newController(t, store, clock, capableLocal(), func(o *ControllerOptions) {
		o.TTL = time.Hour
		o.OnChange = func(s Status) { changes = append(changes, s) }
	})
	if _, err := server.Enable(ctx, Actor{}, issue(t, server), "cli", "", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := server.SetToggle(ctx, Actor{UserID: "u"}, RelaxHostStrict, true, Confirmation{}, "api"); !errors.Is(err, ErrConfirmationNeeded) {
		t.Fatalf("dangerous toggle without confirmation: %v", err)
	}
	if _, err := server.SetToggle(ctx, Actor{UserID: "u"}, RelaxHostStrict, true, Confirmation{IUnderstand: true}, "api"); err != nil {
		t.Fatal(err)
	}
	if !server.Effective(RelaxHostStrict) || server.Effective(RelaxSSRFStrict) {
		t.Fatal("toggle not effective one by one")
	}
	clock.Advance(time.Hour - time.Nanosecond)
	if !server.Active() {
		t.Fatal("expired early")
	}
	clock.Advance(time.Nanosecond)
	// Expiry applies at the deadline without any storage round trip.
	if server.Active() || server.Effective(RelaxHostStrict) {
		t.Fatal("relaxation outlived its deadline")
	}
	if err := server.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	rec, _ := store.LoadDevSession(ctx)
	if rec.Active || len(rec.Toggles) != 0 {
		t.Fatalf("stored session not swept: %+v", rec)
	}
	audit := store.Audit()
	last := audit[len(audit)-1].Event
	if last.Kind != EventExpired || !slices.Equal(last.Restored, []Toggle{RelaxHostStrict}) || !last.Alert {
		t.Fatalf("expiry audit: %+v", last)
	}
	if len(changes) != 3 || !changes[1].Active || changes[2].Active {
		t.Fatalf("change notifications: %+v", changes)
	}
}

func TestControllerUnavailableToggle(t *testing.T) {
	ctx := context.Background()
	store, clock := NewMemoryStore(), newFakeClock()
	server := newController(t, store, clock, capableLocal(), func(o *ControllerOptions) { o.Available = []Toggle{RelaxLoginRateLimit} })
	if _, err := server.Enable(ctx, Actor{}, issue(t, server), "cli", "", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := server.SetToggle(ctx, Actor{}, DebugTranscode, true, Confirmation{IUnderstand: true}, "api"); !errors.Is(err, ErrToggleUnavailable) {
		t.Fatalf("unwired toggle: %v", err)
	}
	if _, err := server.SetToggle(ctx, Actor{}, Toggle("nope"), true, Confirmation{}, "api"); !errors.Is(err, ErrUnknownToggle) {
		t.Fatalf("unknown toggle: %v", err)
	}
	if _, err := server.SetToggle(ctx, Actor{}, RelaxLoginRateLimit, true, Confirmation{}, "api"); err != nil {
		t.Fatal(err)
	}
}

func TestControllerStartupDoesNotInherit(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		local   Inputs
		persist bool
		keep    bool
	}{
		{"restart", capableLocal(), false, false},
		{"explicit persistence", capableLocal(), true, true},
		{"incapable instance", Inputs{EnvFlag: true}, true, false},
		{"production instance", Inputs{EnvFlag: true, ConfigEnabled: true, Environment: "production"}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, clock := NewMemoryStore(), newFakeClock()
			first := newController(t, store, clock, capableLocal())
			if _, err := first.Enable(ctx, Actor{}, issue(t, first), "cli", "", 0); err != nil { //nolint:contextcheck // test helper issues a token with its own background context
				t.Fatal(err)
			}
			next := newController(t, store, clock, tc.local, func(o *ControllerOptions) { o.Persist = tc.persist })
			if err := next.Startup(ctx); err != nil {
				t.Fatal(err)
			}
			rec, _ := store.LoadDevSession(ctx)
			if rec.Active != tc.keep || next.Active() != tc.keep {
				t.Fatalf("after startup: stored %t, effective %t", rec.Active, next.Active())
			}
		})
	}
}

func TestControllerIncapableInstanceSwitchesSharedSessionOff(t *testing.T) {
	ctx := context.Background()
	store, clock := NewMemoryStore(), newFakeClock()
	dev := newController(t, store, clock, capableLocal())
	if _, err := dev.Enable(ctx, Actor{}, issue(t, dev), "cli", "", 0); err != nil {
		t.Fatal(err)
	}
	prod := newController(t, store, clock, Inputs{Environment: "production"})
	if prod.Active() || prod.Effective(RelaxPermissionStrict) {
		t.Fatal("production instance reports a session")
	}
	if err := prod.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if err := dev.Refresh(ctx); err != nil || dev.Active() {
		t.Fatalf("shared session survived a production instance: %v", err)
	}
}
