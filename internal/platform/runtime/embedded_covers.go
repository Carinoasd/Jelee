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

// prepareEmbeddedCovers wires the G40.4 catalog sync pass. Disabled
// configuration returns before any factory runs. An unavailable probe
// capability, a missing image store or a failed sandbox registration only
// leaves the pass off and is logged; it never fails startup. The returned
// close removes the pass's private scratch directory.
func prepareEmbeddedCovers(ctx context.Context, enabled bool, probing *probeService, originals *imageadapter.Store, repository app.EmbeddedCoverRepository, logger *slog.Logger,
	prepare func(context.Context) (app.EmbeddedCoverExtractor, func() error, error)) (*jobworker.EmbeddedCoverOptions, func() error) {
	if !enabled {
		return nil, nil
	}
	unavailable := func(code string) (*jobworker.EmbeddedCoverOptions, func() error) {
		if logger != nil {
			// The pass needs only the pinned, sandboxed ffprobe (G37.1: the
			// production image never ships ffmpeg); say so explicitly.
			logger.Warn("embedded cover extraction is enabled but unavailable; it needs the verified ffprobe probe runtime and the image store, never ffmpeg", "component", "images", "code", code)
		}
		return nil, nil
	}
	if probing == nil || !probing.Available() || originals == nil || repository == nil || prepare == nil {
		return unavailable("embedded_cover_prerequisite_unavailable")
	}
	extractor, closeExtractor, err := prepare(ctx)
	if err != nil {
		if closeExtractor != nil {
			_ = closeExtractor()
		}
		return unavailable("embedded_cover_runtime_unavailable")
	}
	return &jobworker.EmbeddedCoverOptions{Repository: repository, Extractor: extractor, Store: originals, Available: probing.Available}, closeExtractor
}

// prepareProductionCoverExtractor registers the sealed cover reads of the
// shipped, protected ffprobe with their own private scratch directory.
func prepareProductionCoverExtractor(ctx context.Context) (app.EmbeddedCoverExtractor, func() error, error) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return nil, nil, proberuntime.ErrUnavailable
	}
	directory, err := scratch.MkdirOwned("", scratch.ServiceProbe)
	if err != nil {
		return nil, nil, proberuntime.ErrUnavailable
	}
	cleanup := func() error { return os.RemoveAll(directory) }
	runner, err := proberuntime.NewCover(ctx, directory)
	if err != nil {
		return nil, cleanup, err
	}
	adapter, err := probe.NewCoverAdapter(runner)
	if err != nil {
		return nil, cleanup, err
	}
	return adapter, cleanup, nil
}
