package app

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type assetResolverFunc func(ctx context.Context, actor domain.Actor, item, imageType string, index int) ([]domain.ItemImage, error)

func (f assetResolverFunc) ResolveItemImageSources(ctx context.Context, actor domain.Actor, item, imageType string, index int) ([]domain.ItemImage, error) {
	return f(ctx, actor, item, imageType, index)
}

type assetRenderFunc func(context.Context, domain.ItemImage, domain.ImageRequest) (ImageResult, error)

func (f assetRenderFunc) RenderItemImage(ctx context.Context, value domain.ItemImage, request domain.ImageRequest) (ImageResult, error) {
	return f(ctx, value, request)
}

const (
	assetUser    = "00000000-0000-4000-8000-000000000001"
	assetSession = "00000000-0000-4000-8000-000000000002"
	assetItem    = "00000000-0000-4000-8000-000000000003"
)

func assetRow(id, kind string, locked bool) domain.ItemImage {
	value := domain.ItemImage{ID: id, ItemID: assetItem, LibraryID: "lib", Type: "Primary", SourceKind: kind, Locked: locked}
	switch kind {
	case domain.ImageSourceRemote:
		value.RemoteURL = "https://images.example/" + id
		value.Content = &domain.ItemImageContent{SHA256: bytes.Repeat([]byte{byte(len(id))}, 32)}
	default:
		value.RootID, value.RootPath, value.RelativePath = "root", "/library", "movie/"+id+".jpg"
	}
	return value
}

type assetHarness struct {
	rows        []domain.ItemImage
	resolveErr  error
	recheck     func(call int) ([]domain.ItemImage, error)
	renderErr   map[string]error
	rendered    []string
	resolves    int
	legacyCalls int
	bodies      []*imageTestBody
}

