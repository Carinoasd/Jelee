//go:build linux || windows

package ignoresource

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/platform/ignore"
)

func TestNativeScanDoesNotEnterExcludedDirectory(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	writeRule(t, root, ".jeleeignore", "skip/\n")
	writeRule(t, root, "skip/.jeleeignore", strings.Repeat("x", ignore.MaxSourceBytes+1))
	called := false
	err := NewResolver().ScanDirectory(context.Background(), root, "skip", ignore.Options{}, func(ScanBatch) error { called = true; return nil })
	if err != ErrChanged || called {
		t.Fatal("excluded directory entered or emitted", err)
	}
}

func TestNativeScanBoundedMetadataAndRuleChain(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	writeRule(t, root, ".jeleeignore", "*.tmp\n")
	writeRule(t, root, "child/.jeleeignore", "!keep.tmp\n")
	for i := 0; i < 257; i++ {
		writeRule(t, root, fmt.Sprintf("child/f-%03d.tmp", i), "1234567")
	}
	writeRule(t, root, "child/keep.tmp", "12")
	count, excluded, kept, done := 0, 0, 0, 0
	var identity [32]byte
	err := NewResolver().ScanDirectory(context.Background(), root, "child", ignore.Options{}, func(b ScanBatch) error {
		if len(b.Entries) > 128 || len(b.Proofs) != 2 {
			t.Fatal("unbounded/missing proof batch")
		}
		if identity == ([32]byte{}) {
			identity = b.Proofs[1].Identity
		} else if identity != b.Proofs[1].Identity {
			t.Fatal("enumeration identity changed")
		}
		if b.Proofs[1].ParentIdentity != b.Proofs[0].Identity {
			t.Fatal("broken parent proof")
		}
		for _, e := range b.Entries {
			count++
			if e.Match.Outcome == ignore.Exclude {
				excluded++
				if e.Size != 7 {
					t.Fatal("metadata resolved outside held directory")
				}
			}
			if e.Path == "child/keep.tmp" {
				kept++
				if e.Match.Outcome == ignore.Exclude || e.Size != 2 {
					t.Fatal("nested negation/metadata", e)
				}
			}
		}
		if b.Done {
			done++
			if b.Skipped != 0 || len(b.Entries) != 0 {
				t.Fatal("invalid completion")
			}
		}
		// Callback owns its proof values; mutation must not poison revalidation.
		b.Proofs[1].Identity[0]++
		return nil
	})
	if err != nil || count != 259 || excluded != 257 || kept != 1 || done != 1 {
		t.Fatal("native filtered enumeration", count, excluded, kept, done, err)
	}
}

func TestNativeScanChangedRuleOrDirectoryNeverCompletes(t *testing.T) {
	for _, mode := range []string{"rule", "directory", "root"} {
		t.Run(mode, func(t *testing.T) {
			base := filepath.Clean(t.TempDir())
			root := filepath.Join(base, "library")
			writeRule(t, root, "child/.jeleeignore", "*.tmp\n")
			writeRule(t, root, "child/a.mkv", "data")
			mutated, done := false, false
			relative := "child"
			// Windows prevents renaming an ancestor while a descendant handle
			// remains open. Exercise replacement of the enumerated root itself;
			// the directory case separately replaces a held nested directory.
			if mode == "root" {
				relative = "."
			}
			err := NewResolver().ScanDirectory(context.Background(), root, relative, ignore.Options{}, func(b ScanBatch) error {
				if b.Done {
					done = true
					return nil
				}
				if mutated {
					return nil
				}
				mutated = true
				switch mode {
				case "rule":
					writeRule(t, root, "child/.jeleeignore", "*.mkv\n")
				case "directory":
					if err := os.Rename(filepath.Join(root, "child"), filepath.Join(root, "old")); err != nil {
						t.Fatal(err)
					}
					writeRule(t, root, "child/.jeleeignore", "*.tmp\n")
				case "root":
					if err := os.Rename(root, filepath.Join(base, "old-root")); err != nil {
						t.Fatal(err)
					}
					writeRule(t, root, "child/.jeleeignore", "*.tmp\n")
				}
				return nil
			})
			if !mutated || done || err != ErrChanged {
				t.Fatal("changed sources completed", done, err)
			}
		})
	}
}

func TestNativeScanCancellationAndCallbackFailure(t *testing.T) {
	for _, mode := range []string{"cancel", "callback"} {
		t.Run(mode, func(t *testing.T) {
			root := filepath.Clean(t.TempDir())
			writeRule(t, root, "a.mkv", "data")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sentinel := errors.New("private callback error")
			done := false
			err := NewResolver().ScanDirectory(ctx, root, ".", ignore.Options{}, func(b ScanBatch) error {
				if b.Done {
					done = true
				}
				if mode == "cancel" {
					cancel()
					return nil
				}
				return sentinel
			})
			want := sentinel
			if mode == "cancel" {
				want = context.Canceled
			}
			if err != want || done {
				t.Fatal("cancellation/callback did not stop completion", err)
			}
		})
	}
}

func TestNativeDirectoryAbsenceAndBoundedReader(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	writeRule(t, root, "file", "content")
	d, err := openNativeRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	r := d.(scanDirectory)
	child, absent, err := r.OpenDirectoryOrAbsent("missing")
	if err != nil || !absent || child != nil {
		t.Fatal("explicit missing child", err)
	}
	child, absent, err = r.OpenDirectoryOrAbsent("file")
	if err == nil || absent || child != nil {
		t.Fatal("nondirectory represented as absence", err)
	}
	for _, name := range []string{"../missing", ".", "a/b"} {
		if _, absent, err = r.OpenDirectoryOrAbsent(name); err != ErrInvalid || absent {
			t.Fatal("invalid component", err)
		}
	}
	entries, err := r.ReadEntries()
	if err != nil || len(entries) != 1 || entries[0].name != "file" || entries[0].state.size != 7 {
		t.Fatal("held metadata", err)
	}
	entries, err = r.ReadEntries()
	if err != io.EOF || len(entries) != 0 {
		t.Fatal("reader EOF", err)
	}
	if err = d.Close(); err != nil {
		t.Fatal(err)
	}
	if _, absent, err = r.OpenDirectoryOrAbsent("missing"); err == nil || absent {
		t.Fatal("closed parent represented as absence", err)
	}
}
