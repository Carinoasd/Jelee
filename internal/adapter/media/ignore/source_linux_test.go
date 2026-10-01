//go:build linux

package ignoresource

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/ignore"
)

func TestSourceLinuxRejectsSymlinksAtEveryBoundary(t *testing.T) {
	for _, boundary := range []string{"root", "root-ancestor", "parent-inside", "parent-outside", "leaf-inside", "leaf-outside", "leaf-dangling"} {
		t.Run(boundary, func(t *testing.T) {
			base := filepath.Clean(t.TempDir())
			root := filepath.Join(base, "library")
			outside := filepath.Join(base, "outside")
			writeRule(t, root, ".jeleeignore", "*.tmp\n")
			writeRule(t, root, "dir/.jeleeignore", "!keep.tmp\n")
			writeRule(t, outside, ".jeleeignore", "!keep.tmp\n")
			candidate := "dir/keep.tmp"
			link, target := "", ""
			switch boundary {
			case "root":
				link = filepath.Join(base, "alias")
				target = root
				root = link
			case "root-ancestor":
				link = filepath.Join(base, "alias")
				target = base
				root = filepath.Join(link, "library")
			case "parent-inside":
				link = filepath.Join(root, "alias")
				target = filepath.Join(root, "dir")
				candidate = "alias/keep.tmp"
			case "parent-outside":
				link = filepath.Join(root, "alias")
				target = outside
				candidate = "alias/keep.tmp"
			case "leaf-inside":
				link = filepath.Join(root, ".jeleeignore")
				target = filepath.Join(root, "dir", ".jeleeignore")
			case "leaf-outside":
				link = filepath.Join(root, ".jeleeignore")
				target = filepath.Join(outside, ".jeleeignore")
			case "leaf-dangling":
				link = filepath.Join(root, ".jeleeignore")
				target = filepath.Join(outside, "missing")
			}
			if boundary == "leaf-inside" || boundary == "leaf-outside" || boundary == "leaf-dangling" {
				if err := os.Remove(link); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(target, link); err != nil {
				t.Fatal("native symlink fixture unavailable")
			}
			h := &diskSourceHarness{}
			o, err := NewResolver().evaluate(context.Background(), root, candidate, ignore.File, ignore.Options{}, h.access())
			zeroObservation(t, o, err, ErrUnsafe)
			h.closed(t)
		})
	}
}

func TestSourceLinuxFIFORejectedWithoutWriter(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	fifo := filepath.Join(root, ".jeleeignore")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal("native FIFO fixture unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	o, err := NewResolver().Evaluate(ctx, root, "file.tmp", ignore.File, ignore.Options{})
	zeroObservation(t, o, err, ErrUnsafe)
	if time.Since(start) > time.Second {
		t.Fatal("FIFO source waited for writer instead of rejecting nonregular file")
	}
}

func TestSourceLinuxUnreadableIsFailureNotAbsence(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("actual chmod denial requires non-root UID; injected read denial is covered separately")
	}
	for _, target := range []string{"leaf", "parent"} {
		t.Run(target, func(t *testing.T) {
			root, _, _ := sourceFixture(t)
			r := NewResolver()
			evaluateDisk(t, r, &diskSourceHarness{}, root, "dir/keep.tmp")
			file := filepath.Join(root, "dir", ".jeleeignore")
			mode := os.FileMode(0600)
			if target == "parent" {
				file = filepath.Dir(file)
				mode = 0700
			}
			if err := os.Chmod(file, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(file, mode) })
			h := &diskSourceHarness{}
			o, err := r.evaluate(context.Background(), root, "dir/keep.tmp", ignore.File, ignore.Options{}, h.access())
			zeroObservation(t, o, err, ErrRead)
			h.closed(t)
		})
	}
}

