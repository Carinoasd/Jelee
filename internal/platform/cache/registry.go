package cache

import (
	"errors"
	"slices"
	"strings"
	"sync"
)

// Member is the registry view of a cache. *Cache satisfies it.
type Member interface {
	Stats() Stats
	Shrink(fraction float64) int
	Purge()
}

// NamedStats pairs a registered cache name with its snapshot.
type NamedStats struct {
	Name  string
	Stats Stats
}

// Registry lets independent caches respond together to memory pressure and
// exposes their statistics. Names become metric label values, so they must be
// fixed constants chosen in code, never derived from requests or data.
type Registry struct {
	mu      sync.Mutex
	members map[string]Member
}

// MaxNameLength bounds a registered name.
const MaxNameLength = 64

var defaultRegistry = NewRegistry()

// Default returns the process-wide registry.
func Default() *Registry { return defaultRegistry }

// NewRegistry returns an empty registry, mainly for tests.
func NewRegistry() *Registry { return &Registry{members: make(map[string]Member)} }

// Register adds m under name. Registering a name again replaces the earlier
// member, which keeps the registry bounded by the set of constant names when a
// component is rebuilt (for example a new HTTP server in tests); the replaced
// cache keeps working but is no longer shrunk or reported.
func (r *Registry) Register(name string, m Member) error {
	if m == nil {
		return errors.New("cache: nil registry member")
	}
	if !validName(name) {
		return errors.New("cache: registry name must be 1-64 characters of a-z, 0-9, '.', '_' or '-'")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.members[name] = m
	return nil
}

// Unregister removes name only while it still refers to m.
func (r *Registry) Unregister(name string, m Member) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if current, ok := r.members[name]; ok && current == m {
		delete(r.members, name)
	}
}

// Shrink asks every registered cache to release fraction of its entries, as
// documented by Cache.Shrink, and returns the total number removed. Callers
// invoke it from a memory pressure signal.
func (r *Registry) Shrink(fraction float64) int {
	removed := 0
	for _, m := range r.snapshotMembers() {
		removed += m.member.Shrink(fraction)
	}
	return removed
}

// Purge empties every registered cache.
func (r *Registry) Purge() {
	for _, m := range r.snapshotMembers() {
		m.member.Purge()
	}
}

// Snapshot returns per-cache statistics sorted by name. It performs no I/O.
func (r *Registry) Snapshot() []NamedStats {
	members := r.snapshotMembers()
	out := make([]NamedStats, len(members))
	for i, m := range members {
		out[i] = NamedStats{Name: m.name, Stats: m.member.Stats()}
	}
	return out
}

type namedMember struct {
	name   string
	member Member
}

// snapshotMembers copies the member list so cache locks are never taken while
// holding the registry lock.
func (r *Registry) snapshotMembers() []namedMember {
	r.mu.Lock()
	out := make([]namedMember, 0, len(r.members))
	for name, m := range r.members {
		out = append(out, namedMember{name: name, member: m})
	}
	r.mu.Unlock()
	slices.SortFunc(out, func(a, b namedMember) int { return strings.Compare(a.name, b.name) })
	return out
}

func validName(name string) bool {
	if name == "" || len(name) > MaxNameLength {
		return false
	}
	for _, c := range []byte(name) {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
