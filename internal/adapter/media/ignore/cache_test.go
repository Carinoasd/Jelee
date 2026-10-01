package ignoresource

import (
	"context"
	"crypto/sha256"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/platform/ignore"
)

func cacheTestProgram(t testing.TB, text string) *ignore.Program {
	t.Helper()
	p, err := ignore.Compile(context.Background(), []ignore.Source{{Path: ".jeleeignore", Text: []byte(text)}}, ignore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func cacheTestKey(value byte) [32]byte { return [32]byte{value} }

func TestCacheEntryLimitAndLRUDoNotMutateEvictedProgram(t *testing.T) {
	var cache programCache
	p := cacheTestProgram(t, "*.nfo")
	for i := range MaxCacheEntries {
		cache.put(cacheTestKey(byte(i)), p, 1)
	}
	if len(cache.entries) != MaxCacheEntries || cache.order.Len() != MaxCacheEntries || cache.weight != MaxCacheEntries {
		t.Fatal("exact entry capacity not retained")
	}
	// Touch the oldest entry. The next-oldest, not the just-used entry, must go.
	held, ok := cache.get(cacheTestKey(1))
	if !ok || held != p {
		t.Fatal("entry disappeared before capacity was exceeded")
	}
	if _, ok := cache.get(cacheTestKey(0)); !ok {
		t.Fatal("cannot touch oldest entry")
	}
	cache.put(cacheTestKey(MaxCacheEntries), cacheTestProgram(t, "*.mkv"), 1)
	if _, ok := cache.get(cacheTestKey(2)); ok {
		t.Fatal("least recently used entry survived overflow")
	}
	if _, ok := cache.get(cacheTestKey(0)); !ok || len(cache.entries) != MaxCacheEntries || cache.weight != MaxCacheEntries {
		t.Fatal("entry eviction changed capacity or discarded a recently used value")
	}
	// Keep a reference across a later eviction; LRU ownership must not change
	// the immutable matcher value used by an already admitted observation.
	for i := 65; i < 129; i++ {
		cache.put(cacheTestKey(byte(i)), p, 1)
	}
	if _, ok := cache.get(cacheTestKey(1)); ok {
		t.Fatal("test did not evict the held program's original entry")
	}
	if got, err := held.Evaluate(context.Background(), "movie.nfo", ignore.File); err != nil || got.Outcome != ignore.Exclude {
		t.Fatal("eviction mutated an in-use program", err)
	}
}

func TestCacheWeightBoundaryAndRejectedInsertPreserveAccounting(t *testing.T) {
	p := cacheTestProgram(t, "*.nfo")
	var cache programCache
	cache.put(cacheTestKey(1), p, MaxCacheWeight)
	if cache.weight != MaxCacheWeight || len(cache.entries) != 1 {
		t.Fatal("exact weight cap rejected")
	}
	cache.put(cacheTestKey(2), p, MaxCacheWeight+1)
	cache.put(cacheTestKey(3), p, -1)
	cache.put(cacheTestKey(4), nil, 1)
	if cache.weight != MaxCacheWeight || len(cache.entries) != 1 {
		t.Fatal("rejected inserts evicted a valid entry or changed weight")
	}
	cache.put(cacheTestKey(1), p, MaxCacheWeight)
	if cache.weight != MaxCacheWeight || len(cache.entries) != 1 {
		t.Fatal("replaying the same entry double-charged cache weight")
	}
	cache.put(cacheTestKey(5), p, 1)
	if _, ok := cache.get(cacheTestKey(1)); ok || cache.weight != 1 || len(cache.entries) != 1 {
		t.Fatal("weight overflow was not reclaimed exactly")
	}
	cache.put(cacheTestKey(6), p, MaxCacheWeight-1)
	if cache.weight != MaxCacheWeight || len(cache.entries) != 2 {
		t.Fatal("combined exact weight boundary rejected")
	}
	cache.put(cacheTestKey(7), p, 1)
	if cache.weight != MaxCacheWeight || len(cache.entries) != 2 {
		t.Fatal("combined weight plus one failed to evict just the old unit")
	}
	if _, ok := cache.get(cacheTestKey(5)); ok {
		t.Fatal("wrong entry evicted under weight pressure")
	}
}

func TestCacheIdentityAndObservationTokenSeparateMeaningfulChanges(t *testing.T) {
	root := fileIdentity{1}
	base := []directoryObservation{{path: ".", identity: root, source: sourceStamp{
		present: true,
		state:   fileState{identity: fileIdentity{2}, size: 4, modifiedUnixNano: 10, kind: nodeRegular},
		digest:  sha256.Sum256([]byte("*.x\n")),
	}}}
	key := programKey(root, base, ignore.CaseSensitive)
	token := observationToken(base, ignore.CaseSensitive)
	for name, mutate := range map[string]func([]directoryObservation){
		"source path":         func(v []directoryObservation) { v[0].path = "child" },
		"file identity":       func(v []directoryObservation) { v[0].source.state.identity[0]++ },
		"size":                func(v []directoryObservation) { v[0].source.state.size++ },
		"mtime":               func(v []directoryObservation) { v[0].source.state.modifiedUnixNano++ },
		"same stat new bytes": func(v []directoryObservation) { v[0].source.digest = sha256.Sum256([]byte("*.y\n")) },
		"deleted":             func(v []directoryObservation) { v[0].source.present = false },
	} {
		t.Run(name, func(t *testing.T) {
			changed := append([]directoryObservation(nil), base...)
			mutate(changed)
			if programKey(root, changed, ignore.CaseSensitive) == key || observationToken(changed, ignore.CaseSensitive) == token {
				t.Fatal("meaningful source change retained its old cache identity")
			}
		})
	}
	if programKey(fileIdentity{9}, base, ignore.CaseSensitive) == key || programKey(root, base, ignore.CaseASCIIInsensitive) == key || observationToken(base, ignore.CaseASCIIInsensitive) == token {
		t.Fatal("root or matching policy was omitted from identity")
	}
	// An absent descendant adds no rules, so the compiled Program can be
	// reused. It still changes the observation token and must be re-observed.
	more := append(append([]directoryObservation(nil), base...), directoryObservation{path: "child", identity: fileIdentity{3}})
	if programKey(root, more, ignore.CaseSensitive) != key || observationToken(more, ignore.CaseSensitive) == token {
		t.Fatal("absence was conflated with compiled rules or lost from provenance")
	}
	before := observationToken(more, ignore.CaseSensitive)
	more[1].identity = fileIdentity{4}
	if observationToken(more, ignore.CaseSensitive) == before {
		t.Fatal("replacement of an absent source's parent kept the old observation")
	}
}
