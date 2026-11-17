package compat

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/adapter/media"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// compatOCR adds OCR results to the extraction fake: the PGS track at
// stream 5 has its SRT, the VobSub track at stream 6 is still pending.
type compatOCR struct {
	*compatExtracted
	ocr bool
}

func (c compatOCR) OCRAvailable() bool { return c.ocr }

func (c compatOCR) ResolveExtracted(ctx context.Context, p access.Principal, sourceID string, kind media.ExtractedKind, index int) (media.Source, error) {
	if kind == media.ExtractedOCRSubtitle {
		c.calls = append(c.calls, string(kind))
		if p.Kind == access.ClientNative && sourceID == testSourceID && index == 5 {
			return media.Source{Root: c.root, RelativePath: "s5.srt"}, nil
		}
		return media.Source{}, media.ErrNotFound
	}
	return c.compatExtracted.ResolveExtracted(ctx, p, sourceID, kind, index)
}

func ocrHarness(t *testing.T, available bool) *libraryHarness {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "s5.srt"), []byte("1\n00:00:01,000 --> 00:00:02,000\nrecognized\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	extracted := compatOCR{compatExtracted: &compatExtracted{root: root, available: true}, ocr: available}
	delivery, err := media.NewHandler(&fakeResolver{root: root}, media.Options{MaxConcurrent: 4, WriteTimeout: 5 * time.Second, WriteError: func(w http.ResponseWriter, _ *http.Request, err error) {
		if errors.Is(err, media.ErrNotFound) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}})
	if err != nil {
		t.Fatal(err)
	}
	h := newLibraryHarnessConfig(t, http.StatusNotFound, func(o *LibraryOptions) { o.DirectPlay, o.Delivery, o.Extracted = true, delivery, extracted })
	h.catalog.sources = []domain.PlaybackSource{{
		ID: testSourceID, Container: "mkv", ContentType: "video/x-matroska", Probed: true,
		Video: []domain.PlaybackVideoTrack{{Index: 0, Codec: "h264", Primary: true}},
		Subtitles: []domain.PlaybackSubtitleTrack{
			{Index: 3, Codec: "subrip", Format: "srt", Extractable: true, Title: "English"},
			{Index: 5, Codec: "hdmv_pgs_subtitle", Format: "pgs", Language: "chi", Title: "Chinese", OCRSource: true},
			{Index: 6, Codec: "dvd_subtitle", Format: "vobsub", Language: "eng", OCRSource: true},
		},
	}}
	return h
}

func TestCompatListsAndDeliversOCRDerivedSubtitles(t *testing.T) {
	h := ocrHarness(t, true)
	info := h.get("/compat/Items/"+wire(testMovieID)+"/PlaybackInfo", nativeToken).Body.String()
	// Streams 0..6 exist and nothing is external, so the OCR candidates
	// are numbered 7 (PGS 5) and 8 (VobSub 6); only 7 has text yet.
	ocr := "/Videos/" + wire(testMovieID) + "/" + wire(testSourceID) + "/Subtitles/7/0/Stream.srt"
	for _, want := range []string{`"Title":"Chinese (OCR)"`, `"Index":7,"IsExternal":true`, `"DeliveryUrl":"` + ocr + `"`} {
		if !strings.Contains(info, want) {
			t.Fatalf("PlaybackInfo lacks %s: %s", want, info)
		}
	}
	if strings.Contains(info, "/Subtitles/8/") || strings.Contains(info, "eng (OCR)") {
		t.Fatalf("pending OCR track listed: %s", info)
	}
	// The bitmap tracks stay listed as they are.
	if !strings.Contains(info, `"Codec":"pgs"`) || !strings.Contains(info, `"Codec":"vobsub"`) {
		t.Fatalf("bitmap tracks missing: %s", info)
	}
	if w := h.get("/compat"+ocr, nativeToken); w.Code != 200 || !strings.Contains(w.Body.String(), "recognized") || w.Header().Get("Content-Type") != "application/x-subrip; charset=UTF-8" {
		t.Fatalf("ocr subtitle %d %q %v", w.Code, w.Body.String(), w.Header())
	}
	for _, tc := range []struct {
		target, token string
		status        int
	}{
		{"/compat/Videos/" + wire(testMovieID) + "/" + wire(testSourceID) + "/Subtitles/7/0/Stream.vtt", nativeToken, http.StatusConflict},
		{"/compat/Videos/" + wire(testMovieID) + "/" + wire(testSourceID) + "/Subtitles/8/0/Stream.srt", nativeToken, http.StatusNotFound},
		{"/compat/Videos/" + wire(testMovieID) + "/" + wire(testSourceID) + "/Subtitles/5/0/Stream.sup", nativeToken, http.StatusConflict},
		{"/compat" + ocr, webToken, http.StatusUnauthorized},
	} {
		if w := h.get(tc.target, tc.token); w.Code != tc.status {
			t.Fatalf("%s: %d, want %d", tc.target, w.Code, tc.status)
		}
	}
}

