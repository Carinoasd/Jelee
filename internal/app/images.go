package app

import (
	"bytes"
	"context"
	"errors"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type Images struct {
	repository ImageSourceRepository
	renderer   ImageRenderer
	// Optional G40 asset rows. Without them only the local Primary poster
	// next to the media file is served.
	assets      ItemImageResolver
	assetRender ItemImageRenderer
}

func NewImages(repository ImageSourceRepository, renderer ImageRenderer) (*Images, error) {
	if repository == nil || renderer == nil {
		return nil, domain.ErrInvalid
	}
	return &Images{repository: repository, renderer: renderer}, nil
}

// WithAssets serves item_images rows first and keeps the local Primary poster
// as the fallback when a slot has no usable row.
func (s *Images) WithAssets(assets ItemImageResolver, renderer ItemImageRenderer) (*Images, error) {
	if s == nil || assets == nil || renderer == nil {
		return nil, domain.ErrInvalid
	}
	next := *s
	next.assets, next.assetRender = assets, renderer
	return &next, nil
}

// errNoAsset means the slot has no usable asset row; the caller may fall
// back to the local Primary poster.
var errNoAsset = errors.New("no usable image asset")

func (s *Images) Get(ctx context.Context, actor domain.Actor, itemID string, request domain.ImageRequest) (ImageResult, error) {
	if s == nil || ctx == nil || !domain.ValidID(actor.UserID) || !domain.ValidID(actor.SessionID) || !domain.ValidID(itemID) {
		return ImageResult{}, domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return ImageResult{}, err
	}
	query, err := domain.NormalizeImageRequest(request)
	if err != nil {
		return ImageResult{}, err
	}
	if s.assets != nil {
		result, err := s.getAsset(ctx, actor, itemID, query)
		if !errors.Is(err, errNoAsset) {
			return result, err
		}
	}
	if query.Type != "Primary" || query.Index != 0 {
		return ImageResult{}, domain.ErrNotFound
	}
	source, err := s.repository.ResolveImageSource(ctx, actor, itemID)
	if err != nil {
		return ImageResult{}, err
	}
	result, err := s.renderer.Render(ctx, source, query)
	if err != nil {
		if result.Body != nil {
			_ = result.Body.Close()
		}
		return ImageResult{}, err
	}
	if result.Body == nil {
		return ImageResult{}, domain.ErrImageUnavailable
	}
	// Decoding never holds a database transaction. Recheck access and binding
	// before handing any bytes to HTTP, including cache hits and 304s.
	current, err := s.repository.ResolveImageSource(ctx, actor, itemID)
	if err == nil && current != source {
		err = domain.ErrNotFound
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		_ = result.Body.Close()
		return ImageResult{}, err
	}
	return result, nil
}

// getAsset tries the usable rows of the slot in G40.10 order. A row whose
// file is gone or whose bytes are not stored yields to the next one; remote
// URLs are never fetched on the request path. Any other failure is final.
func (s *Images) getAsset(ctx context.Context, actor domain.Actor, itemID string, query domain.ImageRequest) (ImageResult, error) {
	candidates, err := s.assets.ResolveItemImageSources(ctx, actor, itemID, query.Type, query.Index)
	if errors.Is(err, domain.ErrNotFound) {
		// Also covers an invisible item; the fallback rechecks access itself.
		return ImageResult{}, errNoAsset
	}
	if err != nil {
		return ImageResult{}, err
	}
	for _, candidate := range candidates {
		result, err := s.assetRender.RenderItemImage(ctx, candidate, query)
		if err != nil {
			if result.Body != nil {
				_ = result.Body.Close()
			}
			if ctx.Err() != nil {
				return ImageResult{}, ctx.Err()
			}
			if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrImageUnavailable) {
				continue
			}
			return ImageResult{}, err
		}
		if result.Body == nil {
			return ImageResult{}, domain.ErrImageUnavailable
		}
		// Same rule as the local poster: the row that produced the bytes must
		// still be visible and bound identically before HTTP sees them.
		current, err := s.assets.ResolveItemImageSources(ctx, actor, itemID, query.Type, query.Index)
		if err == nil && !containsItemImageBinding(current, candidate) {
			err = domain.ErrNotFound
		}
		if err == nil {
			err = ctx.Err()
		}
		if err != nil {
			_ = result.Body.Close()
			return ImageResult{}, err
		}
		return result, nil
	}
	return ImageResult{}, errNoAsset
}

func containsItemImageBinding(values []domain.ItemImage, want domain.ItemImage) bool {
	for _, value := range values {
		if sameItemImageBinding(value, want) {
			return true
		}
	}
	return false
}

func sameItemImageBinding(a, b domain.ItemImage) bool {
	if a.ID != b.ID || a.ItemID != b.ItemID || a.LibraryID != b.LibraryID || a.Type != b.Type || a.Index != b.Index ||
		a.SourceKind != b.SourceKind || a.RootID != b.RootID || a.RootPath != b.RootPath || a.RelativePath != b.RelativePath ||
		a.RemoteURL != b.RemoteURL || (a.Content == nil) != (b.Content == nil) {
		return false
	}
	return a.Content == nil || bytes.Equal(a.Content.SHA256, b.Content.SHA256)
}
