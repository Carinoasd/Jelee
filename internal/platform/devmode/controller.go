package devmode

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// TokenTTL is the lifetime of a one-time enable token.
	TokenTTL = 5 * time.Minute
	// tokenPrefix marks enable tokens so they are recognisable in a shell
	// history or a secret scanner.
	tokenPrefix = "jdm_"
	// warnInterval spaces the periodic WARN reminder while a session is
	// active (G45.3).
	warnInterval = 5 * time.Minute
)

var (
	// ErrToggleUnavailable refuses a catalogued toggle this build does not
	// wire into its subsystem yet; switching it on would change nothing.
	ErrToggleUnavailable = errors.New("devmode_toggle_unavailable")
	// ErrNotLoopback refuses a token request that did not arrive through the
	// loopback-only entry (G45.2).
	ErrNotLoopback = errors.New("devmode_not_loopback")
)

// Actor identifies who drove a transition, for the audit trail. Every field
// is optional: the CLI has no user and background expiry has no actor.
type Actor struct {
	UserID    string
	SessionID string
	IP        string
}

// TokenRedeemer consumes one-time enable tokens inside a session update.
type TokenRedeemer interface {
	// RedeemToken marks the unexpired, unredeemed token with digest as used
	// and reports whether it existed in that state.
	RedeemToken(ctx context.Context, digest [32]byte, now time.Time) (bool, error)
}

// Mutation computes the next record from the locked current one. It returns
// the events to audit with it.
type Mutation func(cur Record, tokens TokenRedeemer) (next Record, events []Event, err error)

// Store persists the shared session (PostgreSQL in production, so every
// instance and the CLI see the same state).
type Store interface {
	LoadDevSession(ctx context.Context) (Record, error)
	// UpdateDevSession locks the record and runs fn. It stores next when its
	// Version differs from the current one and records every returned event
	// for actor in the same transaction, even when fn returns an error (so
	// denials are audited). It returns the stored record and fn's error.
	UpdateDevSession(ctx context.Context, actor Actor, fn Mutation) (Record, error)
	// IssueDevToken stores the digest of a new one-time token and audits it.
	IssueDevToken(ctx context.Context, actor Actor, digest [32]byte, issuedAt, expiresAt time.Time) error
}

// ControllerOptions configure a Controller.
type ControllerOptions struct {
	Store Store
	Clock Clock
	// Local holds this process's own thresholds: Environment,
	// ProductionImage, EnvFlag and ConfigEnabled. TokenVerified and
	// LoopbackEntry are ignored; they are established per enable attempt.
	Local Inputs
	// TTL is the session lifetime; zero selects DefaultTTL.
	TTL time.Duration
	// Persist keeps an active session across a process restart (G45.7); it
	// is off by default and logs a WARN when used.
	Persist bool
	// Available lists the toggles this process wires into its subsystems.
	// SetToggle refuses every other toggle with ErrToggleUnavailable.
	Available []Toggle
	Logger    *slog.Logger
	// RefreshInterval is how often Run reloads the shared session; zero
	// selects two seconds on a developer-capable instance and ten otherwise.
	RefreshInterval time.Duration
	// OnChange is called with the effective status whenever it changes, for
	// example to raise or restore the log level.
	OnChange func(Status)
}

// Controller connects the State machine to shared storage. Reads use a
// cached record and the local clock, so expiry takes effect at the deadline
// even before storage is swept; writes go through Store in one transaction
// each. A Controller of an instance that does not meet the local thresholds
// (or runs in production) never reports an active session, whatever storage
// holds, and switches a stored session off (G45.1, G45.2).
type Controller struct {
	store     Store
	clock     Clock
	local     Inputs
	capable   bool
	ttl       time.Duration
	persist   bool
	available map[Toggle]bool
	logger    *slog.Logger
	interval  time.Duration
	onChange  func(Status)

	rec atomic.Pointer[Record]

	notifyMu sync.Mutex
	notified Status
	warnedAt time.Time
}