func TestCompatHidesOCRWithoutTheRuntime(t *testing.T) {
	h := ocrHarness(t, false)
	info := h.get("/compat/Items/"+wire(testMovieID)+"/PlaybackInfo", nativeToken).Body.String()
	if strings.Contains(info, "(OCR)") || strings.Contains(info, "/Subtitles/7/") {
		t.Fatalf("OCR listed without its runtime: %s", info)
	}
	if w := h.get("/compat/Videos/"+wire(testMovieID)+"/"+wire(testSourceID)+"/Subtitles/7/0/Stream.srt", nativeToken); w.Code != http.StatusNotFound {
		t.Fatalf("OCR index without runtime: %d", w.Code)
	}
}

const (
	compatSupTrack = "00000000-0000-4000-8000-0000000000c1"
	compatIdxTrack = "00000000-0000-4000-8000-0000000000c2"
	compatSubTrack = "00000000-0000-4000-8000-0000000000c3"
)

// ResolveSidecarOCR: the external .sup has its SRT, the .idx pair is
// still pending.
func (c compatOCR) ResolveSidecarOCR(_ context.Context, p access.Principal, sourceID, trackID string) (media.Source, error) {
	c.calls = append(c.calls, "sidecar:"+trackID)
	if p.Kind == access.ClientNative && sourceID == testSourceID && trackID == compatSupTrack {
		return media.Source{Root: c.root, RelativePath: "s5.srt"}, nil
	}
	return media.Source{}, media.ErrNotFound
}

func TestCompatListsAndDeliversOCRFromExternalBitmapSubtitles(t *testing.T) {
	h := ocrHarness(t, true)
	h.catalog.sources[0].External = []domain.PlaybackExternalTrack{
		{ID: compatSupTrack, Kind: domain.SidecarKindSubtitle, Format: "sup", Codec: "pgs", Language: "chi", OCRSource: true},
		{ID: compatIdxTrack, Kind: domain.SidecarKindSubtitle, Format: "idx", Codec: "vobsub", Language: "eng", PairedTrackID: compatSubTrack, OCRSource: true},
		{ID: "00000000-0000-4000-8000-0000000000c4", Kind: domain.SidecarKindAudio, Format: "ac3", Codec: "ac3"},
	}
	info := h.get("/compat/Items/"+wire(testMovieID)+"/PlaybackInfo", nativeToken).Body.String()
	// Streams 0..6 embedded, 7 and 8 the external .sup and .idx files,
	// 9 and 10 the embedded OCR slots as before the files were read, 11
	// and 12 the external OCR slots, 13 the external audio.
	base := "/Videos/" + wire(testMovieID) + "/" + wire(testSourceID)
	for _, want := range []string{
		`"DeliveryUrl":"` + base + `/Subtitles/7/0/Stream.sup"`, `"DeliveryUrl":"` + base + `/Subtitles/8/0/Stream.idx"`,
		`"DeliveryUrl":"` + base + `/Subtitles/9/0/Stream.srt"`, `"DeliveryUrl":"` + base + `/Subtitles/11/0/Stream.srt"`,
		`"Title":"chi (OCR)"`, `"Index":13`,
	} {
		if !strings.Contains(info, want) {
			t.Fatalf("PlaybackInfo lacks %s: %s", want, info)
		}
	}
	if strings.Contains(info, "/Subtitles/12/") || strings.Contains(info, "/Subtitles/10/") {
		t.Fatalf("pending OCR track listed: %s", info)
	}
	if w := h.get("/compat"+base+"/Subtitles/11/0/Stream.srt", nativeToken); w.Code != 200 || !strings.Contains(w.Body.String(), "recognized") || w.Header().Get("Content-Type") != "application/x-subrip; charset=UTF-8" {
		t.Fatalf("external ocr subtitle %d %q %v", w.Code, w.Body.String(), w.Header())
	}
	for _, tc := range []struct {
		target, token string
		status        int
	}{
		{"/compat" + base + "/Subtitles/11/0/Stream.vtt", nativeToken, http.StatusConflict},
		{"/compat" + base + "/Subtitles/12/0/Stream.srt", nativeToken, http.StatusNotFound},
		{"/compat" + base + "/Subtitles/11/0/Stream.srt", webToken, http.StatusUnauthorized},
	} {
		if w := h.get(tc.target, tc.token); w.Code != tc.status {
			t.Fatalf("%s: %d, want %d", tc.target, w.Code, tc.status)
		}
	}
}
