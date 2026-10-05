package images

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// fakeVariantIndex mirrors the PostgreSQL index semantics: touch reports an
// unknown entry, deletion is conditional on the listed access time.
type fakeVariantIndex struct {
	mu                    sync.Mutex
	rows                  map[[64]byte]domain.ImageVariant
	clock                 time.Time
	puts, touches, misses int
	// touchOnDelete simulates a request using an entry after it was listed.
	touchOnDelete map[[64]byte]bool
}

func newFakeVariantIndex() *fakeVariantIndex {
	return &fakeVariantIndex{rows: map[[64]byte]domain.ImageVariant{}, clock: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), touchOnDelete: map[[64]byte]bool{}}
}

func fakeVariantID(content, key [32]byte) (id [64]byte) {
	copy(id[:32], content[:])
	copy(id[32:], key[:])
	return id
}

func (f *fakeVariantIndex) tick() time.Time {
	f.clock = f.clock.Add(time.Minute)
	return f.clock
}

func (f *fakeVariantIndex) PutImageVariant(_ context.Context, content, key [32]byte, size int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.puts++
	now := f.tick()
	f.rows[fakeVariantID(content, key)] = domain.ImageVariant{ContentSHA256: content, VariantKey: key, Bytes: size, CreatedAt: now, LastAccess: now}
	return nil
}

func (f *fakeVariantIndex) TouchImageVariant(_ context.Context, content, key [32]byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.touches++
	row, ok := f.rows[fakeVariantID(content, key)]
	if !ok {
		f.misses++
		return domain.ErrNotFound
	}
	row.LastAccess = f.tick()
	f.rows[fakeVariantID(content, key)] = row
	return nil
}

func (f *fakeVariantIndex) ListOldestImageVariants(_ context.Context, limit int) ([]domain.ImageVariant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	values := make([]domain.ImageVariant, 0, len(f.rows))
	for _, row := range f.rows {
		values = append(values, row)
	}
	slices.SortFunc(values, func(a, b domain.ImageVariant) int { return a.LastAccess.Compare(b.LastAccess) })
	return values[:min(limit, len(values))], nil
}

func (f *fakeVariantIndex) DeleteImageVariant(_ context.Context, value domain.ImageVariant) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := fakeVariantID(value.ContentSHA256, value.VariantKey)
	if f.touchOnDelete[id] {
		delete(f.touchOnDelete, id)
		row := f.rows[id]
		row.LastAccess = f.tick()
		f.rows[id] = row
	}
	row, ok := f.rows[id]
	if !ok || !row.LastAccess.Equal(value.LastAccess) {
		return false, nil
	}
	delete(f.rows, id)
	return true, nil
}

func (f *fakeVariantIndex) len() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.rows)
}

func assetTestJPEG(t *testing.T, path string, width, height int, shade uint8) [32]byte {
	t.Helper()
	picture := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			picture.Set(x, y, color.RGBA{R: shade, G: uint8(x), B: uint8(y), A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, picture, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal("encode asset fixture")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal("create asset fixture directory")
	}
	if err := os.WriteFile(path, encoded.Bytes(), 0600); err != nil {
		t.Fatal("write asset fixture")
	}
	return sha256.Sum256(encoded.Bytes())
}

func assetTestRow(source domain.LocalImageSource, kind, imageType string, index int, relative string) domain.ItemImage {
	return domain.ItemImage{ID: "10000000-0000-4000-8000-0000000000a1", ItemID: source.ItemID, LibraryID: source.LibraryID, Type: imageType,
		Index: index, SourceKind: kind, RootID: source.RootID, RootPath: source.RootPath, RelativePath: relative}
}

func assetTestRead(t *testing.T, result interface {
	io.Reader
	io.Closer
}) []byte {
	t.Helper()
	defer result.Close()
	data, err := io.ReadAll(result)
	if err != nil {
		t.Fatal("read rendered asset", err)
	}
	return data
}

func assetTestStore(t *testing.T, source domain.LocalImageSource, variantBytes int64) (*Store, string) {
	t.Helper()
	root := filepath.Join(filepath.Dir(source.RootPath), "store")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal("create store fixture")
	}
	return openTestStore(t, StoreOptions{Root: root, MediaRoots: []string{source.RootPath}, OriginalBytes: 64 << 20, VariantBytes: variantBytes, MaxEntries: 1024}), root
}

