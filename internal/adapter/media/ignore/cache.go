package ignoresource

import (
	"container/list"
	"crypto/sha256"
	"encoding/binary"
	"hash"

	"github.com/MoYuanCN/Jelee/internal/platform/ignore"
)

type cachedProgram struct {
	key     [32]byte
	program *ignore.Program
	weight  int64
}

// Callers serialize shared access. A call's pending cache has the same bounds,
// but is private and never published until all observation checks succeed.
type programCache struct {
	entries map[[32]byte]*list.Element
	order   list.List
	weight  int64
}

func (c *programCache) get(key [32]byte) (*ignore.Program, bool) {
	e := c.entries[key]
	if e == nil {
		return nil, false
	}
	c.order.MoveToBack(e)
	return e.Value.(cachedProgram).program, true
}

func (c *programCache) put(key [32]byte, program *ignore.Program, weight int64) {
	if program == nil || weight < 0 || weight > MaxCacheWeight {
		return
	}
	if e := c.entries[key]; e != nil {
		c.order.MoveToBack(e)
		return
	}
	if c.entries == nil {
		c.entries = make(map[[32]byte]*list.Element)
	}
	for len(c.entries) >= MaxCacheEntries || c.weight+weight > MaxCacheWeight {
		e := c.order.Front()
		v := e.Value.(cachedProgram)
		delete(c.entries, v.key)
		c.weight -= v.weight
		c.order.Remove(e)
	}
	c.entries[key] = c.order.PushBack(cachedProgram{key, program, weight})
	c.weight += weight
}

func (r *Resolver) lookup(key [32]byte) (*ignore.Program, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cache.get(key)
}

func cacheWeight(sources []ignore.Source) int64 {
	weight := int64(4096)
	for _, source := range sources {
		weight += 2048 + 2*int64(len(source.Path)) + 64*int64(len(source.Text))
	}
	return weight
}

type sourceStamp struct {
	state   fileState
	digest  [32]byte
	present bool
}
type directoryObservation struct {
	path     string
	identity fileIdentity
	source   sourceStamp
}

// Length prefixes and fixed-width integers prevent ambiguous key framing.
func hashString(h hash.Hash, value string) {
	hashUint(h, uint64(len(value)))
	_, _ = h.Write([]byte(value))
}
func hashUint(h hash.Hash, value uint64) {
	var encoded [8]byte
	binary.LittleEndian.PutUint64(encoded[:], value)
	_, _ = h.Write(encoded[:])
}
func hashStamp(h hash.Hash, stamp sourceStamp) {
	_, _ = h.Write(stamp.state.identity[:])
	hashUint(h, uint64(stamp.state.size))
	hashUint(h, uint64(stamp.state.modifiedUnixNano))
	_, _ = h.Write(stamp.digest[:])
}
func programKey(root fileIdentity, chain []directoryObservation, mode ignore.CaseMode) [32]byte {
	h := sha256.New()
	hashString(h, ignore.ProgramVersion)
	hashUint(h, uint64(mode))
	_, _ = h.Write(root[:])
	for _, directory := range chain {
		if directory.source.present {
			hashString(h, directory.path)
			hashStamp(h, directory.source)
		}
	}
	var key [32]byte
	copy(key[:], h.Sum(nil))
	return key
}
func observationToken(chain []directoryObservation, mode ignore.CaseMode) [32]byte {
	h := sha256.New()
	hashString(h, "ignore-source-observation-v1")
	hashString(h, ignore.ProgramVersion)
	hashUint(h, uint64(mode))
	for _, directory := range chain {
		hashString(h, directory.path)
		_, _ = h.Write(directory.identity[:])
		if directory.source.present {
			hashUint(h, 1)
			hashStamp(h, directory.source)
		} else {
			hashUint(h, 0)
		}
	}
	var token [32]byte
	copy(token[:], h.Sum(nil))
	return token
}
