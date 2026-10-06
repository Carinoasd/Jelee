package runtime

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type logSettingsFake struct {
	mu       sync.Mutex
	err      error
	reads    int
	applied  []domain.LogSettings
	pruned   int
	pruneErr error
}

func (f *logSettingsFake) LogSettings(context.Context) (domain.LogSettings, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if f.err != nil {
		return domain.LogSettings{}, f.err
	}
	return domain.LogSettings{LogDays: 3, Revision: int64(f.reads), Overrides: []domain.LogLevelOverride{{Component: "http", Level: "debug"}}}, nil
}

func (f *logSettingsFake) ApplySettings(s domain.LogSettings) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applied = append(f.applied, s)
}

func (f *logSettingsFake) PruneFiles() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pruned++
	return f.pruneErr
}

func (f *logSettingsFake) counts() (int, int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads, len(f.applied), f.pruned
}

// G46.2/G46.9: every instance applies the stored settings on an interval;
// a failed read keeps the settings in force and warns once until reads
// succeed again.
func TestLogSettingsSyncAppliesAndSurvivesFailures(t *testing.T) {
	fake := &logSettingsFake{}
	var logs bytes.Buffer
	sync := newLogSettingsSync(fake, fake, slog.New(slog.NewJSONHandler(&logs, nil)))
	if !sync.refresh(context.Background()) || len(fake.applied) != 1 || fake.applied[0].Overrides[0].Component != "http" {
		t.Fatalf("applied %v", fake.applied)
	}
	fake.err = errors.New("database down")
	for range 3 {
		if sync.refresh(context.Background()) {
			t.Fatal("failed read reported success")
		}
	}
	if strings.Count(logs.String(), "log_settings_unavailable") != 1 || len(fake.applied) != 1 {
		t.Fatalf("failure handling: %s", logs.String())
	}
	fake.err = nil
	if !sync.refresh(context.Background()) || sync.failing {
		t.Fatal("recovery")
	}
	fake.pruneErr = errors.New("busy")
	sync.pruneFiles()
	if !strings.Contains(logs.String(), "log_prune_failed") {
		t.Fatal("prune failure not logged")
	}

	// Run refreshes and prunes on its tickers until cancelled.
	fake = &logSettingsFake{}
	sync = newLogSettingsSync(fake, fake, slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
	sync.interval, sync.prune = 5*time.Millisecond, 7*time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		sync.Run(ctx)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		reads, applied, pruned := fake.counts()
		if reads >= 2 && applied >= 2 && pruned >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("loop did not run: %d %d %d", reads, applied, pruned)
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
}