func TestImageProcessorRendersItemImageFiles(t *testing.T) {
	source, scratch := imageSourceFixture(t)
	p := processorTestNew(t, processorTestOptions(scratch))
	for _, test := range []struct{ kind, imageType, path string }{
		{domain.ImageSourceLocal, "Backdrop", "movie/fanart2.jpg"},
		{domain.ImageSourceNFO, "Logo", "movie/extras/clearlogo.jpeg"},
	} {
		original := assetTestJPEG(t, filepath.Join(source.RootPath, filepath.FromSlash(test.path)), 96, 48, 40)
		row := assetTestRow(source, test.kind, test.imageType, 2, test.path)
		if test.imageType != "Backdrop" {
			row.Index = 0
		}
		request := domain.ImageRequest{Type: test.imageType, Index: row.Index, Width: 32}
		result, err := p.RenderItemImage(context.Background(), row, request)
		if err != nil {
			t.Fatal("render asset", test.kind, err)
		}
		data := assetTestRead(t, result.Body)
		decoded, err := jpeg.Decode(bytes.NewReader(data))
		if err != nil || decoded.Bounds().Dx() != 32 || decoded.Bounds().Dy() != 16 || result.ContentSHA256 != original {
			t.Fatal("asset representation or content tag", test.kind, err)
		}
		if after, err := os.ReadFile(filepath.Join(source.RootPath, filepath.FromSlash(test.path))); err != nil || sha256.Sum256(after) != original {
			t.Fatal("original changed")
		}
		// The request names a different slot than the row.
		if _, err := p.RenderItemImage(context.Background(), row, domain.ImageRequest{Type: "Primary"}); err != domain.ErrInvalid {
			t.Fatal("slot mismatch accepted", err)
		}
	}
	missing := assetTestRow(source, domain.ImageSourceLocal, "Primary", 0, "movie/missing.jpg")
	if _, err := p.RenderItemImage(context.Background(), missing, domain.ImageRequest{}); !errors.Is(err, domain.ErrImageUnavailable) {
		t.Fatal("missing file", err)
	}
	for _, path := range []string{"../escape.jpg", "/abs.jpg", "movie/poster.txt", "movie\\poster.jpg"} {
		row := assetTestRow(source, domain.ImageSourceLocal, "Primary", 0, path)
		if _, err := p.RenderItemImage(context.Background(), row, domain.ImageRequest{}); err != domain.ErrNotFound {
			t.Fatal("unsafe asset path reached the filesystem", path, err)
		}
	}
	imageScratchEmpty(t, scratch)
}

func TestImageProcessorRemoteAndEmbeddedRowsNeverFetch(t *testing.T) {
	source, scratch := imageSourceFixture(t)
	encoded := filepath.Join(filepath.Dir(source.RootPath), "remote.jpg")
	digest := assetTestJPEG(t, encoded, 64, 64, 200)
	remote := domain.ItemImage{ID: "10000000-0000-4000-8000-0000000000b1", ItemID: source.ItemID, LibraryID: source.LibraryID, Type: "Primary",
		SourceKind: domain.ImageSourceRemote, RemoteURL: "https://image.invalid/poster.jpg"}
	embedded := assetTestRow(source, domain.ImageSourceEmbedded, "Primary", 0, source.MediaPath)
	// Without a store, and for rows whose bytes were never fetched, nothing
	// is rendered and nothing is admitted: the request path has no fetcher.
	plain := processorTestNew(t, processorTestOptions(scratch))
	withContent := remote
	withContent.Content = &domain.ItemImageContent{SHA256: digest[:], Format: "jpeg", Bytes: 1, FetchedAt: time.Now()}
	for _, row := range []domain.ItemImage{remote, withContent, embedded} {
		if _, err := plain.RenderItemImage(context.Background(), row, domain.ImageRequest{}); err != domain.ErrNotFound {
			t.Fatal("unstored row rendered without a store", row.SourceKind, err)
		}
	}
	if plain.Stats().Admitted != 0 {
		t.Fatal("unstored rows were admitted")
	}
	store, _ := assetTestStore(t, source, 16<<20)
	options := processorTestOptions(scratch)
	options.Store = store
	p := processorTestNew(t, options)
	for _, row := range []domain.ItemImage{remote, withContent, embedded} {
		if _, err := p.RenderItemImage(context.Background(), row, domain.ImageRequest{}); err != domain.ErrNotFound {
			t.Fatal("row without stored bytes rendered", row.SourceKind, err)
		}
	}
	file, err := os.Open(encoded)
	if err != nil {
		t.Fatal(err)
	}
	stored, _, err := store.PutOriginal(context.Background(), file, 1<<20)
	_ = file.Close()
	if err != nil || stored != digest {
		t.Fatal("seed stored original", err)
	}
	embedded.Content = withContent.Content
	for _, row := range []domain.ItemImage{withContent, embedded} {
		result, err := p.RenderItemImage(context.Background(), row, domain.ImageRequest{Width: 16})
		if err != nil || result.ContentSHA256 != digest || result.Width != 16 || result.Height != 16 {
			t.Fatal("stored original was not served", row.SourceKind, err)
		}
		_ = result.Body.Close()
	}
	imageScratchEmpty(t, scratch)
}

