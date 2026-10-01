//go:build jelee_fixture_tools

// gen-fixtures generates small original test media using the pinned developer
// tool. This command is absent from ordinary production builds.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/process"
)

type executable struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type mediaSpec struct {
	VendorVersion string                `json:"vendorVersion"`
	InstallPath   string                `json:"installPath"`
	Executables   map[string]executable `json:"executables"`
	LicenseFiles  []executable          `json:"licenseFiles"`
}

type manifest struct {
	SchemaVersion int `json:"schemaVersion"`
	MediaTools    struct {
		SchemaVersion int                  `json:"schemaVersion"`
		Platforms     map[string]mediaSpec `json:"platforms"`
	} `json:"mediaTools"`
}

type expected struct {
	Width     int  `json:"width,omitempty"`
	Height    int  `json:"height,omitempty"`
	Video     int  `json:"videoStreams,omitempty"`
	Audio     int  `json:"audioStreams,omitempty"`
	Subtitles int  `json:"subtitleStreams,omitempty"`
	Chapters  int  `json:"chapters,omitempty"`
	Invalid   bool `json:"invalid,omitempty"`
}

type fixture struct {
	Name     string   `json:"name"`
	Bytes    int64    `json:"bytes"`
	SHA256   string   `json:"sha256"`
	Expected expected `json:"expected"`
}

type fixtureManifest struct {
	SchemaVersion int       `json:"schemaVersion"`
	Platform      string    `json:"platform"`
	ToolVersion   string    `json:"toolVersion"`
	ToolSHA256    string    `json:"toolSHA256"`
	Files         []fixture `json:"files"`
}

func relativePath(value string) bool {
	if value == "" || strings.ContainsAny(value, "\\:\x00") || strings.HasPrefix(value, "/") {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func digest(reader io.Reader) (string, error) {
	h := sha256.New()
	_, err := io.Copy(h, reader)
	return hex.EncodeToString(h.Sum(nil)), err
}

func verifyFile(root *os.Root, path, want string) error {
	if !relativePath(path) || len(want) != 64 {
		return errors.New("fixture_tool_invalid")
	}
	decoded, err := hex.DecodeString(want)
	if err != nil || len(decoded) != sha256.Size {
		return errors.New("fixture_tool_invalid")
	}
	info, err := root.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("fixture_tool_unavailable")
	}
	f, err := root.Open(path)
	if err != nil {
		return errors.New("fixture_tool_unavailable")
	}
	defer f.Close()
	got, err := digest(f)
	if err != nil || got != want {
		return errors.New("fixture_tool_checksum")
	}
	return nil
}

func loadTool(project string) (mediaSpec, string, error) {
	root, err := os.OpenRoot(project)
	if err != nil {
		return mediaSpec{}, "", errors.New("fixture_project_unavailable")
	}
	defer root.Close()
	f, err := root.Open("tools/manifest.json")
	if err != nil {
		return mediaSpec{}, "", errors.New("fixture_manifest_missing")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, (2<<20)+1))
	if err != nil || len(raw) > 2<<20 {
		return mediaSpec{}, "", errors.New("fixture_manifest_invalid")
	}
	var m manifest
	if err := json.Unmarshal(raw, &m); err != nil || m.SchemaVersion != 1 || m.MediaTools.SchemaVersion != 1 {
		return mediaSpec{}, "", errors.New("fixture_manifest_invalid")
	}
	platform := runtime.GOOS + "-" + runtime.GOARCH
	spec, ok := m.MediaTools.Platforms[platform]
	if !ok {
		return mediaSpec{}, "", errors.New("fixture_platform_unsupported")
	}
	tool, ok := spec.Executables["ffmpeg"]
	if !ok || !relativePath(spec.InstallPath) || !strings.HasPrefix(spec.InstallPath, "media/"+platform+"/") || len(spec.VendorVersion) == 0 || len(spec.VendorVersion) > 100 || len(spec.LicenseFiles) < 1 || len(spec.LicenseFiles) > 16 {
		return mediaSpec{}, "", errors.New("fixture_manifest_invalid")
	}
	base := ".tools/" + spec.InstallPath + "/"
	if err := verifyFile(root, base+tool.Path, tool.SHA256); err != nil {
		return mediaSpec{}, "", err
	}
	for _, license := range spec.LicenseFiles {
		if err := verifyFile(root, base+license.Path, license.SHA256); err != nil {
			return mediaSpec{}, "", err
		}
	}
	path := filepath.Join(project, filepath.FromSlash(base+tool.Path))
	return spec, path, nil
}

