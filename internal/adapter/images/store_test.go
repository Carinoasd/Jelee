package images

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type storeFixture struct {
	base, media, root string
}

func newStoreFixture(t *testing.T) storeFixture {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		t.Skip("private store directories are supported on Linux and Windows only")
	}
	base := imageSourceTestBase(t)
	fixture := storeFixture{base: base, media: filepath.Join(base, "media"), root: filepath.Join(base, "store")}
	for _, directory := range []string{fixture.media, fixture.root} {
		if err := os.Mkdir(directory, 0700); err != nil {
			t.Fatal("create store fixture")
		}
	}
	return fixture
}

func (f storeFixture) options(original, variant int64, entries int) StoreOptions {
	return StoreOptions{Root: f.root, MediaRoots: []string{f.media}, OriginalBytes: original, VariantBytes: variant, MaxEntries: entries}
}

func openTestStore(t *testing.T, options StoreOptions) *Store {
	t.Helper()
	store, err := openStore(context.Background(), options)
	if err != nil {
		t.Fatal("open store", err)
	}
	t.Cleanup(func() { _ = store.Close(context.Background()) })
	return store
}

// storeFiles lists every regular file below root, relative and slash-separated.
func storeFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			relative, _ := filepath.Rel(root, name)
			files = append(files, filepath.ToSlash(relative))
		}
		return nil
	})
	if err != nil {
		t.Fatal("walk store", err)
	}
	slices.Sort(files)
	return files
}

func storeRead(t *testing.T, object *StoreObject) []byte {
	t.Helper()
	data, err := io.ReadAll(object)
	if err != nil {
		t.Fatal("read stored object", err)
	}
	if int64(len(data)) != object.Size() {
		t.Fatal("stored object size mismatch")
	}
	if err := object.Close(); err != nil {
		t.Fatal("close stored object", err)
	}
	return data
}

func storeVariantPath(source, variant [32]byte) string {
	return "variants/" + hex.EncodeToString(source[:]) + "/" + hex.EncodeToString(variant[:])
}

func TestStoreOriginalsAreContentAddressedAndDeduplicated(t *testing.T) {
	fixture := newStoreFixture(t)
	store := openTestStore(t, fixture.options(1<<20, 1<<20, 16))
	content := bytes.Repeat([]byte("poster"), 1000)
	digest, size, err := store.PutOriginal(context.Background(), bytes.NewReader(content), 1<<20)
	if err != nil || size != int64(len(content)) || digest != sha256.Sum256(content) {
		t.Fatal("original put", err, size)
	}
	again, _, err := store.PutOriginal(context.Background(), bytes.NewReader(content), 1<<20)
	if err != nil || again != digest {
		t.Fatal("duplicate original", err)
	}
	name := hex.EncodeToString(digest[:])
	if files := storeFiles(t, fixture.root); !slices.Equal(files, []string{"originals/" + name[:2] + "/" + name}) {
		t.Fatal("unexpected store layout", files)
	}
	info, err := os.Stat(filepath.Join(fixture.root, "originals", name[:2], name))
	if err != nil || info.Mode().Perm() != 0600 && runtime.GOOS == "linux" {
		t.Fatal("original is not private", err)
	}
	object, err := store.OpenOriginal(context.Background(), digest, true)
	if err != nil || !bytes.Equal(storeRead(t, object), content) {
		t.Fatal("original round trip", err)
	}
	stats := store.Stats()
	if stats.Deduplicated != 1 || stats.OriginalEntries != 1 || stats.OriginalBytes != int64(len(content)) || stats.OriginalHits != 1 {
		t.Fatalf("original stats %+v", stats)
	}
	if _, err := store.OpenOriginal(context.Background(), [32]byte{1}, false); !errors.Is(err, domain.ErrNotFound) || store.Stats().OriginalMisses != 1 {
		t.Fatal("absent original was not a miss", err)
	}
}

