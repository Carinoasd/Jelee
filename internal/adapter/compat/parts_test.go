package compat

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

const testPartID = "e0000000-0000-4000-8000-0000000000e2"

// multiPartSources is the test movie as one version in two parts (G21.1).
func multiPartSources() []domain.PlaybackSource {
	part := func(id string, duration int64) domain.PlaybackSource {
		return domain.PlaybackSource{ID: id, Container: "mkv", ContentType: "video/x-matroska", Probed: true, DurationMicros: int64p(duration),
			Video: []domain.PlaybackVideoTrack{{Index: 0, Codec: "h264", Default: true, Primary: true}},
			Audio: []domain.PlaybackAudioTrack{{Index: 1, Codec: "aac", Default: true}}, Subtitles: []domain.PlaybackSubtitleTrack{}, External: []domain.PlaybackExternalTrack{}}
	}
	first, second := part(testSourceID, 3_000_000_000), part(testPartID, 2_000_000_000)
	version := first
	version.Parts = []domain.PlaybackPart{{Number: 1, StartMicros: int64p(0), PlaybackSource: first}, {Number: 2, StartMicros: int64p(3_000_000_000), PlaybackSource: second}}
	version.TotalDurationMicros = int64p(5_000_000_000)
	return []domain.PlaybackSource{version}
}

func TestAdditionalParts(t *testing.T) {
	h := newPlaybackHarness(t, http.StatusNotFound)
	h.catalog.sources = multiPartSources()

	w := h.get("/compat/Videos/"+wire(testMovieID)+"/AdditionalParts", nativeToken)
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	var parts struct {
		Items []struct {
			ID           string `json:"Id"`
			Type         string
			RunTimeTicks int64
			MediaSources []struct {
				ID                 string `json:"Id"`
				SupportsDirectPlay bool
				RunTimeTicks       int64
			}
		}
		TotalRecordCount int
	}
	if err := json.Unmarshal(w.Body.Bytes(), &parts); err != nil {
		t.Fatal(err)
	}
	if parts.TotalRecordCount != 1 || len(parts.Items) != 1 || parts.Items[0].ID != wire(testMovieID) || parts.Items[0].Type != "Movie" ||
		parts.Items[0].RunTimeTicks != 20_000_000_000 || len(parts.Items[0].MediaSources) != 1 ||
		parts.Items[0].MediaSources[0].ID != wire(testPartID) || !parts.Items[0].MediaSources[0].SupportsDirectPlay {
		t.Fatalf("additional parts: %s", w.Body.String())
	}

	// The item reports the part count and the combined runtime.
	var item struct {
		PartCount    int
		RunTimeTicks int64
		MediaSources []struct {
			ID string `json:"Id"`
		}
	}
	if err := json.Unmarshal(h.get("/compat/Items/"+wire(testMovieID), nativeToken).Body.Bytes(), &item); err != nil {
		t.Fatal(err)
	}
	if item.PartCount != 2 || item.RunTimeTicks != 50_000_000_000 || len(item.MediaSources) != 1 || item.MediaSources[0].ID != wire(testSourceID) {
		t.Fatalf("item: %+v", item)
	}

	// PlaybackInfo points at part 1; naming part 2 answers with part 2.
	var info struct {
		MediaSources []struct {
			ID           string `json:"Id"`
			RunTimeTicks int64
		}
	}
	if err := json.Unmarshal(h.get("/compat/Items/"+wire(testMovieID)+"/PlaybackInfo", nativeToken).Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if len(info.MediaSources) != 1 || info.MediaSources[0].ID != wire(testSourceID) || info.MediaSources[0].RunTimeTicks != 30_000_000_000 {
		t.Fatalf("playback info: %+v", info)
	}
	if err := json.Unmarshal(h.get("/compat/Items/"+wire(testMovieID)+"/PlaybackInfo?MediaSourceId="+wire(testPartID), nativeToken).Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if len(info.MediaSources) != 1 || info.MediaSources[0].ID != wire(testPartID) || info.MediaSources[0].RunTimeTicks != 20_000_000_000 {
		t.Fatalf("part 2 playback info: %+v", info)
	}

	// A single-file item has none; a missing item is the hidden status.
	h.catalog.sources = nil
	if w = h.get("/compat/Videos/"+wire(testMovieID)+"/AdditionalParts", nativeToken); w.Code != http.StatusOK || w.Body.String() != `{"Items":[],"TotalRecordCount":0,"StartIndex":0}` {
		t.Fatalf("single file: %d %q", w.Code, w.Body.String())
	}
	assertEmpty(t, "missing", h.get("/compat/Videos/"+wire("d0000000-0000-4000-8000-0000000000dd")+"/AdditionalParts", nativeToken), http.StatusNotFound)
	if w = h.get("/compat/Videos/not-an-id/AdditionalParts", nativeToken); w.Code != http.StatusBadRequest {
		t.Fatal("bad id", w.Code)
	}
	if w = h.get("/compat/Videos/"+wire(testMovieID)+"/AdditionalParts", ""); w.Code != http.StatusUnauthorized {
		t.Fatal("anonymous", w.Code)
	}
}
