//go:build linux || windows

package nfo

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func nativePreparationSourceFixture(t *testing.T, directory bool) domain.NFOItemScope {
	t.Helper()
	root := filepath.Join(t.TempDir(), "parent", "root")
	if err := os.MkdirAll(filepath.Join(root, "folder"), 0700); err != nil {
		t.Fatal("create owned native root")
	}
	kind, relative := "Movie", "folder/owned.mkv"
	if directory {
		kind, relative = "Series", "folder"
	} else if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(relative)), []byte("owned media"), 0600); err != nil {
		t.Fatal("create owned native media")
	}
	scope := nativePathScope(root, kind, relative, directory)
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(scope.Source.RelativePath)), []byte("<movie><title>owned</title></movie>"), 0600); err != nil {
		t.Fatal("create owned native NFO")
	}
	return scope
}

func TestNativePreparationSourceBindsMediaAncestorsAndOriginal(t *testing.T) {
	for _, directory := range []bool{false, true} {
		scope := nativePreparationSourceFixture(t, directory)
		source, receipt, err := readNativePreparationSource(context.Background(), scope, 1024)
		if err != nil || source == nil || domain.ValidateNFONativePreparationReceipt(receipt) != nil || receipt.RootIdentity() != source.nativeRoot.record || receipt.NFOFileIdentity() != source.nativeFile.record {
			t.Fatal("native preparation did not bind original source", err)
		}
		plan, err := planNativePreparationPaths(scope)
		if err != nil || len(receipt.AncestorIdentities()) != len(plan.ancestors) {
			t.Fatal("ancestor receipt incomplete")
		}
		file, err := os.Open(plan.media)
		if err != nil {
			t.Fatal("open owned media")
		}
		id, err := observeNFONativeIdentity(file)
		closeErr := file.Close()
		if err != nil || closeErr != nil || receipt.MediaIdentity() != id.record || id.record[2] != plan.mediaKind {
			t.Fatal("receipt differs from media object")
		}
		if !bytes.Equal(source.original, []byte("<movie><title>owned</title></movie>")) {
			t.Fatal("original bytes differ")
		}
	}
}

func TestNativePreparationSourceRejectsMissingWrongKindAndLinks(t *testing.T) {
	for _, change := range []string{"missing", "directory", "symlink", "ancestor_symlink", "nfo_symlink"} {
		t.Run(change, func(t *testing.T) {
			scope := nativePreparationSourceFixture(t, false)
			media := filepath.Join(scope.Source.RootPath, filepath.FromSlash(scope.MediaPath))
			target := media
			if change == "ancestor_symlink" {
				target = filepath.Dir(media)
			}
			if change == "nfo_symlink" {
				target = filepath.Join(scope.Source.RootPath, filepath.FromSlash(scope.Source.RelativePath))
			}
			if err := os.Rename(target, target+".retained"); err != nil {
				t.Fatal("retain owned object")
			}
			switch change {
			case "directory":
				if err := os.Mkdir(target, 0700); err != nil {
					t.Fatal("replace owned media with directory")
				}
			case "symlink", "ancestor_symlink", "nfo_symlink":
				if err := os.Symlink(target+".retained", target); err != nil {
					t.Skip("platform symlink privilege unavailable")
				}
			}
			if source, receipt, err := readNativePreparationSource(context.Background(), scope, 1024); err == nil || source != nil || !receipt.Empty() {
				t.Fatal("invalid native scope produced preparation source")
			}
		})
	}
}

func TestNativePreparationSourceRejectsReplacementDuringOriginalRead(t *testing.T) {
	for _, change := range []string{"media_missing", "media_same_bytes", "relative_ancestor", "absolute_ancestor", "cancel"} {
		t.Run(change, func(t *testing.T) {
			scope := nativePreparationSourceFixture(t, false)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			mutationBlocked := false
			reader := func(ctx context.Context, root, relative string, max int64) (*Source, error) {
				source, err := readNativeSource(ctx, root, relative, max)
				if err != nil {
					return nil, err
				}
				media := filepath.Join(root, filepath.FromSlash(scope.MediaPath))
				switch change {
				case "media_missing", "media_same_bytes":
					info, err := os.Stat(media)
					if err != nil {
						t.Fatal("stat owned media")
					}
					if err := os.Rename(media, media+".retained"); err != nil {
						t.Fatal("retain original media")
					}
					if change == "media_same_bytes" {
						if err := os.WriteFile(media, []byte("owned media"), 0600); err != nil {
							t.Fatal("create replacement media")
						}
						if err := os.Chtimes(media, info.ModTime(), info.ModTime()); err != nil {
							t.Fatal("restore media timestamp")
						}
					}
				case "relative_ancestor":
					folder := filepath.Dir(media)
					if err := os.Rename(folder, folder+".retained"); err != nil {
						if runtime.GOOS == "windows" && (errors.Is(err, syscall.Errno(5)) || errors.Is(err, syscall.Errno(32))) {
							mutationBlocked = true
							return source, nil
						}
						t.Fatal("retain relative ancestor")
					}
					if err := os.Mkdir(folder, 0700); err != nil {
						t.Fatal("replace relative ancestor")
					}
					for _, name := range []string{filepath.Base(media), filepath.Base(relative)} {
						if err := os.Rename(filepath.Join(folder+".retained", name), filepath.Join(folder, name)); err != nil {
							t.Fatal("preserve original file objects")
						}
					}
				case "absolute_ancestor":
					parent := filepath.Dir(root)
					if err := os.Rename(parent, parent+".retained"); err != nil {
						if runtime.GOOS == "windows" && (errors.Is(err, syscall.Errno(5)) || errors.Is(err, syscall.Errno(32))) {
							mutationBlocked = true
							return source, nil
						}
						t.Fatal("retain absolute ancestor")
					}
					if err := os.Mkdir(parent, 0700); err != nil {
						t.Fatal("replace absolute ancestor")
					}
					if err := os.Rename(filepath.Join(parent+".retained", filepath.Base(root)), root); err != nil {
						t.Fatal("preserve configured root object")
					}
				case "cancel":
					cancel()
				}
				return source, nil
			}
			source, receipt, err := readNativePreparationSourceWith(ctx, scope, 1024, reader)
			if mutationBlocked {
				if err != nil || source == nil || receipt.Empty() {
					t.Fatal("unchanged scope rejected after platform prevented rename")
				}
				t.Log("platform held directory handles prevented ancestor replacement; no replacement was performed")
				return
			}
			if err == nil || source != nil || !receipt.Empty() {
				t.Fatal("scope changed during read but preparation survived")
			}
			if change == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal("cancellation result differs")
			}
		})
	}
}
