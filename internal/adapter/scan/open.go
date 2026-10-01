package scan

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// openDirectory pins the configured root, checks each visible component without
// following its leaf link, then opens the directory through the same os.Root.
// Before/after identity comparisons reject observed replacements. They are not
// an atomic "no internal symlink was traversed" guarantee; os.Root provides the
// boundary against external symlink traversal even during pathname changes.
func openDirectory(ctx context.Context, absolute, relative string) (*os.File, error) {
	if ctx == nil {
		return nil, domain.ErrScanUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(absolute) || !utf8.ValidString(absolute) || hasControl(absolute) {
		return nil, domain.ErrScanUnavailable
	}
	if !supportedPlatform() {
		return nil, domain.ErrScanUnavailable
	}
	absolute = filepath.Clean(absolute)
	if err := validRelative(relative); err != nil {
		return nil, err
	}
	beforeRoot, err := os.Lstat(absolute)
	if err != nil || beforeRoot.Mode().Type() != os.ModeDir {
		return nil, scanError(ctx, domain.ErrScanUnavailable)
	}
	// The terminal dot makes the supplied root a directory component on Unix,
	// avoiding a read-open of a FIFO substituted for the configured root itself.
	root, err := os.OpenRoot(absolute + string(os.PathSeparator) + ".")
	if err != nil {
		return nil, scanError(ctx, domain.ErrScanUnavailable)
	}
	defer root.Close()
	actualRoot, err := root.Stat(".")
	if err != nil || !os.SameFile(beforeRoot, actualRoot) {
		return nil, scanError(ctx, domain.ErrScanUnavailable)
	}
	var prefixes []string
	var identities []os.FileInfo
	if relative != "." {
		prefix := ""
		for _, component := range strings.Split(relative, "/") {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if prefix != "" {
				prefix += "/"
			}
			prefix += component
			info, err := root.Lstat(filepath.FromSlash(prefix))
			if err != nil || info.Mode().Type() != os.ModeDir {
				return nil, scanError(ctx, domain.ErrScanUnavailable)
			}
			prefixes = append(prefixes, prefix)
			identities = append(identities, info)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Windows uses the terminal separator to enforce opening a directory. Linux
	// additionally uses O_DIRECTORY/O_NONBLOCK to reject special-file races.
	file, err := root.OpenFile(filepath.FromSlash(relative)+string(os.PathSeparator), directoryFlags(), 0)
	if err != nil {
		return nil, scanError(ctx, domain.ErrScanUnavailable)
	}
	valid := false
	defer func() {
		if !valid {
			_ = file.Close()
		}
	}()
	opened, err := file.Stat()
	expected := beforeRoot
	if len(identities) > 0 {
		expected = identities[len(identities)-1]
	}
	if err != nil || opened.Mode().Type() != os.ModeDir || !os.SameFile(expected, opened) {
		return nil, scanError(ctx, domain.ErrScanUnavailable)
	}
	for index, prefix := range prefixes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		info, err := root.Lstat(filepath.FromSlash(prefix))
		if err != nil || info.Mode().Type() != os.ModeDir || !os.SameFile(identities[index], info) {
			return nil, scanError(ctx, domain.ErrScanUnavailable)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	valid = true
	return file, nil
}
