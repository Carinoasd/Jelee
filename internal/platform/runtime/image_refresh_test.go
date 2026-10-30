package runtime

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	imageadapter "github.com/MoYuanCN/Jelee/internal/adapter/images"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
)

func imageRefreshTestProcessor(t *testing.T, withStore bool) *imageadapter.Processor {
	t.Helper()
	// The store refuses a directory without a private DACL on Windows.
	base := imagesIntegrationScratch(t)
	temp, root := filepath.Join(base, "temp"), filepath.Join(base, "store")
	for _, dir := range []string{temp, root} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	options := imageadapter.Options{TempRoot: temp, MaxConcurrent: 2, MaxImageBytes: 96 << 20, MaxSourceBytes: 16 << 20, MaxOutputBytes: 2 << 20,
		CacheBytes: 32 << 20, MaxOutputDimension: 1024, CacheEntries: 128, DefaultQuality: 85, Timeout: 15 * time.Second, CacheTTL: 300 * time.Second}
	var store *imageadapter.Store
	if withStore {
		var err error
		if store, err = imageadapter.OpenStore(context.Background(), imageadapter.StoreOptions{Root: root, OriginalBytes: 64 << 20, VariantBytes: 16 << 20, MaxEntries: 1024}); err != nil {
			t.Fatal(err)
		}
		options.Store = store
	}
	processor, err := imageadapter.New(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = processor.Shutdown(ctx)
		if store != nil {
			_ = store.Close(ctx)
		}
	})
	return processor
}

// The refresher exists only with fetching enabled and an image store; the
// default is the local-only mode in which no client is built.
func TestPrepareImageRefreshOnlyWhenEnabledWithStore(t *testing.T) {
	withStore := imageRefreshTestProcessor(t, true)
	if fetcher, err := prepareImageRefresh(config.Config{}, withStore, nil); err != nil || fetcher != nil {
		t.Fatal("local-only mode built a fetcher", err)
	}
	if fetcher, err := prepareImageRefresh(config.Config{EnableImageFetch: true}, nil, nil); err != nil || fetcher != nil {
		t.Fatal("fetcher without images", err)
	}
	if fetcher, err := prepareImageRefresh(config.Config{EnableImageFetch: true}, imageRefreshTestProcessor(t, false), nil); err != nil || fetcher != nil {
		t.Fatal("fetcher without a store", err)
	}
	if fetcher, err := prepareImageRefresh(config.Config{EnableImageFetch: true}, withStore, nil); err != nil || fetcher == nil {
		t.Fatal("enabled fetcher", err)
	}
}
