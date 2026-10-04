package app

import (
	"context"
	"io"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type ImageSourceRepository interface {
	// ResolveImageSource rechecks the live session and library access in the
	// same read that binds the item to its unique local media source.
	ResolveImageSource(context.Context, domain.Actor, string) (domain.LocalImageSource, error)
}

// ItemImageResolver is the read side used by image requests.
type ItemImageResolver interface {
	// ResolveItemImageSources returns the usable rows of one slot in G40.10
	// order: the locked row first, then local, NFO, remote and embedded. A
	// URL reference without fetched content is never usable. A missing or
	// invisible item, and a visible item without usable rows, are ErrNotFound.
	ResolveItemImageSources(ctx context.Context, actor domain.Actor, item, imageType string, index int) ([]domain.ItemImage, error)
}

// ItemImageSummaryReader is the listing side of the image assets: the
// selected source of every slot of a page of items in one read, for the
// image tags of the compatibility layer (G24.2). Visibility is userID's
// library grant; invisible and missing items are absent.
type ItemImageSummaryReader interface {
	ItemImageSummaries(ctx context.Context, userID string, itemIDs []string, galleryMax int) (map[string][]domain.ItemImageSummary, error)
}

// ItemImageRepository stores G40 image references. Reads recheck the live
// session and library grant in the same statement; an invisible item is
// ErrNotFound. Writes require a live administrator and audit manual
// replacement, lock changes and deletion. No method touches files.
type ItemImageRepository interface {
	ItemImageResolver
	// UpsertItemImage merges one observation into its (item, type, index,
	// source kind) row. A locked row is returned unchanged with Skipped set
	// unless the input is manual.
	UpsertItemImage(context.Context, domain.Actor, domain.ItemImageInput) (domain.ItemImageUpsert, error)
	ListItemImages(context.Context, domain.Actor, string) ([]domain.ItemImage, error)
	// ResolveItemImage picks the usable source of one slot by G40.10: the
	// locked row first, then local, NFO, remote and embedded.
	ResolveItemImage(ctx context.Context, actor domain.Actor, item, imageType string, index int) (domain.ItemImage, error)
	SetItemImageLock(ctx context.Context, actor domain.Actor, item, imageType string, index int, sourceKind string, locked bool) (domain.ItemImage, error)
	DeleteItemImage(ctx context.Context, actor domain.Actor, item, imageType string, index int, sourceKind string) error
}

// ImageVariantIndex mirrors the filesystem variant store for eviction. It is
// internal bookkeeping and takes no actor.
type ImageVariantIndex interface {
	PutImageVariant(ctx context.Context, content, key [32]byte, size int64) error
	// TouchImageVariant returns ErrNotFound for an unknown entry.
	TouchImageVariant(ctx context.Context, content, key [32]byte) error
	ListOldestImageVariants(ctx context.Context, limit int) ([]domain.ImageVariant, error)
	// DeleteImageVariant removes the listed entry only if it was not touched
	// after listing and reports whether it did.
	DeleteImageVariant(context.Context, domain.ImageVariant) (bool, error)
}

type ImageResult struct {
	// Body exposes an independent reader over immutable encoded bytes. Close
	// is mandatory, including HEAD and 304, and releases processing admission.
	Body              io.ReadSeekCloser
	ContentType, ETag string
	Size              int64
	Width, Height     int
	// ContentSHA256 is the digest of the original bytes the representation
	// was derived from; zero when unknown. It is the immutable image tag.
	ContentSHA256 [32]byte
}

type ImageRenderer interface {
	Render(context.Context, domain.LocalImageSource, domain.ImageRequest) (ImageResult, error)
}

// ItemImageRenderer renders one authorized item_images row. It never fetches
// a remote URL: a row whose bytes are neither a readable library file nor
// held by the persistent store is ErrNotFound, so the caller can fall back.
type ItemImageRenderer interface {
	RenderItemImage(context.Context, domain.ItemImage, domain.ImageRequest) (ImageResult, error)
}
