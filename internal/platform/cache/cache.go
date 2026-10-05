// Package cache provides bounded in-process caches with LRU eviction, TTL
// expiry, hit-rate counters, and cooperative shrinking under memory pressure.
//
// Shared caches must only hold data that is identical for every caller. Data
// derived from an account, session, or permission check must never enter a
// shared cache.
package cache

import (
	"container/list"
	"errors"
	"math"
	"sync"
	"time"
)

// Options bounds a cache. MaxEntries is required. MaxBytes is optional and
// requires Size, which estimates the retained bytes of one entry.
//
// TTL bounds positive entries stored with Set; zero disables time expiry.
// NegativeTTL bounds entries stored with SetNegative; zero disables negative
// caching, so SetNegative stores nothing.
//
// Ownership: the cache stores V as given and returns it as stored. When V
// holds mutable memory (slices, maps, pointers), either every caller must treat
// stored and returned values as immutable, or Clone must return a deep copy.
// Clone is applied once when a value enters the cache and once per successful
// Get, so no caller ever shares mutable memory with another caller.
type Options[K comparable, V any] struct {
	MaxEntries  int
	MaxBytes    int64
	Size        func(K, V) int64
	TTL         time.Duration
	NegativeTTL time.Duration
	Clone       func(V) V
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Stats is a point-in-time snapshot. Counters are cumulative since creation:
// Evictions counts capacity and Shrink removals, Expirations counts entries
// found or removed after their TTL. Delete and Purge are not counted.
type Stats struct {
	Hits, Misses, Evictions, Expirations uint64
	Entries                              int
	Bytes                                int64
	MaxEntries                           int
	MaxBytes                             int64
}

// HitRatio returns hits / (hits + misses), or zero before the first lookup.
func (s Stats) HitRatio() float64 {
	total := s.Hits + s.Misses
	if total == 0 {
		return 0
	}
	return float64(s.Hits) / float64(total)
}

type entry[K comparable, V any] struct {
	key     K
	value   V
	size    int64
	expires time.Time // zero means no expiry
}

// Cache is a bounded, concurrency-safe LRU cache with optional TTLs.
type Cache[K comparable, V any] struct {
	mu      sync.Mutex
	opts    Options[K, V]
	entries map[K]*list.Element
	order   list.List // front is most recently used
	bytes   int64
	stats   Stats
}

// New validates options and returns an empty cache.
func New[K comparable, V any](opts Options[K, V]) (*Cache[K, V], error) {
	switch {
	case opts.MaxEntries <= 0:
		return nil, errors.New("cache: MaxEntries must be positive")
	case opts.MaxBytes < 0:
		return nil, errors.New("cache: MaxBytes must not be negative")
	case opts.MaxBytes > 0 && opts.Size == nil:
		return nil, errors.New("cache: MaxBytes requires Size")
	case opts.TTL < 0 || opts.NegativeTTL < 0:
		return nil, errors.New("cache: TTL must not be negative")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Cache[K, V]{opts: opts, entries: make(map[K]*list.Element)}, nil
}

// Get returns a live entry and marks it most recently used. An expired entry
// is removed and reported as a miss.
func (c *Cache[K, V]) Get(key K) (V, bool) {
	var zero V
	c.mu.Lock()
	element, ok := c.entries[key]
	if !ok {
		c.stats.Misses++
		c.mu.Unlock()
		return zero, false
	}
	e := element.Value.(*entry[K, V])
	if !e.expires.IsZero() && !c.opts.Now().Before(e.expires) {
		c.removeLocked(element)
		c.stats.Expirations++
		c.stats.Misses++
		c.mu.Unlock()
		return zero, false
	}
	c.order.MoveToFront(element)
	c.stats.Hits++
	value := e.value
	c.mu.Unlock()
	// The stored value is never mutated, so copying outside the lock is safe.
	if c.opts.Clone != nil {
		value = c.opts.Clone(value)
	}
	return value, true
}

// Set stores a positive entry with TTL. It reports whether the value was
// stored; a value larger than MaxBytes or with a negative size is rejected and
// any older entry for key is removed so a stale value cannot be served.
func (c *Cache[K, V]) Set(key K, value V) bool { return c.set(key, value, c.opts.TTL) }

// SetNegative stores an entry with NegativeTTL, typically a "not found"
// marker. Callers encode negativity in V. It stores nothing, and removes any
// older entry for key, when negative caching is disabled.
func (c *Cache[K, V]) SetNegative(key K, value V) bool {
	if c.opts.NegativeTTL == 0 {
		c.Delete(key)
		return false
	}
	return c.set(key, value, c.opts.NegativeTTL)
}

func (c *Cache[K, V]) set(key K, value V, ttl time.Duration) bool {
	if c.opts.Clone != nil {
		value = c.opts.Clone(value)
	}
	var size int64
	if c.opts.Size != nil {
		size = c.opts.Size(key, value)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if old, ok := c.entries[key]; ok {
		c.removeLocked(old)
	}
	if size < 0 || (c.opts.MaxBytes > 0 && size > c.opts.MaxBytes) {
		return false
	}
	for c.order.Len() >= c.opts.MaxEntries || (c.opts.MaxBytes > 0 && c.bytes > c.opts.MaxBytes-size) {
		c.removeLocked(c.order.Back())
		c.stats.Evictions++
	}
	e := &entry[K, V]{key: key, value: value, size: size}
	if ttl > 0 {
		e.expires = c.opts.Now().Add(ttl)
	}
	c.entries[key] = c.order.PushFront(e)
	c.bytes += size
	return true
}

// Delete removes key if present.
func (c *Cache[K, V]) Delete(key K) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if element, ok := c.entries[key]; ok {
		c.removeLocked(element)
	}
}

// Purge removes every entry. Counters are kept.
func (c *Cache[K, V]) Purge() {
	c.mu.Lock()
	defer c.mu.Unlock()
	clear(c.entries)
	c.order.Init()
	c.bytes = 0
}

// Shrink releases memory under pressure. It first drops every expired entry,
// then evicts least recently used entries until at most (1-fraction) of the
// entries present at the call remain. Fraction is clamped to [0, 1]; a
// fraction of 1 empties the cache. It returns the number of removed entries.
func (c *Cache[K, V]) Shrink(fraction float64) int {
	if math.IsNaN(fraction) || fraction < 0 {
		fraction = 0
	}
	fraction = min(fraction, 1)
	c.mu.Lock()
	defer c.mu.Unlock()
	start := c.order.Len()
	keep := max(start-int(math.Ceil(float64(start)*fraction)), 0)
	now := c.opts.Now()
	// The entry count is bounded, so one pass is cheap and needs no timer.
	for element := c.order.Back(); element != nil; {
		previous := element.Prev()
		if e := element.Value.(*entry[K, V]); !e.expires.IsZero() && !now.Before(e.expires) {
			c.removeLocked(element)
			c.stats.Expirations++
		}
		element = previous
	}
	for c.order.Len() > keep {
		c.removeLocked(c.order.Back())
		c.stats.Evictions++
	}
	return start - c.order.Len()
}

// Stats returns counters and current occupancy. It does not sweep expired
// entries; they still count toward occupancy until touched or shrunk.
func (c *Cache[K, V]) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.stats
	s.Entries, s.Bytes = c.order.Len(), c.bytes
	s.MaxEntries, s.MaxBytes = c.opts.MaxEntries, c.opts.MaxBytes
	return s
}

func (c *Cache[K, V]) removeLocked(element *list.Element) {
	e := element.Value.(*entry[K, V])
	delete(c.entries, e.key)
	c.bytes -= e.size
	c.order.Remove(element)
}
