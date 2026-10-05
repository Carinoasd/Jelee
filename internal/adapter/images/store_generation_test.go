package images

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// storeWindowsRules applies, on any platform, the Windows refusals that
// matter to the store: a directory cannot be renamed (Windows refuses while
// any file below it is open, and a reader may hold one at any time), and a
// pinned file, one held open elsewhere without FILE_SHARE_DELETE or left
// delete-pending by a filesystem without POSIX semantics, can be neither
// renamed nor removed.
type storeWindowsRules struct {
	mu         sync.Mutex
	pinned     map[string]bool
	dirRenames int
}

func newStoreWindowsRules() *storeWindowsRules {
	return &storeWindowsRules{pinned: make(map[string]bool)}
}

func (r *storeWindowsRules) install(s *Store) {
	root := s.root
	refused := func(op, name string) error {
		return &fs.PathError{Op: op, Path: name, Err: fs.ErrPermission}
	}
	s.renameName = func(oldName, newName string) error {
		r.mu.Lock()
		defer r.mu.Unlock()
		if info, err := root.Lstat(oldName); err == nil && info.IsDir() {
			r.dirRenames++
			return refused("rename", oldName)
		}
		if r.pinned[filepath.ToSlash(oldName)] || r.pinned[filepath.ToSlash(newName)] {
			return refused("rename", oldName)
		}
		return root.Rename(oldName, newName)
	}
	s.removeName = func(name string) error {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.pinned[filepath.ToSlash(name)] {
			return refused("remove", name)
		}
		return root.Remove(name)
	}
}

func (r *storeWindowsRules) pin(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pinned[name] = true
}

func (r *storeWindowsRules) unpinAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	clear(r.pinned)
}

func storePutTestVariant(t *testing.T, store *Store, source, key [32]byte, content string) {
	t.Helper()
	if _, err := store.PutVariant(context.Background(), source, key, bytes.NewReader([]byte(content)), 1024); err != nil {
		t.Fatal("put variant", err)
	}
}

func storeVariantContent(t *testing.T, store *Store, source, key [32]byte) (string, error) {
	t.Helper()
	object, err := store.OpenVariant(context.Background(), source, key)
	if err != nil {
		return "", err
	}
	return string(storeRead(t, object)), nil
}

func TestStoreClearVariantsUnderWindowsRulesNeverRevivesStaleVariants(t *testing.T) {
	fixture := newStoreFixture(t)
	rules := newStoreWindowsRules()
	open := func() *Store {
		store, err := openStoreWith(context.Background(), fixture.options(1<<20, 1<<20, 16), rules.install)
		if err != nil {
			t.Fatal("open store", err)
		}
		t.Cleanup(func() { _ = store.Close(context.Background()) })
		return store
	}
	store := open()
	digest, _, err := store.PutOriginal(context.Background(), bytes.NewReader([]byte("original")), 1024)
	if err != nil {
		t.Fatal(err)
	}
	other := sha256.Sum256([]byte("other source"))
	heldKey := StoreVariantKey("primary-v1", "jpeg", 1, 1, 85)
	staleKey := StoreVariantKey("primary-v1", "jpeg", 2, 2, 85)
	againKey := StoreVariantKey("primary-v1", "webp", 3, 3, 85)
	plainKey := StoreVariantKey("primary-v1", "webp", 4, 4, 85)
	storePutTestVariant(t, store, digest, heldKey, "held")
	storePutTestVariant(t, store, digest, staleKey, "stale")
	storePutTestVariant(t, store, other, againKey, "again-old")
	storePutTestVariant(t, store, other, plainKey, "plain")
	heldPath, plainPath := storeVariantPath(store, digest, heldKey), storeVariantPath(store, other, plainKey)
	stalePath, againOldPath := storeVariantPath(store, digest, staleKey), storeVariantPath(store, other, againKey)
	rules.pin(stalePath)
	rules.pin(againOldPath)
	held, err := store.OpenVariant(context.Background(), digest, heldKey)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.ClearVariants(context.Background()); err != nil {
		t.Fatal("clear failed under Windows rules", err)
	}
	if got := string(storeRead(t, held)); got != "held" {
		t.Fatal("held variant changed during clear", got)
	}
	for _, key := range [][2][32]byte{{digest, heldKey}, {digest, staleKey}, {other, againKey}, {other, plainKey}} {
		if _, err := storeVariantContent(t, store, key[0], key[1]); !errors.Is(err, domain.ErrNotFound) {
			t.Fatal("cleared variant served", err)
		}
	}
	if stats := store.Stats(); stats.VariantEntries != 0 || stats.VariantBytes != 0 || stats.Cleared != 1 {
		t.Fatalf("clear accounting %+v", stats)
	}
	files := storeFiles(t, fixture.root)
	if slices.Contains(files, heldPath) || slices.Contains(files, plainPath) || !slices.Contains(files, stalePath) || !slices.Contains(files, againOldPath) {
		t.Fatal("clear did not remove exactly the unpinned variants", files)
	}
	if object, err := store.OpenOriginal(context.Background(), digest, true); err != nil || string(storeRead(t, object)) != "original" {
		t.Fatal("clear touched originals", err)
	}
	// A pinned stale file never blocks rebuilding the same key.
	storePutTestVariant(t, store, other, againKey, "again-new")
	if got, err := storeVariantContent(t, store, other, againKey); err != nil || got != "again-new" {
		t.Fatal("rebuilt variant", got, err)
	}
	if err := store.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Restart while the stale files are still pinned: they stay on disk but
	// are never indexed again, and the rebuilt key serves its new bytes.
	reopened := open()
	if stats := reopened.Stats(); stats.VariantEntries != 1 || stats.OriginalEntries != 1 {
		t.Fatalf("restart indexed stale variants %+v", stats)
	}
	if _, err := storeVariantContent(t, reopened, digest, staleKey); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("stale variant revived after restart", err)
	}
	if got, err := storeVariantContent(t, reopened, other, againKey); err != nil || got != "again-new" {
		t.Fatal("restart served the stale bytes of a rebuilt key", got, err)
	}
	if files := storeFiles(t, fixture.root); !slices.Contains(files, stalePath) || !slices.Contains(files, againOldPath) {
		t.Fatal("pinned stale files were not left in place", files)
	}

	// Once released, a later clear reclaims every stale generation.
	rules.unpinAll()
	if err := reopened.ClearVariants(context.Background()); err != nil {
		t.Fatal(err)
	}
	name := hex.EncodeToString(digest[:])
	if files := storeFiles(t, fixture.root); !slices.Equal(files, []string{"originals/" + name[:2] + "/" + name}) {
		t.Fatal("stale generations kept after release", files)
	}
	entries, err := os.ReadDir(filepath.Join(fixture.root, "variants"))
	if err != nil || len(entries) != 1 || entries[0].Name() != storeGenerationName(reopened.generation.Load()) {
		t.Fatal("stale generation directories kept", err, entries)
	}
}

