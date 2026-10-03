package nfo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestItemNFOObservationRejectsPhysicalReplacementWithIdenticalMetadata(t *testing.T) {
	for _, mode := range []string{"root", "parent", "media", "nfo"} {
		t.Run(mode, func(t *testing.T) {
			reader, scope := itemSelectionFixture(t, "Movie")
			nfoPath := filepath.Join(scope.Source.RootPath, "folder", "Film.nfo")
			mediaPath := filepath.Join(scope.Source.RootPath, "folder", "Film.mkv")
			nfoBytes := []byte(`<movie><title>Local</title><lockedfields>Name</lockedfields></movie>`)
			mediaBytes := []byte("original media")
			if err := os.WriteFile(nfoPath, nfoBytes, 0600); err != nil {
				t.Fatal(err)
			}
			nfoInfo, err := os.Stat(nfoPath)
			if err != nil {
				t.Fatal(err)
			}
			mediaInfo, err := os.Stat(mediaPath)
			if err != nil {
				t.Fatal(err)
			}
			observed, err := reader.ObserveItemNFO(context.Background(), scope)
			if err != nil {
				t.Fatal(err)
			}
			original := observed.Selection()
			caller := observed.Selection()
			caller.Fields.Fields[0].Value = "caller"
			caller.Fields.LockedFields[0] = "caller"
			if fresh := observed.Selection(); fresh.Fields.Fields[0].Value != "Local" || fresh.Fields.LockedFields[0] != "Name" {
				t.Fatal("caller altered owned observation")
			}
			backup := filepath.Join(t.TempDir(), "backup")
			switch mode {
			case "root":
				if err := os.Rename(scope.Source.RootPath, backup); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Dir(nfoPath), 0700); err != nil {
					t.Fatal(err)
				}
			case "parent":
				if err := os.Rename(filepath.Dir(nfoPath), backup); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Dir(nfoPath), 0700); err != nil {
					t.Fatal(err)
				}
			case "media":
				if err := os.Rename(mediaPath, backup); err != nil {
					t.Fatal(err)
				}
			case "nfo":
				if err := os.Rename(nfoPath, backup); err != nil {
					t.Fatal(err)
				}
			}
			if mode != "media" {
				if err := os.WriteFile(nfoPath, nfoBytes, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(nfoPath, nfoInfo.ModTime(), nfoInfo.ModTime()); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(mediaPath, mediaBytes, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(mediaPath, mediaInfo.ModTime(), mediaInfo.ModTime()); err != nil {
				t.Fatal(err)
			}
			fresh, err := reader.SelectItemNFO(context.Background(), scope)
			if err != nil || fresh.CandidateDigest != original.CandidateDigest || fresh.RelativePath != original.RelativePath || fresh.Fields.Stamp != original.Fields.Stamp {
				t.Fatal("fixture changed a value already covered by old field checks", err)
			}
			if value, err := observed.Recheck(context.Background()); !errors.Is(err, domain.ErrNFOSourceChanged) || value != nil {
				t.Fatal("physical replacement accepted with identical filenames/hash/size/mtime", err)
			}
		})
	}
}
