package app

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type imageRepoFunc func(context.Context, domain.Actor, string) (domain.LocalImageSource, error)

func (f imageRepoFunc) ResolveImageSource(c context.Context, a domain.Actor, id string) (domain.LocalImageSource, error) {
	return f(c, a, id)
}

type imageRenderFunc func(context.Context, domain.LocalImageSource, domain.ImageRequest) (ImageResult, error)

func (f imageRenderFunc) Render(c context.Context, s domain.LocalImageSource, q domain.ImageRequest) (ImageResult, error) {
	return f(c, s, q)
}

type imageTestBody struct {
	*bytes.Reader
	closed bool
}

func (b *imageTestBody) Close() error { b.closed = true; return nil }

func TestImagesRecheckAccessAndBindingBeforeReturning(t *testing.T) {
	actor := domain.Actor{UserID: "00000000-0000-4000-8000-000000000001", SessionID: "00000000-0000-4000-8000-000000000002"}
	id := "00000000-0000-4000-8000-000000000003"
	for _, mode := range []string{"success", "revoked", "rebound", "cancelled", "renderer error"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			body := &imageTestBody{Reader: bytes.NewReader([]byte("encoded"))}
			calls := 0
			repo := imageRepoFunc(func(context.Context, domain.Actor, string) (domain.LocalImageSource, error) {
				calls++
				s := domain.LocalImageSource{ItemID: id, SourceID: "source"}
				if calls == 2 {
					switch mode {
					case "revoked":
						return domain.LocalImageSource{}, domain.ErrNotFound
					case "rebound":
						s.SourceID = "other"
					case "cancelled":
						cancel()
					}
				}
				return s, nil
			})
			render := imageRenderFunc(func(_ context.Context, _ domain.LocalImageSource, q domain.ImageRequest) (ImageResult, error) {
				if q.Width != 640 || q.Height != 640 || q.Type != "Primary" || q.Format != "jpeg" {
					t.Fatal("normalization")
				}
				value := ImageResult{Body: body, ContentType: "image/jpeg", Size: 7}
				if mode == "renderer error" {
					return value, domain.ErrImageUnavailable
				}
				return value, nil
			})
			service, err := NewImages(repo, render)
			if err != nil {
				t.Fatal(err)
			}
			result, err := service.Get(ctx, actor, id, domain.ImageRequest{})
			if mode == "success" {
				if err != nil || result.Body != body || body.closed || calls != 2 {
					t.Fatal("successful image", err)
				}
				_ = result.Body.Close()
			} else if err == nil || result.Body != nil || !body.closed {
				t.Fatal("returned image after rejection", err)
			}
		})
	}
}

func TestImagesRejectBeforeRendering(t *testing.T) {
	actor := domain.Actor{UserID: "00000000-0000-4000-8000-000000000001", SessionID: "00000000-0000-4000-8000-000000000002"}
	id := "00000000-0000-4000-8000-000000000003"
	service, _ := NewImages(imageRepoFunc(func(context.Context, domain.Actor, string) (domain.LocalImageSource, error) {
		return domain.LocalImageSource{}, domain.ErrNotFound
	}), imageRenderFunc(func(context.Context, domain.LocalImageSource, domain.ImageRequest) (ImageResult, error) {
		t.Fatal("unauthorized render")
		return ImageResult{}, nil
	}))
	if _, err := service.Get(context.Background(), actor, id, domain.ImageRequest{}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := service.Get(context.Background(), actor, id, domain.ImageRequest{Width: 2049}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal(err)
	}
}