func TestStoreVariantsRoundTripAndKeysBindParameters(t *testing.T) {
	fixture := newStoreFixture(t)
	store := openTestStore(t, fixture.options(1<<20, 1<<20, 16))
	source := sha256.Sum256([]byte("source"))
	small, large := StoreVariantKey("primary-v1", "jpeg", 320, 480, 85), StoreVariantKey("primary-v1", "jpeg", 640, 960, 85)
	for _, other := range [][32]byte{StoreVariantKey("primary-v1", "webp", 320, 480, 85), StoreVariantKey("primary-v1", "jpeg", 320, 480, 90), StoreVariantKey("primary-v2", "jpeg", 320, 480, 85)} {
		if other == small {
			t.Fatal("variant key ignores a parameter")
		}
	}
	if size, err := store.PutVariant(context.Background(), source, small, bytes.NewReader([]byte("small")), 1024); err != nil || size != 5 {
		t.Fatal("variant put", err)
	}
	if _, err := store.PutVariant(context.Background(), source, large, bytes.NewReader([]byte("large!")), 1024); err != nil {
		t.Fatal("second variant put", err)
	}
	object, err := store.OpenVariant(context.Background(), source, small)
	if err != nil || !bytes.Equal(storeRead(t, object), []byte("small")) {
		t.Fatal("variant round trip", err)
	}
	files := storeFiles(t, fixture.root)
	if !slices.Equal(files, []string{storeVariantPath(source, large), storeVariantPath(source, small)}) && !slices.Equal(files, []string{storeVariantPath(source, small), storeVariantPath(source, large)}) {
		t.Fatal("unexpected variant layout", files)
	}
	if store.Stats().VariantBytes != 2*storeHeaderBytes+11 {
		t.Fatal("variant bytes include the header", store.Stats())
	}
}

type failingReader struct {
	data []byte
	err  error
	read func()
}

func (r *failingReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, r.err
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	if r.read != nil {
		r.read()
	}
	return n, nil
}

func TestStoreInterruptedWritesLeaveNoPartialObject(t *testing.T) {
	fixture := newStoreFixture(t)
	store := openTestStore(t, fixture.options(1<<20, 1<<20, 16))
	source := sha256.Sum256([]byte("source"))
	key := StoreVariantKey("primary-v1", "jpeg", 1, 1, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cases := map[string]struct {
		want error
		run  func() error
	}{
		"reader error": {domain.ErrImageUnavailable, func() error {
			_, err := store.PutVariant(context.Background(), source, key, &failingReader{data: make([]byte, 64<<10), err: io.ErrUnexpectedEOF}, 1<<19)
			return err
		}},
		"cancel during copy": {context.Canceled, func() error {
			reader := &failingReader{data: make([]byte, 128<<10), err: io.EOF, read: cancel}
			_, err := store.PutVariant(ctx, source, key, reader, 1<<19)
			return err
		}},
		"over limit": {domain.ErrImageTooLarge, func() error {
			_, _, err := store.PutOriginal(context.Background(), bytes.NewReader(make([]byte, 2049)), 2048)
			return err
		}},
		"empty": {domain.ErrInvalid, func() error {
			_, _, err := store.PutOriginal(context.Background(), bytes.NewReader(nil), 2048)
			return err
		}},
		"fsync failure": {domain.ErrImageUnavailable, func() error {
			store.syncFile = func(*os.File) error { return errors.New("injected fsync failure") }
			defer func() { store.syncFile = (*os.File).Sync }()
			_, _, err := store.PutOriginal(context.Background(), bytes.NewReader([]byte("never durable")), 2048)
			return err
		}},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			if err := test.run(); !errors.Is(err, test.want) {
				t.Fatal("interrupted write result", err)
			}
			if files := storeFiles(t, fixture.root); len(files) != 0 {
				t.Fatal("interrupted write left files", files)
			}
			if stats := store.Stats(); stats.OriginalEntries != 0 || stats.VariantEntries != 0 || stats.OriginalBytes != 0 || stats.VariantBytes != 0 {
				t.Fatalf("interrupted write was indexed %+v", stats)
			}
		})
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("cancellation case did not run")
	}
}

