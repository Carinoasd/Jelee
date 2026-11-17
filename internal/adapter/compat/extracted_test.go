package compat

import (
	"context"
	"encoding/json"
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

type compatExtracted struct {
	root      string
	available bool
	calls     []string
}

func (c *compatExtracted) Available() bool { return c.available }

func (c *compatExtracted) ResolveExtracted(_ context.Context, p access.Principal, sourceID string, kind media.ExtractedKind, index int) (media.Source, error) {
	c.calls = append(c.calls, string(kind))
	if p.Kind != access.ClientNative || sourceID != testSourceID {
		return media.Source{}, media.ErrNotFound
	}
	switch {
	case kind == media.ExtractedSubtitle && index == 3:
		return media.Source{Root: c.root, RelativePath: "t3.srt"}, nil
	case kind == media.ExtractedSubtitle && index == 6:
		return media.Source{Root: c.root, RelativePath: "t6.sup"}, nil
	case kind == media.ExtractedAttachmentStream && index == 4:
		return media.Source{Root: c.root, RelativePath: "a1.ttf"}, nil
	}
	return media.Source{}, media.ErrNotFound
}

func extractedHarness(t *testing.T, available bool) (*libraryHarness, *compatExtracted) {
	t.Helper()
	root := t.TempDir()
	if os.WriteFile(filepath.Join(root, "t3.srt"), []byte("1\n00:00:00,000 --> 00:00:01,000\nembedded\n"), 0o600) != nil || os.WriteFile(filepath.Join(root, "a1.ttf"), []byte("font"), 0o600) != nil {
		t.Fatal("write cache fixture")
	}
	extracted := &compatExtracted{root: root, available: available}
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
	stream := 4
	h.catalog.sources = []domain.PlaybackSource{{
		ID: testSourceID, Container: "mkv", ContentType: "video/x-matroska", Probed: true,
		Video:       []domain.PlaybackVideoTrack{{Index: 0, Codec: "h264", Primary: true}},
		Subtitles:   []domain.PlaybackSubtitleTrack{{Index: 3, Codec: "subrip", Format: "srt", Extractable: true, Title: "English"}, {Index: 5, Codec: "hdmv_pgs_subtitle", Format: "pgs"}},
		Attachments: []domain.PlaybackAttachment{{ID: 1, StreamIndex: &stream, FileName: "Sans.ttf", Font: true}},
	}}
	return h, extracted
}

func TestCompatDeliversExtractedSubtitlesAndFonts(t *testing.T) {
	h, extracted := extractedHarness(t, true)
	info := h.get("/compat/Items/"+wire(testMovieID)+"/PlaybackInfo", nativeToken).Body.String()
	subtitle := "/Videos/" + wire(testMovieID) + "/" + wire(testSourceID) + "/Subtitles/3/0/Stream.srt"
	attachment := "/Videos/" + wire(testMovieID) + "/" + wire(testSourceID) + "/Attachments/4"
	for _, want := range []string{`"DeliveryUrl":"` + subtitle + `"`, `"DeliveryMethod":"External"`, `"Title":"English"`, `"MediaAttachments":[{"Index":4,"FileName":"Sans.ttf","MimeType":"font/ttf","DeliveryUrl":"` + attachment + `"}]`} {
		if !strings.Contains(info, want) {
			t.Fatalf("PlaybackInfo lacks %s: %s", want, info)
		}
	}
	if w := h.get("/compat"+subtitle, nativeToken); w.Code != 200 || !strings.Contains(w.Body.String(), "embedded") || w.Header().Get("Content-Type") != "application/x-subrip; charset=UTF-8" {
		t.Fatalf("subtitle %d %q %v", w.Code, w.Body.String(), w.Header())
	}
	if w := h.get("/compat"+attachment, nativeToken); w.Code != 200 || w.Body.String() != "font" || w.Header().Get("Content-Type") != "font/ttf" {
		t.Fatalf("attachment %d %q", w.Code, w.Body.String())
	}
	calls := len(extracted.calls)
	// Another format, a bitmap track or a web session is never extracted.
	for _, tc := range []struct {
		target, token string
		status        int
	}{
		{"/compat/Videos/" + wire(testMovieID) + "/" + wire(testSourceID) + "/Subtitles/3/0/Stream.vtt", nativeToken, http.StatusConflict},
		{"/compat/Videos/" + wire(testMovieID) + "/" + wire(testSourceID) + "/Subtitles/5/0/Stream.sup", nativeToken, http.StatusConflict},
		{"/compat" + subtitle, webToken, http.StatusUnauthorized},
		{"/compat" + attachment, webToken, http.StatusUnauthorized},
		{"/compat/Videos/" + wire(testMovieID) + "/" + wire(testSourceID) + "/Attachments/04", nativeToken, http.StatusBadRequest},
		{"/compat/Videos/" + wire(testMovieID) + "/" + wire(testSourceID) + "/Attachments/x", nativeToken, http.StatusBadRequest},
		{"/compat/Videos/" + wire(testMovieID) + "/f1000000000040008000000000000009/Attachments/4", nativeToken, http.StatusNotFound},
	} {
		if w := h.get(tc.target, tc.token); w.Code != tc.status {
			t.Fatalf("%s: %d, want %d", tc.target, w.Code, tc.status)
		}
	}
	if len(extracted.calls) != calls {
		t.Fatalf("refused requests reached the resolver: %v", extracted.calls[calls:])
	}
}

func TestCompatKeepsEmbedWhenExtractionIsUnavailable(t *testing.T) {
	h, extracted := extractedHarness(t, false)
	info := h.get("/compat/Items/"+wire(testMovieID)+"/PlaybackInfo", nativeToken).Body.String()
	if strings.Contains(info, "/Subtitles/3/") || strings.Contains(info, "/Attachments/") || !strings.Contains(info, `"MediaAttachments":[{"Index":4,"FileName":"Sans.ttf","MimeType":"font/ttf"}]`) {
		t.Fatalf("unavailable extraction advertised: %s", info)
	}
	if w := h.get("/compat/Videos/"+wire(testMovieID)+"/"+wire(testSourceID)+"/Subtitles/3/0/Stream.srt", nativeToken); w.Code != http.StatusConflict || len(extracted.calls) != 0 {
		t.Fatalf("embedded subtitle without extraction: %d", w.Code)
	}
}

// G15.5: an extractable embedded PGS track becomes External, with the URL of
// its raw cached .sup, only for a client whose SubtitleProfiles accept PGS
// as an external file; VobSub (two files) and DVB stay Embed.
func TestCompatDeliversExtractedPGSOnlyWhenDeclaredExternal(t *testing.T) {
	h, extracted := extractedHarness(t, true)
	pgs := []byte("PG\x00\x01 raw presentation graphics")
	if err := os.WriteFile(filepath.Join(extracted.root, "t6.sup"), pgs, 0o600); err != nil {
		t.Fatal(err)
	}
	h.catalog.sources[0].Subtitles = append(h.catalog.sources[0].Subtitles,
		domain.PlaybackSubtitleTrack{Index: 6, Codec: "hdmv_pgs_subtitle", Format: "pgs", Extractable: true},
		domain.PlaybackSubtitleTrack{Index: 7, Codec: "dvd_subtitle", Format: "vobsub", Extractable: true},
		domain.PlaybackSubtitleTrack{Index: 8, Codec: "dvb_subtitle", Format: "dvb"})
	ph := &playbackHarness{libraryHarness: h}
	target := "/compat/Items/" + wire(testMovieID) + "/PlaybackInfo"
	url := "/Videos/" + wire(testMovieID) + "/" + wire(testSourceID) + "/Subtitles/6/0/Stream.sup"
	streams := func(body string) map[int]map[string]any {
		var answer struct {
			MediaSources []struct{ MediaStreams []map[string]any }
		}
		if err := json.Unmarshal([]byte(body), &answer); err != nil || len(answer.MediaSources) != 1 {
			t.Fatalf("answer %v: %s", err, body)
		}
		out := map[int]map[string]any{}
		for _, s := range answer.MediaSources[0].MediaStreams {
			if s["Type"] == "Subtitle" {
				out[int(s["Index"].(float64))] = s
			}
		}
		return out
	}
	profile := func(subtitles string) string {
		return `{"DeviceProfile":{"DirectPlayProfiles":[{"Container":"mkv","Type":"Video"}],"SubtitleProfiles":` + subtitles + `}}`
	}
	external := streams(ph.post(target, nativeToken, profile(`[{"Format":"pgssub","Method":"External"},{"Format":"dvdsub","Method":"External"},{"Format":"dvbsub","Method":"External"}]`)).Body.String())
	if s := external[6]; s["DeliveryMethod"] != "External" || s["DeliveryUrl"] != url || s["SupportsExternalStream"] != true || s["IsExternal"] == true {
		t.Fatalf("PGS declared external: %v", s)
	}
	for _, index := range []int{7, 8} {
		if s := external[index]; s["DeliveryMethod"] != "Embed" || s["DeliveryUrl"] != nil {
			t.Fatalf("stream %d left the container: %v", index, s)
		}
	}
	// Embed, Encode, another format or no profile at all keep PGS inside.
	for _, body := range []string{
		profile(`[{"Format":"pgssub","Method":"Embed"}]`),
		profile(`[{"Format":"pgssub","Method":"Encode"},{"Format":"srt","Method":"External"}]`),
		profile(`[{"Format":"pgssub","Method":"External","Container":"mp4"}]`),
		`{}`,
	} {
		if s := streams(ph.post(target, nativeToken, body).Body.String())[6]; s["DeliveryMethod"] != "Embed" || s["DeliveryUrl"] != nil {
			t.Fatalf("%s: %v", body, s)
		}
	}
	if s := streams(h.get(target, nativeToken).Body.String())[6]; s["DeliveryMethod"] != "Embed" {
		t.Fatalf("GET PlaybackInfo: %v", s)
	}
	// The route serves the raw stream in its own format names only.
	for _, format := range []string{"sup", "pgssub", "PGS"} {
		w := h.get("/compat/Videos/"+wire(testMovieID)+"/"+wire(testSourceID)+"/Subtitles/6/0/Stream."+format, nativeToken)
		if w.Code != 200 || w.Body.String() != string(pgs) || w.Header().Get("Content-Type") != "application/x-pgs" {
			t.Fatalf("Stream.%s: %d %q %v", format, w.Code, w.Body.String(), w.Header())
		}
	}
	calls := len(extracted.calls)
	for _, target := range []string{"/Subtitles/6/0/Stream.srt", "/Subtitles/7/0/Stream.sub", "/Subtitles/7/0/Stream.idx", "/Subtitles/8/0/Stream.sup"} {
		if w := h.get("/compat/Videos/"+wire(testMovieID)+"/"+wire(testSourceID)+target, nativeToken); w.Code != http.StatusConflict {
			t.Fatalf("%s: %d", target, w.Code)
		}
	}
	if len(extracted.calls) != calls {
		t.Fatalf("refused requests reached the resolver: %v", extracted.calls[calls:])
	}
}
