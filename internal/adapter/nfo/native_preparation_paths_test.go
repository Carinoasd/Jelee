package nfo

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func nativePathScope(root, kind, relative string, directory bool) domain.NFOItemScope {
	const id = "11111111-1111-4111-8111-111111111111"
	s := domain.NFOItemScope{ItemID: id, LibraryID: id, SourceID: id, RootID: id, Kind: kind, Revision: 1, Generation: 1, RootGeneration: 1, Source: domain.NFOSource{RootPath: root}}
	if directory {
		s.DirectoryPath = relative
		s.Source.RelativePath, _ = domain.DirectoryNFOPath(relative, kind)
	} else {
		s.MediaPath = relative
		s.Source.RelativePath, _ = domain.AdjacentNFOPath(relative)
	}
	return s
}

func TestNativePreparationPathsIncludeAbsoluteAncestorsAndScopedKinds(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	for _, kind := range []string{"Movie", "HomeVideo", "Episode", "Series", "Season"} {
		for _, directory := range []bool{false, true} {
			if directory && kind != "Series" && kind != "Season" || !directory && kind == "Season" {
				continue
			}
			for _, relative := range []string{"folder/owned.mkv", "."} {
				if relative == "." && !directory {
					continue
				}
				scope := nativePathScope(root, kind, relative, directory)
				p, err := planNativePreparationPaths(scope)
				if err != nil || p.root != root || p.media != filepath.Join(root, filepath.FromSlash(relative)) || p.nfo != filepath.Join(root, filepath.FromSlash(scope.Source.RelativePath)) {
					t.Fatalf("valid scoped path plan rejected: kind=%s directory=%v rootTarget=%v valid=%v err=%v", kind, directory, relative == ".", domain.ValidNFOItemScope(scope), err)
				}
				wantKind := byte(1)
				if directory {
					wantKind = 2
				}
				if p.mediaKind != wantKind || !slices.Contains(p.ancestors, root) || !slices.Contains(p.ancestors, filepath.Dir(root)) || !slices.Contains(p.ancestors, filepath.Dir(p.nfo)) {
					t.Fatal("scope kind or relevant ancestor omitted")
				}
				if p.ancestors[len(p.ancestors)-1] != filepath.Dir(p.nfo) {
					t.Fatal("receipt ordering must end with the NFO parent for persisted plan binding")
				}
				seen := map[string]bool{}
				for _, ancestor := range p.ancestors {
					if seen[ancestor] {
						t.Fatal("duplicate ancestor")
					}
					seen[ancestor] = true
				}
				again, err := planNativePreparationPaths(scope)
				if err != nil || !slices.Equal(p.ancestors, again.ancestors) {
					t.Fatal("unstable receipt order")
				}
			}
		}
	}
}

func TestNativePreparationPathsRejectInvalidAndCombinedDepth(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	base, err := planNativePreparationPaths(nativePathScope(root, "Movie", "owned.mkv", false))
	if err != nil {
		t.Fatal("base plan rejected")
	}
	remaining := domain.NFONativePreparationMaxAncestors - len(base.ancestors)
	limit, err := planNativePreparationPaths(nativePathScope(root, "Movie", strings.Repeat("a/", remaining)+"owned.mkv", false))
	if err != nil || len(limit.ancestors) != domain.NFONativePreparationMaxAncestors {
		t.Fatal("exact combined limit rejected")
	}
	if limit.ancestors[len(limit.ancestors)-1] != filepath.Dir(limit.nfo) {
		t.Fatal("maximum depth receipt must still end with the NFO parent")
	}
	if p, err := planNativePreparationPaths(nativePathScope(root, "Movie", strings.Repeat("a/", remaining+1)+"owned.mkv", false)); err == nil || len(p.ancestors) != 0 {
		t.Fatal("combined limit plus one accepted")
	}
	for _, scope := range []domain.NFOItemScope{
		nativePathScope(root+string(filepath.Separator)+"..", "Movie", "owned.mkv", false),
		nativePathScope("relative", "Movie", "owned.mkv", false),
		nativePathScope(root, "Movie", "../owned.mkv", false),
		nativePathScope(root, "Movie", strings.Repeat("a/", 127)+"owned.mkv", false),
		nativePathScope(filepath.Join(root, strings.Repeat("a"+string(filepath.Separator), 128)), "Movie", "owned.mkv", false),
	} {
		if p, err := planNativePreparationPaths(scope); err == nil || len(p.ancestors) != 0 || p.root != "" {
			t.Fatal("invalid or excessive combined path depth accepted")
		}
	}
}
