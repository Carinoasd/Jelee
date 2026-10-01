package postgres

import (
	"github.com/MoYuanCN/Jelee/internal/domain"
	"testing"
)

func TestIgnoreExecutionPrivateReadsFenced(t *testing.T) {
	f, l, _ := manifestFixture(t)
	request, err := f.s.ReadIgnoreRequest(f.ctx, l)
	if err != nil || request == nil || request.Intent != ignoreTestIntent() {
		t.Fatal("request unavailable", err)
	}
	root, err := f.s.ReadIgnoreRoot(f.ctx, l, f.registration.RootID)
	if err != nil || root == "" {
		t.Fatal("root unavailable", err)
	}
	other, err := f.s.RegisterLibrary(f.ctx, "other", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if value, err := f.s.ReadIgnoreRoot(f.ctx, l, other.RootID); err != domain.ErrNotFound || value != "" {
		t.Fatal("foreign root exposed", err)
	}
	l.Generation++
	if value, err := f.s.ReadIgnoreRequest(f.ctx, l); err != domain.ErrJobLeaseLost || value != nil {
		t.Fatal("stale request exposed", err)
	}
	if value, err := f.s.ReadIgnoreRoot(f.ctx, l, f.registration.RootID); err != domain.ErrJobLeaseLost || value != "" {
		t.Fatal("stale root exposed", err)
	}
}
