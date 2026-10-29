package nfo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// The Linux file system lets one directory hold names that differ only by
// case, and lets a test make links and FIFOs without privileges.

func TestInspectNFOPathFindsEveryIllegalLocationOnLinux(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	mk := func(relative string) string {
		full := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		return full
	}
	write := func(relative string) {
		if err := os.WriteFile(mk(relative), []byte("<movie><title>T</title></movie>"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("real/target.nfo")
	if err := os.Symlink("target.nfo", mk("real/link.nfo")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", mk("linkdir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), mk("outside")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(mk("real/dir.nfo"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(mk("real/fifo.nfo"), 0o600); err != nil {
		t.Fatal(err)
	}
	write("real/Case.nfo")
	write("real/CASE.NFO")
	write("real/unique.nfo")
	write("real/Other.nfo")
	if err := os.Symlink("real/target.nfo", mk("dangling-parent.nfo")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ path, want string }{
		{"real/target.nfo", ""},
		{"real/unique.nfo", ""},
		{"real/link.nfo", domain.NFOPathSymlink},
		{"dangling-parent.nfo", domain.NFOPathSymlink},
		{"linkdir/target.nfo", domain.NFOPathElementSymlink},
		{"outside/anything.nfo", domain.NFOPathElementSymlink},
		{"real/dir.nfo", domain.NFOPathNotRegular},
		{"real/fifo.nfo", domain.NFOPathNotRegular},
		{"real/target.nfo/inside.nfo", domain.NFOPathNotRegular},
		{"real/Case.nfo", domain.NFOPathCaseCollision},
		{"real/CASE.NFO", domain.NFOPathCaseCollision},
		{"real/absent.nfo", ""},
	} {
		got, err := InspectNFOPath(ctx, root, tc.path)
		if err != nil || got != tc.want {
			t.Errorf("%s: %q %v, want %q", tc.path, got, err, tc.want)
		}
	}
	// A root that is itself a link is refused like a link on the way.
	linkRoot := filepath.Join(t.TempDir(), "root-link")
	if err := os.Symlink(root, linkRoot); err != nil {
		t.Fatal(err)
	}
	if got, err := InspectNFOPath(ctx, linkRoot, "real/target.nfo"); err != nil || got != domain.NFOPathElementSymlink {
		t.Fatalf("root link: %q %v", got, err)
	}
}

func TestItemSelectionRefusalsCarryTheirReasonWithoutLosingTheRefusal(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, scope *domain.NFOItemScope)
		want  string
	}{
		{"case collision", func(t *testing.T, scope *domain.NFOItemScope) {
			for _, name := range []string{"Film.nfo", "FILM.NFO"} {
				if err := os.WriteFile(filepath.Join(scope.Source.RootPath, "folder", name), []byte("<movie><title>Local</title></movie>"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
		}, domain.NFOPathCaseCollision},
		{"symlinked NFO", func(t *testing.T, scope *domain.NFOItemScope) {
			if err := os.Symlink("Film.mkv", filepath.Join(scope.Source.RootPath, "folder", "Film.nfo")); err != nil {
				t.Fatal(err)
			}
		}, domain.NFOPathSymlink},
		{"NFO is a directory", func(t *testing.T, scope *domain.NFOItemScope) {
			if err := os.Mkdir(filepath.Join(scope.Source.RootPath, "folder", "Film.nfo"), 0o700); err != nil {
				t.Fatal(err)
			}
		}, domain.NFOPathNotRegular},
		{"NFO is a FIFO", func(t *testing.T, scope *domain.NFOItemScope) {
			if err := syscall.Mkfifo(filepath.Join(scope.Source.RootPath, "folder", "Film.nfo"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, domain.NFOPathNotRegular},
		{"symlinked directory", func(t *testing.T, scope *domain.NFOItemScope) {
			if err := os.Symlink("folder", filepath.Join(scope.Source.RootPath, "linked")); err != nil {
				t.Fatal(err)
			}
			scope.MediaPath, scope.Source.RelativePath = "linked/Film.mkv", "linked/Film.nfo"
		}, domain.NFOPathElementSymlink},
		{"symlinked media", func(t *testing.T, scope *domain.NFOItemScope) {
			folder := filepath.Join(scope.Source.RootPath, "folder")
			if err := os.Rename(filepath.Join(folder, "Film.mkv"), filepath.Join(folder, "original.mkv")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("original.mkv", filepath.Join(folder, "Film.mkv")); err != nil {
				t.Fatal(err)
			}
		}, domain.NFOPathElementSymlink},
		{"media is a directory", func(t *testing.T, scope *domain.NFOItemScope) {
			folder := filepath.Join(scope.Source.RootPath, "folder")
			if err := os.Remove(filepath.Join(folder, "Film.mkv")); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(folder, "Film.mkv"), 0o700); err != nil {
				t.Fatal(err)
			}
		}, domain.NFOPathNotRegular},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader, scope := itemSelectionFixture(t, "Movie")
			tc.setup(t, &scope)
			// The rejection is intact for every existing caller...
			if _, err := reader.SelectItemNFO(ctx, scope); !errors.Is(err, domain.ErrNFOInputUnavailable) {
				t.Fatalf("selection no longer refuses: %v", err)
			}
			if _, err := reader.ObserveItemNFO(ctx, scope); !errors.Is(err, domain.ErrNFOInputUnavailable) {
				t.Fatalf("observation no longer refuses: %v", err)
			}
			// ...and the validation names the reason.
			got, err := reader.ValidateItemNFO(ctx, scope)
			if err != nil || got.Status != domain.NFOItemValidationPath || got.PathCode != tc.want {
				t.Fatalf("%+v %v, want %s", got, err, tc.want)
			}
		})
	}
}

func TestItemSelectionKeepsPlainUnavailabilityPlain(t *testing.T) {
	reader, scope := itemSelectionFixture(t, "Movie")
	if err := os.Remove(filepath.Join(scope.Source.RootPath, "folder", "Film.mkv")); err != nil {
		t.Fatal(err)
	}
	_, err := reader.ObserveItemNFO(context.Background(), scope)
	if !errors.Is(err, domain.ErrNFOInputUnavailable) || pathIssueCode(err) != "" {
		t.Fatalf("a missing media file is not a path issue: %v", err)
	}
	// A root that is a link is reported.
	reader, scope = itemSelectionFixture(t, "Movie")
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(scope.Source.RootPath, link); err != nil {
		t.Fatal(err)
	}
	scope.Source.RootPath = link
	if got, err := reader.ValidateItemNFO(context.Background(), scope); err != nil || got.PathCode != domain.NFOPathElementSymlink {
		t.Fatalf("%+v %v", got, err)
	}
}
