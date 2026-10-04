//go:build jelee_matroska_tests

// matroska-smoke runs the shipped mkvtoolnix/MediaInfo helpers inside a
// disposable test image against one self-generated fixture mounted
// read-only. It is absent from ordinary builds and the production Dockerfile.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/matroska"
	"github.com/MoYuanCN/Jelee/internal/adapter/probe"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/mkvruntime"
	"github.com/MoYuanCN/Jelee/internal/platform/process"
	"github.com/MoYuanCN/Jelee/internal/platform/sandbox"
)

const fixture = "/fixtures/subtitles-fonts.mkv"

func main() {
	if len(os.Args) > 1 && os.Args[1] == sandbox.ToolHelperCommand {
		os.Exit(mkvruntime.Helper(os.Args[2:]))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	report, err := run(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "matroska-smoke:", err)
		os.Exit(1)
	}
	_ = json.NewEncoder(os.Stdout).Encode(report)
}

func digest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func run(ctx context.Context) (map[string]any, error) {
	report := map[string]any{"diagnose": mkvruntime.Diagnose(ctx)}
	before, err := digest(fixture)
	if err != nil {
		return nil, errors.New("fixture unavailable")
	}
	temp, err := os.MkdirTemp("", "jelee-matroska-smoke-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(temp)
	cache := filepath.Join(temp, "cache")
	if err := os.Mkdir(cache, 0o700); err != nil {
		return nil, err
	}
	runners := map[sandbox.ToolMode]*process.IsolatedToolRunner{}
	for _, mode := range mkvruntime.Modes {
		runner, err := mkvruntime.New(ctx, mode, temp)
		if err != nil {
			return nil, fmt.Errorf("register %s: %w", mode, err)
		}
		runners[mode] = runner
	}
	source := domain.ProbeSource{RootPath: filepath.Dir(fixture), RelativePath: filepath.Base(fixture)}
	input, err := probe.Open(ctx, source)
	if err != nil {
		return nil, err
	}
	result, err := runners[sandbox.ToolMediaInfo].Run(ctx, process.ToolRequest{Stdin: input.Stdin()})
	_ = input.Close()
	if err != nil {
		return nil, fmt.Errorf("mediainfo: %w", err)
	}
	report["mediainfo"] = probe.ParseMediaInfo(result.Stdout, map[int]bool{0: true, 1: true, 2: true, 3: true}, nil)
	extractor, err := matroska.New(runners[sandbox.ToolIdentify], runners[sandbox.ToolExtract], matroska.Config{CacheRoot: cache})
	if err != nil {
		return nil, err
	}
	items := map[string]string{}
	const sourceID = "00000000-0000-4000-8000-000000000001"
	for name, query := range map[string]struct {
		kind  matroska.Kind
		index int
	}{"srt": {matroska.KindSubtitle, 1}, "ass": {matroska.KindSubtitle, 2}, "font": {matroska.KindAttachment, 1}, "fontStream": {matroska.KindAttachmentStream, 3}} {
		item, err := extractor.Locate(ctx, sourceID, source, query.kind, query.index)
		if err != nil {
			return nil, fmt.Errorf("locate %s: %w", name, err)
		}
		sum, err := digest(filepath.Join(cache, filepath.FromSlash(item.RelativePath)))
		if err != nil {
			return nil, err
		}
		items[name] = item.Format + ":" + sum[:16]
	}
	report["items"] = items
	if _, err := extractor.Locate(ctx, sourceID, source, matroska.KindSubtitle, 0); !errors.Is(err, matroska.ErrNotFound) {
		return nil, errors.New("video track was offered as a subtitle")
	}
	after, err := digest(fixture)
	if err != nil || after != before {
		return nil, errors.New("fixture bytes changed")
	}
	report["sourceUnchanged"] = true
	return report, nil
}
