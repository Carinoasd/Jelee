package app

import (
	"context"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type Images struct {
	repository ImageSourceRepository
	renderer   ImageRenderer
}

func NewImages(repository ImageSourceRepository, renderer ImageRenderer) (*Images, error) {
	if repository == nil || renderer == nil {
		return nil, domain.ErrInvalid
	}
	return &Images{repository: repository, renderer: renderer}, nil
}

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
