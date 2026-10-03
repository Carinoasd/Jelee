package runtime

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
)

func TestJobWindowProductionRuntimeConfiguration(t *testing.T) {
	ctx, store, _, dsn, _ := metricsIntegrationStore(t)
	_, actor := jobMetricsRuntimeAccount(t, ctx, store)
	root := t.TempDir()
	original := []byte("window original media")
	media := filepath.Join(root, "movie.mkv")
	if err := os.WriteFile(media, original, 0600); err != nil {
		t.Fatal(err)
	}
	library, err := store.RegisterLibrary(ctx, "window-runtime", root)
	if err != nil {
		t.Fatal("register runtime library")
	}
	values := map[string]string{"JELEE_DATABASE_URL": dsn, "JELEE_ENABLE_ACCOUNTS": "true", "JELEE_ENABLE_JOBS": "true", "JELEE_JOB_WORKERS": "1", "JELEE_JOB_POLL_MILLISECONDS": "100"}
	now := time.Now().UTC()
	values["JELEE_JOB_WINDOW_START"] = now.Add(time.Hour).Format("15:04")
	values["JELEE_JOB_WINDOW_END"] = now.Add(2 * time.Hour).Format("15:04")
	values["JELEE_JOB_WINDOW_TIMEZONE"] = "UTC"
	cfg, err := config.LoadWith(func(k string) (string, bool) { v, ok := values[k]; return v, ok })
	if err != nil {
		t.Fatal("load runtime window configuration")
	}
	job, _, err := store.SubmitJob(ctx, actor, library.Library.ID, "window-runtime", domain.JobPriorityManual, cfg.Jobs.Policy())
	if err != nil {
		t.Fatal("submit runtime job")
	}
	start := func(c config.Config) func() {
		t.Helper()
		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		life := newLifetime(logger)
		life.listen = func(c context.Context, network, _ string) (net.Listener, error) {
			return (&net.ListenConfig{}).Listen(c, network, "127.0.0.1:0")
		}
		application := newWithLifetime(c, logger, life)
		if application.Err() != nil {
			t.Fatal("construct production job window runtime")
		}
		startup, cancel := context.WithTimeout(ctx, 15*time.Second)
		err := application.Start(startup)
		cancel()
		if err != nil {
			t.Fatal("start production job window runtime")
		}
		stopped := false
		stop := func() {
			if stopped {
				return
			}
			stopped = true
			shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if application.Stop(shutdown) != nil {
				t.Error("stop production window runtime")
			}
		}
		t.Cleanup(stop)
		if life.worker == nil {
			t.Fatal("production runtime did not create worker")
		}
		return stop
	}
	stop := start(cfg)
	timer := time.NewTimer(500 * time.Millisecond)
	select {
	case <-timer.C:
	case <-ctx.Done():
		timer.Stop()
		t.Fatal("window test timed out")
	}
	queued, err := store.GetJob(ctx, actor, job.ID)
	if err != nil || queued.State != domain.JobQueued || queued.Attempts != 0 {
		t.Fatal("production runtime ignored closed window")
	}
	stop()
	// Configuration is restart-scoped; reopening through a new real runtime must
	// consume the original persisted job rather than enqueue a replacement.
	cfg.Jobs.WindowStart = ""
	cfg.Jobs.WindowEnd = ""
	cfg.Jobs.WindowTimezone = ""
	stop = start(cfg)
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		job, err = store.GetJob(ctx, actor, job.ID)
		if err != nil {
			t.Fatal("read resumed runtime job")
		}
		if job.State == domain.JobSucceeded {
			break
		}
		if job.State == domain.JobFailed || job.State == domain.JobCancelled {
			t.Fatalf("runtime job failed: %s", job.ErrorCode)
		}
		select {
		case <-deadline.C:
			t.Fatal("reopened runtime did not execute job")
		case <-tick.C:
		}
	}
	if job.Files != 1 || job.Bytes != int64(len(original)) || job.Attempts != 1 {
		t.Fatal("runtime resume counts differ")
	}
	stop()
	got, err := os.ReadFile(media)
	if err != nil || string(got) != string(original) {
		t.Fatal("runtime changed original media")
	}
}
