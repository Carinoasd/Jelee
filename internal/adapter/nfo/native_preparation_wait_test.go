//go:build linux || windows

package nfo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/resources"
)

func TestWritePreparerNativeDirectoryRootAndLegacySeries(t *testing.T) {
	for _, kind := range []string{"Series", "Season"} {
		for _, directory := range []bool{false, true} {
			if kind == "Season" && !directory {
				continue
			}
			root := filepath.Clean(t.TempDir())
			relative := "owned.mkv"
			if directory {
				relative = "."
			}
			scope := nativePathScope(root, kind, relative, directory)
			if !directory {
				if err := os.WriteFile(filepath.Join(root, relative), []byte("owned media"), 0600); err != nil {
					t.Fatal("create legacy Series media")
				}
			}
			xmlRoot := "tvshow"
			if kind == "Season" {
				xmlRoot = "season"
			}
			body := []byte("<" + xmlRoot + "><title>old</title></" + xmlRoot + ">")
			if err := os.WriteFile(filepath.Join(root, scope.Source.RelativePath), body, 0600); err != nil {
				t.Fatal("create directory-root NFO")
			}
			request := domain.NFOWritePrepareRequest{ItemID: scope.ItemID, Revision: 1, Edits: []domain.NFOWriteTextEdit{{Field: "title", Value: "new"}}, MaxBytes: 1024, Backups: 1}
			budget, _ := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 1, Queue: 0})
			preparer, _ := NewWritePreparer(budget)
			prepared, err := preparer.PrepareNFOWrite(context.Background(), scope, request)
			wantKind := byte(1)
			if directory {
				wantKind = 2
			}
			if err != nil || prepared.NativeObservation.Empty() || prepared.NativeObservation.MediaIdentity()[2] != wantKind || domain.ValidateNFOWritePreparation(prepared) != nil || budget.Stats() != (resources.Stats{}) {
				t.Fatal("native directory or legacy scope rejected", err)
			}
		}
	}
}

func TestWritePreparerNativeReceiptSurvivesCPUWaitOnlyForSameScope(t *testing.T) {
	for _, change := range []string{"unchanged", "media_missing", "media_same_bytes", "relative_ancestor", "absolute_ancestor", "cancel"} {
		t.Run(change, func(t *testing.T) {
			scope := nativePreparationSourceFixture(t, false)
			request := domain.NFOWritePrepareRequest{ItemID: scope.ItemID, Revision: scope.Revision, Edits: []domain.NFOWriteTextEdit{{Field: "title", Value: "new"}}, MaxBytes: 1024, Backups: 1}
			budget, err := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 2, Queue: 8})
			if err != nil {
				t.Fatal("create controlled budget")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			releaseCPU, err := budget.Acquire(ctx, app.WorkCPU)
			if err != nil {
				t.Fatal("hold CPU permit")
			}
			defer func() {
				if releaseCPU != nil {
					releaseCPU()
				}
			}()
			preparer, _ := NewWritePreparer(budget)
			type result struct {
				value domain.NFOWritePreparation
				err   error
			}
			done := make(chan result, 1)
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				value, err := preparer.PrepareNFOWrite(ctx, scope, request)
				done <- result{value, err}
			}()
			defer func() {
				cancel()
				select {
				case <-finished:
				case <-time.After(10 * time.Second):
					t.Error("preparation goroutine did not join")
				}
			}()
			// CPU is held while IO is available. Waiting with IO=0 proves the
			// first observation finished and its filesystem handles were closed.
			ticker := time.NewTicker(time.Millisecond)
			defer ticker.Stop()
			for budget.Stats().Waiting != 1 || budget.Stats().IO != 0 {
				select {
				case r := <-done:
					t.Fatalf("preparer did not reach CPU wait: %v", r.err)
				case <-ctx.Done():
					t.Fatal("CPU wait not observed")
				case <-ticker.C:
				}
			}
			media := filepath.Join(scope.Source.RootPath, filepath.FromSlash(scope.MediaPath))
			switch change {
			case "media_missing", "media_same_bytes":
				info, err := os.Stat(media)
				if err != nil {
					t.Fatal("stat owned media")
				}
				if err := os.Rename(media, media+".retained"); err != nil {
					t.Fatal("retain original media during CPU wait")
				}
				if change == "media_same_bytes" {
					if err := os.WriteFile(media, []byte("owned media"), 0600); err != nil {
						t.Fatal("replace owned media")
					}
					if err := os.Chtimes(media, info.ModTime(), info.ModTime()); err != nil {
						t.Fatal("restore replacement timestamp")
					}
				}
			case "relative_ancestor":
				folder := filepath.Dir(media)
				if err := os.Rename(folder, folder+".retained"); err != nil {
					t.Fatal("retain relative ancestor during CPU wait")
				}
				if err := os.Mkdir(folder, 0700); err != nil {
					t.Fatal("create replacement ancestor")
				}
				for _, name := range []string{filepath.Base(media), filepath.Base(scope.Source.RelativePath)} {
					if err := os.Rename(filepath.Join(folder+".retained", name), filepath.Join(folder, name)); err != nil {
						t.Fatal("preserve original file objects")
					}
				}
			case "absolute_ancestor":
				parent := filepath.Dir(scope.Source.RootPath)
				if err := os.Rename(parent, parent+".retained"); err != nil {
					t.Fatal("retain absolute ancestor during CPU wait")
				}
				if err := os.Mkdir(parent, 0700); err != nil {
					t.Fatal("create replacement absolute ancestor")
				}
				if err := os.Rename(filepath.Join(parent+".retained", filepath.Base(scope.Source.RootPath)), scope.Source.RootPath); err != nil {
					t.Fatal("preserve original root object")
				}
			case "cancel":
				cancel()
			}
			releaseCPU()
			releaseCPU = nil
			var r result
			select {
			case r = <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("preparation did not join after CPU wait")
			}
			if change == "unchanged" {
				if r.err != nil || r.value.NativeObservation.Empty() || domain.ValidateNFOWritePreparation(r.value) != nil {
					t.Fatal("unchanged receipt did not survive CPU wait", r.err)
				}
			} else if r.err == nil || len(r.value.Original) != 0 || !r.value.NativeObservation.Empty() {
				t.Fatal("changed scope or cancellation returned a preparation")
			}
			if change == "cancel" && !errors.Is(r.err, context.Canceled) {
				t.Fatal("cancelled CPU wait result differs")
			}
			if budget.Stats() != (resources.Stats{}) {
				t.Fatal("preparation leaked a permit or waiter")
			}
		})
	}
}