func TestStoreStartupRemovesOnlyOwnLeftovers(t *testing.T) {
	fixture := newStoreFixture(t)
	store := openTestStore(t, fixture.options(1<<20, 1<<20, 16))
	if err := store.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	source := sha256.Sum256([]byte("source"))
	sourceName := hex.EncodeToString(source[:])
	trash := filepath.Join(fixture.root, "tmp", "trash-"+sourceName[:32])
	if err := os.MkdirAll(filepath.Join(trash, sourceName), 0700); err != nil {
		t.Fatal(err)
	}
	owned := []string{filepath.Join("tmp", "put-"+sourceName[:32]+".partial"), filepath.Join("tmp", "trash-"+sourceName[:32], sourceName, sourceName)}
	foreign := []string{filepath.Join("tmp", "notes.txt"), filepath.Join("tmp", "put-not-hex.partial"), filepath.Join("originals", "operator.jpg"), filepath.Join("variants", "README")}
	for _, name := range append(slices.Clone(owned), foreign...) {
		if err := os.WriteFile(filepath.Join(fixture.root, name), []byte("half written"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	reopened := openTestStore(t, fixture.options(1<<20, 1<<20, 16))
	files := storeFiles(t, fixture.root)
	for _, name := range owned {
		if slices.Contains(files, filepath.ToSlash(name)) {
			t.Fatal("stale owned file kept", name)
		}
	}
	for _, name := range foreign {
		if !slices.Contains(files, filepath.ToSlash(name)) {
			t.Fatal("foreign file removed", name)
		}
	}
	if _, err := os.Stat(trash); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("detached tree kept", err)
	}
	if stats := reopened.Stats(); stats.Foreign < uint64(len(foreign)) || stats.OriginalEntries != 0 || stats.VariantEntries != 0 {
		t.Fatalf("foreign files were accounted as objects %+v", stats)
	}
}

func storeCorrupt(t *testing.T, name string, change func([]byte) []byte) {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, change(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestStoreCorruptObjectsMissAndRebuild(t *testing.T) {
	fixture := newStoreFixture(t)
	store := openTestStore(t, fixture.options(1<<20, 1<<20, 16))
	source := sha256.Sum256([]byte("source"))
	content := []byte("encoded variant bytes")
	for name, change := range map[string]func([]byte) []byte{
		"payload bit": func(data []byte) []byte { data[len(data)-1] ^= 1; return data },
		"truncated":   func(data []byte) []byte { return data[:len(data)-3] },
		"header":      func(data []byte) []byte { data[0] = 'X'; return data },
		"length":      func(data []byte) []byte { data[len(storeHeaderMagic)+7]++; return data },
	} {
		t.Run(name, func(t *testing.T) {
			key := StoreVariantKey("primary-v1", name, 1, 1, 1)
			if _, err := store.PutVariant(context.Background(), source, key, bytes.NewReader(content), 1024); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(fixture.root, filepath.FromSlash(storeVariantPath(source, key)))
			storeCorrupt(t, path, change)
			before := store.Stats().Corrupt
			if _, err := store.OpenVariant(context.Background(), source, key); !errors.Is(err, domain.ErrNotFound) {
				t.Fatal("corrupt variant served", err)
			}
			if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) || store.Stats().Corrupt != before+1 {
				t.Fatal("corrupt variant kept", err)
			}
			if _, err := store.PutVariant(context.Background(), source, key, bytes.NewReader(content), 1024); err != nil {
				t.Fatal("rebuild", err)
			}
			object, err := store.OpenVariant(context.Background(), source, key)
			if err != nil || !bytes.Equal(storeRead(t, object), content) {
				t.Fatal("rebuilt variant", err)
			}
		})
	}
	original := bytes.Repeat([]byte{7}, 4096)
	digest, _, err := store.PutOriginal(context.Background(), bytes.NewReader(original), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	name := hex.EncodeToString(digest[:])
	path := filepath.Join(fixture.root, "originals", name[:2], name)
	storeCorrupt(t, path, func(data []byte) []byte { data[10] ^= 1; return data })
	// Size-only checks cannot see a same-length change; verification can.
	object, err := store.OpenOriginal(context.Background(), digest, false)
	if err != nil {
		t.Fatal("size check rejected an intact length", err)
	}
	_ = object.Close()
	if _, err := store.OpenOriginal(context.Background(), digest, true); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("verified open served a corrupt original", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("corrupt original kept", err)
	}
	if _, _, err := store.PutOriginal(context.Background(), bytes.NewReader(original), 1<<20); err != nil {
		t.Fatal(err)
	}
	storeCorrupt(t, path, func(data []byte) []byte { return data[:100] })
	if _, err := store.OpenOriginal(context.Background(), digest, false); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("truncated original served", err)
	}
	// After a restart the index takes sizes from disk; the content hash still
	// catches a variant damaged while the process was down.
	key := StoreVariantKey("primary-v1", "restart", 1, 1, 1)
	if _, err := store.PutVariant(context.Background(), source, key, bytes.NewReader(content), 1024); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	storeCorrupt(t, filepath.Join(fixture.root, filepath.FromSlash(storeVariantPath(source, key))), func(data []byte) []byte { data[len(data)-1] ^= 1; return data })
	reopened := openTestStore(t, fixture.options(1<<20, 1<<20, 16))
	if _, err := reopened.OpenVariant(context.Background(), source, key); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("corrupt variant served after restart", err)
	}
}

type storeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *storeClock) advance() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(2 * storeTouchInterval)
	return c.now
}

