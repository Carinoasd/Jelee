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

type imageSummaryFunc func(context.Context, string, []string, int) (map[string][]domain.ItemImageSummary, error)

func (f imageSummaryFunc) ItemImageSummaries(ctx context.Context, userID string, ids []string, gallery int) (map[string][]domain.ItemImageSummary, error) {
	return f(ctx, userID, ids, gallery)
}

func TestImagesSummariesValidateAndBatch(t *testing.T) {
	user, item := "10000000-0000-4000-8000-000000000001", "10000000-0000-4000-8000-000000000002"
	base, err := NewImages(imageRepoFunc(func(context.Context, domain.Actor, string) (domain.LocalImageSource, error) {
		return domain.LocalImageSource{}, domain.ErrNotFound
	}), imageRenderFunc(func(context.Context, domain.LocalImageSource, domain.ImageRequest) (ImageResult, error) {
		return ImageResult{}, domain.ErrNotFound
	}))
	if err != nil {
		t.Fatal(err)
	}
	// Without a reader nothing is advertised and nothing is read.
	if got, err := base.Summaries(context.Background(), user, []string{item}); err != nil || got == nil || len(got) != 0 {
		t.Fatal("summaries without reader", got, err)
	}
	if _, err := base.WithSummaries(nil); err != domain.ErrInvalid {
		t.Fatal("nil reader accepted", err)
	}
	calls := 0
	images, err := base.WithSummaries(imageSummaryFunc(func(_ context.Context, u string, ids []string, gallery int) (map[string][]domain.ItemImageSummary, error) {
		calls++
		if u != user || len(ids) != 2 || gallery != domain.ItemImageSummaryGalleryMax {
			t.Fatalf("reader got %s %v %d", u, ids, gallery)
		}
		return map[string][]domain.ItemImageSummary{item: {{ItemID: item, Type: "Primary"}}}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := images.Summaries(context.Background(), user, []string{item, item}); err != nil || len(got[item]) != 1 || calls != 1 {
		t.Fatal("batched summaries", got, err, calls)
	}
	if got, err := images.Summaries(context.Background(), user, nil); err != nil || len(got) != 0 || calls != 1 {
		t.Fatal("empty request read the store", got, err)
	}
	tooMany := make([]string, domain.ItemImageSummaryItemsMax+1)
	for i := range tooMany {
		tooMany[i] = item
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for name, call := range map[string]func() error{
		"bad user":  func() error { _, err := images.Summaries(context.Background(), "x", []string{item}); return err },
		"bad item":  func() error { _, err := images.Summaries(context.Background(), user, []string{"x"}); return err },
		"too many":  func() error { _, err := images.Summaries(context.Background(), user, tooMany); return err },
		"cancelled": func() error { _, err := images.Summaries(cancelled, user, []string{item}); return err },
	} {
		if err := call(); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	if calls != 1 {
		t.Fatal("rejected requests reached the reader")
	}
}
