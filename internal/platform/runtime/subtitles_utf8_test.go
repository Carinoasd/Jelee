package runtime

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/adapter/media"
	"github.com/MoYuanCN/Jelee/internal/adapter/subtitles"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"golang.org/x/text/encoding/traditionalchinese"
)

const (
	utf8TestSource = "11111111-1111-4111-8111-111111111111"
	utf8TestTrack  = "22222222-2222-4222-8222-222222222222"
)

type utf8Tracks struct {
	source media.Source
	err    error
	calls  int
}

func (u *utf8Tracks) ResolveTrack(_ context.Context, _ access.Principal, _ string, kind media.TrackKind, _ string) (media.Source, error) {
	u.calls++
	if kind != media.TrackSubtitle {
		return media.Source{}, media.ErrNotFound
	}
	return u.source, u.err
}

func utf8TestConfig(t *testing.T) config.Config {
	t.Helper()
	var c config.Config
	c.Matroska = config.MatroskaConfig{EnableExtraction: true, CacheRoot: filepath.Clean(t.TempDir()), CacheMaxBytes: 256 << 20}
	if err := os.Chmod(c.Matroska.CacheRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	return c
}

// The track is authorized before the cache is touched; a copy keeps the
// track's limits and device; the reasons for no copy are logged with the
// track ID and a fixed code only.
func TestUTF8ServiceAuthorizesThenConverts(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("the derived cache is off on Windows")
	}
	library := t.TempDir()
	text := strings.Repeat("1\n00:00:01,000 --> 00:00:02,000\n我們必須在天黑之前離開這個地方。\n\n", 40)
	big5, err := traditionalchinese.Big5.NewEncoder().Bytes([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(library, "Secret Name.zh.srt"), big5, 0o600); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	c := utf8TestConfig(t)
	tracks := &utf8Tracks{err: media.ErrNotFound}
	service := newUTF8Service(c, tracks, slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	if !service.Available() {
		t.Fatal("service unavailable")
	}
	ctx := context.Background()
	if _, err := service.ResolveUTF8Track(ctx, access.Principal{}, utf8TestSource, utf8TestTrack); !errors.Is(err, media.ErrNotFound) {
		t.Fatalf("unauthorized: %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(c.Matroska.CacheRoot, subtitles.DerivedDirectory)); len(entries) != 0 {
		t.Fatal("cache touched before authorization")
	}
	tracks.err = nil
	tracks.source = media.Source{Root: library, RelativePath: "Secret Name.zh.srt", Charset: "Big5", DeviceID: "dev", ShareStreams: 2}
	source, err := service.ResolveUTF8Track(ctx, access.Principal{}, utf8TestSource, utf8TestTrack)
	if err != nil || source.Root != c.Matroska.CacheRoot || source.DeviceID != "dev" || source.ShareStreams != 2 || source.Charset != "UTF-8" {
		t.Fatalf("copy %+v %v", source, err)
	}
	data, err := os.ReadFile(filepath.Join(source.Root, filepath.FromSlash(source.RelativePath)))
	if err != nil || string(data) != text {
		t.Fatalf("copy bytes %v", err)
	}
	for _, tc := range []struct {
		charset, kind, name, code string
	}{
		{"", "", "Secret Name.zh.srt", subtitles.ReasonCharsetUnknown},
		{"UTF-8", "", "Secret Name.zh.srt", subtitles.ReasonAlreadyUTF8},
		{"GB18030", "", "Secret Name.zh.srt", subtitles.ReasonCharsetMismatch},
		{"Big5", domain.SidecarSubtitleVobSub, "Secret Name.zh.sub", subtitles.ReasonFormat},
	} {
		logs.Reset()
		tracks.source = media.Source{Root: library, RelativePath: tc.name, Charset: tc.charset, SubtitleKind: tc.kind}
		if _, err := service.ResolveUTF8Track(ctx, access.Principal{}, utf8TestSource, utf8TestTrack); !errors.Is(err, media.ErrNotDerived) {
			t.Fatalf("%s: %v", tc.code, err)
		}
		line := logs.String()
		if !strings.Contains(line, `"code":"`+tc.code+`"`) || !strings.Contains(line, utf8TestTrack) || strings.Contains(line, "Secret") || strings.Contains(line, library) {
			t.Fatalf("%s log: %s", tc.code, line)
		}
	}
	// Without a usable cache root every track falls back to its original.
	broken := utf8TestConfig(t)
	if err := os.Chmod(broken.Matroska.CacheRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	unavailable := newUTF8Service(broken, tracks, slog.New(slog.DiscardHandler))
	tracks.source = media.Source{Root: library, RelativePath: "Secret Name.zh.srt", Charset: "Big5"}
	if _, err := unavailable.ResolveUTF8Track(ctx, access.Principal{}, utf8TestSource, utf8TestTrack); unavailable.Available() || !errors.Is(err, media.ErrNotDerived) {
		t.Fatalf("unavailable cache: %v", err)
	}
}

func TestSubtitleCacheBoundsStayWithinTheConfiguredTotal(t *testing.T) {
	var c config.Config
	for _, total := range []int64{0, 256 << 20, 1 << 30, 1 << 40} {
		c.Matroska.CacheMaxBytes = total
		extraction, derived := subtitleCacheBounds(c)
		want := total
		if total == 0 {
			want = config.DefaultMatroskaConfig().CacheMaxBytes
		}
		if extraction+derived != want || derived < subtitles.MinDerivedMaxBytes || derived > want/8 {
			t.Errorf("%d: %d + %d", total, extraction, derived)
		}
	}
}

// One cleanup job walks both caches below the root: sources are the union
// and each cache sweeps only its own directories.
func TestDerivedSweeperCombinesBothCaches(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("the derived cache is off on Windows")
	}
	c := utf8TestConfig(t)
	sweeper, ok := prepareSubtitleCacheSweeper(c).(derivedSweeper)
	if !ok {
		t.Fatal("cleanup does not cover the derived copies")
	}
	only := func(name string) string { return name }
	extractionOnly, both, derivedOnly := only("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"), only("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"), only("cccccccc-cccc-4ccc-8ccc-cccccccccccc")
	for _, dir := range []string{extractionOnly, both, filepath.Join(subtitles.DerivedDirectory, both), filepath.Join(subtitles.DerivedDirectory, derivedOnly)} {
		if err := os.MkdirAll(filepath.Join(c.Matroska.CacheRoot, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	if n, err := sweeper.CountSources(ctx); err != nil || n != 3 {
		t.Fatalf("count %d %v", n, err)
	}
	if names, err := sweeper.ListSources(ctx, extractionOnly, 1); err != nil || len(names) != 1 || names[0] != both {
		t.Fatalf("list %v %v", names, err)
	}
	if sweep, err := sweeper.SweepSource(ctx, derivedOnly, false, time.Now()); err != nil || sweep.Orphaned != 1 {
		t.Fatalf("derived orphan %+v %v", sweep, err)
	}
	if sweep, err := sweeper.SweepSource(ctx, both, false, time.Now()); err != nil || sweep.Orphaned != 2 {
		t.Fatalf("shared orphan %+v %v", sweep, err)
	}
	if sweep, err := sweeper.SweepSource(ctx, both, false, time.Now()); err != nil || sweep.Orphaned != 0 {
		t.Fatalf("repeated orphan %+v %v", sweep, err)
	}
	if _, err := sweeper.EnforceCapacity(ctx); err != nil {
		t.Fatal(err)
	}
}
