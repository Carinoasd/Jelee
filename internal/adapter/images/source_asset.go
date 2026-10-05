package images

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/MoYuanCN/Jelee/internal/adapter/probe"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// assetBinding is the file an item_images row names: a library root plus a
// relative image path, both from the authorized catalog row.
type assetBinding struct {
	rootPath, relative string
	root, image        os.FileInfo
}

// itemImageFile reports whether a row is read from a library file. Embedded
// rows name the media file, not an image; they are served from the store.
func itemImageFile(value domain.ItemImage) bool {
	return value.RootID != "" && (value.SourceKind == domain.ImageSourceLocal || value.SourceKind == domain.ImageSourceNFO)
}

func validItemImageFile(value domain.ItemImage) bool {
	return itemImageFile(value) && domain.ValidID(value.ID) && domain.ValidID(value.ItemID) && domain.ValidID(value.LibraryID) &&
		domain.ValidID(value.RootID) && filepath.IsAbs(value.RootPath) && len(value.RootPath) <= 4096 && utf8.ValidString(value.RootPath) &&
		!strings.ContainsFunc(value.RootPath, unicode.IsControl) && domain.ValidItemImageRelativePath(value.RelativePath) &&
		domain.ValidItemImageFileName(value.RelativePath)
}

// stageItemImage copies one library image file named by an item_images row
// into private scratch, like stageLocalPrimary but without a directory scan:
// the row already selected the file. The same reopen-and-rehash Verify binds
// the staged bytes to the current file.
func stageItemImage(ctx context.Context, value domain.ItemImage, tempRoot string, maxSourceBytes int64) (result *stagedImage, resultErr error) {
	if ctx == nil || maxSourceBytes < 1 || maxSourceBytes > 64<<20 || !validItemImageFile(value) {
		return nil, domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	temp, err := imageTempRoot(value.RootPath, tempRoot)
	if err != nil {
		return nil, imageSourceError(ctx, err)
	}
	binding := &assetBinding{rootPath: filepath.Clean(value.RootPath), relative: value.RelativePath}
	staged := &stagedImage{ctx: ctx, temp: temp, asset: binding}
	defer func() {
		if resultErr != nil {
			if staged.Close() != nil && ctx.Err() == nil {
				resultErr = domain.ErrImageUnavailable
			}
			result = nil
		}
	}()
	binding.root, err = os.Lstat(binding.rootPath)
	if err != nil || !binding.root.IsDir() {
		return nil, imageSourceError(ctx, err)
	}
	input, err := probe.Open(ctx, domain.ProbeSource{RootPath: binding.rootPath, RelativePath: binding.relative})
	if err != nil {
		return nil, imageSourceError(ctx, err)
	}
	defer func() {
		if input.Close() != nil && resultErr == nil {
			resultErr = domain.ErrImageUnavailable
			_ = staged.Close()
			result = nil
		}
	}()
	binding.image, err = input.Stdin().Stat()
	if err != nil || !binding.image.Mode().IsRegular() {
		return nil, imageSourceError(ctx, err)
	}
	if binding.image.Size() <= 0 || binding.image.Size() > maxSourceBytes {
		return nil, domain.ErrImageTooLarge
	}
	staged.size = binding.image.Size()
	if err := staged.copyFrom(ctx, input.Stdin(), maxSourceBytes); err != nil {
		return nil, err
	}
	if err := staged.Verify(ctx); err != nil {
		return nil, err
	}
	key := sha256.New()
	for _, part := range []string{"item-image-v1", value.ID, value.ItemID, value.LibraryID, value.SourceKind, value.RootID, binding.rootPath, binding.relative} {
		imageHashString(key, part)
	}
	var numbers [16]byte
	binary.BigEndian.PutUint64(numbers[:8], uint64(staged.size))
	binary.BigEndian.PutUint64(numbers[8:], uint64(binding.image.ModTime().UnixNano()))
	_, _ = key.Write(numbers[:])
	_, _ = key.Write(staged.content[:])
	copy(staged.key[:], key.Sum(nil))
	return staged, nil
}

func (s *stagedImage) verifyAsset(ctx context.Context) error {
	binding := s.asset
	root, err := os.Lstat(binding.rootPath)
	if err != nil || !root.IsDir() || !os.SameFile(binding.root, root) {
		return imageSourceError(ctx, err)
	}
	if err := rehashImageFile(ctx, binding.rootPath, binding.relative, binding.image, s.size, s.content); err != nil {
		return err
	}
	return ctx.Err()
}