func (h *assetHarness) service(t *testing.T) *Images {
	t.Helper()
	legacy := imageRepoFunc(func(context.Context, domain.Actor, string) (domain.LocalImageSource, error) {
		h.legacyCalls++
		return domain.LocalImageSource{ItemID: assetItem, SourceID: "poster"}, nil
	})
	render := imageRenderFunc(func(context.Context, domain.LocalImageSource, domain.ImageRequest) (ImageResult, error) {
		h.rendered = append(h.rendered, "legacy")
		body := &imageTestBody{Reader: bytes.NewReader([]byte("legacy"))}
		h.bodies = append(h.bodies, body)
		return ImageResult{Body: body, ContentType: "image/jpeg", Size: 6}, nil
	})
	service, err := NewImages(legacy, render)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.WithAssets(nil, nil); err == nil {
		t.Fatal("missing asset dependencies accepted")
	}
	service, err = service.WithAssets(assetResolverFunc(func(_ context.Context, actor domain.Actor, item, imageType string, index int) ([]domain.ItemImage, error) {
		if actor.UserID != assetUser || item != assetItem {
			t.Error("asset lookup lost the actor or item")
		}
		h.resolves++
		if h.resolves > 1 && h.recheck != nil {
			return h.recheck(h.resolves)
		}
		if h.resolveErr != nil {
			return nil, h.resolveErr
		}
		var rows []domain.ItemImage
		for _, row := range h.rows {
			if row.Type == imageType && row.Index == index {
				rows = append(rows, row)
			}
		}
		if len(rows) == 0 {
			return nil, domain.ErrNotFound
		}
		return rows, nil
	}), assetRenderFunc(func(_ context.Context, value domain.ItemImage, request domain.ImageRequest) (ImageResult, error) {
		if request.Type != value.Type || request.Index != value.Index {
			t.Error("asset rendered for another slot")
		}
		h.rendered = append(h.rendered, value.ID)
		body := &imageTestBody{Reader: bytes.NewReader([]byte(value.ID))}
		h.bodies = append(h.bodies, body)
		result := ImageResult{Body: body, ContentType: "image/jpeg", Size: int64(len(value.ID))}
		if err := h.renderErr[value.ID]; err != nil {
			return result, err
		}
		return result, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func (h *assetHarness) get(t *testing.T, request domain.ImageRequest) (string, error) {
	t.Helper()
	result, err := h.service(t).Get(context.Background(), domain.Actor{UserID: assetUser, SessionID: assetSession}, assetItem, request)
	for i, body := range h.bodies {
		returned := err == nil && i == len(h.bodies)-1
		if body.closed == returned {
			t.Fatalf("body %d closed=%v while returned=%v", i, body.closed, returned)
		}
	}
	if err != nil {
		if result.Body != nil {
			t.Fatal("error returned a body")
		}
		return "", err
	}
	data := make([]byte, result.Size)
	_, _ = result.Body.Read(data)
	_ = result.Body.Close()
	return string(data), nil
}

func TestImageAssetsServeBestSourceAndFallBack(t *testing.T) {
	// The repository orders rows (locked first, then local > nfo > remote >
	// embedded); the service must try them in that order.
	rows := []domain.ItemImage{assetRow("locked", domain.ImageSourceRemote, true), assetRow("local", domain.ImageSourceLocal, false),
		assetRow("nfo", domain.ImageSourceNFO, false)}
	h := &assetHarness{rows: rows}
	if got, err := h.get(t, domain.ImageRequest{}); err != nil || got != "locked" || h.legacyCalls != 0 || h.resolves != 2 {
		t.Fatal("best source", got, err, h.legacyCalls)
	}
	// A locked remote image whose bytes are not stored yields to the next
	// source; nothing is fetched by the request.
	h = &assetHarness{rows: rows, renderErr: map[string]error{"locked": domain.ErrNotFound, "local": domain.ErrImageUnavailable}}
	if got, err := h.get(t, domain.ImageRequest{}); err != nil || got != "nfo" || len(h.rendered) != 3 || h.legacyCalls != 0 {
		t.Fatal("fallback order", got, err, h.rendered)
	}
	// Every asset unusable: the Primary slot keeps the local poster.
	h = &assetHarness{rows: rows, renderErr: map[string]error{"locked": domain.ErrNotFound, "local": domain.ErrNotFound, "nfo": domain.ErrNotFound}}
	if got, err := h.get(t, domain.ImageRequest{}); err != nil || got != "legacy" || h.legacyCalls != 2 {
		t.Fatal("legacy fallback after assets", got, err, h.legacyCalls)
	}
	// A real processing answer is final and does not fall back.
	h = &assetHarness{rows: rows, renderErr: map[string]error{"locked": domain.ErrImageTooLarge}}
	if _, err := h.get(t, domain.ImageRequest{}); !errors.Is(err, domain.ErrImageTooLarge) || h.legacyCalls != 0 || len(h.rendered) != 1 {
		t.Fatal("final render error", err)
	}
	// Repository failure other than not found is final.
	h = &assetHarness{resolveErr: domain.ErrDatabase}
	if _, err := h.get(t, domain.ImageRequest{}); !errors.Is(err, domain.ErrDatabase) || h.legacyCalls != 0 {
		t.Fatal("repository failure", err)
	}
}

func TestImageAssetsTypesIndexesAndCompatibility(t *testing.T) {
	backdrop := assetRow("backdrop2", domain.ImageSourceLocal, false)
	backdrop.Type, backdrop.Index = "Backdrop", 2
	logo := assetRow("logo", domain.ImageSourceLocal, false)
	logo.Type = "Logo"
	h := &assetHarness{rows: []domain.ItemImage{backdrop, logo}}
	if got, err := h.get(t, domain.ImageRequest{Type: "Fanart", Index: 2}); err != nil || got != "backdrop2" {
		t.Fatal("Fanart alias and index", got, err)
	}
	h = &assetHarness{rows: []domain.ItemImage{backdrop, logo}}
	if got, err := h.get(t, domain.ImageRequest{Type: "Logo"}); err != nil || got != "logo" {
		t.Fatal("logo", got, err)
	}
	// Other types never fall back to the Primary poster.
	h = &assetHarness{rows: []domain.ItemImage{backdrop}}
	if _, err := h.get(t, domain.ImageRequest{Type: "Backdrop", Index: 1}); !errors.Is(err, domain.ErrNotFound) || h.legacyCalls != 0 {
		t.Fatal("missing gallery index", err)
	}
	// No rows at all: unchanged local Primary behaviour, including the
	// invisible-item case the fallback rechecks itself.
	h = &assetHarness{}
	if got, err := h.get(t, domain.ImageRequest{}); err != nil || got != "legacy" || h.legacyCalls != 2 {
		t.Fatal("compatibility fallback", got, err)
	}
	h = &assetHarness{}
	if _, err := h.get(t, domain.ImageRequest{Type: "Logo", Index: 1}); !errors.Is(err, domain.ErrInvalid) || h.resolves != 0 {
		t.Fatal("invalid slot reached repository", err)
	}
}

func TestImageAssetsRecheckBindingBeforeReturning(t *testing.T) {
	row := assetRow("remote", domain.ImageSourceRemote, false)
	for name, recheck := range map[string]func(int) ([]domain.ItemImage, error){
		"revoked": func(int) ([]domain.ItemImage, error) { return nil, domain.ErrNotFound },
		"content replaced": func(int) ([]domain.ItemImage, error) {
			changed := row
			changed.Content = &domain.ItemImageContent{SHA256: bytes.Repeat([]byte{9}, 32)}
			return []domain.ItemImage{changed}, nil
		},
		"path moved": func(int) ([]domain.ItemImage, error) {
			changed := assetRow("remote", domain.ImageSourceLocal, false)
			return []domain.ItemImage{changed}, nil
		},
		"row removed": func(int) ([]domain.ItemImage, error) {
			return []domain.ItemImage{assetRow("other", domain.ImageSourceLocal, false)}, nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := &assetHarness{rows: []domain.ItemImage{row}, recheck: recheck}
			if _, err := h.get(t, domain.ImageRequest{}); !errors.Is(err, domain.ErrNotFound) || h.legacyCalls != 0 {
				t.Fatal("served after binding changed", err)
			}
		})
	}
	h := &assetHarness{rows: []domain.ItemImage{row}, recheck: func(int) ([]domain.ItemImage, error) {
		locked := row
		locked.Locked = true // Lock changes do not alter the served bytes.
		return []domain.ItemImage{assetRow("local", domain.ImageSourceLocal, false), locked}, nil
	}}
	if got, err := h.get(t, domain.ImageRequest{}); err != nil || got != "remote" {
		t.Fatal("unchanged binding rejected", err)
	}
}