// NewController validates opts and returns a Controller with an inactive
// cached record. Call Startup before serving.
func NewController(opts ControllerOptions) (*Controller, error) {
	if opts.Store == nil {
		return nil, errors.New("developer mode store must be provided")
	}
	ttl, err := normalizeTTL(opts.TTL, DefaultTTL)
	if err != nil {
		return nil, err
	}
	c := &Controller{store: opts.Store, clock: opts.Clock, ttl: ttl, persist: opts.Persist, available: map[Toggle]bool{},
		logger: opts.Logger, interval: opts.RefreshInterval, onChange: opts.OnChange}
	c.local = Inputs{Environment: opts.Local.Environment, ProductionImage: opts.Local.ProductionImage, EnvFlag: opts.Local.EnvFlag, ConfigEnabled: opts.Local.ConfigEnabled}
	// The local thresholds never change, so the gate is evaluated once and
	// request paths read a bool.
	full := c.local
	full.TokenVerified, full.LoopbackEntry = true, true
	c.capable = Evaluate(full).Allowed
	if c.clock == nil {
		c.clock = realClock{}
	}
	if c.logger == nil {
		c.logger = slog.New(slog.DiscardHandler)
	}
	for _, t := range opts.Available {
		if !t.Known() {
			return nil, ErrUnknownToggle
		}
		c.available[t] = true
	}
	if c.interval <= 0 {
		c.interval = 10 * time.Second
		if c.Capable() {
			c.interval = 2 * time.Second
		}
	}
	c.rec.Store(&Record{})
	return c, nil
}

// Capable reports whether this process meets its own thresholds: the
// environment flag and the configuration switch, outside production. Only a
// capable instance issues tokens, mounts developer routes or applies a
// session.
func (c *Controller) Capable() bool { return c.capable }

// Production reports whether a production marker disables developer mode.
func (c *Controller) Production() bool { return c.local.IsProduction() }

// TTL is the configured session lifetime.
func (c *Controller) TTL() time.Duration { return c.ttl }

// Available reports whether t is wired into this process.
func (c *Controller) Available(t Toggle) bool { return c.available[t] }

// Status is the effective session of this instance.
func (c *Controller) Status() Status {
	if !c.Capable() {
		return Status{}
	}
	return statusOf(*c.rec.Load(), c.clock.Now())
}

// Active reports whether an unexpired session applies to this instance.
func (c *Controller) Active() bool {
	rec := c.rec.Load()
	return rec.Active && c.clock.Now().Before(rec.ExpiresAt) && c.Capable()
}

// Effective reports whether t is switched on in an unexpired session that
// applies to this instance. It is cheap enough for every request.
func (c *Controller) Effective(t Toggle) bool {
	rec := c.rec.Load()
	return rec.Active && slices.Contains(rec.Toggles, t) && c.clock.Now().Before(rec.ExpiresAt) && c.Capable()
}

// NewToken returns a fresh one-time token and its digest.
func NewToken() (string, [32]byte, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", [32]byte{}, err
	}
	token := tokenPrefix + hex.EncodeToString(raw)
	return token, sha256.Sum256([]byte(token)), nil
}

// TokenDigest validates the token format and returns its digest.
func TokenDigest(token string) ([32]byte, bool) {
	rest, ok := strings.CutPrefix(token, tokenPrefix)
	if !ok || len(rest) != 64 {
		return [32]byte{}, false
	}
	if _, err := hex.DecodeString(rest); err != nil || strings.ToLower(rest) != rest {
		return [32]byte{}, false
	}
	return sha256.Sum256([]byte(token)), true
}

// IssueToken creates a one-time enable token. Only a capable instance issues
// tokens, and only to a request that arrived through the loopback entry.
func (c *Controller) IssueToken(ctx context.Context, actor Actor, loopback bool) (string, time.Time, error) {
	switch {
	case c.Production():
		c.logger.Error("developer mode token refused in production", "component", "devmode", "code", "devmode_production_denied")
		return "", time.Time{}, ErrProduction
	case !c.Capable():
		return "", time.Time{}, ErrDenied
	case !loopback:
		c.logger.Warn("developer mode token refused outside the loopback entry", "component", "devmode", "code", "devmode_not_loopback")
		return "", time.Time{}, ErrNotLoopback
	}
	token, digest, err := NewToken()
	if err != nil {
		return "", time.Time{}, err
	}
	now := c.clock.Now()
	expires := now.Add(TokenTTL)
	if err = c.store.IssueDevToken(ctx, actor, digest, now, expires); err != nil {
		return "", time.Time{}, err
	}
	c.logger.Warn("developer mode enable token issued", "component", "devmode", "expiresAt", expires.UTC().Format(time.RFC3339))
	return token, expires, nil
}

