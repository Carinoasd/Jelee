package images

import (
	"context"
	"crypto/sha256"
	"testing"
)

// The image-variants repair (G50.4) counts the live generation, clears it
// and then counts nothing: a second run is a no-op.
func TestStoreLiveVariantsFollowClear(t *testing.T) {
	var none *Store
	if entries, bytes := none.LiveVariants(); entries != 0 || bytes != 0 {
		t.Fatal("nil store counted variants")
	}
	fixture := newStoreFixture(t)
	store := openTestStore(t, fixture.options(1<<20, 1<<20, 16))
	source := sha256.Sum256([]byte("source"))
	storePutTestVariant(t, store, source, StoreVariantKey("primary-v1", "jpeg", 1, 1, 85), "one")
	storePutTestVariant(t, store, source, StoreVariantKey("primary-v1", "jpeg", 2, 2, 85), "two!")
	if entries, bytes := store.LiveVariants(); entries != 2 || bytes <= 0 {
		t.Fatalf("live variants %d/%d", entries, bytes)
	}
	if err := store.ClearVariants(context.Background()); err != nil {
		t.Fatal(err)
	}
	if entries, bytes := store.LiveVariants(); entries != 0 || bytes != 0 {
		t.Fatalf("after clear %d/%d", entries, bytes)
	}
}
