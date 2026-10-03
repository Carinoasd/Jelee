//go:build windows

package nfo

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
)

func TestPreparedWriterWindowsSlashRootRebuild(t *testing.T) {
	root, source, prepared, budget := preparedWriterFixture(t, []byte(`<movie><title>old</title><!--keep--></movie>`), "Movie")
	prepared.Scope.Source.RootPath = filepath.ToSlash(root)
	writer, _ := NewWriterWithBudget(budget)
	rebuilt, err := writer.rebuildPrepared(context.Background(), source, prepared)
	if err != nil || rebuilt == nil || !bytes.Equal(rebuilt.original, prepared.Replacement) {
		t.Fatal("slash root representation rejected frozen controlled output")
	}
	prepared.Scope.Source.RootPath = filepath.ToSlash(t.TempDir())
	if _, err := writer.rebuildPrepared(context.Background(), source, prepared); err != ErrChanged {
		t.Fatal("different physical root representation admitted")
	}
}