// mutate applies fn to a State restored from the locked record and caches
// the stored result.
func (c *Controller) mutate(ctx context.Context, actor Actor, fn func(*State, TokenRedeemer) error) (Record, error) {
	var emitted []Event
	var fnErr error
	rec, err := c.store.UpdateDevSession(ctx, actor, func(cur Record, tokens TokenRedeemer) (Record, []Event, error) {
		var events []Event
		st := restore(c.clock, c.ttl, cur, ObserverFunc(func(e Event) { events = append(events, e) }))
		fnErr = fn(st, tokens)
		next := st.record()
		next.Version = cur.Version
		if !sameSession(cur, next) {
			next.Version++
		}
		emitted = events
		return next, events, fnErr
	})
	// Only fn's own outcome means the transaction committed; any other error
	// is a storage failure and leaves the cache and the log untouched.
	if err != fnErr {
		return rec, err
	}
	c.setRecord(rec)
	for _, e := range emitted {
		c.logEvent(e)
	}
	return rec, err
}

// Enable opens a session for the CLI (G45.1): this process's environment
// flag and configuration switch, outside production, plus a valid one-time
// token issued through the loopback entry. A presented token is consumed
// even when the attempt is refused for another reason.
func (c *Controller) Enable(ctx context.Context, actor Actor, token, source, configDiff string, ttl time.Duration) (Status, error) {
	in := c.local
	digest, wellFormed := TokenDigest(token)
	rec, err := c.mutate(ctx, actor, func(st *State, tokens TokenRedeemer) error {
		if !in.IsProduction() && wellFormed {
			ok, err := tokens.RedeemToken(ctx, digest, c.clock.Now())
			if err != nil {
				return err
			}
			// Tokens are only ever issued through the loopback entry.
			in.TokenVerified, in.LoopbackEntry = ok, ok
		}
		_, err := st.Enable(EnableRequest{Inputs: in, Source: source, ConfigDiff: configDiff, TTL: ttl})
		return err
	})
	return statusOf(rec, c.clock.Now()), err
}

// Disable closes the shared session and restores every production default.
func (c *Controller) Disable(ctx context.Context, actor Actor, source, reason string) error {
	_, err := c.mutate(ctx, actor, func(st *State, _ TokenRedeemer) error { return st.Disable(source, reason) })
	return err
}

// SetToggle switches one toggle of the active session. Dangerous toggles need
// confirm.IUnderstand to be switched on (G45.6).
func (c *Controller) SetToggle(ctx context.Context, actor Actor, t Toggle, on bool, confirm Confirmation, source string) (Status, error) {
	if !t.Known() {
		return c.Status(), ErrUnknownToggle
	}
	if on && !c.available[t] {
		return c.Status(), ErrToggleUnavailable
	}
	if !c.Capable() {
		return Status{}, ErrInactive
	}
	rec, err := c.mutate(ctx, actor, func(st *State, _ TokenRedeemer) error { return st.SetToggle(t, on, confirm, source) })
	return statusOf(rec, c.clock.Now()), err
}

// Startup reconciles the stored session with this process before it serves:
// an instance that is not capable switches a stored session off, and a
// capable one does so too unless Persist is set (G45.7). It also writes the
// startup WARN lines (G45.2, G45.3).
func (c *Controller) Startup(ctx context.Context) error {
	switch {
	case c.Production() && (c.local.EnvFlag || c.local.ConfigEnabled):
		c.logger.Error("developer mode configuration ignored in production", "component", "devmode", "code", "devmode_production_denied")
	case c.Capable():
		c.logger.Warn("developer mode capable instance: never run this configuration in production", "component", "devmode", "ttl", c.ttl.String(), "persistAcrossRestart", c.persist)
	}
	rec, err := c.store.LoadDevSession(ctx)
	if err != nil {
		return err
	}
	c.setRecord(rec)
	if !rec.Active || !c.clock.Now().Before(rec.ExpiresAt) {
		return c.sweep(ctx, rec)
	}
	switch {
	case !c.Capable():
		return c.forceOff(ctx, "instance does not meet the developer mode thresholds")
	case !c.persist:
		return c.forceOff(ctx, "process restart")
	}
	c.logger.Warn("developer mode session persisted across restart", "component", "devmode", "code", "devmode_persisted", "expiresAt", rec.ExpiresAt.UTC().Format(time.RFC3339))
	c.notify()
	return nil
}

