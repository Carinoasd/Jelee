package logging

import (
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Components are the independently configurable level scopes: the thirteen
// scopes G46.2 names plus "system" (startup self-checks and process
// lifecycle).
var Components = []string{"http", "auth", "access", "scan", "probe", "nfo", "images", "jobs", "webhook", "compat", "media", "db", "gc", "system"}

// MandatoryComponents are the scopes no level setting can lower or switch
// off (G46.10): "audit" mirrors audit trail events, "security" carries the
// security log of G46.3 (failed logins, locks, blocked clients, denied
// access, SSRF refusals, developer mode changes). Their threshold is never
// above INFO, whatever the global level.
var MandatoryComponents = []string{"security", "audit"}

// componentAliases maps component names used by call sites onto their
// scope. "ignore" rules are evaluated while scanning libraries and the
// "metrics" lifecycle messages concern database snapshots held for scrapes;
// developer mode changes are security events (G46.3).
var componentAliases = map[string]string{
	"ignore": "scan", "metrics": "db",
	"webhooks": "webhook",
	"playback": "media", "watch_stats": "media", "subtitle_ocr": "media", "matroska": "media",
	"client_control": "access", "share": "access",
	"accounts": "auth", "setup": "auth",
	"devmode":   "security",
	"selfcheck": "system",
}

// ErrUnknownComponent reports a level change for a scope that does not exist.
var ErrUnknownComponent = errors.New("unknown log component")

// ErrInvalidLevel reports an unsupported level name.
var ErrInvalidLevel = errors.New("invalid log level")

// ErrMandatoryComponent refuses a level change of an audit or security
// scope (G46.10).
var ErrMandatoryComponent = errors.New("log component cannot be lowered or disabled")

// GlobalComponent addresses the global level in SetLevel.
const GlobalComponent = ""

// mandatoryThreshold is the highest threshold a mandatory scope can have.
const mandatoryThreshold = slog.LevelInfo

// ScopeFor returns the scope a component name is filtered under, or false
// when the name is neither a scope nor a known alias.
func ScopeFor(component string) (string, bool) {
	if alias, ok := componentAliases[component]; ok {
		return alias, true
	}
	if slices.Contains(Components, component) || slices.Contains(MandatoryComponents, component) {
		return component, true
	}
	return "", false
}

// Mandatory reports whether component (a scope or alias) belongs to a
// scope that cannot be lowered or disabled.
func Mandatory(component string) bool {
	scope, ok := ScopeFor(component)
	return ok && slices.Contains(MandatoryComponents, scope)
}

func knownComponent(component string) bool {
	_, ok := ScopeFor(component)
	return ok
}

// ComponentInfo describes one scope for administration interfaces.
type ComponentInfo struct {
	Name      string
	Aliases   []string
	Mandatory bool
}

// ComponentTable lists every scope with its aliases, configurable scopes
// first.
func ComponentTable() []ComponentInfo {
	var table []ComponentInfo
	for _, group := range [][]string{Components, MandatoryComponents} {
		for _, name := range group {
			info := ComponentInfo{Name: name, Mandatory: slices.Contains(MandatoryComponents, name)}
			for alias, scope := range componentAliases {
				if scope == name {
					info.Aliases = append(info.Aliases, alias)
				}
			}
			slices.Sort(info.Aliases)
			table = append(table, info)
		}
	}
	return table
}

// ParseLevel accepts debug, info, warn and error (case-insensitive).
func ParseLevel(name string) (slog.Level, error) {
	switch strings.ToLower(name) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, ErrInvalidLevel
}

// LevelName is the lower case name ParseLevel accepts.
func LevelName(level slog.Level) string {
	switch {
	case level <= slog.LevelDebug:
		return "debug"
	case level <= slog.LevelInfo:
		return "info"
	case level <= slog.LevelWarn:
		return "warn"
	}
	return "error"
}

// LevelOverride is a runtime level set by an administrator (G46.2). An
// empty Component addresses the global level; a zero ExpiresAt never
// expires.
type LevelOverride struct {
	Component string
	Level     slog.Level
	ExpiresAt time.Time
}

type componentLevel struct {
	level slog.LevelVar
	set   atomic.Bool
}

// levels holds the global and per-component thresholds. The component map is
// built once and never mutated, so lookups need no lock; writers serialize on
// mu and recompute every effective threshold from three inputs: the
// configured levels, the administrator overrides (which expire) and the
// developer mode verbose switch.
type levels struct {
	global     slog.LevelVar
	components map[string]*componentLevel
	min        slog.LevelVar

	mu         sync.Mutex
	baseGlobal slog.Level
	base       map[string]slog.Level
	overrides  map[string]LevelOverride
	devVerbose bool
	timer      *time.Timer
	now        func() time.Time
}

func newLevels(global slog.Level) *levels {
	l := &levels{components: make(map[string]*componentLevel, len(Components)+len(MandatoryComponents)), baseGlobal: global,
		base: map[string]slog.Level{}, overrides: map[string]LevelOverride{}, now: time.Now}
	for _, c := range Components {
		l.components[c] = &componentLevel{}
	}
	for _, c := range MandatoryComponents {
		l.components[c] = &componentLevel{}
	}
	l.mu.Lock()
	l.recompute()
	l.mu.Unlock()
	return l
}

// set changes the configured level of a scope or, for GlobalComponent, the
// global level.
func (l *levels) set(component string, level slog.Level) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if component == GlobalComponent {
		l.baseGlobal = level
	} else {
		scope, ok := ScopeFor(component)
		if !ok {
			return ErrUnknownComponent
		}
		if Mandatory(scope) {
			return ErrMandatoryComponent
		}
		l.base[scope] = level
	}
	l.recompute()
	return nil
}

