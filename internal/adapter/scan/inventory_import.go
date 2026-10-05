package scan

import (
	"context"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"os"
	"path/filepath"
	"strings"
)

func (*Scanner) VerifyInventoryImport(ctx context.Context, source domain.InventoryImportSource) error {
	if ctx == nil {
		return domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validInventoryImportFile(ctx, source) {
		if err := ctx.Err(); err != nil {
			return err
		}
		return domain.ErrConflict
	}
	return nil
}
func validInventoryImportFile(ctx context.Context, source domain.InventoryImportSource) bool {
	if ctx == nil || ctx.Err() != nil || !filepath.IsAbs(source.RootPath) || domain.ImportVideoContentType(source.Path) == "" {
		return false
	}
	info, err := os.Lstat(source.RootPath)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	root, err := os.OpenRoot(source.RootPath)
	if err != nil {
		return false
	}
	defer root.Close()
	parts := strings.Split(source.Path, "/")
	for i := range parts {
		info, err = root.Lstat(strings.Join(parts[:i+1], "/"))
		if err != nil || info.Mode()&os.ModeSymlink != 0 || i < len(parts)-1 && !info.IsDir() {
			return false
		}
	}
	return ctx.Err() == nil && info.Mode().IsRegular() && info.Size() == source.Size && info.ModTime().UnixNano() == source.ModifiedUnixNano
}
