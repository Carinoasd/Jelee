package scan

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// ConsistencyProber answers the consistency checker's file probes (G50.3).
// Each probe reads the metadata of one file through an os.Root pinned at the
// library root, so a link cannot lead it outside the root; it never opens or
// reads the file.
type ConsistencyProber struct{}

// FileExists reports whether a regular file exists at the root-relative
// path. A missing file or one replaced by another kind of entry is false; a
// root that cannot be opened, an unsafe path or a refused traversal is an
// error, which the checker reports as unconfirmed rather than missing.
func (ConsistencyProber) FileExists(ctx context.Context, rootPath, relative string) (bool, error) {
	if ctx == nil {
		return false, domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if !filepath.IsAbs(rootPath) || hasControl(rootPath) || relative == "." {
		return false, domain.ErrInvalid
	}
	if err := validRelative(relative); err != nil {
		return false, err
	}
	root, err := os.OpenRoot(filepath.Clean(rootPath))
	if err != nil {
		return false, domain.ErrScanUnavailable
	}
	defer func() { _ = root.Close() }()
	info, err := root.Lstat(filepath.FromSlash(relative))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	case err != nil:
		return false, domain.ErrScanIO
	}
	return info.Mode().IsRegular(), nil
}
