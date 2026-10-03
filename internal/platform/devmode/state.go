package devmode

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

const (
	// DefaultTTL is the default developer-mode lifetime (G45.7).
	DefaultTTL = 12 * time.Hour
	// MaxTTL is the hard upper bound; configuration cannot exceed it.
	MaxTTL = 24 * time.Hour
)

var (
	ErrDenied             = errors.New("devmode_denied")
	ErrProduction         = errors.New("devmode_production_denied")
	ErrAlreadyActive      = errors.New("devmode_already_active")
	ErrInactive           = errors.New("devmode_inactive")
	ErrUnknownToggle      = errors.New("devmode_unknown_toggle")
	ErrUnknownOperation   = errors.New("devmode_unknown_operation")
	ErrConfirmationNeeded = errors.New("devmode_confirmation_required")
	ErrInvalidTTL         = errors.New("devmode_invalid_ttl")
)

// Clock supplies the current time. Tests inject a fake clock.
type Clock interface{ Now() time.Time }

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// EventKind classifies Observer events.
type EventKind string

const (
	EventEnabled            EventKind = "enabled"
	EventDisabled           EventKind = "disabled"
	EventExpired            EventKind = "expired"
	EventDenied             EventKind = "denied"
	EventToggleChanged      EventKind = "toggle_changed"
	EventDangerousConfirmed EventKind = "dangerous_confirmed"
)

// Event is one auditable state transition. Adapters map it to audit records
// (time, source, config diff) and WARN logs.
type Event struct {
	Kind EventKind
	At   time.Time
	// Source identifies the actor or entry point, e.g. "cli" or "loopback".
	Source string
	// Reason explains denials and disables.
	Reason string
	// Alert marks events that must page or WARN prominently, such as a
	// production denial.
	Alert bool
	// Missing lists unmet thresholds for gate denials.
	Missing []Requirement
	// ConfigDiff is the caller-supplied configuration diff for enables.
	ConfigDiff string
	// EnabledAt and ExpiresAt describe the session for enable, disable and
	// expiry events.
	EnabledAt time.Time
	ExpiresAt time.Time
	// Toggle and Value describe toggle changes.
	Toggle Toggle
	Value  bool
	// Operation names the dangerous operation for confirmation events.
	Operation DangerousOperation
	// Restored lists toggles that reverted to production defaults when the
	// session ended.
	Restored []Toggle
}

// Observer receives events synchronously, in transition order, while the
// state lock is held. Implementations must be fast and must not call back
// into State.
type Observer interface{ Observe(Event) }

// ObserverFunc adapts a function to Observer.
type ObserverFunc func(Event)

func (f ObserverFunc) Observe(e Event) { f(e) }

// Options configure a State.
type Options struct {
	Clock    Clock
	Observer Observer
	// TTL is the session lifetime; zero selects DefaultTTL. It must not
	// exceed MaxTTL.
	TTL time.Duration
}

// EnableRequest describes one enable attempt.
type EnableRequest struct {
	Inputs     Inputs
	Source     string
	ConfigDiff string
	// TTL overrides Options.TTL for this session when positive; it is still
	// bounded by MaxTTL.
	TTL time.Duration
}

// Status is a point-in-time snapshot.
type Status struct {
	Active    bool
	EnabledAt time.Time
	ExpiresAt time.Time
	Source    string
	// Toggles holds only the toggles currently switched on.
	Toggles []Toggle
}

// State is the in-memory developer-mode session. A new State is always
// inactive, so a process restart never inherits a session (G45.7). It is safe
// for concurrent use.
type State struct {
	mu       sync.Mutex
	clock    Clock
	observer Observer
	ttl      time.Duration

	active    bool
	enabledAt time.Time
	expiresAt time.Time
	source    string
	toggles   map[Toggle]bool
}

// New returns an inactive State.
func New(opts Options) (*State, error) {
	ttl, err := normalizeTTL(opts.TTL, DefaultTTL)
	if err != nil {
		return nil, err
	}
	clock := opts.Clock
	if clock == nil {
		clock = realClock{}
	}
	return &State{clock: clock, observer: opts.Observer, ttl: ttl, toggles: map[Toggle]bool{}}, nil
}

func normalizeTTL(ttl, fallback time.Duration) (time.Duration, error) {
	switch {
	case ttl == 0:
		return fallback, nil
	case ttl < 0 || ttl > MaxTTL:
		return 0, fmt.Errorf("%w: %s exceeds bounds (0, %s]", ErrInvalidTTL, ttl, MaxTTL)
	}
	return ttl, nil
}

func (s *State) emit(e Event) {
	if s.observer != nil {
		s.observer.Observe(e)
	}
}

// expireLocked closes an elapsed session. Every read and write calls it first
// so no relaxation outlives its deadline even without a background sweeper.
func (s *State) expireLocked(now time.Time) {
	if !s.active || now.Before(s.expiresAt) {
		return
	}
	restored := s.resetLocked()
	s.emit(Event{Kind: EventExpired, At: now, Source: s.source, Reason: "ttl elapsed", Alert: true,
		EnabledAt: s.enabledAt, ExpiresAt: s.expiresAt, Restored: restored})
	s.source = ""
}

