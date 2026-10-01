package nfo

import (
	"context"
	"path/filepath"
	"strings"
)

// ReadFile opens only a regular file within the administrator-owned root.
// The relative path uses slash separators. Errors never contain either path.
func ReadFile(ctx context.Context, rootAbs, relativeSlash string, maxBytes int64) (*Document, error) {
	source, err := ReadSource(ctx, rootAbs, relativeSlash, maxBytes)
	if err != nil {
		return nil, err
	}
	return source.Parse(ctx)
}

// IsNFOName recognizes only a filename, including movie.nfo, tvshow.nfo,
// season.nfo and arbitrary video/episode stems, without guessing media kind.
func IsNFOName(name string) bool {
	return len(name) > 4 && !strings.ContainsAny(name, "/\\:\x00") && strings.EqualFold(filepath.Ext(name), ".nfo")
}
