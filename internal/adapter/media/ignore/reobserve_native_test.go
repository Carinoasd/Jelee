//go:build linux || windows

package ignoresource

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/platform/ignore"
)

func TestBaselineObservationMissingAncestorAndLegacyBoundary(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	writeRule(t, root, ".jeleeignore", "*.tmp\n")
	r := NewResolver()
	for _, candidate := range []string{"gone/movie.mkv", "gone/deeper/movie.mkv", "gone/excluded.tmp"} {
		o, err := r.EvaluateBaseline(context.Background(), root, candidate, ignore.File, ignore.Options{})
		if err != nil {
			t.Fatal(err)
		}
		p := o.DirectoryProofs()
		if len(p) != 2 || !p[1].MissingDirectory || p[1].Directory != "gone" || p[1].ParentIdentity != p[0].Identity || p[1].Identity != ([32]byte{}) || p[1].RulePresent {
			t.Fatal("invalid missing ancestor proof")
		}
		if candidate == "gone/excluded.tmp" && o.Match.Outcome != ignore.Exclude {
			t.Fatal("ancestor rules lost after missing directory")
		}
		p[1].Directory = "poison"
		if o.DirectoryProofs()[1].Directory != "gone" {
			t.Fatal("missing proof shares caller memory")
		}
	}
	if _, err := r.Evaluate(context.Background(), root, "gone/movie.mkv", ignore.File, ignore.Options{}); err != ErrRead {
		t.Fatal("legacy strict reader changed", err)
	}
	a, err := r.EvaluateBaseline(context.Background(), root, "gone/a.mkv", ignore.File, ignore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := r.EvaluateBaseline(context.Background(), root, "other/a.mkv", ignore.File, ignore.Options{})
	if err != nil || a.Token() == b.Token() {
		t.Fatal("absence token omitted missing child", err)
	}
}

func TestBaselineObservationRejectsAppearanceAndUnsafeParent(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	writeRule(t, root, ".jeleeignore", "")
	r := NewResolver()
	opens := 0
	access := sourceAccess{compile: ignore.Compile, openRoot: func(s string) (directory, error) {
		opens++
		if opens == 2 {
			if err := os.Mkdir(filepath.Join(root, "gone"), 0700); err != nil {
				t.Fatal(err)
			}
		}
		return openNativeRoot(s)
	}}
	o, err := r.evaluateSources(context.Background(), root, "gone/movie.mkv", ignore.File, ignore.Options{}, access, true)
	if err != ErrChanged || len(o.DirectoryProofs()) != 0 || o.Token() != ([32]byte{}) {
		t.Fatal("new ancestor accepted as absent", err)
	}
	writeRule(t, root, "not-a-directory", "file")
	if o, err = r.EvaluateBaseline(context.Background(), root, "not-a-directory/movie.mkv", ignore.File, ignore.Options{}); err != ErrUnsafe || len(o.DirectoryProofs()) != 0 {
		t.Fatal("non-directory represented as absence", err)
	}
}

func TestReobserveDirectoryIncludesExcludedDescendantAndAbsence(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	writeRule(t, root, ".jeleeignore", "hidden/\n")
	writeRule(t, root, "hidden/.jeleeignore", "*.mkv\n")
	r := NewResolver()
	p, err := r.ReobserveDirectory(context.Background(), root, "hidden")
	if err != nil || len(p) != 2 || p[1].MissingDirectory || !p[1].RulePresent {
		t.Fatal("new rule hid retained source", err)
	}
	p, err = r.ReobserveDirectory(context.Background(), root, "hidden/gone/deeper")
	if err != nil || len(p) != 3 || p[2].Directory != "hidden/gone" || !p[2].MissingDirectory {
		t.Fatal("missing ancestor chain", err)
	}
	p, err = r.ReobserveDirectory(context.Background(), root, ".")
	if err != nil || len(p) != 1 || p[0].Directory != "." {
		t.Fatal("root observation", err)
	}
}

func TestReobserveDirectoryRejectsChangesBetweenPasses(t *testing.T) {
	for _, mode := range []string{"rule", "appearance", "leaf-appearance"} {
		t.Run(mode, func(t *testing.T) {
			root := filepath.Clean(t.TempDir())
			writeRule(t, root, ".jeleeignore", "*.tmp\n")
			if mode != "appearance" {
				if err := os.Mkdir(filepath.Join(root, "child"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			opens := 0
			p, err := NewResolver().reobserveDirectory(context.Background(), root, "child", func(s string) (directory, error) {
				opens++
				if opens == 2 {
					switch mode {
					case "rule":
						writeRule(t, root, ".jeleeignore", "*.mkv\n")
					case "appearance":
						if err := os.Mkdir(filepath.Join(root, "child"), 0700); err != nil {
							t.Fatal(err)
						}
					case "leaf-appearance":
						writeRule(t, root, "child/.jeleeignore", "*.mkv\n")
					}
				}
				return openNativeRoot(s)
			})
			if err != ErrChanged || p != nil {
				t.Fatal("changed source returned evidence", err)
			}
		})
	}
}

func TestReobserveInvalidCancelledAndOccupiedSlots(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	r := NewResolver()
	for _, name := range []string{"", "../bad", "a/../b", "/abs"} {
		if p, err := r.ReobserveDirectory(context.Background(), root, name); err != ErrInvalid || p != nil {
			t.Fatal("unsafe relative accepted", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if p, err := r.ReobserveDirectory(ctx, root, "."); err != context.Canceled || p != nil {
		t.Fatal("cancelled observation", err)
	}
	r.slots <- struct{}{}
	r.slots <- struct{}{}
	if p, err := r.ReobserveDirectory(context.Background(), root, "."); err != ErrBusy || p != nil {
		t.Fatal("unbounded queue", err)
	}
}
