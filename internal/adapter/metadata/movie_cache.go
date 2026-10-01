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
type movieCache = candidateCache[domain.MovieCandidate]
type seriesCache = candidateCache[domain.SeriesCandidate]
type timedCandidate interface{ FetchedTime() time.Time }
type candidateEntry[V timedCandidate] struct {
	key   candidateKey
	value V
}
type candidateCache[V timedCandidate] struct {
	mu      sync.Mutex
	entries map[candidateKey]*list.Element
	lru     list.List
}

func (c *candidateCache[V]) get(key candidateKey, now time.Time) (V, bool) {
	var zero V
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entries[key]
	if e == nil {
		return zero, false
	}
	entry := e.Value.(candidateEntry[V])
	if !now.Before(entry.value.FetchedTime().Add(movieCacheTTL)) {
		delete(c.entries, key)
		c.lru.Remove(e)
		return zero, false
	}
	c.lru.MoveToFront(e)
	return entry.value, true
}

func (c *candidateCache[V]) put(key candidateKey, value V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[candidateKey]*list.Element)
	}
	if e := c.entries[key]; e != nil {
		// Concurrent misses may finish out of order. Keep the newer data.
		if value.FetchedTime().After(e.Value.(candidateEntry[V]).value.FetchedTime()) {
			e.Value = candidateEntry[V]{key, value}
		}
		c.lru.MoveToFront(e)
		return
	}
	c.entries[key] = c.lru.PushFront(candidateEntry[V]{key, value})
	if len(c.entries) > movieCacheCapacity {
		e := c.lru.Back()
		delete(c.entries, e.Value.(candidateEntry[V]).key)
		c.lru.Remove(e)
	}
}
