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
