//go:build !windows

// These tests need a private cache directory, which Windows cannot prove;
// platform_windows_test.go checks that extraction stays off there.

package matroska

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/adapter/probe"
	"github.com/MoYuanCN/Jelee/internal/platform/process"
	"github.com/MoYuanCN/Jelee/internal/platform/sandbox"
)

const bitmapIdentifyJSON = `{"container":{"recognized":true,"supported":true,"type":"Matroska"},
"tracks":[{"id":0,"type":"video","properties":{"codec_id":"V_MPEG4/ISO/AVC"}},{"id":1,"type":"subtitles","properties":{"codec_id":"S_TEXT/UTF8"}},{"id":2,"type":"subtitles","properties":{"codec_id":"S_HDMV/PGS","language":"chi"}},{"id":3,"type":"subtitles","properties":{"codec_id":"S_VOBSUB","language":"EN-us"}},{"id":4,"type":"subtitles","properties":{"codec_id":"S_DVBSUB"}},{"id":5,"type":"subtitles","properties":{"codec_id":"S_VOBSUB"}}]}`

func openInput(t *testing.T, env testEnv) *probe.Input {
	t.Helper()
	input, err := probe.Open(context.Background(), env.source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = input.Close() })
	return input
}

func TestExtractBitmapsCopiesPGSAndVobSubTracksOnly(t *testing.T) {
	env := newEnv(t, Config{})
	env.identify.output = bitmapIdentifyJSON
	env.output.files["t2"] = "PG pgs bytes"
	// Track 5 lacks its .sub half and is dropped as undecodable.
	env.output.extra = []string{"t3.idx", "t3.sub", "t5.idx"}
	env.output.skipFile = "t3"
	before, _ := os.ReadFile(env.media)
	directory := t.TempDir()
	tracks, err := env.extractor.ExtractBitmaps(context.Background(), env.source, openInput(t, env), directory)
	if err != nil || len(tracks) != 2 {
		t.Fatalf("%v %+v", err, tracks)
	}
	if tracks[0].ID != 2 || tracks[0].Format != BitmapPGS || tracks[0].Language != "chi" || tracks[1].Language != "" || !slices.Equal(tracks[0].Files, []string{"t2.sup"}) {
		t.Fatalf("pgs %+v", tracks[0])
	}
	if tracks[1].ID != 3 || tracks[1].Format != BitmapVobSub || !slices.Equal(tracks[1].Files, []string{"t3.idx", "t3.sub"}) {
		t.Fatalf("vobsub %+v", tracks[1])
	}
	if data, _ := os.ReadFile(filepath.Join(directory, "t2.sup")); string(data) != "PG pgs bytes" {
		t.Fatal("PGS bytes changed")
	}
	plan := env.output.plans[0]
	if !slices.Equal(plan.Tracks, []int{2, 3, 5}) || len(plan.Attachments) != 0 {
		t.Fatalf("plan %+v: only bitmap tracks are extracted (no text, no DVB)", plan)
	}
	if after, _ := os.ReadFile(env.media); string(after) != string(before) {
		t.Fatal("source changed")
	}
	// The text cache is untouched.
	if entries, _ := os.ReadDir(env.cache); len(entries) != 0 {
		t.Fatal("bitmap extraction wrote into the text cache")
	}
	container, _ := ParseIdentify([]byte(bitmapIdentifyJSON))
	if _, ok := container.Tracks[4].BitmapFormat(); ok {
		t.Fatal("DVB offered for OCR")
	}
	if _, ok := container.Tracks[0].BitmapFormat(); ok {
		t.Fatal("video offered for OCR")
	}
}