func (c *storeClock) get() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func TestStoreEvictsLeastRecentlyUsedWithinBounds(t *testing.T) {
	fixture := newStoreFixture(t)
	object := storeHeaderBytes + 10
	store := openTestStore(t, fixture.options(1<<20, 3*object, 16))
	clock := &storeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	store.now = clock.get
	source := sha256.Sum256([]byte("source"))
	keys := [4][32]byte{}
	put := func(index int) {
		clock.advance()
		keys[index] = StoreVariantKey("primary-v1", "jpeg", index+1, 1, 85)
		if _, err := store.PutVariant(context.Background(), source, keys[index], bytes.NewReader(bytes.Repeat([]byte{byte(index)}, 10)), 10); err != nil {
			t.Fatal(err)
		}
	}
	put(0)
	put(1)
	put(2)
	clock.advance()
	held, err := store.OpenVariant(context.Background(), source, keys[0])
	if err != nil {
		t.Fatal(err)
	}
	put(3) // Key 1 is now least recently used.
	if _, err := store.OpenVariant(context.Background(), source, keys[1]); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("least recently used variant kept", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.root, filepath.FromSlash(storeVariantPath(source, keys[1])))); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("evicted file kept", err)
	}
	if !bytes.Equal(storeRead(t, held), bytes.Repeat([]byte{0}, 10)) {
		t.Fatal("held object changed")
	}
	stats := store.Stats()
	if stats.VariantEntries != 3 || stats.VariantBytes != 3*object || stats.Evictions != 1 {
		t.Fatalf("eviction accounting %+v", stats)
	}
	if _, err := store.PutVariant(context.Background(), source, keys[1], bytes.NewReader(make([]byte, 3*object)), 3*object); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("object larger than the class limit accepted", err)
	}
	// Recency survives restart through mtime: key 0 was touched after key 2.
	if err := store.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	reopened := openTestStore(t, fixture.options(1<<20, 2*object, 16))
	for index, kept := range []bool{true, false, false, true} {
		object, err := reopened.OpenVariant(context.Background(), source, keys[index])
		if kept != (err == nil) {
			t.Fatal("restart eviction order", index, err)
		}
		if err == nil {
			_ = object.Close()
		}
	}
}

func TestStoreEntryBoundIsIndependentOfBytes(t *testing.T) {
	fixture := newStoreFixture(t)
	store := openTestStore(t, fixture.options(1<<20, 1<<20, 2))
	var digests [][32]byte
	for index := range 3 {
		digest, _, err := store.PutOriginal(context.Background(), bytes.NewReader([]byte{byte(index)}), 16)
		if err != nil {
			t.Fatal(err)
		}
		digests = append(digests, digest)
	}
	if stats := store.Stats(); stats.OriginalEntries != 2 || stats.Evictions != 1 || len(storeFiles(t, fixture.root)) != 2 {
		t.Fatalf("entry bound %+v", stats)
	}
	if _, err := store.OpenOriginal(context.Background(), digests[0], true); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("oldest original kept", err)
	}
	if err := store.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	// A rebuild with a smaller bound keeps memory at the bound and removes
	// the oldest files instead of indexing the whole tree.
	reopened := openTestStore(t, fixture.options(1<<20, 1<<20, 1))
	if stats := reopened.Stats(); stats.OriginalEntries != 1 || len(storeFiles(t, fixture.root)) != 1 {
		t.Fatalf("rebuild bound %+v", stats)
	}
}