func TestImageProcessorPersistentStoreAndIndex(t *testing.T) {
	source, scratch := imageSourceFixture(t)
	original := assetTestJPEG(t, filepath.Join(source.RootPath, "movie", "Film-poster.jpg"), 80, 40, 90)
	store, storeRoot := assetTestStore(t, source, 16<<20)
	index := newFakeVariantIndex()
	options := processorTestOptions(scratch)
	options.Store, options.Index = store, index
	request := domain.ImageRequest{Width: 40}
	render := func(p *Processor) ([]byte, string) {
		t.Helper()
		result, err := p.Render(context.Background(), source, request)
		if err != nil || result.ContentSHA256 != original {
			t.Fatal("render with store", err)
		}
		etag := result.ETag
		return assetTestRead(t, result.Body), etag
	}
	first := processorTestNew(t, options)
	data, etag := render(first)
	if !store.HasOriginal(original) || index.len() != 1 || index.puts != 1 || first.Stats().Decodes != 1 {
		t.Fatal("original, variant or index row was not written")
	}
	if _, entries, _, _ := store.VariantUsage(); entries != 1 {
		t.Fatal("variant not stored")
	}
	// A new processor has an empty memory cache: the store answers without
	// decoding, refreshes the index and returns identical bytes.
	second := processorTestNew(t, options)
	again, againETag := render(second)
	stats := second.Stats()
	if !bytes.Equal(data, again) || againETag != etag || stats.Decodes != 0 || stats.VariantHits != 1 || index.touches != 1 {
		t.Fatal("stored variant was not reused", stats)
	}
	// An index row lost to a failed write is added back on the next hit.
	clear(index.rows)
	third := processorTestNew(t, options)
	render(third)
	if index.len() != 1 || index.misses != 1 || third.Stats().Decodes != 0 {
		t.Fatal("lost index row was not restored")
	}
	// The original in the store is a byte-identical copy.
	object, err := store.OpenOriginal(context.Background(), original, true)
	if err != nil {
		t.Fatal(err)
	}
	if object.Size() <= 0 {
		t.Fatal("empty stored original")
	}
	_ = object.Close()
	// Without a store nothing persistent is involved.
	plain := processorTestNew(t, processorTestOptions(scratch))
	render(plain)
	if plain.Stats().VariantHits != 0 {
		t.Fatal("variant hit without a store")
	}
	if files := storeFiles(t, storeRoot); len(files) != 2 {
		t.Fatal("unexpected store objects", files)
	}
	imageScratchEmpty(t, scratch)
	// An index without a store is a configuration error.
	invalid := processorTestOptions(scratch)
	invalid.Index = index
	if p, err := New(context.Background(), invalid); p != nil || err != domain.ErrInvalid {
		t.Fatal("index without store accepted")
	}
}

func TestImageProcessorRejectsStoreInsideMediaRoot(t *testing.T) {
	source, scratch := imageSourceFixture(t)
	assetTestJPEG(t, filepath.Join(source.RootPath, "movie", "poster.jpg"), 32, 32, 10)
	inside := filepath.Join(source.RootPath, "jelee-store")
	if err := os.Mkdir(inside, 0700); err != nil {
		t.Fatal(err)
	}
	// The library root was added after the store opened.
	store := openTestStore(t, StoreOptions{Root: inside, OriginalBytes: 64 << 20, VariantBytes: 16 << 20, MaxEntries: 1024})
	options := processorTestOptions(scratch)
	options.Store = store
	p := processorTestNew(t, options)
	if _, err := p.Render(context.Background(), source, domain.ImageRequest{}); err != domain.ErrImageUnavailable {
		t.Fatal("store inside a library root was used", err)
	}
	if files := storeFiles(t, inside); len(files) != 0 {
		t.Fatal("store wrote inside a media root", files)
	}
}