func TestExtractBitmapsBoundsAndFailures(t *testing.T) {
	env := newEnv(t, Config{})
	// No bitmap track: nothing is extracted.
	env.identify.output = `{"container":{"recognized":true,"supported":true,"type":"Matroska"},"tracks":[{"id":0,"type":"subtitles","properties":{"codec_id":"S_TEXT/UTF8"}}]}`
	tracks, err := env.extractor.ExtractBitmaps(context.Background(), env.source, openInput(t, env), t.TempDir())
	if err != nil || len(tracks) != 0 || env.output.calls.Load() != 0 {
		t.Fatalf("%v %+v", err, tracks)
	}
	env.identify.output = "not json"
	if tracks, err := env.extractor.ExtractBitmaps(context.Background(), env.source, openInput(t, env), t.TempDir()); err != nil || tracks != nil {
		t.Fatal("unidentifiable file")
	}
	env.identify.output = bitmapIdentifyJSON
	env.output.files["t2"] = strings.Repeat("x", 64)
	big := filepath.Join(t.TempDir(), "large")
	if err := os.WriteFile(big, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(big, sandbox.ExtractFileLimit+1); err != nil {
		t.Fatal(err)
	}
	from, _ := os.OpenRoot(filepath.Dir(big))
	to, _ := os.OpenRoot(t.TempDir())
	defer func() { _ = from.Close(); _ = to.Close() }()
	if _, err := copyBounded(from, "large", to, "out", 1<<40); err != ErrTooLarge {
		t.Fatalf("oversized file accepted: %v", err)
	}
	if _, err := copyBounded(from, "missing", to, "out", 1<<40); err != ErrUnavailable {
		t.Fatal("missing file")
	}
	env.output.err = process.ErrBusy
	if _, err := env.extractor.ExtractBitmaps(context.Background(), env.source, openInput(t, env), t.TempDir()); err != ErrBusy {
		t.Fatalf("busy runner: %v", err)
	}
	env.output.err = nil
	if _, err := env.extractor.ExtractBitmaps(context.Background(), env.source, openInput(t, env), filepath.Join(t.TempDir(), "absent")); err != ErrUnavailable {
		t.Fatal("missing target directory accepted")
	}
	var absent *Extractor
	if _, err := absent.ExtractBitmaps(context.Background(), env.source, nil, t.TempDir()); err != ErrUnavailable {
		t.Fatal("nil extractor")
	}
	// A source rewritten during extraction is refused.
	input := openInput(t, env)
	env.output.onRun = func() { _ = os.WriteFile(env.media, []byte("rewritten during extraction"), 0o600) }
	if _, err := env.extractor.ExtractBitmaps(context.Background(), env.source, input, t.TempDir()); err != ErrChanged {
		t.Fatalf("changed source accepted: %v", err)
	}
}

// G15.5: PGS and VobSub tracks are copied, unconverted, into the same
// revision as the text tracks; DVB is never extracted.
func TestExtractorCachesBitmapSubtitlesUnconverted(t *testing.T) {
	ctx := context.Background()
	env := newEnv(t, Config{})
	env.identify.output = bitmapIdentifyJSON
	env.output.files["t2"] = "PG\x00\x01 raw presentation graphics"
	env.output.files["t3.idx"] = "# VobSub index file, v7\nsize: 720x480\n"
	env.output.files["t3.sub"] = "\x00\x00\x01\xba program stream"
	// Track 5 lacks its .sub half and is dropped as unusable.
	env.output.extra = []string{"t3.idx", "t3.sub", "t5.idx"}
	// A v1 revision of the same source is replaced on use.
	stale := filepath.Join(env.cache, testSource, strings.Repeat("0", 32))
	if err := os.MkdirAll(stale, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stale, entryFile), []byte(`{"version":1,"subtitles":[],"attachments":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(env.media)
	for _, query := range []struct {
		kind   Kind
		index  int
		format string
		bytes  string
	}{{KindSubtitle, 2, "sup", env.output.files["t2"]}, {KindVobSubIndex, 3, "idx", env.output.files["t3.idx"]}, {KindVobSubData, 3, "sub", env.output.files["t3.sub"]}, {KindSubtitle, 1, "srt", "content of t1"}} {
		item, err := env.extractor.Locate(ctx, testSource, env.source, query.kind, query.index)
		if err != nil || item.Format != query.format || env.read(t, item) != query.bytes {
			t.Fatalf("locate %+v: %v %+v", query, err, item)
		}
	}
	for _, query := range []struct {
		kind  Kind
		index int
	}{{KindSubtitle, 3}, {KindVobSubIndex, 2}, {KindVobSubData, 2}, {KindSubtitle, 4}, {KindVobSubIndex, 4}, {KindVobSubIndex, 5}, {KindVobSubData, 5}} {
		if _, err := env.extractor.Locate(ctx, testSource, env.source, query.kind, query.index); err != ErrNotFound {
			t.Fatalf("locate %+v: %v", query, err)
		}
	}
	if plan := env.output.plans[0]; !slices.Equal(plan.Tracks, []int{1, 2, 3, 5}) || env.output.calls.Load() != 1 {
		t.Fatalf("plan %+v (%d runs): DVB is not extracted, one run serves every track", plan, env.output.calls.Load())
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("v1 revision kept")
	}
	if after, _ := os.ReadFile(env.media); string(after) != string(before) {
		t.Fatal("source changed")
	}
}

func TestExtractorLeavesOutBitmapsOverBudget(t *testing.T) {
	ctx := context.Background()
	// A bitmap track over the entry budget is left out; text stays.
	env := newEnv(t, Config{MaxEntryBytes: 1 << 20, MaxCacheBytes: 1 << 20})
	env.identify.output = bitmapIdentifyJSON
	env.output.files["t2"] = strings.Repeat("p", 1<<20)
	env.output.extra = []string{"t3.idx", "t3.sub"}
	if _, err := env.extractor.Locate(ctx, testSource, env.source, KindSubtitle, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := env.extractor.Locate(ctx, testSource, env.source, KindSubtitle, 2); err != ErrNotFound {
		t.Fatalf("oversized PGS kept: %v", err)
	}
	if _, err := env.extractor.Locate(ctx, testSource, env.source, KindVobSubData, 3); err != nil {
		t.Fatalf("VobSub within budget dropped: %v", err)
	}
	// mkvextract stopped by the per-file limit fails the run: text tracks
	// and fonts are extracted again without bitmap tracks.
	env = newEnv(t, Config{})
	env.identify.output = bitmapIdentifyJSON
	env.output.refuse = func(plan sandbox.Extraction) error {
		if slices.Contains(plan.Tracks, 2) {
			return process.ErrExit
		}
		return nil
	}
	if _, err := env.extractor.Locate(ctx, testSource, env.source, KindSubtitle, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := env.extractor.Locate(ctx, testSource, env.source, KindSubtitle, 2); err != ErrNotFound || env.output.calls.Load() != 2 {
		t.Fatalf("fallback: %v after %d runs", err, env.output.calls.Load())
	}
	if plans := env.output.plans; !slices.Equal(plans[1].Tracks, []int{1}) {
		t.Fatalf("fallback plan %+v", plans[1])
	}
	// Other failures are not retried.
	env = newEnv(t, Config{})
	env.identify.output = bitmapIdentifyJSON
	env.output.err = process.ErrBusy
	if _, err := env.extractor.Locate(ctx, testSource, env.source, KindSubtitle, 1); err != ErrBusy || env.output.calls.Load() != 1 {
		t.Fatalf("busy: %v after %d runs", err, env.output.calls.Load())
	}
}

func TestExtractionPlanPrefersTextTracks(t *testing.T) {
	var tracks []Track
	for id := range sandbox.MaxExtractTracks + 2 {
		codec := "S_HDMV/PGS"
		if id >= 4 {
			codec = "S_TEXT/UTF8"
		}
		tracks = append(tracks, Track{ID: id, Type: "subtitles", CodecID: codec})
	}
	// 30 text tracks come first; two of the four PGS tracks still fit.
	value, plan := extractionPlan(Container{Tracks: tracks}, true)
	if len(plan.Tracks) != sandbox.MaxExtractTracks || !slices.IsSorted(plan.Tracks) || plan.Tracks[1] != 1 || plan.Tracks[2] != 4 || len(value.Subtitles) != sandbox.MaxExtractTracks {
		t.Fatalf("plan %+v", plan.Tracks)
	}
	if value.Subtitles[0].Format != "sup" || value.Subtitles[2].Format != "srt" {
		t.Fatalf("entries %+v", value.Subtitles[:3])
	}
	if _, plan := extractionPlan(Container{Tracks: tracks[:4]}, false); len(plan.Tracks) != 0 {
		t.Fatal("bitmap tracks planned without bitmaps")
	}
}
