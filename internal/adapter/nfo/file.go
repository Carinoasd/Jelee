package nfo

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ReadFile opens only a regular file within the administrator-owned root.
// The relative path uses slash separators. Errors never contain either path.
func ReadFile(ctx context.Context, rootAbs, relativeSlash string, maxBytes int64) (*Document, error) {
	if ctx == nil || maxBytes < 1 || maxBytes > MaxAllowedBytes {
		return nil, ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(rootAbs) || !fs.ValidPath(relativeSlash) || relativeSlash == "." || strings.ContainsAny(relativeSlash, "\\:\x00") {
		return nil, ErrNotFound
	}
	relative := filepath.Clean(filepath.FromSlash(relativeSlash))
	if !filepath.IsLocal(relative) {
		return nil, ErrNotFound
	}
	root, err := os.OpenRoot(rootAbs)
	if err != nil {
		return nil, ErrNotFound
	}
	defer root.Close()
	file, err := root.OpenFile(relative, readOnlyFlags(), 0)
	if err != nil {
		return nil, ErrNotFound
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, ErrNotFound
	}
	if info.Size() > maxBytes {
		return nil, ErrTooLarge
	}
	closed := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(closed); _ = file.Close() })
	defer func() {
		if !stop() {
			<-closed
		}
	}()
	return Read(ctx, file, maxBytes)
}

// IsNFOName recognizes only a filename, including movie.nfo, tvshow.nfo,
// season.nfo and arbitrary video/episode stems, without guessing media kind.
func IsNFOName(name string) bool {
	return len(name) > 4 && !strings.ContainsAny(name, "/\\:\x00") && strings.EqualFold(filepath.Ext(name), ".nfo")
}
