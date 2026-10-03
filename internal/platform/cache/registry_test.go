package cache

import (
	"sync"
	"testing"
)

func TestRegistryShrinkAndSnapshot(t *testing.T) {
	r := NewRegistry()
	a := mustNew(t, Options[int, int]{MaxEntries: 10})
	b := mustNew(t, Options[string, int]{MaxEntries: 10})
	for i := 0; i < 4; i++ {
		a.Set(i, i)
		b.Set(string(rune('a'+i)), i)
	}
	a.Get(0)
	a.Get(99)
	if err := r.Register("zeta", a); err != nil {
		t.Fatal(err)
	}
	if err := r.Register("alpha", b); err != nil {
		t.Fatal(err)
	}
	snap := r.Snapshot()
	if len(snap) != 2 || snap[0].Name != "alpha" || snap[1].Name != "zeta" || snap[1].Stats.Hits != 1 || snap[1].Stats.Misses != 1 {
		t.Fatalf("snapshot %+v", snap)
	}
	if removed := r.Shrink(0.5); removed != 4 {
		t.Fatalf("registry shrink removed %d", removed)
	}
	if a.Stats().Entries != 2 || b.Stats().Entries != 2 {
		t.Fatal("members not shrunk")
	}
	r.Purge()
	if a.Stats().Entries != 0 || b.Stats().Entries != 0 {
		t.Fatal("members not purged")
	}
}

func TestRegistryNamesAndReplacement(t *testing.T) {
	r := NewRegistry()
	a := mustNew(t, Options[int, int]{MaxEntries: 1})
	for _, bad := range []string{"", "Upper", "has space", "user:42", string(make([]byte, MaxNameLength+1))} {
		if err := r.Register(bad, a); err == nil {
			t.Errorf("name %q accepted", bad)
		}
	}
	if err := r.Register("ok", nil); err == nil {
		t.Fatal("nil member accepted")
	}
	b := mustNew(t, Options[int, int]{MaxEntries: 1})
	_ = r.Register("x.y", a)
	_ = r.Register("x.y", b)
	if snap := r.Snapshot(); len(snap) != 1 {
		t.Fatalf("replacement grew registry: %d", len(snap))
	}
	r.Unregister("x.y", a) // stale owner must not remove the replacement
	if len(r.Snapshot()) != 1 {
		t.Fatal("stale unregister removed replacement")
	}
	r.Unregister("x.y", b)
	if len(r.Snapshot()) != 0 {
		t.Fatal("unregister")
	}
	if Default() == nil || Default() != Default() {
		t.Fatal("default registry")
	}
}

func TestRegistryConcurrent(t *testing.T) {
	r := NewRegistry()
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		c := mustNew(t, Options[int, int]{MaxEntries: 8})
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				c.Set(i, i)
				_ = r.Register("shared", c)
				r.Shrink(0.25)
				_ = r.Snapshot()
			}
		}()
	}
	wg.Wait()
}
