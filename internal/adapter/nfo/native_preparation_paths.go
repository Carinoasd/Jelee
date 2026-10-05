package nfo

import (
	"path/filepath"
	"slices"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// nativePreparationPaths fixes receipt ordering from resolver-owned scope.
// Paths are private; this plan alone does not authorize filesystem access.
type nativePreparationPaths struct {
	root, media, nfo string
	mediaKind        byte
	ancestors        []string
}

func (nativePreparationPaths) String() string   { return "nfo native scope paths (redacted)" }
func (nativePreparationPaths) GoString() string { return "nfo native scope paths (redacted)" }

func planNativePreparationPaths(scope domain.NFOItemScope) (nativePreparationPaths, error) {
	if !domain.ValidNFOItemScope(scope) || !filepath.IsAbs(scope.Source.RootPath) || filepath.Clean(scope.Source.RootPath) != filepath.FromSlash(scope.Source.RootPath) {
		return nativePreparationPaths{}, domain.ErrInvalid
	}
	p := nativePreparationPaths{root: filepath.Clean(scope.Source.RootPath), mediaKind: 1}
	relative := scope.MediaPath
	if scope.DirectoryPath != "" {
		relative, p.mediaKind = scope.DirectoryPath, 2
	}
	p.media = filepath.Join(p.root, filepath.FromSlash(relative))
	p.nfo = filepath.Join(p.root, filepath.FromSlash(scope.Source.RelativePath))
	// Include the absolute chain outside the configured root, then both
	// relative parent chains. Bound before appending and deduplicate in order.
	var rootChain []string
	for current := p.root; ; current = filepath.Dir(current) {
		if len(rootChain) == domain.NFONativePreparationMaxAncestors {
			return nativePreparationPaths{}, domain.ErrInvalid
		}
		rootChain = append(rootChain, current)
		if filepath.Dir(current) == current {
			break
		}
	}
	slices.Reverse(rootChain)
	p.ancestors = rootChain
	for _, target := range []string{p.media, p.nfo} {
		if target == p.root {
			continue
		}
		var chain []string
		for parent := filepath.Dir(target); parent != p.root; parent = filepath.Dir(parent) {
			if parent == filepath.Dir(parent) || len(chain) == domain.NFONativePreparationMaxAncestors {
				return nativePreparationPaths{}, domain.ErrInvalid
			}
			chain = append(chain, parent)
		}
		slices.Reverse(chain)
		for _, parent := range chain {
			if slices.Contains(p.ancestors, parent) {
				continue
			}
			if len(p.ancestors) == domain.NFONativePreparationMaxAncestors {
				return nativePreparationPaths{}, domain.ErrInvalid
			}
			p.ancestors = append(p.ancestors, parent)
		}
	}
	return p, nil
}
