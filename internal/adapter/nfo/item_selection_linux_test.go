package nfo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestItemNFOSelectionRejectsCaseCollisionAndSymlinks(t *testing.T) {
	for _, mode := range []string{"collision", "candidate-link", "parent-link", "media-link", "permission"} {
		t.Run(mode, func(t *testing.T) {
			reader, scope := itemSelectionFixture(t, "Movie")
			folder := filepath.Join(scope.Source.RootPath, "folder")
			switch mode {
			case "collision":
				for _, name := range []string{"Film.nfo", "FILM.NFO"} {
					if err := os.WriteFile(filepath.Join(folder, name), []byte("<movie><title>Local</title></movie>"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			case "candidate-link":
				if err := os.Symlink("Film.mkv", filepath.Join(folder, "Film.nfo")); err != nil {
					t.Fatal(err)
				}
			case "parent-link":
				if err := os.Symlink("folder", filepath.Join(scope.Source.RootPath, "linked")); err != nil {
					t.Fatal(err)
				}
				scope.MediaPath = "linked/Film.mkv"
				scope.Source.RelativePath = "linked/Film.nfo"
			case "media-link":
				if err := os.Rename(filepath.Join(folder, "Film.mkv"), filepath.Join(folder, "original.mkv")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("original.mkv", filepath.Join(folder, "Film.mkv")); err != nil {
					t.Fatal(err)
				}
			case "permission":
				if os.Geteuid() == 0 {
					t.Skip("mode 000 permission denial requires an unprivileged process")
				}
				if err := os.Chmod(folder, 0000); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := os.Chmod(folder, 0700); err != nil {
						t.Error(err)
					}
				})
			}
			if value, err := reader.SelectItemNFO(context.Background(), scope); !errors.Is(err, domain.ErrNFOInputUnavailable) || value.RelativePath != "" {
				t.Fatal("unsafe or unavailable source was treated as absent", err)
			}
		})
	}
}

func TestItemNFOSelectionDirectoryBoundCannotProveAbsence(t *testing.T) {
	reader, scope := itemSelectionFixture(t, "Movie")
	for i := 0; i < maxItemDirectoryEntries; i++ {
		if err := os.WriteFile(filepath.Join(scope.Source.RootPath, "folder", fmt.Sprintf("unrelated_%05d", i)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if value, err := reader.SelectItemNFO(context.Background(), scope); !errors.Is(err, domain.ErrNFOSourceLimit) || value.RelativePath != "" {
		t.Fatal("bounded incomplete listing was treated as absence", err)
	}
}
