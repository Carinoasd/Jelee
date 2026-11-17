package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type fakeImageManager struct {
	rows      []domain.ItemImage
	listErr   error
	uploads   []domain.ItemImageInput
	confirms  []bool
	deletes   []string
	locks     []bool
	change    domain.ItemImageChange
	changeErr error
}

func (f *fakeImageManager) ListItemImages(context.Context, domain.Actor, string) ([]domain.ItemImage, error) {
	return f.rows, f.listErr
}

func (f *fakeImageManager) UploadItemImage(_ context.Context, _ domain.Actor, in domain.ItemImageInput, confirm bool) (domain.ItemImageChange, error) {
	f.uploads, f.confirms = append(f.uploads, in), append(f.confirms, confirm)
	return f.change, f.changeErr
}

func (f *fakeImageManager) DeleteItemImages(_ context.Context, _ domain.Actor, _, imageType string, _ int, kind string) (domain.ItemImageChange, error) {
	f.deletes = append(f.deletes, imageType+"/"+kind)
	return f.change, f.changeErr
}

func (f *fakeImageManager) SetItemImageLock(_ context.Context, _ domain.Actor, _, imageType string, index int, kind string, locked bool) (domain.ItemImage, error) {
	f.locks = append(f.locks, locked)
	return domain.ItemImage{Type: imageType, Index: index, SourceKind: kind, Locked: locked}, nil
}

type fakeImageIngester struct {
	calls int
	err   error
}

func (f *fakeImageIngester) IngestItemImage(_ context.Context, body io.Reader, size int64) (domain.ItemImageContent, error) {
	f.calls++
	if f.err != nil {
		return domain.ItemImageContent{}, f.err
	}
	data, _ := io.ReadAll(body)
	if int64(len(data)) != size {
		return domain.ItemImageContent{}, domain.ErrInvalid
	}
	return domain.ItemImageContent{SHA256: bytes.Repeat([]byte{7}, 32), Format: "png", Bytes: size}, nil
}

type fakeVariantCleaner struct{ cleared [][32]byte }

func (f *fakeVariantCleaner) ClearItemImageVariants(_ context.Context, content [32]byte) error {
	f.cleared = append(f.cleared, content)
	return nil
}

const adminImageItem = "10000000-0000-4000-8000-0000000000d1"

func adminImageBase() (*Images, error) {
	return NewImages(imageRepoFunc(func(context.Context, domain.Actor, string) (domain.LocalImageSource, error) {
		return domain.LocalImageSource{}, domain.ErrNotFound
	}), imageRenderFunc(func(context.Context, domain.LocalImageSource, domain.ImageRequest) (ImageResult, error) {
		return ImageResult{}, domain.ErrNotFound
	}))
}

func assetActor() domain.Actor { return domain.Actor{UserID: assetUser, SessionID: assetSession} }

func adminImages(t *testing.T, manager *fakeImageManager, ingester ItemImageIngester, cleaner ImageVariantCleaner) *Images {
	t.Helper()
	base, err := adminImageBase()
	if err != nil {
		t.Fatal(err)
	}
	images, err := base.WithManagement(manager, ingester, cleaner)
	if err != nil {
		t.Fatal(err)
	}
	return images
}

func TestImagesUploadNeedsConfirmationForAnOccupiedSlot(t *testing.T) {
	manager := &fakeImageManager{rows: []domain.ItemImage{{Type: "Backdrop", Index: 2, SourceKind: domain.ImageSourceLocal}}}
	ingester, cleaner := &fakeImageIngester{}, &fakeVariantCleaner{}
	images := adminImages(t, manager, ingester, cleaner)
	ctx, actor, body := context.Background(), assetActor(), []byte("image")
	// Fanart is Backdrop: slot 2 is occupied, so nothing is read or stored.
	if _, err := images.UploadImage(ctx, actor, adminImageItem, "Fanart", 2, bytes.NewReader(body), int64(len(body)), false); !errors.Is(err, domain.ErrConfirmationRequired) {
		t.Fatal("replacement without confirmation", err)
	}
	if ingester.calls != 0 || len(manager.uploads) != 0 {
		t.Fatal("an unconfirmed replacement was ingested")
	}
	// Another slot is free.
	released := [32]byte{9}
	manager.change = domain.ItemImageChange{Created: true, Released: [][32]byte{released}}
	change, err := images.UploadImage(ctx, actor, adminImageItem, "Backdrop", 1, bytes.NewReader(body), int64(len(body)), false)
	if err != nil || !change.Created || ingester.calls != 1 || len(manager.uploads) != 1 || manager.uploads[0].Type != "Backdrop" || manager.uploads[0].Content == nil {
		t.Fatal("upload into a free slot", err)
	}
	if len(cleaner.cleared) != 1 || cleaner.cleared[0] != released {
		t.Fatal("released variants not cleared", cleaner.cleared)
	}
	if _, err := images.UploadImage(ctx, actor, adminImageItem, "Fanart", 2, bytes.NewReader(body), int64(len(body)), true); err != nil || !manager.confirms[1] {
		t.Fatal("confirmed replacement", err)
	}
	// A refused write keeps the variants.
	manager.changeErr = domain.ErrConfirmationRequired
	if _, err := images.UploadImage(ctx, actor, adminImageItem, "Primary", 0, bytes.NewReader(body), int64(len(body)), false); !errors.Is(err, domain.ErrConfirmationRequired) || len(cleaner.cleared) != 2 {
		t.Fatal("repository refusal", err, len(cleaner.cleared))
	}
}

