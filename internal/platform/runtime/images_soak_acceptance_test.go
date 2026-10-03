//go:build jelee_probe_tests

package runtime

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	imageadapter "github.com/MoYuanCN/Jelee/internal/adapter/images"
)

type imagesSoakAcceptanceReport struct {
	Version            int                       `json:"version"`
	RunID              string                    `json:"runID"`
	Scope              string                    `json:"scope"`
	Result             string                    `json:"result"`
	ErrorCode          string                    `json:"errorCode"`
	Configuration      imagesMemoryConfiguration `json:"configuration"`
	Fixtures           imagesMemoryFixtures      `json:"fixtures"`
	FixtureFiles       int64                     `json:"fixtureFiles"`
	FixtureDirectories int64                     `json:"fixtureDirectories"`
	FixtureBytes       int64                     `json:"fixtureBytes"`
	imagesSoakWorkReport
	NegativeAfter            imagesMemoryNegative     `json:"negativeAfter"`
	CancellationAfter        imagesMemoryCancellation `json:"cancellationAfter"`
	SourceSampleCount        int                      `json:"sourceSampleCount"`
	OriginalSamplesUnchanged bool                     `json:"originalSamplesUnchanged"`
	Shutdown                 imagesMemoryShutdown     `json:"shutdown"`
	MemorySummary            imagesSoakMemorySummary  `json:"memorySummary"`
}

func imagesSoakFixtureBytes(root string) (int64, error) {
	var files, dirs, total int64
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.Type()&os.ModeSymlink != 0 {
			return errImagesMemoryHTTP
		}
		if entry.IsDir() {
			dirs++
			if dirs > 106 {
				return errImagesMemoryHTTP
			}
			return nil
		}
		info, e := entry.Info()
		if e != nil || !info.Mode().IsRegular() || info.Size() < 0 {
			return errImagesMemoryHTTP
		}
		files++
		total += info.Size()
		if files > 2008 || total > 1<<40 {
			return errImagesMemoryHTTP
		}
		return nil
	})
	if err != nil || files != 2008 || dirs != 106 || total <= 0 {
		return 0, errImagesMemoryHTTP
	}
	return total, nil
}

// This test is intentionally opt-in. A controller must supply the fixed
// fixtures, owned database/schema, bounded Docker profile, and real SIGTERM.
// Passing the Go test alone is not sufficient for external formal acceptance.
func TestImagesSoakAcceptance(t *testing.T) {
	if os.Getenv("JELEE_IMAGES_SOAK_ACCEPTANCE") != "true" {
		t.Skip("opt-in 600s or 24h real workload")
	}
	scope, runID := os.Getenv("JELEE_IMAGES_SOAK_MODE"), os.Getenv("JELEE_IMAGES_SOAK_RUN_ID")
	if scope != "formal" && scope != "smoke" {
		t.Fatal("soak_scope_invalid")
	}
	output, err := openImagesSoakOutput(os.Stdout)
	if err != nil {
		t.Fatal("soak_output_unavailable")
	}
	defer output.Close()
	finalStream, err := newImagesSoakStream(output, runID)
	if err != nil {
		t.Fatal("soak_identity_invalid")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Hour)
	defer cancel()
	base := imagesMemoryAcceptanceReport{Version: 1, Result: "failed", FixtureItems: 1000}
	report := imagesSoakAcceptanceReport{Version: 1, RunID: runID, Scope: scope, Result: "failed"}
	var collector *imagesSoakCollector
	defer func() {
		if collector != nil {
			collector.abort()
			if !report.MemorySummary.Complete {
				report.MemorySummary = collector.failedSummary()
			}
			finalStream = collector.stream
		}
		report.Configuration, report.Fixtures = base.Configuration, base.Fixtures
		report.NegativeAfter, report.CancellationAfter = base.Negative, base.Cancellation
		report.SourceSampleCount, report.OriginalSamplesUnchanged = base.SourceSampleCount, base.OriginalSamplesUnchanged
		report.Shutdown = base.Shutdown
		wanted := 288
		if scope == "smoke" {
			wanted = 2
		}
		if report.ErrorCode == "" && report.MemorySummary.Complete && report.Shutdown.Result == "passed" && len(report.Rounds) == wanted && report.WorkFinishedNanos-report.WorkStartedNanos >= int64(time.Duration(wanted)*5*time.Minute) {
			report.Result = "passed"
		} else if report.ErrorCode == "" {
			report.ErrorCode = "soak_incomplete"
		}
		writeCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if finalStream.final(writeCtx, report) != nil {
			t.Error("soak_final_write_failed")
		}
		if report.Result != "passed" {
			t.Errorf("soak_acceptance_failed: %s", report.ErrorCode)
		}
	}()
	var hooks imagesAcceptanceHooks
	hooks.jobs = true
	hooks.configure = func(configuration imagesMemoryConfiguration) error {
		collector, err = startImagesSoakCollector(ctx, output, runID, scope, configuration)
		return err
	}
	hooks.phase = func(phase string) error { _, err := collector.phase(collector.ctx, phase); return err }
	hooks.observe = func(read func() imageadapter.Stats) error { return collector.observe(read) }
	hooks.work = func(workCtx context.Context, session imagesAcceptanceSession, base *imagesMemoryAcceptanceReport) string {
		unlink := context.AfterFunc(workCtx, collector.cancel)
		defer unlink()
		bytes, err := imagesSoakFixtureBytes("/media")
		if err != nil {
			return "soak_fixture_inventory_invalid"
		}
		report.FixtureFiles, report.FixtureDirectories, report.FixtureBytes = 2008, 106, bytes
		roundHooks := imagesSoakRoundHooks{origin: collector.started, begin: collector.begin, phase: collector.phase, emit: collector.emit, hourRange: collector.hourRange}
		return runImagesSoakRounds(collector.ctx, scope, bytes, roundHooks, session, base, &report.imagesSoakWorkReport)
	}
	hooks.ready = func() error { return collector.stream.ready(collector.ctx) }
	hooks.finish = func(*imagesMemoryAcceptanceReport) error {
		report.MemorySummary, err = collector.finish()
		return err
	}
	report.ErrorCode = runImagesAcceptance(ctx, hooks, &base)
}
