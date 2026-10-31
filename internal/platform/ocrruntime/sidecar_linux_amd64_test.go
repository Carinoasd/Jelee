package ocrruntime

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/image/font/gofont/goregular"

	"github.com/MoYuanCN/Jelee/internal/adapter/bitmapsub/bitmapsubtest"
	"github.com/MoYuanCN/Jelee/internal/adapter/matroska"
	"github.com/MoYuanCN/Jelee/internal/adapter/probe"
	"github.com/MoYuanCN/Jelee/internal/adapter/subtitleocr"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/sandbox"
)

// noExtractor stands in for mkvtoolnix: external bitmap subtitles are read
// in place and must never reach it.
type noExtractor struct{ t *testing.T }

func (n noExtractor) ExtractBitmaps(context.Context, domain.ProbeSource, *probe.Input, string) ([]matroska.BitmapTrack, error) {
	n.t.Error("external bitmap subtitles reached mkvextract")
	return nil, matroska.ErrUnavailable
}

// TestRealOCRPipelineFromSidecarFiles recognizes an external .sup and an
// external .idx+.sub pair with the sandboxed Tesseract runtime; the files
// are only read.
func TestRealOCRPipelineFromSidecarFiles(t *testing.T) {
	requireHostRuntime(t, "JELEE_OCR_HOST_RUNTIME")
	root := hostToolsRoot(t)
	library := t.TempDir()
	pgsLines := []string{"The bridge is closed tonight.", "Take the north road instead."}
	vobLines := []string{"Who sent you here?"}
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
	digests := map[string]string{}
	for name, data := range map[string][]byte{"Film.eng.sup": pgs.Bytes(), "Film.eng.idx": index.Bytes(), "Film.eng.sub": sub.Bytes()} {
		path := filepath.Join(library, name)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		digests[path] = digestFile(t, path)
	}
	runners := hostRunners(t, 2, hostOCRRegistration(t, root))
	recognizer, err := subtitleocr.NewRecognizer(runners[sandbox.ToolOCR], privateDirectory(t, "pictures"))
	if err != nil {
		t.Fatal(err)
	}
	identity, err := Identity([]string{"eng"})
	if err != nil {
		t.Fatal(err)
	}
	cache, work := privateDirectory(t, "ocr-cache"), privateDirectory(t, "ocr-work")
	service, err := subtitleocr.New(noExtractor{t}, recognizer, subtitleocr.Config{CacheRoot: cache, WorkRoot: work, Languages: []string{"eng"}, PicturesPerMinute: 600, Concurrency: 2, QueueSize: 2, Identity: identity})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = service.Close() }()
	const sourceID = "00000000-0000-4000-8000-0000000000b1"
	file := func(name string) domain.ProbeSource { return domain.ProbeSource{RootPath: library, RelativePath: name} }
	locate := func(request subtitleocr.SidecarRequest) string {
		deadline := time.Now().Add(2 * time.Minute)
		for {
			item, err := service.LocateSidecar(context.Background(), request)
			if err == nil {
				data, readErr := os.ReadFile(filepath.Join(cache, filepath.FromSlash(item.RelativePath)))
				if readErr != nil {
					t.Fatal(readErr)
				}
				return string(data)
			}
			if !errors.Is(err, subtitleocr.ErrPending) || time.Now().After(deadline) {
				t.Fatalf("%s: %v (stats %+v)", request.TrackID, err, service.Stats())
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	supSRT := locate(subtitleocr.SidecarRequest{SourceID: sourceID, TrackID: "00000000-0000-4000-8000-0000000000b2", Format: subtitleocr.SidecarPGS, Language: "eng", Files: []domain.ProbeSource{file("Film.eng.sup")}})
	vobSRT := locate(subtitleocr.SidecarRequest{SourceID: sourceID, TrackID: "00000000-0000-4000-8000-0000000000b3", Format: subtitleocr.SidecarVobSub, Language: "eng", Files: []domain.ProbeSource{file("Film.eng.idx"), file("Film.eng.sub")}})
	t.Logf("external .sup as SRT:\n%s\nexternal .idx+.sub as SRT:\n%s", supSRT, vobSRT)
	for _, check := range []struct {
		srt, timing string
		lines       []string
	}{{supSRT, "00:00:01,000 --> 00:00:02,000", pgsLines}, {vobSRT, "00:00:01,000 --> 00:00:02,0", vobLines}} {
		if !strings.Contains(check.srt, check.timing) {
			t.Fatalf("timing missing: %q", check.srt)
		}
		for _, line := range check.lines {
			if !strings.Contains(check.srt, line) {
				t.Fatalf("%q not recognized in %q", line, check.srt)
			}
		}
	}
	for path, digest := range digests {
		if digestFile(t, path) != digest {
			t.Fatalf("%s changed", filepath.Base(path))
		}
	}
	if entries, _ := os.ReadDir(library); len(entries) != 3 {
		t.Fatal("OCR wrote next to the external files")
	}
	if entries, _ := os.ReadDir(work); len(entries) != 0 {
		t.Fatal("work files left behind")
	}
}
