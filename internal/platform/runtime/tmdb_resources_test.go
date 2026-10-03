package runtime

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/adapter/metadata"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/platform/resources"
)

func TestMetadataPreflightUsesSharedBudgetBeforeNetwork(t *testing.T) {
	b, err := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 1, Queue: 0})
	if err != nil {
		t.Fatal(err)
	}
	hold, err := b.Acquire(context.Background(), app.WorkCPU)
	if err != nil {
		t.Fatal(err)
	}
	defer hold()
	l := newLifetime(slog.New(slog.NewTextHandler(io.Discard, nil)))
	service, err := prepareMetadataWithBudget(strings.Repeat("a", 32), l, b)
	if err != nil || service == nil || l.prepareTMDB == nil {
		t.Fatalf("prepare: %v", err)
	}
	defer l.closePool()
	// A full zero-length queue must fail before resolving or dialing TMDB.
	if err := l.prepareTMDB(context.Background()); !errors.Is(err, metadata.ErrUnavailable) {
		t.Fatalf("preflight busy: %v", err)
	}
	if s := b.Stats(); s.CPU != 1 || s.IO != 0 || s.Waiting != 0 {
		t.Fatalf("preflight changed budget: %+v", s)
	}
}
