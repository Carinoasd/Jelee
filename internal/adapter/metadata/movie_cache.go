package metadata

import (
	"container/list"
	"sync"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

const movieCacheCapacity = 256
const movieCacheTTL = 24 * time.Hour

type movieKey struct {
	id       int32
	language string
}
type movieEntry struct {
	key   movieKey
	movie domain.MovieCandidate
}
type movieCache struct {
	mu      sync.Mutex
	entries map[movieKey]*list.Element
	lru     list.List
}

func (c *movieCache) get(key movieKey, now time.Time) (domain.MovieCandidate, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entries[key]
	if e == nil {
		return domain.MovieCandidate{}, false
	}
	entry := e.Value.(movieEntry)
	if !now.Before(entry.movie.FetchedAt.Add(movieCacheTTL)) {
		delete(c.entries, key)
		c.lru.Remove(e)
		return domain.MovieCandidate{}, false
	}
	c.lru.MoveToFront(e)
	return entry.movie, true
}

func (c *movieCache) put(key movieKey, movie domain.MovieCandidate) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[movieKey]*list.Element)
	}
	if e := c.entries[key]; e != nil {
		// Concurrent misses may finish out of order. Keep the newer data.
		if movie.FetchedAt.After(e.Value.(movieEntry).movie.FetchedAt) {
			e.Value = movieEntry{key, movie}
		}
		c.lru.MoveToFront(e)
		return
	}
	c.entries[key] = c.lru.PushFront(movieEntry{key, movie})
	if len(c.entries) > movieCacheCapacity {
		e := c.lru.Back()
		delete(c.entries, e.Value.(movieEntry).key)
		c.lru.Remove(e)
	}
}