func TestStoreClearVariantsKeepsOriginalsAndRebuilds(t *testing.T) {
	fixture := newStoreFixture(t)
	store := openTestStore(t, fixture.options(1<<20, 1<<20, 16))
	digest, _, err := store.PutOriginal(context.Background(), bytes.NewReader([]byte("original")), 1024)
	if err != nil {
		t.Fatal(err)
	}
	key := StoreVariantKey("primary-v1", "jpeg", 1, 1, 85)
	if _, err := store.PutVariant(context.Background(), digest, key, bytes.NewReader([]byte("variant")), 1024); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(fixture.root, "variants", hex.EncodeToString(digest[:]), "operator-note")
	if err := os.WriteFile(foreign, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	held, err := store.OpenVariant(context.Background(), digest, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ClearVariants(context.Background()); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && !bytes.Equal(storeRead(t, held), []byte("variant")) {
		t.Fatal("held variant changed during clear")
	}
	if _, err := store.OpenVariant(context.Background(), digest, key); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("cleared variant served", err)
	}
	object, err := store.OpenOriginal(context.Background(), digest, true)
	if err != nil || !bytes.Equal(storeRead(t, object), []byte("original")) {
		t.Fatal("clear touched originals", err)
	}
	files := storeFiles(t, fixture.root)
	name := hex.EncodeToString(digest[:])
	if len(files) != 2 || files[0] != "originals/"+name[:2]+"/"+name || filepath.Base(files[1]) != "operator-note" {
		t.Fatal("clear removed a foreign file or kept a variant", files)
	}
	if stats := store.Stats(); stats.VariantEntries != 0 || stats.VariantBytes != 0 || stats.Cleared != 1 {
		t.Fatalf("clear accounting %+v", stats)
	}
	if _, err := store.PutVariant(context.Background(), digest, key, bytes.NewReader([]byte("variant")), 1024); err != nil {
		t.Fatal("rebuild after clear", err)
	}
	object, err = store.OpenVariant(context.Background(), digest, key)
	if err != nil || !bytes.Equal(storeRead(t, object), []byte("variant")) {
		t.Fatal("rebuilt variant", err)
	}
	// Removing the whole variants directory while stopped is also safe.
	if err := store.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(fixture.root, "variants")); err != nil {
		t.Fatal(err)
	}
	reopened := openTestStore(t, fixture.options(1<<20, 1<<20, 16))
	if stats := reopened.Stats(); stats.OriginalEntries != 1 || stats.VariantEntries != 0 {
		t.Fatalf("restart after manual clear %+v", stats)
	}
}

