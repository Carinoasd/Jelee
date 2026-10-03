package logging

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
)

// Components are the independently configurable level scopes from G46.2.
var Components = []string{"http", "auth", "access", "scan", "probe", "nfo", "images", "jobs", "webhook", "compat", "media", "db", "gc"}

// componentAliases maps component names used by existing call sites onto the
// G46.2 scopes. "ignore" rules are evaluated while scanning libraries, and the
// "metrics" lifecycle messages concern database snapshots held for scrapes.
var componentAliases = map[string]string{"ignore": "scan", "metrics": "db"}

// ErrUnknownComponent reports a level change for a scope that does not exist.
var ErrUnknownComponent = errors.New("unknown log component")

// ErrInvalidLevel reports an unsupported level name.
var ErrInvalidLevel = errors.New("invalid log level")

// GlobalComponent addresses the global level in SetLevel.
const GlobalComponent = ""

// ScopeFor returns the G46.2 scope a component name is filtered under, or
// false when the name is neither a scope nor a known alias.
func ScopeFor(component string) (string, bool) {
	if alias, ok := componentAliases[component]; ok {
		return alias, true
	}
	for _, c := range Components {
		if c == component {
			return c, true
		}
	}
	return "", false
}

func knownComponent(component string) bool {
	_, ok := ScopeFor(component)
	return ok
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

type componentLevel struct {
	level slog.LevelVar
	set   atomic.Bool
}

// levels holds the global and per-component thresholds. The component map is
// built once and never mutated, so lookups need no lock; only writers
// serialize to keep the cached minimum consistent.
type levels struct {
	global     slog.LevelVar
	components map[string]*componentLevel
	min        slog.LevelVar
	mu         sync.Mutex
}

func newLevels(global slog.Level) *levels {
	l := &levels{components: make(map[string]*componentLevel, len(Components))}
	for _, c := range Components {
		l.components[c] = &componentLevel{}
	}
	l.global.Set(global)
	l.min.Set(global)
	return l
}

func (l *levels) set(component string, level slog.Level) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if component == GlobalComponent {
		l.global.Set(level)
	} else {
		scope, ok := ScopeFor(component)
		if !ok {
			return ErrUnknownComponent
		}
		c := l.components[scope]
		c.level.Set(level)
		c.set.Store(true)
	}
	l.recompute()
	return nil
}

func (l *levels) reset(component string) error {
	scope, ok := ScopeFor(component)
	if !ok {
		return ErrUnknownComponent
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.components[scope].set.Store(false)
	l.recompute()
	return nil
}

func (l *levels) recompute() {
	min := l.global.Level()
	for _, c := range l.components {
		if c.set.Load() && c.level.Level() < min {
			min = c.level.Level()
		}
	}
	l.min.Set(min)
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

// levelHandler applies component thresholds in front of the formatting
// handler and normalizes attribute and group keys, which slog never passes
// through ReplaceAttr for groups. Enabled uses the lowest active threshold as
// a cheap prefilter; Handle then applies the exact threshold of the record's
// component. A nil levels only guards keys.
type levelHandler struct {
	next      slog.Handler
	levels    *levels
	component string
}

func (h *levelHandler) Enabled(ctx context.Context, level slog.Level) bool {
	if h.levels != nil && level < h.levels.min.Level() {
		return false
	}
	return h.next.Enabled(ctx, level)
}

func (h *levelHandler) Handle(ctx context.Context, r slog.Record) error {
	component, unsafe := h.component, false
	r.Attrs(func(a slog.Attr) bool {
		if component == "" && a.Key == "component" && a.Value.Kind() == slog.KindString {
			component = a.Value.String()
		}
		if !unsafe && needsKeyGuard(a) {
			unsafe = true
		}
		return true
	})
	if h.levels != nil && r.Level < h.levels.threshold(component) {
		return nil
	}
	if unsafe {
		clean := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
		r.Attrs(func(a slog.Attr) bool {
			clean.AddAttrs(guardKeys(a))
			return true
		})
		r = clean
	}
	return h.next.Handle(ctx, r)
}

func (h *levelHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	component := h.component
	clean := make([]slog.Attr, 0, len(attrs))
	for _, a := range attrs {
		if a.Key == "component" && a.Value.Kind() == slog.KindString {
			component = a.Value.String()
		}
		clean = append(clean, guardKeys(a))
	}
	return &levelHandler{next: h.next.WithAttrs(clean), levels: h.levels, component: component}
}

func (h *levelHandler) WithGroup(name string) slog.Handler {
	return &levelHandler{next: h.next.WithGroup(safeKey(name)), levels: h.levels, component: h.component}
}

// safeKey keeps identifier-like keys. Keys are code constants by contract, so
// anything else is treated as data that must not reach the output.
func safeKey(key string) string {
	if key == "" || (len(key) <= 32 && (key[0] < '0' || key[0] > '9') && safeToken(key, 32, "_") && (len(key) < 12 || !strings.ContainsAny(key, "0123456789"))) {
		return key
	}
	return "redactedKey"
}

func needsKeyGuard(a slog.Attr) bool {
	if safeKey(a.Key) != a.Key || a.Value.Kind() == slog.KindLogValuer {
		return true
	}
	if a.Value.Kind() == slog.KindGroup {
		for _, member := range a.Value.Group() {
			if needsKeyGuard(member) {
				return true
			}
		}
	}
	return false
}

func guardKeys(a slog.Attr) slog.Attr {
	a.Key = safeKey(a.Key)
	a.Value = a.Value.Resolve()
	if a.Value.Kind() != slog.KindGroup {
		return a
	}
	members := a.Value.Group()
	clean := make([]slog.Attr, len(members))
	for i, member := range members {
		clean[i] = guardKeys(member)
	}
	return slog.Attr{Key: a.Key, Value: slog.GroupValue(clean...)}
}
