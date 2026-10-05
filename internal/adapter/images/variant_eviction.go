package images

import (
	"context"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

const (
	// Eviction starts above the high mark and stops at the low mark, both in
	// tenths of the store's variant byte and entry bounds. The store's own
	// LRU still enforces the hard bounds if the index falls behind.
	variantEvictHigh  = 9
	variantEvictLow   = 8
	variantEvictPage  = 128
	variantEvictPages = 64
)

// EvictVariants removes least recently used variants through the database
// index until the store holds at most targetBytes and targetEntries. A file
// is deleted only after DeleteImageVariant removed its row under the access
// time it was listed with, so a variant used meanwhile is kept. Rows whose
// file is already gone are removed as well. Work is bounded per call.
func EvictVariants(ctx context.Context, store *Store, index app.ImageVariantIndex, targetBytes int64, targetEntries int) (int, error) {
	if ctx == nil || store == nil || index == nil || targetBytes < 0 || targetEntries < 0 {
		return 0, domain.ErrInvalid
	}
	removed := 0
	below := func() bool {
		bytes, entries, _, _ := store.VariantUsage()
		return bytes <= targetBytes && entries <= targetEntries
	}
	for range variantEvictPages {
		if below() {
			return removed, nil
		}
		page, err := index.ListOldestImageVariants(ctx, variantEvictPage)
		if err != nil {
			return removed, err
		}
		progressed := false
		for _, entry := range page {
			if below() {
				return removed, nil
			}
			deleted, err := index.DeleteImageVariant(ctx, entry)
			if err != nil {
				return removed, err
			}
			if !deleted {
				continue // Used after listing; it is no longer the oldest.
			}
			progressed = true
			// The row is gone; finish removing its file even if the caller is
			// being cancelled, so no file outlives its index entry by choice.
			if err := store.RemoveVariant(context.WithoutCancel(ctx), entry.ContentSHA256, entry.VariantKey); err != nil {
				return removed, err
			}
			removed++
		}
		if !progressed {
			// An empty index, or every listed entry was touched meanwhile.
			// Files without a row age out through the store's own bound.
			return removed, nil
		}
	}
	return removed, nil
}

func (p *Processor) wakeEviction() {
	if p.evictWake == nil {
		return
	}
	select {
	case p.evictWake <- struct{}{}:
	default:
	}
}

// evictLoop is the only eviction goroutine. It runs one bounded pass per
// wake-up and exits with the processor lifetime.
func (p *Processor) evictLoop() {
	defer close(p.evictDone)
	store, index := p.options.Store, p.options.Index
	for {
		select {
		case <-p.lifetime.Done():
			return
		case <-p.evictWake:
		}
		bytes, entries, limitBytes, maxEntries := store.VariantUsage()
		if bytes*10 < limitBytes*variantEvictHigh && entries*10 < maxEntries*variantEvictHigh {
			continue
		}
		removed, err := EvictVariants(p.lifetime, store, index, limitBytes/10*variantEvictLow, maxEntries/10*variantEvictLow)
		p.indexEvictions.Add(uint64(removed))
		if err != nil && p.lifetime.Err() == nil {
			p.indexFailures.Add(1)
		}
	}
}
