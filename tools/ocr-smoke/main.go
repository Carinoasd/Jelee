//go:build jelee_ocr_tests

// ocr-smoke runs the shipped, sandboxed Tesseract registration (production
// container paths and protected-file checks) inside a disposable test image:
// it recognizes self-generated PGS pictures through the subtitle OCR service
// and reports the result as JSON. It is absent from ordinary builds and from
// every Dockerfile.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/bitmapsub/bitmapsubtest"
	"github.com/MoYuanCN/Jelee/internal/adapter/matroska"
	"github.com/MoYuanCN/Jelee/internal/adapter/probe"
	"github.com/MoYuanCN/Jelee/internal/adapter/subtitleocr"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/ocrruntime"
	"github.com/MoYuanCN/Jelee/internal/platform/sandbox"
)

var lines = []string{"The bridge is closed tonight.", "Take the north road instead.", "The bridge is closed tonight."}

func main() {
	if len(os.Args) > 1 && os.Args[1] == sandbox.ToolHelperCommand {
		os.Exit(ocrruntime.ToolHelper(os.Args[2:]))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	report, err := run(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ocr-smoke:", err)
		_ = json.NewEncoder(os.Stderr).Encode(report)
		os.Exit(1)
	}
	_ = json.NewEncoder(os.Stdout).Encode(report)
}

// staticTrack stands in for the mkvtoolnix extraction: it writes one
// generated PGS track, as mkvextract would, into the job's work directory.
type staticTrack struct{ data []byte }

func (s staticTrack) ExtractBitmaps(_ context.Context, _ domain.ProbeSource, _ *probe.Input, directory string) ([]matroska.BitmapTrack, error) {
	if err := os.WriteFile(filepath.Join(directory, "t0.sup"), s.data, 0o600); err != nil {
		return nil, err
	}
	return []matroska.BitmapTrack{{ID: 0, Format: matroska.BitmapPGS, Language: "eng", Files: []string{"t0.sup"}}}, nil
}

func private(parent, name string) (string, error) {
	path := filepath.Join(parent, name)
	return path, os.Mkdir(path, 0o700)
}

func run(ctx context.Context) (map[string]any, error) {
	report := map[string]any{"diagnose": ocrruntime.Diagnose(ctx)}
	var cues []bitmapsubtest.Cue
	for i, line := range lines {
		img, err := bitmapsubtest.RenderText(nil, line, 48, bitmapsubtest.Style{Fill: color.NRGBA{255, 255, 255, 255}, Outline: color.NRGBA{0, 0, 0, 255}, OutlineWidth: 3, Padding: 2})
		if err != nil {
			return report, err
		}
		cues = append(cues, bitmapsubtest.Cue{Start: time.Duration(i*3+1) * time.Second, End: time.Duration(i*3+2) * time.Second, Image: img, X: 400, Y: 950})
	}
	var track bytes.Buffer
	if err := bitmapsubtest.EncodePGS(&track, 1920, 1080, cues); err != nil {
		return report, err
	}
	temp, err := os.MkdirTemp("", "jelee-ocr-smoke-")
	if err != nil {
		return report, err
	}
	defer os.RemoveAll(temp)
	runs, err := private(temp, "runs")
	if err != nil {
		return report, err
	}
	pictures, err := private(temp, "pictures")
	if err != nil {
		return report, err
	}
	cache, err := private(temp, "cache")
	if err != nil {
		return report, err
	}
	work, err := private(temp, "work")
	if err != nil {
		return report, err
	}
	runner, err := ocrruntime.New(ctx, 2, runs)
	if err != nil {
		return report, fmt.Errorf("register: %w", err)
	}
	recognizer, err := subtitleocr.NewRecognizer(runner, pictures)
	if err != nil {
		return report, err
	}
	identity, err := ocrruntime.Identity([]string{"eng"})
	if err != nil {
		return report, err
	}
	service, err := subtitleocr.New(staticTrack{track.Bytes()}, recognizer, subtitleocr.Config{CacheRoot: cache, WorkRoot: work, Languages: []string{"eng"}, PicturesPerMinute: 60, Concurrency: 2, QueueSize: 1, Identity: identity})
	if err != nil {
		return report, err
	}
	defer service.Close()
	// Any regular file stands in for the source; only its revision matters.
	media := filepath.Join(temp, "source.mkv")
	if err := os.WriteFile(media, track.Bytes(), 0o600); err != nil {
		return report, err
	}
	source := domain.ProbeSource{RootPath: temp, RelativePath: "source.mkv"}
	const sourceID = "00000000-0000-4000-8000-0000000000c1"
	started := time.Now()
	for {
		item, err := service.Locate(ctx, sourceID, source, 0)
		if err == nil {
			data, readErr := os.ReadFile(filepath.Join(cache, filepath.FromSlash(item.RelativePath)))
			if readErr != nil {
				return report, readErr
			}
			report["srt"] = string(data)
			break
		}
		if !errors.Is(err, subtitleocr.ErrPending) {
			report["stats"] = service.Stats()
			return report, err
		}
		select {
		case <-ctx.Done():
			report["stats"] = service.Stats()
			return report, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
	report["seconds"] = time.Since(started).Seconds()
	report["stats"] = service.Stats()
	return report, nil
}
