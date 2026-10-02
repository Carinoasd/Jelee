package images

import (
	"container/list"
	"sync"
	"time"
)

// encodedImage owns a tight-capacity byte slice. It is immutable after
// creation; only independent ReadSeekCloser readers reach callers.
type encodedImage struct {
	data          []byte
	width, height int
	etag          string
}

type cacheEntry struct {
	key     [32]byte
	image   encodedImage
	expires time.Time
}

type imageCache struct {
	mu         sync.Mutex
	entries    map[[32]byte]*list.Element
	order      list.List
	bytes      int64
	maxBytes   int64
	maxEntries int
	ttl        time.Duration
	now        func() time.Time
	closed     bool
	evictions  uint64
}

func newImageCache(maxBytes int64, maxEntries int, ttl time.Duration) *imageCache {
	return &imageCache{entries: make(map[[32]byte]*list.Element), maxBytes: maxBytes,
		maxEntries: maxEntries, ttl: ttl, now: time.Now}
}

func (c *imageCache) remove(element *list.Element) {
	entry := element.Value.(cacheEntry)
	delete(c.entries, entry.key)
	c.bytes -= int64(cap(entry.image.data))
	c.order.Remove(element)
	c.evictions++
}

func (c *imageCache) expire(now time.Time) {
	// Access order and expiry order differ. The bounded entry count makes a
	// full pass safe and avoids a background timer or an unbounded expiry heap.
	for element := c.order.Back(); element != nil; {
		previous := element.Prev()
		if !now.Before(element.Value.(cacheEntry).expires) {
			c.remove(element)
		}
		element = previous
	}
}

func (c *imageCache) get(key [32]byte) (encodedImage, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return encodedImage{}, false
	}
	c.expire(c.now())
	element, ok := c.entries[key]
	if !ok {
		return encodedImage{}, false
	}
	c.order.MoveToFront(element)
	return element.Value.(cacheEntry).image, true
}

// put takes shared immutable ownership; it never copies a source image or
// decoded pixels. An evicted value held by a response remains in that request's
// reserved processing slot until Body.Close.
func (c *imageCache) put(key [32]byte, value encodedImage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || len(value.data) == 0 || cap(value.data) != len(value.data) || int64(cap(value.data)) > c.maxBytes {
		return
	}
	now := c.now()
	c.expire(now)
	if old, ok := c.entries[key]; ok {
		c.remove(old)
	}
	for c.order.Len() >= c.maxEntries || c.bytes > c.maxBytes-int64(cap(value.data)) {
		c.remove(c.order.Back())
	}
	element := c.order.PushFront(cacheEntry{key: key, image: value, expires: now.Add(c.ttl)})
	c.entries[key] = element
	c.bytes += int64(cap(value.data))
}

func (c *imageCache) shutdown() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	c.entries = nil
	c.order.Init()
	c.bytes = 0
}

func (c *imageCache) stats() (entries int, bytes int64, evictions uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		c.expire(c.now())
	}
	return c.order.Len(), c.bytes, c.evictions
}