func TestStoreRejectsUnsafeRoots(t *testing.T) {
	fixture := newStoreFixture(t)
	nested := filepath.Join(fixture.media, "store")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	cases := map[string]StoreOptions{
		"inside media":   {Root: nested, MediaRoots: []string{fixture.media}},
		"equal to media": {Root: fixture.media, MediaRoots: []string{fixture.media}},
		"media inside":   {Root: fixture.base, MediaRoots: []string{fixture.media}},
		"second media":   {Root: fixture.root, MediaRoots: []string{filepath.Join(fixture.base, "other"), filepath.Join(fixture.root, "library")}},
		"missing":        {Root: filepath.Join(fixture.base, "missing"), MediaRoots: []string{fixture.media}},
		"relative":       {Root: "store", MediaRoots: []string{fixture.media}},
	}
	if runtime.GOOS == "linux" {
		alias, aliasParent := filepath.Join(fixture.base, "alias"), filepath.Join(fixture.base, "parent-alias")
		if os.Symlink(fixture.root, alias) != nil || os.Symlink(fixture.base, aliasParent) != nil {
			t.Fatal("create symlink fixture")
		}
		cases["symlink alias"] = StoreOptions{Root: alias, MediaRoots: []string{fixture.media}}
		cases["symlink ancestor"] = StoreOptions{Root: filepath.Join(aliasParent, "store"), MediaRoots: []string{fixture.media}}
		// A media root reached through a symlink is compared by its target.
		mediaAlias := filepath.Join(fixture.base, "media-alias")
		if os.Symlink(fixture.root, mediaAlias) != nil {
			t.Fatal("create media symlink fixture")
		}
		cases["media symlink to store"] = StoreOptions{Root: fixture.root, MediaRoots: []string{mediaAlias}}
		shared := filepath.Join(fixture.base, "shared")
		if os.Mkdir(shared, 0700) != nil || os.Chmod(shared, 0750) != nil {
			t.Fatal("create shared fixture")
		}
		cases["group readable"] = StoreOptions{Root: shared, MediaRoots: []string{fixture.media}}
		linked := filepath.Join(fixture.base, "linked")
		if os.Mkdir(linked, 0700) != nil || os.Symlink(fixture.media, filepath.Join(linked, "variants")) != nil {
			t.Fatal("create linked fixture")
		}
		cases["symlinked subdirectory"] = StoreOptions{Root: linked, MediaRoots: []string{filepath.Join(fixture.base, "other")}}
		open := filepath.Join(fixture.base, "open-sub")
		if os.MkdirAll(filepath.Join(open, "originals"), 0700) != nil || os.Chmod(filepath.Join(open, "originals"), 0755) != nil {
			t.Fatal("create open subdirectory fixture")
		}
		cases["shared subdirectory"] = StoreOptions{Root: open, MediaRoots: []string{filepath.Join(fixture.base, "other")}}
	}
	for name, options := range cases {
		t.Run(name, func(t *testing.T) {
			options.OriginalBytes, options.VariantBytes, options.MaxEntries = 1<<20, 1<<20, 16
			if store, err := openStore(context.Background(), options); err == nil {
				_ = store.Close(context.Background())
				t.Fatal("unsafe store root accepted")
			}
		})
	}
	if _, err := os.Stat(filepath.Join(fixture.media, "variants")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("store wrote through a media symlink", err)
	}
	for name, options := range map[string]StoreOptions{
		"public small originals": fixture.options(1<<20, 16<<20, 1024),
		"public small variants":  fixture.options(64<<20, 1<<20, 1024),
		"public small index":     fixture.options(64<<20, 16<<20, 16),
	} {
		if _, err := OpenStore(context.Background(), options); !errors.Is(err, domain.ErrInvalid) {
			t.Fatal("public bounds", name, err)
		}
	}
	// A fresh installation has no library roots yet; later roots are checked
	// with CheckMediaRoot on every request that reads them.
	empty, err := OpenStore(context.Background(), StoreOptions{Root: fixture.root, OriginalBytes: 64 << 20, VariantBytes: 16 << 20, MaxEntries: 1024})
	if err != nil || empty.CheckMediaRoot(fixture.base) == nil {
		t.Fatal("store without media roots", err)
	}
	if err := empty.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(context.Background(), fixture.options(64<<20, 16<<20, 1024))
	if err != nil {
		t.Fatal("valid public store", err)
	}
	defer store.Close(context.Background())
	if store.CheckMediaRoot(fixture.base) == nil || store.CheckMediaRoot(filepath.Join(fixture.root, "x")) == nil || store.CheckMediaRoot(fixture.media) != nil {
		t.Fatal("later media root overlap check")
	}
}

func TestStoreConcurrentWritersKeepOneCopy(t *testing.T) {
	fixture := newStoreFixture(t)
	store := openTestStore(t, fixture.options(1<<20, 1<<20, 16))
	content := bytes.Repeat([]byte("concurrent"), 4096)
	source := sha256.Sum256(content)
	key := StoreVariantKey("primary-v1", "jpeg", 1, 1, 85)
	const writers = 16
	var wait sync.WaitGroup
	errs := make(chan error, 3*writers)
	stop := make(chan struct{})
	var readers sync.WaitGroup
	for range 4 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				object, err := store.OpenVariant(context.Background(), source, key)
				if errors.Is(err, domain.ErrNotFound) {
					continue
				}
				if err != nil {
					errs <- err
					return
				}
				data, err := io.ReadAll(object)
				_ = object.Close()
				if err != nil || !bytes.Equal(data, content) {
					errs <- errors.New("reader saw a partial variant")
					return
				}
			}
		}()
	}
	for range writers {
		wait.Add(2)
		go func() {
			defer wait.Done()
			if _, err := store.PutVariant(context.Background(), source, key, bytes.NewReader(content), 1<<19); err != nil {
				errs <- err
			}
		}()
		go func() {
			defer wait.Done()
			if digest, _, err := store.PutOriginal(context.Background(), bytes.NewReader(content), 1<<20); err != nil || digest != source {
				errs <- errors.New("concurrent original put failed")
			}
		}()
	}
	wait.Wait()
	close(stop)
	readers.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	name := hex.EncodeToString(source[:])
	want := []string{"originals/" + name[:2] + "/" + name, storeVariantPath(source, key)}
	if files := storeFiles(t, fixture.root); !slices.Equal(files, want) {
		t.Fatal("concurrent writers left extra files", files)
	}
	stats := store.Stats()
	if stats.OriginalEntries != 1 || stats.VariantEntries != 1 || stats.Deduplicated != 2*writers-2 {
		t.Fatalf("concurrent accounting %+v", stats)
	}
	store.mu.Lock()
	locks := len(store.locks)
	store.mu.Unlock()
	if locks != 0 {
		t.Fatal("key locks leaked", locks)
	}
}

