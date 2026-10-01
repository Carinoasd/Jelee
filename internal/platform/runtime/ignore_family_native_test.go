//go:build !race && (linux || windows)

package runtime

import (
	"context"
	"github.com/MoYuanCN/Jelee/internal/platform/legacyignore"
	"github.com/MoYuanCN/Jelee/internal/platform/process"
	"os"
	"testing"
)

func TestFamilyIgnoreServiceNativeHealthAndCleanup(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TMPDIR", root)
	t.Setenv("TMP", root)
	t.Setenv("TEMP", root)
	s, err := newFamilyIgnoreService(context.Background(), true, prepareProductionFamilyIgnore)
	if err != nil || !s.Available() {
		t.Fatal("native helper health failed", err)
	}
	defer s.Close()
	helper, ok := s.backend.(*process.IgnoreRunner)
	if !ok || helper.Stats().Started != 1 || helper.Stats().Active != 0 {
		t.Fatal("readiness did not execute and join helper")
	}
	result, err := s.Evaluate(context.Background(), legacyignore.Batch{Paths: []string{"/a"}})
	if err != nil || len(result.Decisions) != 1 || result.Decisions[0].Kind != legacyignore.BlankExclude {
		t.Fatal("blank source health", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 || helper.Stats().Active != 0 || s.Available() {
		t.Fatal("service leaked helper or temporary files", err)
	}
}
