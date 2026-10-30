package app

import (
	"context"
	"io"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// WithManagement adds the administrator image API (G40.10, G40.11). The
// ingester is required for uploads and the cleaner clears the variants of
// content nothing references any more; either may be nil, for example
// without a persistent image store.
func (s *Images) WithManagement(manager ItemImageManager, ingester ItemImageIngester, cleaner ImageVariantCleaner) (*Images, error) {
	if s == nil || manager == nil {
		return nil, domain.ErrInvalid
	}
	next := *s
	next.manager, next.ingester, next.cleaner = manager, ingester, cleaner
	return &next, nil
}

// manageable validates the common arguments; Fanart is an alias of Backdrop.
func (s *Images) manageable(ctx context.Context, actor domain.Actor, item, imageType string, index int) (string, error) {
	if s == nil || ctx == nil || !domain.ValidID(actor.UserID) || !domain.ValidID(actor.SessionID) {
		return "", domain.ErrInvalid
	}
	if s.manager == nil {
		return "", domain.ErrImageUnavailable
	}
	if imageType == "Fanart" {
		imageType = "Backdrop"
	}
	if !domain.ValidItemImageSlot(imageType, index) {
		return "", domain.ErrInvalid
	}
	if !domain.ValidID(item) {
		return "", domain.ErrNotFound
	}
	return imageType, ctx.Err()
}

// ItemImages lists every image row of a visible item for an administrator.
func (s *Images) ItemImages(ctx context.Context, actor domain.Actor, item string) ([]domain.ItemImage, error) {
	if _, err := s.manageable(ctx, actor, item, "Primary", 0); err != nil {
		return nil, err
	}
	return s.manager.ListItemImages(ctx, actor, item)
}

// UploadImage verifies an uploaded image, stores it and makes it the slot's
// locked upload row. Replacing an occupied slot needs confirm. The slot is
// checked before the body is read, so a refused request costs no decoding;
// the repository checks again atomically with the write.
func (s *Images) UploadImage(ctx context.Context, actor domain.Actor, item, imageType string, index int, body io.Reader, size int64, confirm bool) (domain.ItemImageChange, error) {
	imageType, err := s.manageable(ctx, actor, item, imageType, index)
	if err != nil {
		return domain.ItemImageChange{}, err
	}
	if body == nil || size < 1 {
		return domain.ItemImageChange{}, domain.ErrInvalid
	}
	rows, err := s.manager.ListItemImages(ctx, actor, item)
	if err != nil {
		return domain.ItemImageChange{}, err
	}
	if !confirm && slotOccupied(rows, imageType, index) {
		return domain.ItemImageChange{}, domain.ErrConfirmationRequired
	}
	if s.ingester == nil {
		return domain.ItemImageChange{}, domain.ErrImageUnavailable
	}
	content, err := s.ingester.IngestItemImage(ctx, body, size)
	if err != nil {
		return domain.ItemImageChange{}, err
	}
	change, err := s.manager.UploadItemImage(ctx, actor, domain.ItemImageInput{ItemID: item, Type: imageType, Index: index, Content: &content}, confirm)
	if err != nil {
		return domain.ItemImageChange{}, err
	}
	s.clearReleased(ctx, change.Released)
	return change, nil
}

// DeleteImages removes the references of one slot, of one source kind or of
// every kind when sourceKind is empty. It always needs confirm. Library
// files and stored originals stay; only generated variants of content that
// nothing references any more are cleared.
func (s *Images) DeleteImages(ctx context.Context, actor domain.Actor, item, imageType string, index int, sourceKind string, confirm bool) (domain.ItemImageChange, error) {
	imageType, err := s.manageable(ctx, actor, item, imageType, index)
	if err != nil {
		return domain.ItemImageChange{}, err
	}
	if sourceKind != "" && !domain.ValidItemImageSourceKind(sourceKind) {
		return domain.ItemImageChange{}, domain.ErrInvalid
	}
	if !confirm {
		return domain.ItemImageChange{}, domain.ErrConfirmationRequired
	}
	change, err := s.manager.DeleteItemImages(ctx, actor, item, imageType, index, sourceKind)
	if err != nil {
		return domain.ItemImageChange{}, err
	}
	s.clearReleased(ctx, change.Released)
	return change, nil
}

// SetImageLock locks or unlocks the slot's row of one source kind. Locking
// hands the slot lock over from any other source; refreshes never rewrite a
// locked row (G40.10).
func (s *Images) SetImageLock(ctx context.Context, actor domain.Actor, item, imageType string, index int, sourceKind string, locked bool) (domain.ItemImage, error) {
	imageType, err := s.manageable(ctx, actor, item, imageType, index)
	if err != nil {
		return domain.ItemImage{}, err
	}
	if !domain.ValidItemImageSourceKind(sourceKind) {
		return domain.ItemImage{}, domain.ErrInvalid
	}
	return s.manager.SetItemImageLock(ctx, actor, item, imageType, index, sourceKind, locked)
}

// clearReleased is best effort: the change is committed, and variants are
// derived data that the LRU pass and repair also remove.
func (s *Images) clearReleased(ctx context.Context, released [][32]byte) {
	if s.cleaner == nil {
		return
	}
	for _, content := range released {
		if ctx.Err() != nil {
			return
		}
		_ = s.cleaner.ClearItemImageVariants(ctx, content)
	}
}

func slotOccupied(rows []domain.ItemImage, imageType string, index int) bool {
	for _, row := range rows {
		if row.Type == imageType && row.Index == index {
			return true
		}
	}
	return false
}
