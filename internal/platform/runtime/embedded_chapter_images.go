package runtime

import (
	"context"
	"log/slog"
	"os"
	"runtime"

	imageadapter "github.com/MoYuanCN/Jelee/internal/adapter/images"
	"github.com/MoYuanCN/Jelee/internal/adapter/probe"
	"github.com/MoYuanCN/Jelee/internal/app"
	jobworker "github.com/MoYuanCN/Jelee/internal/platform/jobs"
	"github.com/MoYuanCN/Jelee/internal/platform/proberuntime"
	"github.com/MoYuanCN/Jelee/internal/platform/scratch"
)

// prepareChapterImages wires the G40.1 catalog sync pass like the embedded
// cover pass: disabled configuration returns before any factory runs, and an
// unavailable probe capability, a missing image store or a failed sandbox
// registration only leaves the pass off and is logged. The returned close
// removes the pass's private scratch directory.
func prepareChapterImages(ctx context.Context, enabled bool, probing *probeService, originals *imageadapter.Store, repository app.ChapterImageRepository, logger *slog.Logger,
	prepare func(context.Context) (app.ChapterImageExtractor, func() error, error)) (*jobworker.ChapterImageOptions, func() error) {
	if !enabled {
		return nil, nil
	}
	unavailable := func(code string) (*jobworker.ChapterImageOptions, func() error) {
		if logger != nil {
			// Chapter pictures are copied, never generated: the pass needs
			// only the pinned, sandboxed ffprobe (ADR 0007, G37.1).
			logger.Warn("chapter image extraction is enabled but unavailable; it needs the verified ffprobe probe runtime and the image store, never ffmpeg", "component", "images", "code", code)
		}
		return nil, nil
	}
	if probing == nil || !probing.Available() || originals == nil || repository == nil || prepare == nil {
		return unavailable("chapter_image_prerequisite_unavailable")
	}
	extractor, closeExtractor, err := prepare(ctx)
	if err != nil {
		if closeExtractor != nil {
			_ = closeExtractor()
		}
		return unavailable("chapter_image_runtime_unavailable")
	}
	return &jobworker.ChapterImageOptions{Repository: repository, Extractor: extractor, Store: originals, Available: probing.Available}, closeExtractor
}

// prepareProductionChapterExtractor registers the sealed chapter image reads
// of the shipped, protected ffprobe with their own private scratch directory.
func prepareProductionChapterExtractor(ctx context.Context) (app.ChapterImageExtractor, func() error, error) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return nil, nil, proberuntime.ErrUnavailable
	}
	directory, err := scratch.MkdirOwned("", scratch.ServiceProbe)
	if err != nil {
		return nil, nil, proberuntime.ErrUnavailable
	}
	cleanup := func() error { return os.RemoveAll(directory) }
	runner, err := proberuntime.NewChapters(ctx, directory)
	if err != nil {
		return nil, cleanup, err
	}
	adapter, err := probe.NewChapterAdapter(runner)
	if err != nil {
		return nil, cleanup, err
	}
	return adapter, cleanup, nil
}
