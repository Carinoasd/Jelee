package images

import (
	"bytes"
	"sync"
	"testing"
	"time"
)

func cacheTestImage(size int, value byte) encodedImage {
	data := make([]byte, size)
	for index := range data {
		data[index] = value
	}
	return encodedImage{data: data, width: 1, height: 1}
}

func TestImageCacheLRUAndIndependentByteBound(t *testing.T) {
	for _, limits := range []struct {
		name    string
		bytes   int64
		entries int
	}{
		{"entries", 100, 2}, {"bytes", 6, 10},
	} {
		t.Run(limits.name, func(t *testing.T) {
			cache := newImageCache(limits.bytes, limits.entries, time.Minute)
			a, b, c := [32]byte{1}, [32]byte{2}, [32]byte{3}
			cache.put(a, cacheTestImage(3, 1))
			cache.put(b, cacheTestImage(3, 2))
			held, ok := cache.get(a)
			if !ok {
				t.Fatal("first cached image missing")
			}
			cache.put(c, cacheTestImage(3, 3))
			if _, ok := cache.get(b); ok {
				t.Fatal("least recently used image was retained")
			}
			if _, ok := cache.get(a); !ok {
				t.Fatal("cache hit did not update recency")
			}
			cache.put(a, cacheTestImage(3, 4))
			if !bytes.Equal(held.data, []byte{1, 1, 1}) {
				t.Fatal("replacement changed a held immutable image")
			}
			entries, size, _ := cache.stats()
			if entries != 2 || size != 6 {
				t.Fatal("cache did not account for encoded capacity")
			}
		})
	}
}

func TestImageCacheTTLDoesNotExtendOnHit(t *testing.T) {
	cache := newImageCache(100, 10, time.Second)
	now := time.Unix(123, 0)
	cache.now = func() time.Time { return now }
	a, b := [32]byte{1}, [32]byte{2}
	cache.put(a, cacheTestImage(3, 1))
	now = now.Add(500 * time.Millisecond)
	cache.put(b, cacheTestImage(3, 2))
	if _, ok := cache.get(a); !ok {
		t.Fatal("premature expiry")
	}
	now = now.Add(500 * time.Millisecond)
	if _, ok := cache.get(a); ok {
		t.Fatal("exact TTL boundary did not expire")
	}
	if _, ok := cache.get(b); !ok {
		t.Fatal("later entry expired with older entry")
	}
	entries, size, _ := cache.stats()
	if entries != 1 || size != 3 {
		t.Fatal("expiry did not free accounting")
	}
}

func TestImageCacheRejectsOversizedOrUntightenedCapacity(t *testing.T) {
	cache := newImageCache(4, 1, time.Minute)
	for _, value := range []encodedImage{{}, {data: make([]byte, 1, 4)}, cacheTestImage(5, 0)} {
		cache.put([32]byte{1}, value)
		if entries, size, _ := cache.stats(); entries != 0 || size != 0 {
			t.Fatal("invalid encoded allocation entered cache")
		}
	}
	cache.put([32]byte{1}, cacheTestImage(4, 1))
	if _, ok := cache.get([32]byte{1}); !ok {
		t.Fatal("exact byte bound rejected")
	}
}

func TestImageCacheShutdownPreservesHeldValueAndRejectsNewEntries(t *testing.T) {
	cache := newImageCache(100, 2, time.Minute)
	key := [32]byte{1}
	cache.put(key, cacheTestImage(3, 7))
	held, _ := cache.get(key)
	cache.shutdown()
	cache.shutdown()
	cache.put(key, cacheTestImage(3, 9))
	if _, ok := cache.get(key); ok {
		t.Fatal("closed cache accepted an entry")
	}
	if entries, size, _ := cache.stats(); entries != 0 || size != 0 {
		t.Fatal("shutdown retained cache accounting")
	}
	if !bytes.Equal(held.data, []byte{7, 7, 7}) {
		t.Fatal("shutdown changed a held image")
	}
}

func TestImageCacheConcurrentAccessStaysBounded(t *testing.T) {
	cache := newImageCache(64, 8, time.Minute)
	var group sync.WaitGroup
	for worker := range 8 {
		group.Go(func() {
			for index := range 100 {
				key := [32]byte{byte(worker), byte(index % 16)}
				cache.put(key, cacheTestImage(8, byte(index)))
				cache.get(key)
				entries, size, _ := cache.stats()
				if entries > 8 || size > 64 {
					t.Error("concurrent cache exceeded budget")
				}
			}
		})
	}
	group.Wait()
}