func TestStoreRestartMakesHighestGenerationLive(t *testing.T) {
	fixture := newStoreFixture(t)
	store := openTestStore(t, fixture.options(1<<20, 1<<20, 16))
	digest, _, err := store.PutOriginal(context.Background(), bytes.NewReader([]byte("original")), 1024)
	if err != nil {
		t.Fatal(err)
	}
	key := StoreVariantKey("primary-v1", "jpeg", 1, 1, 85)
	storePutTestVariant(t, store, digest, key, "variant")
	old := store.generation.Load()
	if err := store.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	// A clear that stopped right after its commit point: the next generation
	// exists, the old one was not swept yet.
	if err := os.Mkdir(filepath.Join(fixture.root, "variants", storeGenerationName(old+1)), 0700); err != nil {
		t.Fatal(err)
	}
	reopened := openTestStore(t, fixture.options(1<<20, 1<<20, 16))
	if reopened.generation.Load() != old+1 {
		t.Fatal("restart did not make the highest generation live", reopened.generation.Load())
	}
	if stats := reopened.Stats(); stats.VariantEntries != 0 || stats.OriginalEntries != 1 {
		t.Fatalf("restart indexed a cleared generation %+v", stats)
	}
	if _, err := storeVariantContent(t, reopened, digest, key); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("cleared variant revived", err)
	}
	name := hex.EncodeToString(digest[:])
	if files := storeFiles(t, fixture.root); !slices.Equal(files, []string{"originals/" + name[:2] + "/" + name}) {
		t.Fatal("startup kept the stale generation", files)
	}
	storePutTestVariant(t, reopened, digest, key, "rebuilt")
	if got, err := storeVariantContent(t, reopened, digest, key); err != nil || got != "rebuilt" {
		t.Fatal("rebuild in the live generation", got, err)
	}
}

func TestStoreTreatsPreGenerationVariantsAsStale(t *testing.T) {
	fixture := newStoreFixture(t)
	source := sha256.Sum256([]byte("source"))
	key := StoreVariantKey("primary-v1", "jpeg", 1, 1, 85)
	bucket := filepath.Join(fixture.root, "variants", hex.EncodeToString(source[:]))
	if err := os.MkdirAll(bucket, 0700); err != nil {
		t.Fatal(err)
	}
	payload := []byte("legacy")
	legacy := append(storeHeader(int64(len(payload)), sha256.Sum256(payload)), payload...)
	for name, data := range map[string][]byte{
		filepath.Join(bucket, hex.EncodeToString(key[:])): legacy,
		filepath.Join(bucket, "operator-note"):            []byte("keep"),
		filepath.Join(fixture.root, "variants", "README"): []byte("keep"),
	} {
		if err := os.WriteFile(name, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	store := openTestStore(t, fixture.options(1<<20, 1<<20, 16))
	if stats := store.Stats(); stats.VariantEntries != 0 || stats.Foreign < 1 {
		t.Fatalf("pre-generation variant indexed %+v", stats)
	}
	if _, err := storeVariantContent(t, store, source, key); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("pre-generation variant served", err)
	}
	sourceName := hex.EncodeToString(source[:])
	want := []string{"variants/" + sourceName + "/operator-note", "variants/README"}
	if files := storeFiles(t, fixture.root); !slices.Equal(files, want) {
		t.Fatal("startup removed a foreign file or kept a pre-generation variant", files)
	}
	if info, err := os.Stat(filepath.Join(fixture.root, "variants", storeGenerationName(1))); err != nil || !info.IsDir() || store.generation.Load() != 1 {
		t.Fatal("first generation not created", err)
	}
}

func TestStoreGenerationNames(t *testing.T) {
	for _, generation := range []uint64{1, 2, 0xff, 1 << 40, ^uint64(0)} {
		name := storeGenerationName(generation)
		if parsed, ok := storeParseGeneration(name); !ok || parsed != generation || len(name) != storeGenerationDigits {
			t.Fatal("generation round trip", generation, name)
		}
	}
	for _, name := range []string{"0000000000000000", "000000000000000A", "00000000000001", "README", hex.EncodeToString(make([]byte, 32))} {
		if _, ok := storeParseGeneration(name); ok {
			t.Fatal("accepted generation name", name)
		}
	}
}