// Refresh reloads the shared session, closes an elapsed one and switches off
// a session this instance may not apply.
func (c *Controller) Refresh(ctx context.Context) error {
	rec, err := c.store.LoadDevSession(ctx)
	if err != nil {
		return err
	}
	c.setRecord(rec)
	if rec.Active && c.clock.Now().Before(rec.ExpiresAt) && !c.Capable() {
		return c.forceOff(ctx, "instance does not meet the developer mode thresholds")
	}
	return c.sweep(ctx, rec)
}

func (c *Controller) sweep(ctx context.Context, rec Record) error {
	if rec.Active && !c.clock.Now().Before(rec.ExpiresAt) {
		_, err := c.mutate(ctx, Actor{}, func(st *State, _ TokenRedeemer) error { st.Sweep(); return nil })
		return err
	}
	c.notify()
	return nil
}

func (c *Controller) forceOff(ctx context.Context, reason string) error {
	c.logger.Warn("developer mode session switched off", "component", "devmode", "code", "devmode_forced_off", "reason", reason)
	_, err := c.mutate(ctx, Actor{}, func(st *State, _ TokenRedeemer) error {
		if err := st.Disable("instance", reason); err != nil && !errors.Is(err, ErrInactive) {
			return err
		}
		return nil
	})
	return err
}

// Run refreshes the shared session until ctx ends and repeats the WARN
// reminder while a session is active. Errors are logged, never fatal.
func (c *Controller) Run(ctx context.Context) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	var failedAt time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		refresh, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := c.Refresh(refresh)
		cancel()
		if err != nil && ctx.Err() == nil && time.Since(failedAt) >= time.Minute {
			failedAt = time.Now()
			c.logger.Warn("developer mode state refresh failed", "component", "devmode", "code", "devmode_refresh_failed")
		}
		c.remind()
	}
}

// remind writes the periodic WARN while a session is active (G45.3).
func (c *Controller) remind() {
	st := c.Status()
	c.notifyMu.Lock()
	due := st.Active && (c.warnedAt.IsZero() || c.clock.Now().Sub(c.warnedAt) >= warnInterval)
	if due {
		c.warnedAt = c.clock.Now()
	} else if !st.Active {
		c.warnedAt = time.Time{}
	}
	c.notifyMu.Unlock()
	if due {
		c.logger.Warn("developer mode is active; production restrictions may be relaxed", "component", "devmode", "code", "devmode_active",
			"expiresAt", st.ExpiresAt.UTC().Format(time.RFC3339), "toggles", toggleNames(st.Toggles))
	}
}

func (c *Controller) setRecord(rec Record) {
	stored := rec
	stored.Toggles = slices.Clone(rec.Toggles)
	c.rec.Store(&stored)
	c.notify()
}

// notify calls OnChange when the effective status differs from the last one
// reported.
func (c *Controller) notify() {
	st := c.Status()
	c.notifyMu.Lock()
	changed := st.Active != c.notified.Active || !slices.Equal(st.Toggles, c.notified.Toggles) || !st.ExpiresAt.Equal(c.notified.ExpiresAt)
	if changed {
		c.notified = st
	}
	c.notifyMu.Unlock()
	if changed && c.onChange != nil {
		c.onChange(st)
	}
}

func (c *Controller) logEvent(e Event) {
	attrs := []any{"component", "devmode", "code", "devmode_" + string(e.Kind), "source", e.Source}
	if e.Reason != "" {
		attrs = append(attrs, "reason", e.Reason)
	}
	if e.Toggle != "" {
		attrs = append(attrs, "toggle", string(e.Toggle), "value", e.Value)
	}
	if len(e.Missing) > 0 {
		missing := make([]string, len(e.Missing))
		for i, m := range e.Missing {
			missing[i] = string(m)
		}
		attrs = append(attrs, "missing", strings.Join(missing, ","))
	}
	if len(e.Restored) > 0 {
		attrs = append(attrs, "restored", toggleNames(e.Restored))
	}
	if !e.ExpiresAt.IsZero() {
		attrs = append(attrs, "expiresAt", e.ExpiresAt.UTC().Format(time.RFC3339))
	}
	if e.Kind == EventDenied && e.Alert {
		c.logger.Error("developer mode event", attrs...)
		return
	}
	c.logger.Warn("developer mode event", attrs...)
}

func toggleNames(ts []Toggle) string {
	names := make([]string, len(ts))
	for i, t := range ts {
		names[i] = string(t)
	}
	return strings.Join(names, ",")
}