func runTool(ctx context.Context, toolPath, tempRoot, operation string, args []string) (process.Result, error) {
	runner, err := process.NewFixtureRunner(process.Config{MaxConcurrent: 1, Timeout: 30 * time.Second, MaxStdoutBytes: 64 << 10, MaxStderrBytes: 64 << 10, TempRoot: tempRoot}, process.Tool{ID: "ffmpeg", Path: toolPath, Operations: map[string][]string{operation: args}})
	if err != nil {
		return process.Result{}, err
	}
	return runner.Run(ctx, process.Request{Tool: "ffmpeg", Operation: operation})
}

func writeNew(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errors.New("fixture_write_failed")
	}
	_, writeErr := f.Write(data)
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		return errors.New("fixture_write_failed")
	}
	return nil
}

func makeImage(path string) error {
	img := image.NewNRGBA(image.Rect(0, 0, 16, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			img.SetNRGBA(x, y, color.NRGBA{uint8(x * 16), uint8(y * 16), 128, 255})
		}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errors.New("fixture_write_failed")
	}
	err = png.Encode(f, img)
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return errors.New("fixture_write_failed")
	}
	return nil
}

func generate(ctx context.Context, project string) (output string, returnErr error) {
	spec, toolPath, err := loadTool(project)
	if err != nil {
		return "", err
	}
	parent := filepath.Join(project, ".testfixtures")
	if err := os.Mkdir(parent, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", errors.New("fixture_directory_failed")
	}
	info, err := os.Lstat(parent)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("fixture_directory_failed")
	}
	output, err = os.MkdirTemp(parent, "media-"+runtime.GOOS+"-"+runtime.GOARCH+"-")
	if err != nil {
		return "", errors.New("fixture_directory_failed")
	}
	owned := output
	// Only remove the fresh directory this invocation owns after a failure.
	defer func() {
		if returnErr != nil {
			_ = os.RemoveAll(owned)
		}
	}()
	temp := filepath.Join(output, "process")
	if err := os.Mkdir(temp, 0700); err != nil {
		return "", errors.New("fixture_directory_failed")
	}
	version, err := runTool(ctx, toolPath, temp, "version", []string{"-version"})
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(string(version.Stdout), "ffmpeg version "+spec.VendorVersion+" ") {
		return "", errors.New("fixture_tool_version")
	}
	files := []struct {
		name string
		data string
	}{
		{"english.srt", "1\n00:00:00,000 --> 00:00:00,750\nJelee synthetic subtitle\n"},
		{"chinese.srt", "1\n00:00:00,000 --> 00:00:00,750\nJelee 自建字幕\n"},
		{"chapters.ffmetadata", ";FFMETADATA1\ntitle=Jelee synthetic test\n[CHAPTER]\nTIMEBASE=1/1000\nSTART=0\nEND=500\ntitle=Part one\n[CHAPTER]\nTIMEBASE=1/1000\nSTART=500\nEND=1000\ntitle=Part two\n"},
		{"corrupt.mkv", "Jelee deliberately invalid synthetic media\x00\x01"},
		{"movie.nfo", "<?xml version=\"1.0\" encoding=\"UTF-8\"?><movie><title>Jelee 自建测试</title><year>2026</year><plot>Original generated fixture.</plot></movie>\n"},
		{"invalid.nfo", "<movie><title>Jelee invalid fixture</movie>\n"},
		{"tvshow.nfo", "<tvshow><title>Jelee synthetic series</title><year>2026</year></tvshow>\n"},
		{"episode.nfo", "<episodedetails><title>Jelee episode</title><season>1</season><episode>1</episode></episodedetails>\n"},
	}
	for _, entry := range files {
		if err := writeNew(filepath.Join(output, entry.name), []byte(entry.data)); err != nil {
			return "", err
		}
	}
	if err := makeImage(filepath.Join(output, "poster.png")); err != nil {
		return "", err
	}
	common := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-n", "-filter_threads", "1", "-filter_complex_threads", "1"}
	expectations := map[string]expected{"corrupt.mkv": {Invalid: true}, "invalid.nfo": {Invalid: true}}
	for _, size := range []struct {
		name, dimensions string
		width, height    int
	}{{"video-180p.mp4", "320x180", 320, 180}, {"video-360p.mp4", "640x360", 640, 360}} {
		args := append(append([]string(nil), common...), "-f", "lavfi", "-i", "testsrc2=size="+size.dimensions+":rate=24", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-t", "1", "-map", "0:v", "-map", "1:a", "-c:v", "libx264", "-threads", "1", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", "64k", "-movflags", "+faststart", filepath.Join(output, size.name))
		if _, err := runTool(ctx, toolPath, temp, "generate-video", args); err != nil {
			return "", err
		}
		expectations[size.name] = expected{Width: size.width, Height: size.height, Video: 1, Audio: 1}
	}
	args := append(append([]string(nil), common...), "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-f", "lavfi", "-i", "sine=frequency=880:sample_rate=48000", "-i", filepath.Join(output, "english.srt"), "-i", filepath.Join(output, "chinese.srt"), "-f", "ffmetadata", "-i", filepath.Join(output, "chapters.ffmetadata"), "-t", "1", "-map", "0:v", "-map", "1:a", "-map", "2:a", "-map", "3:s", "-map", "4:s", "-map_metadata", "5", "-map_chapters", "5", "-c:v", "libx264", "-threads", "1", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", "64k", "-c:s", "srt", "-metadata:s:s:0", "language=eng", "-metadata:s:s:1", "language=zho", filepath.Join(output, "multi.mkv"))
	if _, err := runTool(ctx, toolPath, temp, "generate-video", args); err != nil {
		return "", err
	}
	expectations["multi.mkv"] = expected{Width: 320, Height: 180, Video: 1, Audio: 2, Subtitles: 2, Chapters: 2}
	audio := append(append([]string(nil), common...), "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-t", "1", "-c:a", "flac", filepath.Join(output, "audio.flac"))
	if _, err := runTool(ctx, toolPath, temp, "generate-audio", audio); err != nil {
		return "", err
	}
	if ctx.Err() != nil {
		return "", process.ErrCancelled
	}
	expectations["audio.flac"] = expected{Audio: 1}
	if err := os.Remove(temp); err != nil {
		return "", errors.New("fixture_cleanup_failed")
	}
	entries, err := os.ReadDir(output)
	if err != nil {
		return "", errors.New("fixture_read_failed")
	}
	result := fixtureManifest{SchemaVersion: 1, Platform: runtime.GOOS + "-" + runtime.GOARCH, ToolVersion: spec.VendorVersion, ToolSHA256: spec.Executables["ffmpeg"].SHA256, Files: make([]fixture, 0, len(entries))}
	var total int64
	for _, entry := range entries {
		if ctx.Err() != nil {
			return "", process.ErrCancelled
		}
		f, err := os.Open(filepath.Join(output, entry.Name()))
		if err != nil {
			return "", errors.New("fixture_read_failed")
		}
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() > 4<<20 {
			f.Close()
			return "", errors.New("fixture_size_limit")
		}
		total += info.Size()
		if total > 16<<20 {
			f.Close()
			return "", errors.New("fixture_size_limit")
		}
		hash, err := digest(f)
		f.Close()
		if err != nil {
			return "", errors.New("fixture_read_failed")
		}
		result.Files = append(result.Files, fixture{Name: entry.Name(), Bytes: info.Size(), SHA256: hash, Expected: expectations[entry.Name()]})
	}
	raw, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", errors.New("fixture_manifest_failed")
	}
	if err := writeNew(filepath.Join(output, "fixtures.json"), append(raw, '\n')); err != nil {
		return "", err
	}
	if ctx.Err() != nil {
		return "", process.ErrCancelled
	}
	return output, nil
}

func main() {
	if len(os.Args) != 1 {
		fmt.Fprintln(os.Stderr, "gen-fixtures: no arguments accepted")
		os.Exit(2)
	}
	project, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "gen-fixtures: fixture_project_unavailable")
		os.Exit(1)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	ctx, timeout := context.WithTimeout(ctx, 2*time.Minute)
	defer timeout()
	output, err := generate(ctx, project)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gen-fixtures: "+err.Error())
		os.Exit(1)
	}
	relative, _ := filepath.Rel(project, output)
	fmt.Println("Generated original fixtures: " + filepath.ToSlash(relative) + " (hashes and expected metadata in fixtures.json)")
}