func TestImagesUploadRefusals(t *testing.T) {
	ctx, actor, body := context.Background(), assetActor(), []byte("x")
	manager := &fakeImageManager{}
	for name, c := range map[string]struct {
		images        *Images
		item, kind    string
		index         int
		size          int64
		want          error
		wantIngestion bool
	}{
		"invalid slot":   {adminImages(t, manager, &fakeImageIngester{}, nil), adminImageItem, "Primary", 1, 1, domain.ErrInvalid, false},
		"unknown type":   {adminImages(t, manager, &fakeImageIngester{}, nil), adminImageItem, "Poster", 0, 1, domain.ErrInvalid, false},
		"invalid item":   {adminImages(t, manager, &fakeImageIngester{}, nil), "nope", "Primary", 0, 1, domain.ErrNotFound, false},
		"empty":          {adminImages(t, manager, &fakeImageIngester{}, nil), adminImageItem, "Primary", 0, 0, domain.ErrInvalid, false},
		"no store":       {adminImages(t, manager, nil, nil), adminImageItem, "Primary", 0, 1, domain.ErrImageUnavailable, false},
		"bad image":      {adminImages(t, manager, &fakeImageIngester{err: domain.ErrImageUnsupported}, nil), adminImageItem, "Primary", 0, 1, domain.ErrImageUnsupported, true},
		"no management":  {func() *Images { i, _ := adminImageBase(); return i }(), adminImageItem, "Primary", 0, 1, domain.ErrImageUnavailable, false},
		"hidden or gone": {adminImages(t, &fakeImageManager{listErr: domain.ErrNotFound}, &fakeImageIngester{}, nil), adminImageItem, "Primary", 0, 1, domain.ErrNotFound, false},
	} {
		before := len(manager.uploads)
		if _, err := c.images.UploadImage(ctx, actor, c.item, c.kind, c.index, bytes.NewReader(body), c.size, true); !errors.Is(err, c.want) {
			t.Fatalf("%s: %v, want %v", name, err, c.want)
		}
		if len(manager.uploads) != before {
			t.Fatal(name, "reached the repository")
		}
	}
	if _, err := (&Images{}).WithManagement(nil, nil, nil); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("management without a repository", err)
	}
}

func TestImagesDeleteAndLock(t *testing.T) {
	manager, cleaner := &fakeImageManager{}, &fakeVariantCleaner{}
	images := adminImages(t, manager, nil, cleaner)
	ctx, actor := context.Background(), assetActor()
	if _, err := images.DeleteImages(ctx, actor, adminImageItem, "Primary", 0, "", false); !errors.Is(err, domain.ErrConfirmationRequired) || len(manager.deletes) != 0 {
		t.Fatal("delete without confirmation", err)
	}
	if _, err := images.DeleteImages(ctx, actor, adminImageItem, "Primary", 0, "tmdb", true); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("unknown source", err)
	}
	manager.change = domain.ItemImageChange{Released: [][32]byte{{1}, {2}}}
	if _, err := images.DeleteImages(ctx, actor, adminImageItem, "Fanart", 0, domain.ImageSourceUpload, true); err != nil || manager.deletes[0] != "Backdrop/upload" {
		t.Fatal("delete", err, manager.deletes)
	}
	if len(cleaner.cleared) != 2 {
		t.Fatal("released variants not cleared")
	}
	manager.changeErr = domain.ErrNotFound
	if _, err := images.DeleteImages(ctx, actor, adminImageItem, "Primary", 0, "", true); !errors.Is(err, domain.ErrNotFound) || len(cleaner.cleared) != 2 {
		t.Fatal("missing slot", err)
	}
	value, err := images.SetImageLock(ctx, actor, adminImageItem, "Fanart", 3, domain.ImageSourceLocal, true)
	if err != nil || value.Type != "Backdrop" || value.Index != 3 || !value.Locked {
		t.Fatal("lock", err)
	}
	if _, err := images.SetImageLock(ctx, actor, adminImageItem, "Primary", 0, "", false); !errors.Is(err, domain.ErrInvalid) || len(manager.locks) != 1 {
		t.Fatal("lock without a source", err)
	}
	if rows, err := images.ItemImages(ctx, actor, adminImageItem); err != nil || rows != nil {
		t.Fatal("list", err)
	}
	if _, err := images.ItemImages(ctx, domain.Actor{}, adminImageItem); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("list without an actor", err)
	}
}
