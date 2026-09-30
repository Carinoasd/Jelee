//go:build jelee_probe_tests

// probe-smoke accepts only self-generated acceptance fixtures in a disposable
// test image. It is absent from ordinary builds and the production Dockerfile.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/probe"
	"github.com/MoYuanCN/Jelee/internal/platform/proberuntime"
	"github.com/MoYuanCN/Jelee/internal/platform/sandbox"
	"github.com/MoYuanCN/Jelee/tools"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == sandbox.HelperCommand {
		os.Exit(proberuntime.Helper(os.Args[2:]))
	}
	if len(os.Args) != 1 {
		os.Exit(64)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	dir, err := os.MkdirTemp("", "jelee-probe-acceptance-")
	if err != nil {
		return errors.New("acceptance_temporary_unavailable")
	}
	defer os.RemoveAll(dir)
	runner, err := proberuntime.New(ctx, dir)
	if err != nil {
		return err
	}
	spec, err := tools.FFprobeSpec("linux-amd64")
	if err != nil {
		return errors.New("acceptance_identity_unavailable")
	}
	adapter, err := probe.NewAdapter(runner, spec.VendorVersion+"|"+spec.SHA256)
	if err != nil {
		return err
	}
	passed := 0
	for _, fixture := range []struct {
		name              string
		streams, chapters int
		width, height     int64
	}{
		{"video-180p.mp4", 2, 0, 320, 180},
		{"video-360p.mp4", 2, 0, 640, 360},
		{"multi.mkv", 5, 2, 320, 180},
	} {
		path := filepath.Join("/media", fixture.name)
		before, err := os.ReadFile(path) // only the test generator's <=4MiB files.
		if err != nil || len(before) > 4<<20 {
			return errors.New("acceptance_fixture_unavailable")
		}
		observation, err := adapter.Probe(ctx, probe.Source{RootPath: "/media", RelativePath: fixture.name})
		if err != nil {
			return err
		}
		metadata := observation.Metadata
		if len(metadata.Streams) != fixture.streams || len(metadata.Chapters) != fixture.chapters || metadata.Format.SizeBytes == nil || *metadata.Format.SizeBytes != int64(len(before)) || metadata.Format.DurationMicros == nil || *metadata.Format.DurationMicros < 900000 || *metadata.Format.DurationMicros > 1200000 || observation.ToolIdentity != spec.VendorVersion+"|"+spec.SHA256 || len(observation.Fingerprint) != 64 {
			return errors.New("acceptance_metadata_mismatch")
		}
		video := metadata.Streams[0]
		if video.Codec == nil || *video.Codec != "h264" || video.Video == nil || video.Video.Width == nil || *video.Video.Width != fixture.width || video.Video.Height == nil || *video.Video.Height != fixture.height {
			return errors.New("acceptance_video_mismatch")
		}
		if fixture.name == "multi.mkv" {
			for _, index := range []int{1, 2} {
				if metadata.Streams[index].Codec == nil || *metadata.Streams[index].Codec != "aac" {
					return errors.New("acceptance_audio_mismatch")
				}
			}
			for offset, language := range []string{"eng", "zho"} {
				stream := metadata.Streams[3+offset]
				if stream.Codec == nil || *stream.Codec != "subrip" || stream.Language == nil || *stream.Language != language {
					return errors.New("acceptance_subtitle_mismatch")
				}
			}
		}
		after, err := os.ReadFile(path)
		if err != nil || sha256.Sum256(before) != sha256.Sum256(after) {
			return errors.New("acceptance_source_changed")
		}
		passed++
	}
	for _, name := range []string{"corrupt.mkv", "external.concat", "remote.m3u8"} {
		observation, err := adapter.Probe(ctx, probe.Source{RootPath: "/media", RelativePath: name})
		if !errors.Is(err, probe.ErrFailed) || len(observation.Metadata.Streams) != 0 || observation.Fingerprint != "" {
			return errors.New("acceptance_reference_or_corruption_not_rejected")
		}
		passed++
	}
	return json.NewEncoder(os.Stdout).Encode(struct {
		Cases  int    `json:"cases"`
		Result string `json:"result"`
	}{passed, "isolated_normalization_passed"})
}
