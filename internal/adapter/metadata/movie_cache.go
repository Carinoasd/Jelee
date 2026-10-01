package metadata

import (
	"container/list"
	"sync"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

const movieCacheCapacity = 256
const movieCacheTTL = 24 * time.Hour

type candidateKey struct {
	id       int32
	language string
}
type movieKey = candidateKey
type movieCache = candidateCache[candidateKey, domain.MovieCandidate]
type seriesCache = candidateCache[candidateKey, domain.SeriesCandidate]
type timedCandidate interface{ FetchedTime() time.Time }
type candidateEntry[K comparable, V timedCandidate] struct {
	key   K
	value V
}
type candidateCache[K comparable, V timedCandidate] struct {
	capacity int
	mu       sync.Mutex
	entries  map[K]*list.Element
	lru      list.List
}

func (c *candidateCache[K, V]) get(key K, now time.Time) (V, bool) {
	var zero V
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entries[key]
	if e == nil {
		return zero, false
	}
	entry := e.Value.(candidateEntry[K, V])
	if !now.Before(entry.value.FetchedTime().Add(movieCacheTTL)) {
		delete(c.entries, key)
		c.lru.Remove(e)
		return zero, false
	}
	c.lru.MoveToFront(e)
	return entry.value, true
}

func (c *candidateCache[K, V]) put(key K, value V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[K]*list.Element)
	}
	if e := c.entries[key]; e != nil {
		// Concurrent misses may finish out of order. Keep the newer data.
		if value.FetchedTime().After(e.Value.(candidateEntry[K, V]).value.FetchedTime()) {
			e.Value = candidateEntry[K, V]{key, value}
		}
		c.lru.MoveToFront(e)
		return
	}
	c.entries[key] = c.lru.PushFront(candidateEntry[K, V]{key, value})
	limit := c.capacity
	if limit == 0 {
		limit = movieCacheCapacity
	}
	if len(c.entries) > limit {
		e := c.lru.Back()
		delete(c.entries, e.Value.(candidateEntry[K, V]).key)
		c.lru.Remove(e)
	}
}