func TestImageEvictVariantsFollowsIndex(t *testing.T) {
	source, _ := imageSourceFixture(t)
	store, storeRoot := assetTestStore(t, source, 16<<20)
	index := newFakeVariantIndex()
	ctx := context.Background()
	var content [32]byte
	content[0] = 1
	keys := make([][32]byte, 6)
	for i := range keys {
		keys[i] = StoreVariantKey("test", "jpeg", i, i, 85)
		if _, err := store.PutVariant(ctx, content, keys[i], bytes.NewReader(bytes.Repeat([]byte{byte(i)}, 100)), 100); err != nil {
			t.Fatal(err)
		}
		if err := index.PutImageVariant(ctx, content, keys[i], 100); err != nil {
			t.Fatal(err)
		}
	}
	// A stale row whose file the store already dropped, listed first.
	var stale [32]byte
	stale[0] = 9
	index.rows[fakeVariantID(content, stale)] = domain.ImageVariant{ContentSHA256: content, VariantKey: stale, Bytes: 1, LastAccess: index.clock.Add(-time.Hour)}
	// keys[1] is used between listing and deletion and must survive.
	index.touchOnDelete[fakeVariantID(content, keys[1])] = true
	_, entries, _, _ := store.VariantUsage()
	removed, err := EvictVariants(ctx, store, index, 1<<40, entries-3)
	if err != nil || removed != 4 {
		t.Fatal("eviction count", removed, err)
	}
	for i, key := range keys {
		_, inIndex := index.rows[fakeVariantID(content, key)]
		_, statErr := os.Stat(filepath.Join(storeRoot, storeVariantPath(store, content, key)))
		evicted := i == 0 || i == 2 || i == 3
		if inIndex == evicted || (statErr == nil) == evicted {
			t.Fatalf("variant %d index=%v file=%v evicted=%v", i, inIndex, statErr == nil, evicted)
		}
	}
	if _, ok := index.rows[fakeVariantID(content, stale)]; ok {
		t.Fatal("stale index row kept")
	}
	if _, entries, _, _ := store.VariantUsage(); entries != 3 || index.len() != 3 {
		t.Fatal("store and index disagree", entries, index.len())
	}
	if _, err := EvictVariants(ctx, nil, index, 0, 0); err != domain.ErrInvalid {
		t.Fatal("nil store accepted")
	}
	if n, err := EvictVariants(ctx, store, index, 0, 0); err != nil || n != 3 || index.len() != 0 {
		t.Fatal("full eviction", n, err)
	}
	if _, entries, _, _ := store.VariantUsage(); entries != 0 {
		t.Fatal("store kept evicted variants")
	}
}

func TestImageProcessorEvictsThroughIndexAboveHighWater(t *testing.T) {
	source, scratch := imageSourceFixture(t)
	assetTestJPEG(t, filepath.Join(source.RootPath, "movie", "Film-poster.jpg"), 64, 64, 30)
	root := filepath.Join(filepath.Dir(source.RootPath), "store")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	// Bounds far below the public minimum keep this test small.
	store := openTestStore(t, StoreOptions{Root: root, MediaRoots: []string{source.RootPath}, OriginalBytes: 64 << 20, VariantBytes: 1 << 20, MaxEntries: 10})
	index := newFakeVariantIndex()
	options := processorTestOptions(scratch)
	options.Store, options.Index = store, index
	p := processorTestNew(t, options)
	for width := 16; width < 26; width++ {
		result, err := p.Render(context.Background(), source, domain.ImageRequest{Width: width})
		if err != nil {
			t.Fatal(err)
		}
		_ = result.Body.Close()
	}
	deadline := time.Now().Add(10 * time.Second)
	for p.Stats().IndexEvictions == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	// Shutdown joins a pass that may still be between a row and its file.
	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Eviction ran from the database index down to the low mark, and every
	// remaining variant is still indexed.
	_, entries, _, _ := store.VariantUsage()
	if p.Stats().IndexEvictions == 0 || entries > 9 || index.len() != entries {
		t.Fatal("index-driven eviction", p.Stats().IndexEvictions, entries, index.len())
	}
	index.mu.Lock()
	defer index.mu.Unlock()
	for _, row := range index.rows {
		if _, err := os.Stat(filepath.Join(root, storeVariantPath(store, row.ContentSHA256, row.VariantKey))); err != nil {
			t.Fatal("index names a removed variant")
		}
	}
}
