package runtime

import (
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/adapter/metadata"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
)

func TestGovernedMetadataRejectsBudgetOutsideAdapterBounds(t *testing.T) {
	l := newLifetime(slog.New(slog.NewTextHandler(io.Discard, nil)))
	// Bypasses configuration validation: the adapter checks again.
	for _, budget := range []config.TMDBConfig{{RequestIntervalMs: 19}, {MaxConcurrent: 33}, {DailyRequestLimit: -1}} {
		service, err := prepareGovernedMetadata(strings.Repeat("a", 32), l, nil, budget)
		if !errors.Is(err, metadata.ErrGovernance) || service != nil || l.prepareTMDB != nil || l.closeTMDB != nil {
			t.Fatalf("budget %+v accepted: %v", budget, err)
		}
	}
	service, err := prepareGovernedMetadata(strings.Repeat("a", 32), l, nil, config.TMDBConfig{RequestIntervalMs: 1000, MaxConcurrent: 1, DailyRequestLimit: 10})
	if err != nil || service == nil || l.prepareTMDB == nil {
		t.Fatalf("valid budget rejected: %v", err)
	}
	l.closeTMDB()
	if service, err := prepareGovernedMetadata("", l, nil, config.TMDBConfig{RequestIntervalMs: 19}); service != nil || err != nil {
		t.Fatal("budget validated without a TMDB credential")
	}
}
