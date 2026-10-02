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