func (s *State) resetLocked() []Toggle {
	var restored []Toggle
	for _, t := range Toggles() {
		if s.toggles[t] {
			restored = append(restored, t)
		}
	}
	s.active = false
	s.toggles = map[Toggle]bool{}
	return restored
}

// Enable evaluates the gate and opens a session when every threshold holds.
// Denials are reported to the Observer; production denials carry Alert.
func (s *State) Enable(req EnableRequest) (Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock.Now()
	s.expireLocked(now)

	decision := Evaluate(req.Inputs)
	if !decision.Allowed {
		reason, err := "gate thresholds not met", ErrDenied
		if decision.Production {
			reason, err = "production environment ignores developer configuration", ErrProduction
		}
		s.emit(Event{Kind: EventDenied, At: now, Source: req.Source, Reason: reason, Alert: decision.Alert, Missing: decision.Missing})
		return s.statusLocked(), err
	}
	if s.active {
		s.emit(Event{Kind: EventDenied, At: now, Source: req.Source, Reason: "already active"})
		return s.statusLocked(), ErrAlreadyActive
	}
	ttl, err := normalizeTTL(req.TTL, s.ttl)
	if err != nil {
		s.emit(Event{Kind: EventDenied, At: now, Source: req.Source, Reason: "invalid ttl"})
		return s.statusLocked(), err
	}
	s.active = true
	s.enabledAt = now
	s.expiresAt = now.Add(ttl)
	s.source = req.Source
	s.toggles = map[Toggle]bool{}
	s.emit(Event{Kind: EventEnabled, At: now, Source: req.Source, Alert: true, ConfigDiff: req.ConfigDiff,
		EnabledAt: s.enabledAt, ExpiresAt: s.expiresAt})
	return s.statusLocked(), nil
}

// Disable closes the active session and restores every production default.
func (s *State) Disable(source, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock.Now()
	s.expireLocked(now)
	if !s.active {
		return ErrInactive
	}
	restored := s.resetLocked()
	s.emit(Event{Kind: EventDisabled, At: now, Source: source, Reason: reason,
		EnabledAt: s.enabledAt, ExpiresAt: s.expiresAt, Restored: restored})
	s.source = ""
	return nil
}

// Sweep closes an elapsed session. Callers may run it periodically so the
// expiry event is emitted promptly; correctness does not depend on it.
func (s *State) Sweep() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked(s.clock.Now())
}

// SetToggle switches one toggle while developer mode is active. Switching a
// dangerous toggle on requires IUnderstand; switching any toggle back to its
// production default never does.
func (s *State) SetToggle(t Toggle, on bool, confirm Confirmation, source string) error {
	if !t.Known() {
		return ErrUnknownToggle
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock.Now()
	s.expireLocked(now)
	if !s.active {
		s.emit(Event{Kind: EventDenied, At: now, Source: source, Reason: "toggle while inactive", Toggle: t, Value: on})
		return ErrInactive
	}
	if on && t.Dangerous() && !confirm.IUnderstand {
		s.emit(Event{Kind: EventDenied, At: now, Source: source, Reason: "confirmation required", Toggle: t, Value: on})
		return ErrConfirmationNeeded
	}
	if s.toggles[t] == on {
		return nil
	}
	if on {
		s.toggles[t] = true
	} else {
		delete(s.toggles, t)
	}
	s.emit(Event{Kind: EventToggleChanged, At: now, Source: source, Alert: on, Toggle: t, Value: on})
	return nil
}

// Effective reports whether t is currently switched on. It is always false
// outside an active, unexpired session.
func (s *State) Effective(t Toggle) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked(s.clock.Now())
	return s.active && s.toggles[t]
}

// Active reports whether an unexpired session exists.
func (s *State) Active() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked(s.clock.Now())
	return s.active
}

// Status returns a snapshot after applying expiry.
func (s *State) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked(s.clock.Now())
	return s.statusLocked()
}

func (s *State) statusLocked() Status {
	if !s.active {
		return Status{}
	}
	st := Status{Active: true, EnabledAt: s.enabledAt, ExpiresAt: s.expiresAt, Source: s.source}
	for _, t := range Toggles() {
		if s.toggles[t] {
			st.Toggles = append(st.Toggles, t)
		}
	}
	return st
}

// ConfirmDangerous authorises one G45.6 operation. Every operation requires
// IUnderstand; dev-only operations additionally require an active session.
// Successful confirmations are emitted for the audit trail.
func (s *State) ConfirmDangerous(op DangerousOperation, confirm Confirmation, source string) error {
	if !op.Known() {
		return ErrUnknownOperation
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock.Now()
	s.expireLocked(now)
	if op.RequiresDevMode() && !s.active {
		s.emit(Event{Kind: EventDenied, At: now, Source: source, Reason: "dev-only operation while inactive", Operation: op})
		return ErrInactive
	}
	if !confirm.IUnderstand {
		s.emit(Event{Kind: EventDenied, At: now, Source: source, Reason: "confirmation required", Operation: op})
		return ErrConfirmationNeeded
	}
	s.emit(Event{Kind: EventDangerousConfirmed, At: now, Source: source, Alert: true, Operation: op})
	return nil
}