func (l *levels) reset(component string) error {
	scope, ok := ScopeFor(component)
	if !ok {
		return ErrUnknownComponent
	}
	if Mandatory(scope) {
		return ErrMandatoryComponent
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.base, scope)
	l.recompute()
	return nil
}

// setOverrides replaces every administrator override. Expired entries are
// ignored; an unknown or mandatory scope refuses the whole list.
func (l *levels) setOverrides(list []LevelOverride) error {
	next := make(map[string]LevelOverride, len(list))
	for _, o := range list {
		scope := GlobalComponent
		if o.Component != GlobalComponent {
			var ok bool
			if scope, ok = ScopeFor(o.Component); !ok {
				return ErrUnknownComponent
			}
			if Mandatory(scope) {
				return ErrMandatoryComponent
			}
		}
		o.Component = scope
		next[scope] = o
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.overrides = next
	l.recompute()
	return nil
}

func (l *levels) setDeveloperVerbose(on bool) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.devVerbose == on {
		return false
	}
	l.devVerbose = on
	l.recompute()
	return true
}

// recompute derives every effective threshold. It drops expired overrides
// and arms a timer for the next expiry. Callers hold mu.
func (l *levels) recompute() {
	now := l.now()
	var next time.Time
	for scope, o := range l.overrides {
		if !o.ExpiresAt.IsZero() && !now.Before(o.ExpiresAt) {
			delete(l.overrides, scope)
			continue
		}
		if !o.ExpiresAt.IsZero() && (next.IsZero() || o.ExpiresAt.Before(next)) {
			next = o.ExpiresAt
		}
	}
	global := l.baseGlobal
	if l.devVerbose {
		global = min(global, slog.LevelDebug)
	}
	if o, ok := l.overrides[GlobalComponent]; ok {
		global = o.Level
	}
	l.global.Set(global)
	lowest := global
	for scope, c := range l.components {
		level, set := slog.Level(0), false
		switch {
		case Mandatory(scope):
			level, set = min(global, mandatoryThreshold), true
		default:
			if o, ok := l.overrides[scope]; ok {
				level, set = o.Level, true
			} else if b, ok := l.base[scope]; ok {
				level, set = b, true
			}
		}
		if set {
			c.level.Set(level)
			lowest = min(lowest, level)
		}
		c.set.Store(set)
	}
	l.min.Set(lowest)
	if l.timer != nil {
		l.timer.Stop()
		l.timer = nil
	}
	if !next.IsZero() {
		l.timer = time.AfterFunc(next.Sub(now), l.expire)
	}
}

// stop disarms the expiry timer.
func (l *levels) stop() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.timer != nil {
		l.timer.Stop()
		l.timer = nil
	}
}

func (l *levels) expire() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.recompute()
}

// threshold returns the effective level for a component; unknown or empty
// components follow the global level.
func (l *levels) threshold(component string) slog.Level {
	if scope, ok := ScopeFor(component); ok {
		if c := l.components[scope]; c.set.Load() {
			return c.level.Level()
		}
	}
	return l.global.Level()
}

// ScopeLevel reports the levels of one scope (or the global level).
type ScopeLevel struct {
	// Component is the scope; empty for the global level.
	Component string
	Aliases   []string
	Mandatory bool
	// Configured is the level from the configuration (or a SetLevel
	// call); ConfiguredSet is false when the scope follows the global
	// level.
	Configured    slog.Level
	ConfiguredSet bool
	// Effective is the threshold in force now.
	Effective slog.Level
	// Override is the administrator override in force, if any.
	Override *LevelOverride
}

// report lists the global level followed by every scope.
func (l *levels) report() []ScopeLevel {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.recompute()
	out := []ScopeLevel{{Configured: l.baseGlobal, ConfiguredSet: true, Effective: l.global.Level()}}
	if o, ok := l.overrides[GlobalComponent]; ok {
		out[0].Override = &o
	}
	for _, info := range ComponentTable() {
		s := ScopeLevel{Component: info.Name, Aliases: info.Aliases, Mandatory: info.Mandatory, Effective: l.threshold(info.Name)}
		s.Configured, s.ConfiguredSet = l.base[info.Name]
		if o, ok := l.overrides[info.Name]; ok {
			s.Override = &o
		}
		out = append(out, s)
	}
	return out
}
