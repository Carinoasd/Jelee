package images

import (
	"context"
	"image/color"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// G40.13: every resize request is exactly one of a memory cache hit, a
// stored variant served, or a generated variant, so the exposed counters
// give the hit rate. Repeated requests must hit and never generate again,
// and a restarted processor on the same store must serve from the store.
func TestImageVariantCountersShowRepeatedRequestsHit(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		t.Skip("private store directories are supported on Linux and Windows only")
	}
	source, scratch := imageSourceFixture(t)
	processorTestPNG(t, source, color.NRGBA{G: 200, A: 255})
	root := filepath.Join(imageSourceTestBase(t), "store")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal("create store root")
	}
	store := openTestStore(t, StoreOptions{Root: root, MediaRoots: []string{source.RootPath}, OriginalBytes: 64 << 20, VariantBytes: 16 << 20, MaxEntries: 1024})
	options := processorTestOptions(scratch)
	options.Store = store
	render := func(p *Processor, request domain.ImageRequest) string {
		t.Helper()
		result, err := p.Render(context.Background(), source, request)
		if err != nil {
			t.Fatal("render", err)
		}
		defer func() { _ = result.Body.Close() }()
		if _, err := io.Copy(io.Discard, result.Body); err != nil {
			t.Fatal("read rendered body", err)
		}
		return result.ETag
	}
	type counts struct{ hits, misses, served, generated uint64 }
	read := func(p *Processor) counts {
		s := p.Stats()
		return counts{s.CacheHits, s.CacheMisses, s.VariantHits, s.Generated}
	}
	small, large := domain.ImageRequest{Width: 16, Height: 16}, domain.ImageRequest{Width: 32, Height: 32}

	first := processorTestNew(t, options)
	tag := render(first, small)
	if got := read(first); got != (counts{misses: 1, generated: 1}) {
		t.Fatalf("cold request: %+v", got)
	}
	for range 3 {
		if render(first, small) != tag {
			t.Fatal("repeated request changed the representation")
		}
	}
	render(first, large)
	if got := read(first); got != (counts{hits: 3, misses: 2, generated: 2}) {
		t.Fatalf("repeated requests did not hit the memory cache: %+v", got)
	}
	if s := first.Stats(); s.Decodes != s.Generated || s.Completed != 5 {
		t.Fatalf("lookups are not partitioned: %+v", s)
	}

	// A new process (empty memory cache) on the same store serves the stored
	// variant without decoding, then hits memory.
	second := processorTestNew(t, options)
	for range 2 {
		if render(second, small) != tag {
			t.Fatal("stored variant differs from the generated one")
		}
	}
	if got := read(second); got != (counts{hits: 1, misses: 1, served: 1}) {
		t.Fatalf("restarted processor did not use the stored variant: %+v", got)
	}
	if second.Stats().Decodes != 0 {
		t.Fatal("stored variant was decoded again")
	}
}
