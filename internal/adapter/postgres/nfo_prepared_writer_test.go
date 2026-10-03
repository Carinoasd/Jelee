package postgres

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/adapter/nfo"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/resources"
)

// This invokes the adapter on an isolated fixture. It is not a runtime job and
// does not activate read-write policy or claim durable filesystem recovery.
func TestNFOPreparedWriterConsumesPersistedRecipeAfterReopen(t *testing.T) {
	f, service, scope, request := nfoWritePreparationFixture(t)
	saved, replay, err := service.Prepare(f.ctx, f.a, "prepared-writer", request)
	if err != nil || replay {
		t.Fatal("save controlled fixture")
	}
	fresh, err := Open(f.ctx, f.s.Pool.Config().ConnString(), 2)
	if err != nil {
		t.Fatal("reopen fixture repository")
	}
	defer fresh.Pool.Close()
	digest, _ := domain.NFOWriteRequestDigest(request)
	recovered, err := fresh.FindNFOWritePreparation(f.ctx, f.a, "prepared-writer", digest)
	if err != nil || recovered.ID != saved.ID || !bytes.Equal(recovered.Replacement, saved.Replacement) {
		t.Fatal("persisted recipe differs")
	}
	b, _ := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 1, Queue: 0})
	w, _ := nfo.NewWriterWithBudget(b)
	source, err := nfo.ReadSource(f.ctx, scope.Source.RootPath, scope.Source.RelativePath, request.MaxBytes)
	if err != nil {
		t.Fatal("observe private source")
	}
	for _, needle := range []string{"<!--keep-->", "<vendor a='1'/>"} {
		tampered := domain.CloneNFOWritePreparation(recovered)
		tampered.Replacement = bytes.ReplaceAll(tampered.Replacement, []byte(needle), []byte("<!--removed-->"))
		if err := w.ReplacePrepared(f.ctx, source, tampered); err == nil {
			t.Fatal("uncontrolled saved XML accepted")
		}
	}
	before, _ := os.ReadFile(filepath.Join(scope.Source.RootPath, scope.Source.RelativePath))
	entries, _ := os.ReadDir(scope.Source.RootPath)
	if !bytes.Equal(before, saved.Original) || len(entries) != 2 {
		t.Fatal("rejected recipe touched source or sidecar")
	}
	if err := w.ReplacePrepared(f.ctx, source, recovered); err != nil {
		t.Fatal("persisted controlled recipe rejected", err)
	}
	after, _ := os.ReadFile(filepath.Join(scope.Source.RootPath, scope.Source.RelativePath))
	backup, _ := os.ReadFile(filepath.Join(scope.Source.RootPath, scope.Source.RelativePath+".jelee.bak"))
	if !bytes.Equal(after, saved.Replacement) || !bytes.Equal(backup, saved.Original) || b.Stats() != (resources.Stats{}) {
		t.Fatal("writer changed persisted UUID, output or backup")
	}
	retained, err := fresh.FindNFOWritePreparation(f.ctx, f.a, "prepared-writer", digest)
	if err != nil || !bytes.Equal(retained.Original, saved.Original) || !bytes.Equal(retained.Replacement, saved.Replacement) {
		t.Fatal("filesystem write mutated immutable preparation")
	}
}
