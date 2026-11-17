//go:build !windows

package subtitleocr

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/bitmapsub/bitmapsubtest"
	"github.com/MoYuanCN/Jelee/internal/adapter/formatsamples"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

const (
	testSupTrack = "00000000-0000-4000-8000-0000000000a1"
	testIdxTrack = "00000000-0000-4000-8000-0000000000a2"
)

// sidecarFiles writes external bitmap subtitles next to the stand-in media
// file.
func sidecarFiles(t *testing.T, e env, files map[string][]byte) {
	t.Helper()
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(e.source.RootPath, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func (e env) supRequest() SidecarRequest {
	return SidecarRequest{SourceID: testSource, TrackID: testSupTrack, Format: SidecarPGS, Language: "eng",
		Files: []domain.ProbeSource{{RootPath: e.source.RootPath, RelativePath: "Film.eng.sup"}}}
}

func (e env) idxRequest() SidecarRequest {
	return SidecarRequest{SourceID: testSource, TrackID: testIdxTrack, Format: SidecarVobSub, Language: "jpn",
		Files: []domain.ProbeSource{{RootPath: e.source.RootPath, RelativePath: "Film.jpn.idx"}, {RootPath: e.source.RootPath, RelativePath: "Film.jpn.sub"}}}
}

// locateSidecar polls until the OCR result of an external track exists or
// fails.
func (e env) locateSidecar(t *testing.T, request SidecarRequest) (Item, error) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		item, err := e.service.LocateSidecar(context.Background(), request)
		if !errors.Is(err, ErrPending) || time.Now().After(deadline) {
			return item, err
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestServiceRecognizesAnExternalSupInPlace(t *testing.T) {
	e := newEnv(t, Config{})
	sidecarFiles(t, e, map[string][]byte{"Film.eng.sup": samplePGS(t)})
	sup := filepath.Join(e.source.RootPath, "Film.eng.sup")
	before, media := digest(t, sup), digest(t, e.media)
	request := e.supRequest()
	if _, err := e.service.LocateSidecar(context.Background(), request); err != ErrPending {
		t.Fatalf("first lookup: %v", err)
	}
	item, err := e.locateSidecar(t, request)
	if err != nil || item.RelativePath != testSupTrack+"/"+strings.Split(item.RelativePath, "/")[1]+"/s0.srt" {
		t.Fatalf("%v %+v", err, item)
	}
	data, err := os.ReadFile(filepath.Join(e.cache, filepath.FromSlash(item.RelativePath)))
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Split(string(data), "\n"); len(lines) != 13 || lines[1] != "00:00:01,000 --> 00:00:02,000" || !strings.HasPrefix(lines[2], "picture ") || lines[10] != lines[2] {
		t.Fatalf("srt:\n%s", data)
	}
	stats := e.settled(t, 1)
	// Read in place: mkvextract never ran, and nothing was written next to
	// the external file or into the work directory.
	if e.extractor.calls.Load() != 0 || stats.Recognized != 2 || stats.Reused != 1 || stats.Completed != 1 {
		t.Fatalf("extractor %d stats %+v", e.extractor.calls.Load(), stats)
	}
	if digest(t, sup) != before || digest(t, e.media) != media {
		t.Fatal("original changed")
	}
	if entries, _ := os.ReadDir(e.source.RootPath); len(entries) != 2 {
		t.Fatalf("library holds %d entries", len(entries))
	}
	if entries, _ := os.ReadDir(e.work); len(entries) != 0 {
		t.Fatal("work directory used")
	}
	if got := e.recognizer.languages[0]; len(got) != 1 || got[0] != "eng" {
		t.Fatalf("languages %q", got)
	}
	// A changed file is a new revision; the old one is removed.
	if err := os.WriteFile(sup, samplePGS(t)[:len(samplePGS(t))-40], 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.service.LocateSidecar(context.Background(), request); err != ErrPending {
		t.Fatal("changed file served stale OCR")
	}
	second, err := e.locateSidecar(t, request)
	if err != nil || second.RelativePath == item.RelativePath {
		t.Fatalf("%v %+v", err, second)
	}
	e.settled(t, 2)
	if revisions, _ := os.ReadDir(filepath.Join(e.cache, testSupTrack)); len(revisions) != 1 {
		t.Fatal("old revision kept")
	}
	// The cut stream keeps the cues before the fault, marked truncated.
	root, _ := os.OpenRoot(e.cache)
	defer func() { _ = root.Close() }()
	value, err := readEntry(root, strings.TrimSuffix(second.RelativePath, "/s0.srt")+"/"+entryFile)
	if err != nil || len(value.Tracks) != 1 || !value.Tracks[0].Truncated || value.Tracks[0].Format != SidecarPGS {
		t.Fatalf("%v %+v", err, value)
	}
}

func TestServiceRecognizesAnExternalVobSubPair(t *testing.T) {
	e := newEnv(t, Config{Languages: []string{"chi_tra", "eng", "jpn"}})
	index, sub := sampleVobSub(t)
	sidecarFiles(t, e, map[string][]byte{"Film.jpn.idx": index, "Film.jpn.sub": sub})
	item, err := e.locateSidecar(t, e.idxRequest())
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(e.cache, filepath.FromSlash(item.RelativePath)))
	if !strings.HasPrefix(string(data), "1\n00:00:01,500 --> 00:00:03,0") || !strings.Contains(string(data), "\npicture ") {
		t.Fatalf("srt %q", data)
	}
	if got := e.recognizer.languages[0]; len(got) != 2 || got[0] != "jpn" || got[1] != "eng" {
		t.Fatalf("languages %q", got)
	}
	// Replacing only the .sub half is a new revision too.
	e.settled(t, 1)
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(e.source.RootPath, "Film.jpn.sub"), later, later); err != nil {
		t.Fatal(err)
	}
	if _, err := e.service.LocateSidecar(context.Background(), e.idxRequest()); err != ErrPending {
		t.Fatal("changed .sub served stale OCR")
	}
}

func TestServiceRefusesInvalidOrOversizedSidecars(t *testing.T) {
	e := newEnv(t, Config{})
	sidecarFiles(t, e, map[string][]byte{"Film.eng.sup": samplePGS(t)})
	good := e.supRequest()
	for name, request := range map[string]SidecarRequest{
		"bad source":   {SourceID: "x", TrackID: testSupTrack, Format: SidecarPGS, Files: good.Files},
		"bad track":    {SourceID: testSource, TrackID: "../x", Format: SidecarPGS, Files: good.Files},
		"bad format":   {SourceID: testSource, TrackID: testSupTrack, Format: "dvb", Files: good.Files},
		"pgs pair":     {SourceID: testSource, TrackID: testSupTrack, Format: SidecarPGS, Files: append(good.Files, good.Files...)},
		"vobsub alone": {SourceID: testSource, TrackID: testSupTrack, Format: SidecarVobSub, Files: good.Files},
		"missing file": {SourceID: testSource, TrackID: testSupTrack, Format: SidecarPGS, Files: []domain.ProbeSource{{RootPath: e.source.RootPath, RelativePath: "gone.sup"}}},
		"escape":       {SourceID: testSource, TrackID: testSupTrack, Format: SidecarPGS, Files: []domain.ProbeSource{{RootPath: e.source.RootPath, RelativePath: "../Film.eng.sup"}}},
	} {
		if _, err := e.service.LocateSidecar(context.Background(), request); err != ErrNotFound {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// A file over the bound is not queued at all (sparse, nothing read).
	huge := filepath.Join(e.source.RootPath, "Huge.sup")
	file, err := os.Create(huge)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(MaxSidecarBytes + 1); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	request := good
	request.Files = []domain.ProbeSource{{RootPath: e.source.RootPath, RelativePath: "Huge.sup"}}
	if _, err := e.service.LocateSidecar(context.Background(), request); err != ErrNotFound || e.service.Stats().Queued != 0 {
		t.Fatal("oversized file queued")
	}
	var absent *Service
	if _, err := absent.LocateSidecar(context.Background(), good); err != ErrNotFound {
		t.Fatal("nil service located")
	}
	if e.recognizer.calls.Load() != 0 {
		t.Fatal("refused requests reached the recognizer")
	}
}

func TestServiceStopsAJobAtItsDeadline(t *testing.T) {
	e := newEnv(t, Config{})
	e.recognizer.gate = make(chan struct{})
	var cues []bitmapsubtest.Cue
	for i := range 8 {
		cues = append(cues, bitmapsubtest.Cue{Start: time.Duration(i+1) * time.Second, End: time.Duration(i+1)*time.Second + 500*time.Millisecond, Image: render(t, strings.Repeat("W", i+1)), X: 100, Y: 900})
	}
	sidecarFiles(t, e, map[string][]byte{"Film.eng.sup": encodePGS(t, cues)})
	request := e.supRequest()
	if _, err := e.service.LocateSidecar(context.Background(), request); err != ErrPending {
		t.Fatal(err)
	}
	// The first picture is being recognized and the decoder waits to hand
	// over more; then the job runs out of time.
	waitFor(t, func() bool { return e.recognizer.active.Load() == 1 })
	time.Sleep(50 * time.Millisecond)
	e.clock.Advance(MaxJobDuration + time.Minute)
	close(e.recognizer.gate)
	item, err := e.locateSidecar(t, request)
	if err != nil {
		t.Fatal(err)
	}
	stats := e.settled(t, 1)
	root, _ := os.OpenRoot(e.cache)
	defer func() { _ = root.Close() }()
	value, err := readEntry(root, strings.TrimSuffix(item.RelativePath, "/s0.srt")+"/"+entryFile)
	if err != nil || len(value.Tracks) != 1 || !value.Tracks[0].Truncated || value.Tracks[0].Cues == 0 || value.Tracks[0].Cues >= len(cues) || stats.Completed != 1 {
		t.Fatalf("%v %+v %+v", err, value, stats)
	}
}

func encodePGS(t *testing.T, cues []bitmapsubtest.Cue) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := bitmapsubtest.EncodePGS(&out, 1920, 1080, cues); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// The format sample suite's external .sup and .idx+.sub (B11) go through
// the same path as the synthetic tracks above.
func TestServiceReadsTheFormatSampleBitmapSubtitles(t *testing.T) {
	e := newEnv(t, Config{})
	files := map[string][]byte{}
	for _, sample := range formatsamples.Subtitles() {
		if sample.Format == "sup" || sample.Format == "idx" || sample.SubtitleKind == domain.SidecarSubtitleVobSub {
			files[sample.File] = sample.Data
		}
	}
	if len(files) != 3 {
		t.Fatalf("samples %d", len(files))
	}
	sidecarFiles(t, e, files)
	file := func(name string) domain.ProbeSource {
		return domain.ProbeSource{RootPath: e.source.RootPath, RelativePath: name}
	}
	for _, request := range []SidecarRequest{
		{SourceID: testSource, TrackID: testSupTrack, Format: SidecarPGS, Language: "jpn", Files: []domain.ProbeSource{file("pgs-synthetic.sup")}},
		{SourceID: testSource, TrackID: testIdxTrack, Format: SidecarVobSub, Language: "jpn", Files: []domain.ProbeSource{file("vobsub-synthetic.idx"), file("vobsub-synthetic.sub")}},
	} {
		item, err := e.locateSidecar(t, request)
		if err != nil {
			t.Fatalf("%s: %v (stats %+v)", request.Format, err, e.service.Stats())
		}
		data, _ := os.ReadFile(filepath.Join(e.cache, filepath.FromSlash(item.RelativePath)))
		if !strings.HasPrefix(string(data), "1\n00:00:01,000 --> 00:00:02,0") {
			t.Fatalf("%s srt %q", request.Format, data)
		}
	}
	if stats := e.settled(t, 2); stats.Pictures != 2 || stats.Failed != 0 {
		t.Fatalf("stats %+v", stats)
	}
}
