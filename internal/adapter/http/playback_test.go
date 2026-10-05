package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

type fakePlaybackRepository struct {
	calls  int
	actor  domain.Actor
	result []domain.PlaybackSourceRecord
	err    error
}

func (r *fakePlaybackRepository) ListPlaybackSources(_ context.Context, actor domain.Actor, item string) ([]domain.PlaybackSourceRecord, error) {
	r.calls++
	r.actor = actor
	if item != itemID {
		return nil, domain.ErrNotFound
	}
	return r.result, r.err
}

func playbackHTTPFixture(t *testing.T, wired bool) (http.Handler, *fakePlaybackRepository, *bytes.Buffer) {
	t.Helper()
	codec := func(s string) *string { return &s }
	n := func(v int64) *int64 { return &v }
	repo := &fakePlaybackRepository{result: []domain.PlaybackSourceRecord{{
		ID: sourceID, ContentType: "video/mp4", FileName: "Film.1080p.mp4",
		Metadata: &domain.MediaMetadata{Format: domain.MediaFormat{BitRate: n(8_000_000)}, Streams: []domain.MediaStream{
			{Index: 0, Kind: "video", Codec: codec("h264"), Video: &domain.MediaVideo{Width: n(1920), Height: n(1080)}},
			{Index: 1, Kind: "audio", Codec: codec("eac3"), Audio: &domain.MediaAudio{}},
		}},
		Sidecars: []domain.SidecarTrackRecord{{ID: libraryID, Track: domain.SidecarTrack{Kind: "subtitle", Format: "srt", Language: "en"}, Size: 10}},
	}}}
	catalog := app.NewCatalog(&fakeRepository{})
	if wired {
		var err error
		if catalog, err = catalog.WithPlayback(repo); err != nil {
			t.Fatal(err)
		}
	}
	cfg := validConfig()
	cfg.EnableCatalog, cfg.EnableDirect = true, true
	logs := &bytes.Buffer{}
	handler, err := New(cfg, &fakeBackend{}, catalog, &fakeResolver{}, slog.New(slog.NewJSONHandler(logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return handler, repo, logs
}

func playbackHTTPRequest(handler http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	var r *http.Request
	if reader != nil {
		r = httptest.NewRequest(method, "http://localhost"+path, reader)
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, "http://localhost"+path, nil)
	}
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func playbackErrorCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("error body: %s", w.Body.String())
	}
	return envelope.Error.Code
}

func TestPlaybackInfoAndCheckHTTP(t *testing.T) {
	handler, repo, logs := playbackHTTPFixture(t, true)
	native, web := strings.Repeat("n", 43), strings.Repeat("w", 43)
	info := "/api/v1/items/" + itemID + "/playback"
	check := info + "/check"

	w := playbackHTTPRequest(handler, "GET", info, "", native)
	var got struct {
		Data struct {
			ItemID   string                  `json:"itemId"`
			Delivery map[string]bool         `json:"delivery"`
			Sources  []domain.PlaybackSource `json:"sources"`
		} `json:"data"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Data.ItemID != itemID || len(got.Data.Sources) != 1 {
		t.Fatalf("info %d: %s", w.Code, w.Body.String())
	}
	if d := got.Data.Delivery; !d["directPlay"] || d["transcoding"] || d["hls"] || d["dash"] || d["remux"] || len(d) != 5 {
		t.Fatalf("delivery declaration differs: %v", d)
	}
	source := got.Data.Sources[0]
	if source.Container != "mp4" || !source.Probed || len(source.External) != 1 || source.External[0].Codec != "srt" || repo.actor.UserID != userID || repo.actor.SessionID != sessionID {
		t.Fatalf("source differs: %+v", source)
	}
	if want := "/api/v1/sources/" + sourceID + "/subtitles/" + libraryID; source.External[0].URL != want {
		t.Fatalf("external track URL %q, want %q", source.External[0].URL, want)
	}

	w = playbackHTTPRequest(handler, "POST", check, `{"containers":["MP4"],"videoCodecs":["avc"],"audioCodecs":["aac"],"subtitleFormats":["subrip"],"maxBitrate":8000000}`, native)
	var decided struct {
		Data struct {
			DirectPlayable bool                      `json:"directPlayable"`
			Decisions      []domain.PlaybackDecision `json:"decisions"`
		} `json:"data"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &decided) != nil || decided.Data.DirectPlayable || len(decided.Data.Decisions) != 1 {
		t.Fatalf("check %d: %s", w.Code, w.Body.String())
	}
	d := decided.Data.Decisions[0]
	if d.DirectPlay || d.Code != "direct_play_unsupported" || strings.Join(d.Reasons, ",") != "audio_codec_unsupported" || len(d.Tracks) != 2 ||
		d.Tracks[0].Supported || d.Tracks[0].Reason != "audio_codec_unsupported" || !d.Tracks[1].Supported || !d.Tracks[1].External {
		t.Fatalf("decision differs: %+v", d)
	}
	// G16.4: the unreadable track is marked, nothing replaces it, and only the
	// external track carries its direct delivery URL.
	if d.Tracks[0].Code != "direct_play_unsupported" || d.Tracks[0].URL != "" || d.Tracks[1].Code != "" ||
		d.Tracks[1].URL != "/api/v1/sources/"+sourceID+"/subtitles/"+libraryID {
		t.Fatalf("track decisions differ: %+v", d.Tracks)
	}
	if !strings.Contains(logs.String(), `"msg":"direct play unsupported"`) || !strings.Contains(logs.String(), "audio_codec_unsupported") || strings.Contains(logs.String(), "Film") {
		t.Fatalf("unsupported decision not traceable in logs: %s", logs.String())
	}
	w = playbackHTTPRequest(handler, "POST", check, `{"containers":["mp4"],"videoCodecs":["h264"],"audioCodecs":["eac3"],"maxBitrate":8000000}`, native)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"directPlayable":true`) || !strings.Contains(w.Body.String(), `"reasons":[]`) {
		t.Fatalf("supported check %d: %s", w.Code, w.Body.String())
	}
	w = playbackHTTPRequest(handler, "POST", check, `{}`, native)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "container_unsupported") {
		t.Fatalf("empty declaration %d: %s", w.Code, w.Body.String())
	}

	calls := repo.calls
	for _, tc := range []struct {
		name, method, path, body, token string
		status                          int
		code                            string
	}{
		{"web info", "GET", info, "", web, 403, "web_playback_disabled"},
		{"web check", "POST", check, `{}`, web, 403, "web_playback_disabled"},
		{"anonymous", "GET", info, "", "x", 401, "authentication_required"},
		{"transcode query info", "GET", info + "?videoCodec=h264", "", native, 409, "transcode_disabled"},
		{"transcode query check", "POST", check + "?maxStreamingBitrate=1", `{}`, native, 409, "transcode_disabled"},
		{"transcode on web too", "GET", info + "?transcodingProtocol=hls", "", web, 409, "transcode_disabled"},
		{"transcode body", "POST", check, `{"containers":["mp4"],"audioCodec":"aac"}`, native, 409, "transcode_disabled"},
		{"transcode body bitrate", "POST", check, `{"maxStreamingBitrate":1}`, native, 409, "transcode_disabled"},
		{"transcode body nested", "POST", check, `{"profiles":[{"subtitleMethod":"Encode"}]}`, native, 409, "transcode_disabled"},
		{"transcode body container", "POST", check, `{"container":"hls"}`, native, 409, "transcode_disabled"},
		{"query field", "GET", info + "?deviceId=1", "", native, 400, "invalid_request"},
		{"unknown body field", "POST", check, `{"profiles":[]}`, native, 400, "invalid_request"},
		{"bad token", "POST", check, `{"containers":["mp 4"]}`, native, 400, "invalid_request"},
		{"too many tokens", "POST", check, `{"containers":[` + strings.Repeat(`"a",`, 32) + `"b"]}`, native, 400, "invalid_request"},
		{"negative bitrate", "POST", check, `{"maxBitrate":-1}`, native, 400, "invalid_request"},
		{"null field", "POST", check, `{"containers":null}`, native, 400, "invalid_request"},
		{"missing item", "GET", "/api/v1/items/" + libraryID + "/playback", "", native, 404, "not_found"},
		{"malformed item", "GET", "/api/v1/items/x/playback", "", native, 404, "not_found"},
		{"wrong method", "PUT", check, `{}`, native, 405, "method_not_allowed"},
	} {
		w := playbackHTTPRequest(handler, tc.method, tc.path, tc.body, tc.token)
		if w.Code != tc.status || playbackErrorCode(t, w) != tc.code {
			t.Errorf("%s: %d %s", tc.name, w.Code, w.Body.String())
		}
	}
	// Only the lookup of the missing item reached storage; a malformed ID
	// is refused before it.
	if repo.calls != calls+1 {
		t.Fatalf("rejected requests reached storage: %d", repo.calls-calls)
	}
	r := httptest.NewRequest("POST", "http://localhost"+check, strings.NewReader(`{}`))
	r.Header.Set("Authorization", "Bearer "+native)
	r.Header.Set("Content-Type", "text/plain")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 415 || playbackErrorCode(t, w) != "unsupported_media_type" {
		t.Fatalf("non-JSON body: %d", w.Code)
	}
}

func TestPlaybackUnwiredAndDisabledRollouts(t *testing.T) {
	handler, _, _ := playbackHTTPFixture(t, false)
	w := playbackHTTPRequest(handler, "GET", "/api/v1/items/"+itemID+"/playback", "", strings.Repeat("n", 43))
	if w.Code != 503 || playbackErrorCode(t, w) != "not_ready" {
		t.Fatalf("unwired playback answered %d", w.Code)
	}
	f := newFixture(t, true, false)
	w = playbackHTTPRequest(f.handler, "GET", "/api/v1/items/"+itemID+"/playback", "", strings.Repeat("n", 43))
	if w.Code != 404 {
		t.Fatalf("playback registered without direct delivery: %d", w.Code)
	}
	if _, err := app.NewCatalog(&fakeRepository{}).WithPlayback(nil); err == nil {
		t.Fatal("nil playback repository accepted")
	}
}
