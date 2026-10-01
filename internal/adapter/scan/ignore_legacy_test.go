//go:build linux || windows

package scan

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestLegacyIgnoreBridgeRecheckChanges(t *testing.T) {
	for _, kind := range []string{"same-metadata-content", "nearer", "removed", "shadowed"} {
		t.Run(kind, func(t *testing.T) {
			root := filepath.Clean(t.TempDir())
			writeScanFile(t, root, ".ignore", []byte("old\n"))
			writeScanFile(t, root, "child/file.mkv", []byte("media"))
			if kind == "shadowed" {
				writeScanFile(t, root, "child/.ignore", []byte("local\n"))
			}
			stamp := time.Unix(1700000000, 0)
			file := filepath.Join(root, ".ignore")
			if err := os.Chtimes(file, stamp, stamp); err != nil {
				t.Fatal(err)
			}
			s := NewIgnoreScanner()
			ctx := context.Background()
			before, err := s.ObserveLegacyIgnore(ctx, domain.ScanDirectory{RootID: testRootID, RootPath: root, Path: "child"})
			if err != nil {
				t.Fatal(err)
			}
			if got, err := s.ReobserveLegacyIgnore(ctx, root, before); err != nil || !reflect.DeepEqual(got, before) {
				t.Fatal("unchanged recheck", err)
			}
			switch kind {
			case "same-metadata-content", "shadowed":
				writeScanFile(t, root, ".ignore", []byte("new\n"))
				if err := os.Chtimes(file, stamp, stamp); err != nil {
					t.Fatal(err)
				}
			case "nearer":
				writeScanFile(t, root, "child/.ignore", nil)
			case "removed":
				if err := os.Remove(file); err != nil {
					t.Fatal(err)
				}
			}
			got, err := s.ReobserveLegacyIgnore(ctx, root, before)
			if kind == "shadowed" {
				if err != nil || !reflect.DeepEqual(got, before) {
					t.Fatal("unread ancestor affected nearest query", err)
				}
			} else if err != domain.ErrInventoryInvalidated || len(got.Proofs) != 0 {
				t.Fatal("source change accepted", err)
			}
		})
	}
}

func TestLegacyIgnoreBridgeCanceledAndMissingDirectory(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	s := NewIgnoreScanner()
	writeScanFile(t, root, "child/file.mkv", nil)
	o, err := s.ObserveLegacyIgnore(context.Background(), domain.ScanDirectory{RootID: testRootID, RootPath: root, Path: "child"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := s.ReobserveLegacyIgnore(ctx, root, o); err != context.Canceled || len(got.Proofs) != 0 {
		t.Fatal("cancellation", err)
	}
	if err := os.Rename(filepath.Join(root, "child"), filepath.Join(root, "moved")); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ReobserveLegacyIgnore(context.Background(), root, o); err == nil || len(got.Proofs) != 0 {
		t.Fatal("missing directory accepted")
	}
}
