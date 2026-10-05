package cache

import (
	"fmt"
	"math"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newClock() *fakeClock { return &fakeClock{now: time.Unix(1_700_000_000, 0)} }

func mustNew[K comparable, V any](t *testing.T, opts Options[K, V]) *Cache[K, V] {
	t.Helper()
	c, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func keys[K comparable, V any](c *Cache[K, V]) []K {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []K
	for e := c.order.Front(); e != nil; e = e.Next() {
		out = append(out, e.Value.(*entry[K, V]).key)
	}
	return out
}

func TestNewRejectsInvalidOptions(t *testing.T) {
	for name, opts := range map[string]Options[string, int]{
		"no entries":      {},
		"negative bytes":  {MaxEntries: 1, MaxBytes: -1},
		"bytes sans size": {MaxEntries: 1, MaxBytes: 10},
		"negative ttl":    {MaxEntries: 1, TTL: -time.Second},
		"negative neg":    {MaxEntries: 1, NegativeTTL: -time.Second},
	} {
		if _, err := New(opts); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestEntryLimitEvictsLeastRecentlyUsed(t *testing.T) {
	c := mustNew(t, Options[string, int]{MaxEntries: 3})
	for i, k := range []string{"a", "b", "c"} {
		c.Set(k, i)
	}
	if _, ok := c.Get("a"); !ok { // a becomes most recent
		t.Fatal("a missing")
	}
	c.Set("d", 3) // evicts b
	if _, ok := c.Get("b"); ok {
		t.Fatal("b survived LRU eviction")
	}
	if got := fmt.Sprint(keys(c)); got != "[d a c]" {
		t.Fatalf("order %s", got)
	}
	c.Set("c", 9) // overwrite moves to front without eviction
	if got := fmt.Sprint(keys(c)); got != "[c d a]" {
		t.Fatalf("order after overwrite %s", got)
	}
	s := c.Stats()
	if s.Entries != 3 || s.Evictions != 1 || s.Hits != 1 || s.Misses != 1 || s.MaxEntries != 3 {
		t.Fatalf("stats %+v", s)
	}
}

func TestByteLimit(t *testing.T) {
	c := mustNew(t, Options[string, []byte]{MaxEntries: 100, MaxBytes: 10,
		Size: func(k string, v []byte) int64 { return int64(len(v)) }})
	c.Set("a", make([]byte, 4))
	c.Set("b", make([]byte, 4))
	c.Set("c", make([]byte, 4)) // 12 > 10: evict a
	if s := c.Stats(); s.Bytes != 8 || s.Entries != 2 || s.Evictions != 1 {
		t.Fatalf("stats %+v", s)
	}
	if _, ok := c.Get("a"); ok {
		t.Fatal("a survived byte eviction")
	}
	if c.Set("huge", make([]byte, 11)) {
		t.Fatal("oversized value stored")
	}
	c.Set("b", make([]byte, 1))
	if c.Set("b", make([]byte, 11)) {
		t.Fatal("oversized overwrite stored")
	}
	if _, ok := c.Get("b"); ok {
		t.Fatal("stale value served after rejected overwrite")
	}
	if s := c.Stats(); s.Bytes != 4 || s.Entries != 1 {
		t.Fatalf("stats after rejection %+v", s)
	}
	c.Set("full", make([]byte, 10))
	if s := c.Stats(); s.Bytes != 10 || s.Entries != 1 {
		t.Fatalf("exact fit %+v", s)
	}
}

func TestTTLWithFakeClock(t *testing.T) {
	clock := newClock()
	c := mustNew(t, Options[string, int]{MaxEntries: 10, TTL: time.Minute, Now: clock.Now})
	c.Set("a", 1)
	clock.Advance(59 * time.Second)
	if v, ok := c.Get("a"); !ok || v != 1 {
		t.Fatal("entry expired early")
	}
	clock.Advance(time.Second)
	if _, ok := c.Get("a"); ok {
		t.Fatal("entry served at TTL")
	}
	if s := c.Stats(); s.Expirations != 1 || s.Entries != 0 || s.Hits != 1 || s.Misses != 1 {
		t.Fatalf("stats %+v", s)
	}
	// A hit does not extend the TTL.
	c.Set("b", 2)
	clock.Advance(30 * time.Second)
	c.Get("b")
	clock.Advance(30 * time.Second)
	if _, ok := c.Get("b"); ok {
		t.Fatal("hit extended TTL")
	}
}

func TestNegativeTTL(t *testing.T) {
	clock := newClock()
	c := mustNew(t, Options[string, string]{MaxEntries: 10, TTL: time.Hour, NegativeTTL: 10 * time.Second, Now: clock.Now})
	c.Set("found", "x")
	if !c.SetNegative("missing", "") {
		t.Fatal("negative entry rejected")
	}
	clock.Advance(9 * time.Second)
	if _, ok := c.Get("missing"); !ok {
		t.Fatal("negative entry expired early")
	}
	clock.Advance(time.Second)
	if _, ok := c.Get("missing"); ok {
		t.Fatal("negative entry outlived NegativeTTL")
	}
	if _, ok := c.Get("found"); !ok {
		t.Fatal("positive entry used negative TTL")
	}

	disabled := mustNew(t, Options[string, string]{MaxEntries: 10, Now: clock.Now})
	disabled.Set("k", "old")
	if disabled.SetNegative("k", "") {
		t.Fatal("negative caching not disabled")
	}
	if _, ok := disabled.Get("k"); ok {
		t.Fatal("stale positive entry survived disabled negative set")
	}
	disabled.Set("forever", "v")
	clock.Advance(1000 * time.Hour)
	if _, ok := disabled.Get("forever"); !ok {
		t.Fatal("zero TTL expired")
	}
}

func TestShrink(t *testing.T) {
	clock := newClock()
	c := mustNew(t, Options[int, int]{MaxEntries: 100, TTL: time.Minute, Now: clock.Now})
	c.Set(0, 0)
	c.Set(1, 1)
	clock.Advance(2 * time.Minute) // 0 and 1 expire
	for i := 2; i < 12; i++ {
		c.Set(i, i)
	}
	c.Get(2) // 2 becomes most recent
	// 12 entries: ceil(12*0.5)=6 removed in total, expired first, then LRU.
	if removed := c.Shrink(0.5); removed != 6 {
		t.Fatalf("removed %d", removed)
	}
	if got := fmt.Sprint(keys(c)); got != "[2 11 10 9 8 7]" {
		t.Fatalf("kept %s", got)
	}
	if s := c.Stats(); s.Expirations != 2 || s.Evictions != 4 {
		t.Fatalf("stats %+v", s)
	}
	if removed := c.Shrink(0); removed != 0 {
		t.Fatal("zero fraction removed live entries")
	}
	if removed := c.Shrink(math.NaN()); removed != 0 {
		t.Fatal("NaN fraction removed live entries")
	}
	if removed := c.Shrink(5); removed != 6 || c.Stats().Entries != 0 {
		t.Fatal("fraction above 1 did not empty the cache")
	}
}

func TestDeletePurgeAndClone(t *testing.T) {
	c := mustNew(t, Options[string, []byte]{MaxEntries: 10, MaxBytes: 100,
		Size:  func(_ string, v []byte) int64 { return int64(cap(v)) },
		Clone: func(v []byte) []byte { return append([]byte(nil), v...) }})
	in := []byte("abc")
	c.Set("k", in)
	in[0] = 'X' // caller keeps mutating its own slice
	out, _ := c.Get("k")
	out[1] = 'Y' // one reader mutates its copy
	again, _ := c.Get("k")
	if string(again) != "abc" {
		t.Fatalf("stored value shared with callers: %q", again)
	}
	c.Delete("k")
	if _, ok := c.Get("k"); ok || c.Stats().Bytes != 0 {
		t.Fatal("delete")
	}
	c.Set("a", []byte("1"))
	c.Set("b", []byte("2"))
	c.Purge()
	if s := c.Stats(); s.Entries != 0 || s.Bytes != 0 || s.Evictions != 0 {
		t.Fatalf("purge %+v", s)
	}
}

func TestConcurrentAccess(t *testing.T) {
	c := mustNew(t, Options[int, []byte]{MaxEntries: 64, MaxBytes: 64 * 8, TTL: time.Millisecond, NegativeTTL: time.Millisecond,
		Size: func(_ int, v []byte) int64 { return int64(len(v)) }})
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 2000; i++ {
				k := (g*7 + i) % 100
				switch i % 7 {
				case 0:
					c.SetNegative(k, nil)
				case 1:
					c.Delete(k)
				case 2:
					c.Shrink(0.1)
				case 3:
					_ = c.Stats()
				default:
					if _, ok := c.Get(k); !ok {
						c.Set(k, make([]byte, 1+i%16))
					}
				}
			}
		}()
	}
	wg.Wait()
	s := c.Stats()
	if s.Entries > 64 || s.Bytes > 64*8 || s.Bytes < 0 {
		t.Fatalf("bounds violated %+v", s)
	}
	var sum int64
	c.mu.Lock()
	for _, e := range c.entries {
		sum += e.Value.(*entry[int, []byte]).size
	}
	if len(c.entries) != c.order.Len() || sum != c.bytes {
		t.Fatalf("index/accounting drift: %d/%d entries, %d/%d bytes", len(c.entries), c.order.Len(), sum, c.bytes)
	}
	c.mu.Unlock()
}

func TestHitRatio(t *testing.T) {
	if (Stats{}).HitRatio() != 0 {
		t.Fatal("empty ratio")
	}
	if r := (Stats{Hits: 3, Misses: 1}).HitRatio(); r != 0.75 {
		t.Fatalf("ratio %v", r)
	}
}
