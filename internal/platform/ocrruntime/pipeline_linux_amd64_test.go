package ocrruntime

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/image/font/gofont/goregular"

	"github.com/MoYuanCN/Jelee/internal/adapter/bitmapsub/bitmapsubtest"
	"github.com/MoYuanCN/Jelee/internal/adapter/matroska"
	"github.com/MoYuanCN/Jelee/internal/adapter/subtitleocr"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/sandbox"
)

func privateDirectory(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// muxFixture writes a PGS and a VobSub track with known English text and
// muxes them, unchanged, into a Matroska file with the project's pinned
// mkvmerge (developer wrapper).
func muxFixture(t *testing.T, root string, pgsLines, vobLines []string) string {
	t.Helper()
	mkvmerge := filepath.Join(root, ".bin", "mkvmerge")
	skipMissing(t, mkvmerge, "make bootstrap-matroska")
	work := t.TempDir()
	cues := func(lines []string, size float64, width, height int) []bitmapsubtest.Cue {
		var out []bitmapsubtest.Cue
		for i, line := range lines {
			img, err := bitmapsubtest.RenderText(goregular.TTF, line, size, whiteOnBlack)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, bitmapsubtest.Cue{Start: time.Duration(i*3+1) * time.Second, End: time.Duration(i*3+2) * time.Second, Image: img, X: max((width-img.Bounds().Dx())/2, 0), Y: height - img.Bounds().Dy() - 30})
		}
		return out
	}
	var pgs, index, sub bytes.Buffer
	if err := bitmapsubtest.EncodePGS(&pgs, 1920, 1080, cues(pgsLines, 48, 1920, 1080)); err != nil {
		t.Fatal(err)
	}
	if err := bitmapsubtest.EncodeVobSub(&index, &sub, 720, 480, cues(vobLines, 28, 720, 480)); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"film.sup": pgs.Bytes(), "dvd.idx": index.Bytes(), "dvd.sub": sub.Bytes()} {
		if err := os.WriteFile(filepath.Join(work, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	output := filepath.Join(t.TempDir(), "Film.mkv")
	command := exec.Command(mkvmerge, "--quiet", "-o", output, "--language", "0:eng", filepath.Join(work, "film.sup"), "--language", "0:eng", filepath.Join(work, "dvd.idx"))
	if result, err := command.CombinedOutput(); err != nil {
		t.Fatalf("mkvmerge: %v %s", err, result)
	}
	return output
}

func TestRealOCRPipelineFromMatroska(t *testing.T) {
	requireHostRuntime(t, "JELEE_OCR_HOST_RUNTIME")
	requireHostRuntime(t, "JELEE_MATROSKA_HOST_RUNTIME")
	root := hostToolsRoot(t)
	pgsLines := []string{"The bridge is closed tonight.", "Take the north road instead."}
	vobLines := []string{"Who sent you here?"}
	media := muxFixture(t, root, pgsLines, vobLines)
	before := digestFile(t, media)
	runners := hostRunners(t, 2, hostOCRRegistration(t, root),
		hostMatroskaRegistration(t, root, "mkvmerge", sandbox.ToolIdentify), hostMatroskaRegistration(t, root, "mkvextract", sandbox.ToolExtract))
	textCache := privateDirectory(t, "text-cache")
	extractor, err := matroska.New(runners[sandbox.ToolIdentify], runners[sandbox.ToolExtract], matroska.Config{CacheRoot: textCache})
	if err != nil {
		t.Fatal(err)
	}
	recognizer, err := subtitleocr.NewRecognizer(runners[sandbox.ToolOCR], privateDirectory(t, "pictures"))
	if err != nil {
		t.Fatal(err)
	}
	identity, err := Identity([]string{"eng"})
	if err != nil {
		t.Fatal(err)
	}
	cache, work := privateDirectory(t, "ocr-cache"), privateDirectory(t, "ocr-work")
	service, err := subtitleocr.New(extractor, recognizer, subtitleocr.Config{CacheRoot: cache, WorkRoot: work, Languages: []string{"eng"}, PicturesPerMinute: 600, Concurrency: 2, QueueSize: 2, Identity: identity})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = service.Close() }()
	source := domain.ProbeSource{RootPath: filepath.Dir(media), RelativePath: filepath.Base(media)}
	const sourceID = "00000000-0000-4000-8000-0000000000a1"
	locate := func(index int) string {
		deadline := time.Now().Add(2 * time.Minute)
		for {
			item, err := service.Locate(context.Background(), sourceID, source, index)
			if err == nil {
				data, readErr := os.ReadFile(filepath.Join(cache, filepath.FromSlash(item.RelativePath)))
				if readErr != nil {
					t.Fatal(readErr)
				}
				return string(data)
			}
			if !errors.Is(err, subtitleocr.ErrPending) || time.Now().After(deadline) {
				t.Fatalf("index %d: %v (stats %+v)", index, err, service.Stats())
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	pgs, vobsub := locate(0), locate(1)
	t.Logf("PGS track as SRT:\n%s\nVobSub track as SRT:\n%s", pgs, vobsub)
	for _, check := range []struct {
		srt, timing string
		lines       []string
	}{{pgs, "00:00:01,000 --> 00:00:02,000", pgsLines}, {vobsub, "00:00:01,000 --> 00:00:02,0", vobLines}} {
		if !strings.Contains(check.srt, check.timing) {
			t.Fatalf("timing missing: %q", check.srt)
		}
		for _, line := range check.lines {
			if !strings.Contains(check.srt, line) {
				t.Fatalf("%q not recognized in %q", line, check.srt)
			}
		}
	}
	if digestFile(t, media) != before {
		t.Fatal("the original Matroska file changed")
	}
	if entries, _ := os.ReadDir(textCache); len(entries) != 0 {
		t.Fatal("bitmap OCR wrote into the text extraction cache")
	}
	if entries, _ := os.ReadDir(work); len(entries) != 0 {
		t.Fatal("work files left behind")
	}
	stats := service.Stats()
	if stats.Completed != 1 || stats.Recognized != 3 || stats.Pictures != 3 {
		t.Fatalf("stats %+v", stats)
	}
}
