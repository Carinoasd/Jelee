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

// ItemImageRepository stores G40 image references. Reads recheck the live
// session and library grant in the same statement; an invisible item is
// ErrNotFound. Writes require a live administrator and audit manual
// replacement, lock changes and deletion. No method touches files.
type ItemImageRepository interface {
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
}

type ImageRenderer interface {
	Render(context.Context, domain.LocalImageSource, domain.ImageRequest) (ImageResult, error)
}