func TestSourceLinuxReplacementWithSameHardlinkedRuleChangesIdentity(t *testing.T) {
	for _, scope := range []string{"root", "directory"} {
		t.Run(scope, func(t *testing.T) {
			root, _, _ := sourceFixture(t)
			r := NewResolver()
			h := &diskSourceHarness{}
			changed := false
			h.afterRead = func(name string, pass int) {
				if pass != 1 || name != "dir/.jeleeignore" || changed {
					return
				}
				changed = true
				moving := filepath.Join(root, "dir")
				if scope == "root" {
					moving = root
				}
				if err := os.Rename(moving, moving+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Join(root, "dir"), 0700); err != nil {
					t.Fatal(err)
				}
				oldRule := filepath.Join(moving+".old", ".jeleeignore")
				if scope == "root" {
					oldRule = filepath.Join(moving+".old", "dir", ".jeleeignore")
					if err := os.Link(filepath.Join(moving+".old", ".jeleeignore"), filepath.Join(root, ".jeleeignore")); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Link(oldRule, filepath.Join(root, "dir", ".jeleeignore")); err != nil {
					t.Fatal(err)
				}
				a, _ := os.Stat(oldRule)
				b, _ := os.Stat(filepath.Join(root, "dir", ".jeleeignore"))
				if a == nil || b == nil || !os.SameFile(a, b) {
					t.Fatal("fixture must retain exactly the same source inode")
				}
			}
			o, err := r.evaluate(context.Background(), root, "dir/keep.tmp", ignore.File, ignore.Options{}, h.access())
			if !changed {
				t.Fatal("replacement barrier not reached")
			}
			zeroObservation(t, o, err, ErrChanged)
			h.closed(t)
		})
	}
}

func TestSourceLinuxFinalReopenRejectsNewSymlink(t *testing.T) {
	root, _, _ := sourceFixture(t)
	outside := filepath.Join(t.TempDir(), "outside")
	writeRule(t, outside, ".jeleeignore", "!keep.tmp\n")
	h := &diskSourceHarness{}
	changed := false
	h.afterRead = func(name string, pass int) {
		if name != "dir/.jeleeignore" || pass != 1 || changed {
			return
		}
		changed = true
		dir := filepath.Join(root, "dir")
		if err := os.Rename(dir, dir+".old"); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, dir); err != nil {
			t.Fatal(err)
		}
	}
	o, err := NewResolver().evaluate(context.Background(), root, "dir/keep.tmp", ignore.File, ignore.Options{}, h.access())
	if !changed {
		t.Fatal("replacement barrier not reached")
	}
	zeroObservation(t, o, err, ErrUnsafe)
	h.closed(t)
	if h.counts.fullReads.Load() != 3 {
		t.Fatal("final pass unexpectedly read source beyond unsafe replaced parent")
	}
}

func TestSourceLinuxExactNamesDoNotFollowPatternCaseMode(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	writeRule(t, root, ".JELEEIGNORE", "*.tmp\n")
	writeRule(t, root, "Case/.jeleeignore", "!keep.tmp\n")
	if _, err := os.Stat(filepath.Join(root, "case")); err == nil {
		t.Skip("filesystem exposes case aliases; run this case on native case-sensitive Linux storage")
	}
	r := NewResolver()
	if o := evaluateDisk(t, r, &diskSourceHarness{}, root, "other.tmp"); o.Match != (ignore.Match{}) {
		t.Fatal("differently cased control leaf was activated")
	}
	if o := evaluateDisk(t, r, &diskSourceHarness{}, root, "Case/keep.tmp"); o.Match.Outcome != ignore.Include || o.Match.Source != "Case/.jeleeignore" {
		t.Fatal("exact source path was not activated")
	}
	for _, mode := range []ignore.CaseMode{ignore.CaseSensitive, ignore.CaseASCIIInsensitive} {
		o, err := r.Evaluate(context.Background(), root, "case/keep.tmp", ignore.File, ignore.Options{Case: mode})
		zeroObservation(t, o, err, ErrRead)
	}
}
