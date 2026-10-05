package images

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// VariantProber answers the consistency checker's image variant probes
// (G50.3) without opening the store: it never rebuilds the index, removes,
// moves or reads a file. Each probe pins the store directory with an
// os.Root, reads the variants directory to find the live generation and
// reads the metadata of one file. It holds nothing open between probes.
type VariantProber struct {
	root string
}

// NewVariantProber checks the configured store path lexically; the store
// itself may not exist yet.
func NewVariantProber(storeRoot string) (*VariantProber, error) {
	if !validStorePath(storeRoot) {
		return nil, domain.ErrImageUnavailable
	}
	return &VariantProber{root: filepath.Clean(storeRoot)}, nil
}

// liveGeneration is the highest generation directory, as the store chooses
// at startup and after every clear.
func liveGeneration(root *os.Root) (uint64, error) {
	directory, err := root.Open(storeVariantsDir)
	if err != nil {
		return 0, domain.ErrImageUnavailable
	}
	defer func() { _ = directory.Close() }()
	var live uint64
	for {
		entries, err := directory.ReadDir(256)
		for _, entry := range entries {
			if generation, ok := storeParseGeneration(entry.Name()); ok && entry.Type() == os.ModeDir {
				live = max(live, generation)
			}
		}
		if err != nil {
			break
		}
	}
	if live == 0 {
		return 0, domain.ErrImageUnavailable
	}
	return live, nil
}

// VariantExists reports whether the live generation holds the variant named
// "<source sha256 hex>/<variant key hex>". A clear between reading the index
// and probing moves every variant to a newer generation, so the generation
// is read for every probe.
func (p *VariantProber) VariantExists(ctx context.Context, object string) (bool, error) {
	if ctx == nil || p == nil {
		return false, domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	source, variant, ok := strings.Cut(object, "/")
	if !ok || !storeHexName(source, 64) || !storeHexName(variant, 64) {
		return false, domain.ErrInvalid
	}
	info, err := os.Lstat(p.root)
	if err != nil || info.Mode().Type() != os.ModeDir {
		return false, domain.ErrImageUnavailable
	}
	root, err := os.OpenRoot(p.root + string(os.PathSeparator) + ".")
	if err != nil {
		return false, domain.ErrImageUnavailable
	}
	defer func() { _ = root.Close() }()
	live, err := liveGeneration(root)
	if err != nil {
		return false, err
	}
	info, err = root.Lstat(filepath.Join(storeGenerationDir(live), source, variant))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	case err != nil:
		return false, domain.ErrImageUnavailable
	}
	return info.Mode().IsRegular(), nil
}
