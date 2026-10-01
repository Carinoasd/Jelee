//go:build linux || windows

package scan

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	ignoresource "github.com/MoYuanCN/Jelee/internal/adapter/media/ignore"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/ignore"
	"github.com/MoYuanCN/Jelee/internal/platform/legacyignore"
)

func TestFamilyIgnoreDoesNotCompleteAfterSourceChange(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	writeScanFile(t, root, ".ignore", []byte("*"))
	writeScanFile(t, root, "file.mkv", nil)
	evaluator := legacyEvaluatorFunc(func(ctx context.Context, b legacyignore.Batch) (legacyignore.BatchResult, error) {
		r := legacyignore.BatchResult{Decisions: make([]legacyignore.Decision, len(b.Paths))}
		for i := range r.Decisions {
			r.Decisions[i] = legacyignore.Decision{Kind: legacyignore.RuleExclude, Line: 1}
		}
		return r, nil
	})
	done := false
	err := NewFamilyIgnoreScanner(evaluator).ScanFamilyIgnoreDirectory(context.Background(), domain.ScanDirectory{RootID: testRootID, RootPath: root, Path: "."}, domain.IgnoreIntent{Mode: domain.IgnoreModeFamily, CaseMode: domain.IgnoreCaseSensitive}, func(b domain.FamilyIgnoreScanBatch) error {
		if b.Inventory.Done {
			done = true
		} else {
			writeScanFile(t, root, ".ignore", []byte("changed"))
		}
		return nil
	})
	if err != domain.ErrInventoryInvalidated || done {
		t.Fatal("changed family emitted Done", err)
	}
}

func TestFamilyIgnoreRejectsReplacedDirectoryCandidate(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	writeScanFile(t, root, "child/file.mkv", nil)
	evaluator := legacyEvaluatorFunc(func(context.Context, legacyignore.Batch) (legacyignore.BatchResult, error) {
		t.Fatal("absent source executed helper")
		return legacyignore.BatchResult{}, nil
	})
	s := NewFamilyIgnoreScanner(evaluator)
	var retained ignoresource.ScanBatch
	if err := s.custom.ScanDirectoryWithChildIdentities(context.Background(), root, ".", ignore.Options{}, func(b ignoresource.ScanBatch) error {
		if len(b.Entries) > 0 {
			retained = b
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(retained.Entries) != 1 || retained.Entries[0].Identity == ([32]byte{}) {
		t.Fatal("directory identity not captured")
	}
	if err := os.Rename(filepath.Join(root, "child"), filepath.Join(root, "old")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "child"), 0700); err != nil {
		t.Fatal(err)
	}
	if b, err := s.familyBatch(context.Background(), domain.ScanDirectory{RootID: testRootID, RootPath: root, Path: "."}, retained, nil); err != domain.ErrInventoryInvalidated || len(b.Inventory.Directories) != 0 {
		t.Fatal("replaced candidate published", err)
	}
}

func TestFamilyIgnoreConcurrentScansDoNotReenterEnumerationSlots(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	evaluator := legacyEvaluatorFunc(func(ctx context.Context, b legacyignore.Batch) (legacyignore.BatchResult, error) {
		entered <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return legacyignore.BatchResult{}, ctx.Err()
		}
		return legacyignore.BatchResult{Decisions: make([]legacyignore.Decision, len(b.Paths))}, nil
	})
	s := NewFamilyIgnoreScanner(evaluator)
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		root := filepath.Clean(t.TempDir())
		writeScanFile(t, root, ".ignore", []byte("unused-pattern"))
		writeScanFile(t, root, "file.mkv", nil)
		go func() {
			results <- s.ScanFamilyIgnoreDirectory(ctx, domain.ScanDirectory{RootID: testRootID, RootPath: root, Path: "."}, domain.IgnoreIntent{Mode: domain.IgnoreModeFamily, CaseMode: domain.IgnoreCaseSensitive}, func(domain.FamilyIgnoreScanBatch) error { return nil })
		}()
	}
	ready := true
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-ctx.Done():
			ready = false
		}
	}
	close(release)
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Error("concurrent scan failed", err)
		}
	}
	if !ready {
		t.Fatal("nested source lookup exhausted enumeration slots")
	}
}

func TestFamilyIgnorePreservesCallbackFailure(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	writeScanFile(t, root, "file.mkv", nil)
	evaluator := legacyEvaluatorFunc(func(context.Context, legacyignore.Batch) (legacyignore.BatchResult, error) {
		t.Fatal("absent source executed helper")
		return legacyignore.BatchResult{}, nil
	})
	sentinel := errors.New("repository canceled batch")
	err := NewFamilyIgnoreScanner(evaluator).ScanFamilyIgnoreDirectory(context.Background(), domain.ScanDirectory{RootID: testRootID, RootPath: root, Path: "."}, domain.IgnoreIntent{Mode: domain.IgnoreModeFamily, CaseMode: domain.IgnoreCaseSensitive}, func(domain.FamilyIgnoreScanBatch) error { return sentinel })
	if err != sentinel {
		t.Fatal("repository failure changed", err)
	}
}

func TestFamilyIgnoreCancellationReachesEvaluatorAndReleasesSlot(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	writeScanFile(t, root, ".ignore", []byte("*"))
	writeScanFile(t, root, "file.mkv", nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	evaluator := legacyEvaluatorFunc(func(helperCtx context.Context, b legacyignore.Batch) (legacyignore.BatchResult, error) {
		deadline, ok := helperCtx.Deadline()
		if !ok || time.Until(deadline) > ignoresource.MaxDuration {
			t.Error("shared scan deadline did not reach evaluator")
		}
		cancel()
		<-helperCtx.Done()
		return legacyignore.BatchResult{}, helperCtx.Err()
	})
	scanner := NewFamilyIgnoreScanner(evaluator)
	emitted := false
	err := scanner.ScanFamilyIgnoreDirectory(ctx, domain.ScanDirectory{RootID: testRootID, RootPath: root, Path: "."}, domain.IgnoreIntent{Mode: domain.IgnoreModeFamily, CaseMode: domain.IgnoreCaseSensitive}, func(domain.FamilyIgnoreScanBatch) error {
		emitted = true
		return nil
	})
	if !errors.Is(err, context.Canceled) || emitted || len(scanner.slots) != 0 {
		t.Fatal("canceled evaluation retained work or emitted a batch", err, emitted, len(scanner.slots))
	}
}
