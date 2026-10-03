//go:build windows || darwin || dragonfly || freebsd || linux || netbsd || openbsd

package nfo

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/resources"
)

func TestWritePreparerReadonlyOutputAndResourceCleanup(t *testing.T) {
	if _, err := NewWritePreparer(nil); err != domain.ErrInvalid {
		t.Fatal("nil budget accepted")
	}
	for _, content := range []string{
		`<movie><title>old</title><uniqueid type='custom'>manual</uniqueid><!--keep--><vendor/></movie>`,
		`<movie><title>old</title><!--keep--><vendor/></movie>`,
		`<movie><lockdata>true</lockdata><title>old</title></movie>`,
		`<movie><title>old</title><uniqueid type='custom'></uniqueid></movie>`,
		`<movie><title>old</title>`,
		`<tvshow><title>old</title></tvshow>`,
	} {
		root, _ := sourceFixture(t, []byte(content))
		scope := domain.NFOItemScope{ItemID: "a0000000-0000-0000-0000-000000000001", LibraryID: "a0000000-0000-0000-0000-000000000002", SourceID: "a0000000-0000-0000-0000-000000000003", RootID: "a0000000-0000-0000-0000-000000000004", Kind: "Movie", Revision: 1, Generation: 1, MediaPath: "電影 title.mkv", Source: domain.NFOSource{RootPath: root, RelativePath: "電影 title.nfo"}}
		request := domain.NFOWritePrepareRequest{ItemID: scope.ItemID, Revision: 1, Edits: []domain.NFOWriteTextEdit{{Field: "title", Value: "new"}}, MaxBytes: DefaultMaxBytes, Backups: 1}
		b := &recordingWriterBudget{Budget: writerBudget(t, 1, 0)}
		p, _ := NewWritePreparer(b)
		value, err := p.PrepareNFOWrite(context.Background(), scope, request)
		success := content == `<movie><title>old</title><uniqueid type='custom'>manual</uniqueid><!--keep--><vendor/></movie>` || content == `<movie><title>old</title><!--keep--><vendor/></movie>`
		if success && err != nil || !success && err == nil {
			t.Fatal("preparation acceptance differs")
		}
		if success {
			if !bytes.Contains(value.Replacement, []byte("<title>new</title>")) || !bytes.Contains(value.Replacement, []byte("<!--keep--><vendor/>")) {
				t.Fatal("retained XML changed")
			}
			if len(b.classes) != 3 || b.classes[0] != app.WorkIO || b.classes[1] != app.WorkCPU || b.classes[2] != app.WorkIO {
				t.Fatal("stages nested or omitted")
			}
		}
		if got := b.Stats(); got != (resources.Stats{}) {
			t.Fatal("permit leaked")
		}
		after, _ := os.ReadFile(filepath.Join(root, scope.Source.RelativePath))
		entries, _ := os.ReadDir(root)
		if !bytes.Equal(after, []byte(content)) || len(entries) != 1 {
			t.Fatal("preparation wrote filesystem")
		}
	}
}