func TestStoreCancellationAndClose(t *testing.T) {
	fixture := newStoreFixture(t)
	store := openTestStore(t, fixture.options(1<<20, 1<<20, 16))
	source, key := sha256.Sum256([]byte("source")), StoreVariantKey("primary-v1", "jpeg", 1, 1, 85)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.PutVariant(cancelled, source, key, bytes.NewReader([]byte("x")), 16); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled put", err)
	}
	if _, _, err := store.PutOriginal(cancelled, bytes.NewReader([]byte("x")), 16); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled original put", err)
	}
	if _, err := store.OpenOriginal(cancelled, source, true); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled open", err)
	}
	if err := store.ClearVariants(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled clear", err)
	}
	// A writer waiting behind the same key can give up.
	unlock, err := store.lockKey(context.Background(), storeKey{class: storeVariants, source: source, variant: key})
	if err != nil {
		t.Fatal(err)
	}
	waiting, stopWaiting := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer stopWaiting()
	if _, err := store.PutVariant(waiting, source, key, bytes.NewReader([]byte("x")), 16); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("waiting writer was not cancelled", err)
	}
	// Close waits for the in-flight operation that holds the key.
	entered := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		close(entered)
		_, err := store.PutVariant(context.Background(), source, key, bytes.NewReader([]byte("late")), 16)
		finished <- err
	}()
	<-entered
	for {
		store.mu.Lock()
		active := store.active
		store.mu.Unlock()
		if active == 1 {
			break
		}
		runtime.Gosched()
	}
	short, stopShort := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer stopShort()
	if err := store.Close(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("close did not wait for an active writer", err)
	}
	unlock()
	if err := <-finished; err != nil {
		t.Fatal("in-flight writer failed", err)
	}
	if err := store.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.OpenVariant(context.Background(), source, key); !errors.Is(err, domain.ErrImageUnavailable) {
		t.Fatal("closed store served", err)
	}
	if files := storeFiles(t, filepath.Join(fixture.root, "tmp")); len(files) != 0 {
		t.Fatal("cancelled writes left staging files", files)
	}
}

func TestStoreConcurrentEvictionAndClearStayConsistent(t *testing.T) {
	fixture := newStoreFixture(t)
	object := storeHeaderBytes + 64
	store := openTestStore(t, fixture.options(1<<20, 8*object, 6))
	sources := [3][32]byte{{1}, {2}, {3}}
	var wait sync.WaitGroup
	errs := make(chan error, 64)
	for worker := range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for step := range 60 {
				source := sources[(worker+step)%len(sources)]
				key := StoreVariantKey("primary-v1", "jpeg", step%10, worker%3, 85)
				payload := bytes.Repeat([]byte{byte(step % 10), byte(worker % 3)}, 32)
				if _, err := store.PutVariant(context.Background(), source, key, bytes.NewReader(payload), 64); err != nil {
					errs <- err
					return
				}
				if object, err := store.OpenVariant(context.Background(), source, key); err == nil {
					data, readErr := io.ReadAll(object)
					_ = object.Close()
					if readErr != nil || !bytes.Equal(data, payload) {
						errs <- errors.New("reader saw wrong variant bytes")
						return
					}
				} else if !errors.Is(err, domain.ErrNotFound) {
					errs <- err
					return
				}
				if worker == 0 && step%15 == 0 {
					if err := store.ClearVariants(context.Background()); err != nil {
						errs <- err
						return
					}
				}
			}
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	files := storeFiles(t, fixture.root)
	store.mu.Lock()
	indexed := len(store.classes[storeVariants].entries)
	bytesIndexed := store.classes[storeVariants].bytes
	for key := range store.classes[storeVariants].entries {
		if !slices.Contains(files, storeVariantPath(key.source, key.variant)) {
			t.Error("indexed variant missing on disk")
		}
	}
	store.mu.Unlock()
	if len(files) != indexed || bytesIndexed != int64(indexed)*object || indexed > 6 || bytesIndexed > 8*object {
		t.Fatal("store and index diverged", files, indexed, bytesIndexed)
	}
}
