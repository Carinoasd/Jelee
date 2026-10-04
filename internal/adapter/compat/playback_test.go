package compat

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/adapter/media"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/go-chi/chi/v5"
)

// fakeResolver serves the fake catalog's movie source and its two external
// subtitles from a temporary directory, like the server's resolver does
// after its own authorization.
type fakeResolver struct {
	root    string
	sources []string
	tracks  []string
}

func (f *fakeResolver) Resolve(_ context.Context, p access.Principal, id string) (media.Source, error) {
	f.sources = append(f.sources, id)
	if id != testSourceID || p.Kind != access.ClientNative {
		return media.Source{}, media.ErrNotFound
	}
	return media.Source{Root: f.root, RelativePath: "movie.mkv", ContentType: "video/x-matroska"}, nil
}

func (f *fakeResolver) ResolveTrack(_ context.Context, _ access.Principal, sourceID string, kind media.TrackKind, trackID string) (media.Source, error) {
	f.tracks = append(f.tracks, trackID)
	if sourceID != testSourceID || kind != media.TrackSubtitle {
		return media.Source{}, media.ErrNotFound
	}
	switch trackID {
	case testAssTrackID:
		return media.Source{Root: f.root, RelativePath: "movie.zho.ass"}, nil
	case testSrtTrackID:
		return media.Source{Root: f.root, RelativePath: "movie.eng.srt"}, nil
	}
	return media.Source{}, media.ErrNotFound
}

var (
	testMovieBytes = bytes.Repeat([]byte("Jelee original bytes 0123456789\n"), 4096)
	testAssBytes   = []byte("[Script Info]\nTitle: signs\n")
	testSrtBytes   = []byte("1\n00:00:01,000 --> 00:00:02,000\nhello\n")
)

type playbackHarness struct {
	*libraryHarness
	resolver *fakeResolver
}

func newPlaybackHarness(t *testing.T, hidden int) *playbackHarness {
	t.Helper()
	root := t.TempDir()
	for name, data := range map[string][]byte{"movie.mkv": testMovieBytes, "movie.zho.ass": testAssBytes, "movie.eng.srt": testSrtBytes} {
		if err := os.WriteFile(filepath.Join(root, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	resolver := &fakeResolver{root: root}
	delivery, err := media.NewHandler(resolver, media.Options{MaxConcurrent: 4, WriteTimeout: 5 * time.Second, WriteError: func(w http.ResponseWriter, _ *http.Request, err error) {
		switch {
		case errors.Is(err, media.ErrTranscodeDisabled):
			w.WriteHeader(http.StatusConflict)
		case errors.Is(err, media.ErrNotFound):
			w.WriteHeader(http.StatusNotFound)
		case errors.Is(err, media.ErrInvalidRange):
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	return &playbackHarness{libraryHarness: newLibraryHarnessWith(t, hidden, true, delivery), resolver: resolver}
}

func (h *playbackHarness) post(target, token, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "http://localhost"+target, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	for k, vs := range authHeader(token) {
		r.Header[k] = vs
	}
	w := httptest.NewRecorder()
	h.handler.ServeHTTP(w, r)
	return w
}

func fixedPlaySession(t *testing.T) {
	t.Helper()
	previous := newPlaySessionID
	newPlaySessionID = func() (string, error) { return "5ee5105e5510115e55105e5510115e55", nil }
	t.Cleanup(func() { newPlaySessionID = previous })
}

// webDeviceProfile is shaped like the profile a browser client posts: its
// direct play profiles carry codec lists, and it declares transcoding,
// codec and subtitle profiles and a bit rate ceiling. All of it is a
// capability declaration.
const webDeviceProfile = `{
 "MaxStreamingBitrate": 120000000, "MaxStaticBitrate": 100000000, "MusicStreamingTranscodingBitrate": 384000,
 "DirectPlayProfiles": [
  {"Container": "webm", "Type": "Video", "VideoCodec": "vp8,vp9,av1", "AudioCodec": "vorbis,opus"},
  {"Container": "mp4,m4v", "Type": "Video", "VideoCodec": "h264,hevc,vp9,av1", "AudioCodec": "aac,mp3,ac3,eac3,flac,opus"},
  {"Container": "mkv", "Type": "Video", "VideoCodec": "h264,hevc,vp9,av1", "AudioCodec": "aac,mp3,ac3,eac3,flac,opus"},
  {"Container": "opus", "Type": "Audio"}
 ],
 "TranscodingProfiles": [
  {"Container": "ts", "Type": "Video", "AudioCodec": "aac", "VideoCodec": "h264", "Context": "Streaming", "Protocol": "hls", "MaxAudioChannels": "2", "MinSegments": "1", "BreakOnNonKeyFrames": true},
  {"Container": "mp4", "Type": "Video", "AudioCodec": "aac", "VideoCodec": "h264", "Context": "Static", "Protocol": "http"}
 ],
 "ContainerProfiles": [],
 "CodecProfiles": [{"Type": "Video", "Codec": "h264", "Conditions": [{"Condition": "LessThanEqual", "Property": "VideoLevel", "Value": "52", "IsRequired": false}]}],
 "SubtitleProfiles": [{"Format": "vtt", "Method": "External"}, {"Format": "ass", "Method": "External"}, {"Format": "pgssub", "Method": "Encode"}],
 "ResponseProfiles": []
}`

const webPlaybackBody = `{"UserId":"` + testUserWire + `","StartTimeTicks":0,"IsPlayback":true,"AutoOpenLiveStream":true,"MaxStreamingBitrate":140000000,
 "AudioStreamIndex":1,"SubtitleStreamIndex":3,"MaxAudioChannels":6,"EnableDirectPlay":true,"EnableDirectStream":true,"EnableTranscoding":true,
 "AllowVideoStreamCopy":true,"AllowAudioStreamCopy":false,"AlwaysBurnInSubtitleWhenTranscoding":false,"DeviceProfile":` + webDeviceProfile + `}`

// A client's capability declaration is not a transformation request: the
// whole PlaybackInfo body above passes, while the same members anywhere
// else are still refused by the unmodified production guard.
func TestPlaybackInfoAcceptsCapabilityDeclarations(t *testing.T) {
	fixedPlaySession(t)
	h := newPlaybackHarness(t, http.StatusNotFound)
	target := "/compat/Items/" + wire(testMovieID) + "/PlaybackInfo"
	checkGolden(t, "playback_info.json", h.post(target+"?UserId="+testUserWire+"&MaxStreamingBitrate=140000000&EnableTranscoding=true&AllowVideoStreamCopy=false&MaxAudioChannels=2", nativeToken, webPlaybackBody))
	checkGolden(t, "playback_info.json", h.get(strings.ToLower(target), nativeToken))
	checkGolden(t, "playback_info.json", h.post(target, nativeToken, ""))
	if h.rejections != 0 {
		t.Fatalf("%d capability declarations refused", h.rejections)
	}
	// The production guard proper is unchanged: the same declaration is a
	// transformation request on any other surface.
	r := httptest.NewRequest(http.MethodPost, "/Items/x/PlaybackInfo", strings.NewReader(webPlaybackBody))
	r.Header.Set("Content-Type", "application/json")
	if err := media.GuardProduction(r); !errors.Is(err, media.ErrTranscodeDisabled) {
		t.Fatalf("production guard accepted a device profile: %v", err)
	}
	for _, query := range []string{"MaxStreamingBitrate=140000000", "EnableTranscoding=true", "DeviceProfile=%7B%22TranscodingProfiles%22%3A%5B%7B%7D%5D%7D"} {
		assertStatus(t, query, h.get("/compat/Videos/"+wire(testMovieID)+"/stream?static=true&"+query, nativeToken), http.StatusConflict)
	}
}

// Real transformation parameters on PlaybackInfo are still refused before
// any lookup, in the query and anywhere in the body outside the declaration.
func TestPlaybackInfoRefusesTransformations(t *testing.T) {
	h := newPlaybackHarness(t, http.StatusNotFound)
	target := "/compat/Items/" + wire(testMovieID) + "/PlaybackInfo"
	for _, query := range []string{"VideoCodec=h264", "AudioCodec=aac", "SegmentContainer=ts", "TranscodingProtocol=hls", "TranscodingContainer=ts", "Static=false",
		"Width=1280", "MaxVideoBitrate=1", "h264-profile=high", "SubtitleMethod=Encode", "Container=m3u8", "TranscodingMaxAudioChannels=2", "Params=a", "CopyTimestamps=true"} {
		assertStatus(t, query, h.post(target+"?"+query, nativeToken, webPlaybackBody), http.StatusConflict)
		assertStatus(t, "GET "+query, h.get(target+"?"+query, nativeToken), http.StatusConflict)
	}
	for _, body := range []string{`{"VideoCodec":"h264"}`, `{"TranscodingContainer":"ts","DeviceProfile":{}}`, `{"Options":{"AudioCodec":"aac"}}`,
		`{"SubtitleMethod":"Encode"}`, `{"SegmentLength":6}`, `{"EnableAdaptiveBitrateStreaming":true}`, `{"DeviceProfile":{},"Static":false}`} {
		assertStatus(t, body, h.post(target, nativeToken, body), http.StatusConflict)
	}
	for _, path := range []string{"/compat/Items/" + wire(testMovieID) + "/PlaybackInfo/master.m3u8", "/compat/Videos/" + wire(testMovieID) + "/hls/0.ts"} {
		assertStatus(t, path, h.get(path, nativeToken), http.StatusConflict)
	}
	if len(h.catalog.lookups) != 0 || len(h.catalog.actors) != 0 {
		t.Fatal("a refused transformation reached the catalog")
	}
	// A malformed declaration is a bad request, not a pass.
	assertStatus(t, "deep profile", h.post(target, nativeToken, `{"DeviceProfile":`+strings.Repeat("[", 40)+strings.Repeat("]", 40)+`}`), http.StatusBadRequest)
}

func playbackBody(profile string, extra string) string {
	return `{"DeviceProfile":` + profile + extra + `}`
}

func TestPlaybackInfoDirectPlayDecision(t *testing.T) {
	fixedPlaySession(t)
	target := "/compat/Items/" + wire(testMovieID) + "/PlaybackInfo"
	mkv := func(video, audio string) string {
		return `{"DirectPlayProfiles":[{"Container":"matroska,webm","Type":"Video","VideoCodec":"` + video + `","AudioCodec":"` + audio + `"}]}`
	}
	unprobed := []domain.PlaybackSource{{ID: testSourceID, Container: "mkv", ContentType: "video/x-matroska", Video: []domain.PlaybackVideoTrack{}, Audio: []domain.PlaybackAudioTrack{}}}
	for _, tc := range []struct {
		name, query, body string
		sources           []domain.PlaybackSource
		playable          bool
	}{
		{name: "no declaration", playable: true},
		{name: "empty body", body: "{}", playable: true},
		{name: "null profile", body: `{"DeviceProfile":null}`, playable: true},
		{name: "matching profile", body: playbackBody(mkv("h265", "eac3,ac-3"), ""), playable: true},
		{name: "profile type by number", body: playbackBody(`{"DirectPlayProfiles":[{"Container":"mkv","Type":1}]}`, ""), playable: true},
		{name: "any codec", body: playbackBody(`{"DirectPlayProfiles":[{"Container":"","Type":"Video"}]}`, ""), playable: true},
		{name: "container not declared", body: playbackBody(`{"DirectPlayProfiles":[{"Container":"mp4","Type":"Video"}]}`, "")},
		{name: "video codec not declared", body: playbackBody(mkv("h264", "eac3"), "")},
		{name: "audio codec not declared", body: playbackBody(mkv("hevc", "aac,opus"), "")},
		{name: "audio profile only", body: playbackBody(`{"DirectPlayProfiles":[{"Container":"mkv","Type":"Audio"}]}`, "")},
		{name: "no direct play profiles", body: playbackBody(`{"DirectPlayProfiles":[]}`, "")},
		{name: "unreadable list", body: playbackBody(`{"DirectPlayProfiles":[{"Container":"mkv","Type":"Video","VideoCodec":"h e v c"}]}`, "")},
		{name: "bitrate in body", body: playbackBody(mkv("hevc", "eac3"), `,"MaxStreamingBitrate":4000000`)},
		{name: "bitrate in profile", body: playbackBody(`{"MaxStreamingBitrate":4000000,"DirectPlayProfiles":[{"Container":"mkv","Type":"Video"}]}`, "")},
		{name: "bitrate at source rate", query: "?MaxStreamingBitrate=4597701", playable: true},
		{name: "query bitrate wins", query: "?MaxStreamingBitrate=4000000", body: `{"MaxStreamingBitrate":9000000}`},
		{name: "direct play off", body: `{"EnableDirectPlay":false}`},
		{name: "direct play off in query", query: "?EnableDirectPlay=false", body: `{"EnableDirectPlay":true}`},
		{name: "source chosen", query: "?MediaSourceId=" + wire(testSourceID), playable: true},
		{name: "source chosen in body", body: `{"MediaSourceId":"` + testSourceID + `"}`, playable: true},
		{name: "other source", query: "?MediaSourceId=" + strings.Repeat("ab", 16)},
		{name: "unprobed codecs are not held against it", body: playbackBody(mkv("h264", "aac"), ""), sources: unprobed, playable: true},
		{name: "unprobed container still checked", body: playbackBody(`{"DirectPlayProfiles":[{"Container":"mp4","Type":"Video"}]}`, ""), sources: unprobed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newPlaybackHarness(t, http.StatusNotFound)
			h.catalog.sources = tc.sources
			w := h.post(target+tc.query, nativeToken, tc.body)
			body := w.Body.String()
			if w.Code != http.StatusOK {
				t.Fatalf("%d %s", w.Code, body)
			}
			if tc.playable {
				if !strings.Contains(body, `"SupportsDirectPlay":true`) || strings.Contains(body, "ErrorCode") || !strings.Contains(body, `"PlaySessionId":"5ee5`) {
					t.Fatalf("not offered: %s", body)
				}
			} else if body != `{"MediaSources":[],"ErrorCode":"NoCompatibleStream"}` {
				t.Fatalf("not refused: %s", body)
			}
			assertNoTranscoding(t, body)
		})
	}
}

func TestPlaybackInfoRefusals(t *testing.T) {
	fixedPlaySession(t)
	h := newPlaybackHarness(t, http.StatusNotFound)
	target := "/compat/Items/" + wire(testMovieID) + "/PlaybackInfo"
	checkGolden(t, "playback_info_no_compatible.json", h.post(target, nativeToken, playbackBody(`{"DirectPlayProfiles":[{"Container":"mp4","Type":"Video"}]}`, "")))
	// Not playable items: no source, no conversion.
	if w := h.get("/compat/Items/"+wire(testLibMovies)+"/PlaybackInfo", nativeToken); w.Body.String() != `{"MediaSources":[],"ErrorCode":"NoCompatibleStream"}` {
		t.Fatalf("library: %s", w.Body)
	}
	// Invisible and missing items look the same; another user is 403.
	invisible := h.get("/compat/Items/"+wire(testSeriesID)+"/PlaybackInfo", nativeToken)
	missing := h.get("/compat/Items/"+strings.Repeat("ab", 16)+"/PlaybackInfo", nativeToken)
	assertEmpty(t, "invisible", invisible, http.StatusNotFound)
	assertEmpty(t, "missing", missing, http.StatusNotFound)
	assertEmpty(t, "other user", h.get(target+"?UserId="+wire(testOtherID), nativeToken), http.StatusForbidden)
	assertEmpty(t, "other user in body", h.post(target, nativeToken, `{"UserId":"`+wire(testOtherID)+`"}`), http.StatusForbidden)
	assertEmpty(t, "web session", h.get(target, webToken), http.StatusUnauthorized)
	assertEmpty(t, "no credential", h.do(http.MethodGet, target, nil), http.StatusUnauthorized)
	assertGeneric(t, "malformed item", h.get("/compat/Items/x/PlaybackInfo", nativeToken), http.StatusBadRequest)
	assertGeneric(t, "malformed body", h.post(target, nativeToken, `{"MaxStreamingBitrate":"fast"}`), http.StatusBadRequest)
	assertGeneric(t, "malformed source", h.get(target+"?MediaSourceId=x", nativeToken), http.StatusBadRequest)
	assertGeneric(t, "malformed bitrate", h.get(target+"?MaxStreamingBitrate=1e9", nativeToken), http.StatusBadRequest)
	r := httptest.NewRequest(http.MethodPost, "http://localhost"+target, strings.NewReader("UserId=x"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for k, vs := range authHeader(nativeToken) {
		r.Header[k] = vs
	}
	w := httptest.NewRecorder()
	h.handler.ServeHTTP(w, r)
	assertEmpty(t, "form body", w, http.StatusUnsupportedMediaType)
	h403 := newPlaybackHarness(t, http.StatusForbidden)
	assertEmpty(t, "invisible 403", h403.get("/compat/Items/"+wire(testSeriesID)+"/PlaybackInfo", nativeToken), http.StatusForbidden)
	// Without direct delivery nothing is offered.
	off := newLibraryHarness(t, http.StatusNotFound, false)
	if w := off.get(target, nativeToken); w.Body.String() != `{"MediaSources":[],"ErrorCode":"NoCompatibleStream"}` {
		t.Fatalf("direct delivery off: %s", w.Body)
	}
}

func assertStatus(t *testing.T, name string, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("%s: %d %q, want %d", name, w.Code, w.Body, status)
	}
}

// assertNoTranscoding is the G10.4 check on every playback answer.
func assertNoTranscoding(t *testing.T, body string) {
	t.Helper()
	for _, banned := range []string{`"SupportsTranscoding":true`, `"SupportsDirectStream":true`, "TranscodingUrl", "TranscodingContainer", "TranscodingSubProtocol", "TranscodeReasons", `"Path"`, "f1000000", `"Encode"`, `"Hls"`} {
		if strings.Contains(body, banned) {
			t.Fatalf("publishes %q: %s", banned, body)
		}
	}
}

func TestVideoStreamServesOriginal(t *testing.T) {
	h := newPlaybackHarness(t, http.StatusNotFound)
	base := "/compat/Videos/" + wire(testMovieID)
	sum := sha256.Sum256(testMovieBytes)
	for _, target := range []string{
		base + "/stream?Static=true&MediaSourceId=" + wire(testSourceID) + "&DeviceId=d1&PlaySessionId=p&Tag=t",
		base + "/stream.mkv?static=true",
		base + "/STREAM.MATROSKA?Static=True",
		base + "/stream.mkv?MediaSourceId=" + wire(testSourceID) + "&DeviceId=d1&PlaySessionId=p&Container=mkv",
		base + "/stream",
		// Static direct play ignores selection members like upstream.
		base + "/stream?static=true&AudioStreamIndex=1&SubtitleStreamIndex=3&StartTimeTicks=0",
	} {
		w := h.get(target, nativeToken)
		if w.Code != http.StatusOK || sha256.Sum256(w.Body.Bytes()) != sum || w.Header().Get("Content-Type") != "video/x-matroska" ||
			w.Header().Get("Content-Security-Policy") != "sandbox; default-src 'none'" {
			t.Fatalf("%s: %d %d bytes %v", target, w.Code, w.Body.Len(), w.Header())
		}
	}
	// The token may come from the api_key query member; it is still a
	// native session token, and a web one is refused.
	if w := h.do(http.MethodGet, base+"/stream?static=true&api_key="+nativeToken, nil); w.Code != http.StatusOK || w.Body.Len() != len(testMovieBytes) {
		t.Fatalf("api_key: %d", w.Code)
	}
	assertEmpty(t, "web api_key", h.do(http.MethodGet, base+"/stream?static=true&api_key="+webToken, nil), http.StatusUnauthorized)
	assertEmpty(t, "web header", h.get(base+"/stream?static=true", webToken), http.StatusUnauthorized)

	// Range and HEAD come from the delivery handler.
	r := httptest.NewRequest(http.MethodGet, "http://localhost"+base+"/stream?static=true", nil)
	r.Header.Set("Range", "bytes=100-199")
	for k, vs := range authHeader(nativeToken) {
		r.Header[k] = vs
	}
	w := httptest.NewRecorder()
	h.handler.ServeHTTP(w, r)
	if w.Code != http.StatusPartialContent || !bytes.Equal(w.Body.Bytes(), testMovieBytes[100:200]) || w.Header().Get("Content-Range") != "bytes 100-199/"+strconv.Itoa(len(testMovieBytes)) {
		t.Fatalf("range: %d %v", w.Code, w.Header())
	}
	head := h.do(http.MethodHead, base+"/stream.mkv?static=true", authHeader(nativeToken))
	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Content-Length") != strconv.Itoa(len(testMovieBytes)) || head.Header().Get("Accept-Ranges") != "bytes" {
		t.Fatalf("head: %d %v", head.Code, head.Header())
	}
	for _, id := range h.resolver.sources {
		if id != testSourceID {
			t.Fatalf("delivery asked for %q", id)
		}
	}
}

// Every request to change the bytes is 409 and never reaches delivery.
func TestVideoStreamRefusesTransformations(t *testing.T) {
	h := newPlaybackHarness(t, http.StatusNotFound)
	base := "/compat/Videos/" + wire(testMovieID)
	for _, target := range []string{
		"/stream?static=true&VideoCodec=hevc", "/stream?static=true&AudioCodec=eac3", "/stream?AudioCodec=aac", "/stream?static=true&MaxStreamingBitrate=1000000",
		"/stream?static=true&TranscodingMaxAudioChannels=2", "/stream?SegmentContainer=ts", "/stream?static=true&SegmentLength=6", "/stream?Static=false",
		"/stream?static=", "/stream?static=true&Width=1280", "/stream?static=true&hevc-level=150", "/stream?static=true&EnableAutoStreamCopy=false",
		"/stream?static=true&SubtitleMethod=Encode", "/stream?static=true&Params=x",
		// Without static, upstream runs its encoder: anything beyond
		// identification asks for a different stream.
		"/stream?AudioStreamIndex=2", "/stream?StartTimeTicks=600000000", "/stream?SubtitleStreamIndex=3", "/stream?Context=Streaming",
		// Another container is a remux, with or without static.
		"/stream.mp4", "/stream.mp4?static=true", "/stream?static=true&Container=mp4", "/stream.ts?static=true",
	} {
		assertStatus(t, target, h.get(base+target, nativeToken), http.StatusConflict)
		assertStatus(t, "HEAD "+target, h.do(http.MethodHead, base+target, authHeader(nativeToken)), http.StatusConflict)
	}
	if len(h.resolver.sources) != 0 {
		t.Fatalf("refused requests reached delivery: %v", h.resolver.sources)
	}
}

func TestVideoStreamLookup(t *testing.T) {
	h := newPlaybackHarness(t, http.StatusNotFound)
	invisible := h.get("/compat/Videos/"+wire(testSeriesID)+"/stream?static=true", testOtherToken)
	assertEmpty(t, "no source", invisible, http.StatusNotFound)
	h.catalog.sourceErr = domain.ErrNotFound
	hidden := h.get("/compat/Videos/"+wire(testMovieID)+"/stream?static=true", testOtherToken)
	assertEmpty(t, "hidden", hidden, http.StatusNotFound)
	h.catalog.sourceErr = nil
	assertEmpty(t, "other source", h.get("/compat/Videos/"+wire(testMovieID)+"/stream?static=true&MediaSourceId="+strings.Repeat("ab", 16), nativeToken), http.StatusNotFound)
	assertGeneric(t, "malformed item", h.get("/compat/Videos/x/stream?static=true", nativeToken), http.StatusBadRequest)
	assertGeneric(t, "malformed source", h.get("/compat/Videos/"+wire(testMovieID)+"/stream?static=true&MediaSourceId=x", nativeToken), http.StatusBadRequest)
	assertEmpty(t, "no credential", h.do(http.MethodGet, "/compat/Videos/"+wire(testMovieID)+"/stream?static=true", nil), http.StatusUnauthorized)
	h.catalog.sourceErr = domain.ErrDatabase
	assertEmpty(t, "unavailable", h.get("/compat/Videos/"+wire(testMovieID)+"/stream?static=true", nativeToken), http.StatusServiceUnavailable)
	h.catalog.sourceErr = nil
	if len(h.resolver.sources) != 0 {
		t.Fatalf("lookups that failed reached delivery: %v", h.resolver.sources)
	}
	if h.catalog.actors[0] != (domain.Actor{UserID: testOtherID, SessionID: testSessionID, IP: testClientIP}) {
		t.Fatalf("actor %+v", h.catalog.actors[0])
	}
	h403 := newPlaybackHarness(t, http.StatusForbidden)
	h403.catalog.sourceErr = domain.ErrNotFound
	assertEmpty(t, "hidden 403", h403.get("/compat/Videos/"+wire(testMovieID)+"/stream?static=true", nativeToken), http.StatusForbidden)
	// The catalog has no audio items.
	assertEmpty(t, "audio", h.get("/compat/Audio/"+wire(testMovieID)+"/stream?static=true", nativeToken), http.StatusNotFound)
	assertEmpty(t, "audio container", h.do(http.MethodHead, "/compat/Audio/"+wire(testMovieID)+"/stream.mp3", authHeader(nativeToken)), http.StatusNotFound)
	assertEmpty(t, "audio web", h.get("/compat/Audio/"+wire(testMovieID)+"/stream", webToken), http.StatusUnauthorized)
	assertEmpty(t, "audio 403", h403.get("/compat/Audio/"+wire(testMovieID)+"/stream", nativeToken), http.StatusForbidden)
}

// External subtitles are listed with the upstream URL form and served as
// they are; another format, a time shift or an embedded track is refused.
func TestSubtitleStream(t *testing.T) {
	h := newPlaybackHarness(t, http.StatusNotFound)
	info := h.get("/compat/Items/"+wire(testMovieID)+"/PlaybackInfo", nativeToken).Body.String()
	assURL := "/Videos/" + wire(testMovieID) + "/" + wire(testSourceID) + "/Subtitles/3/0/Stream.ass"
	srtURL := "/Videos/" + wire(testMovieID) + "/" + wire(testSourceID) + "/Subtitles/4/0/Stream.srt"
	if !strings.Contains(info, `"DeliveryUrl":"`+assURL+`"`) || !strings.Contains(info, `"DeliveryUrl":"`+srtURL+`"`) || strings.Contains(info, `"Codec":"ac3"`) {
		t.Fatalf("subtitle listing: %s", info)
	}
	for target, want := range map[string][]byte{
		assURL: testAssBytes, srtURL: testSrtBytes,
		"/Videos/" + wire(testMovieID) + "/" + wire(testSourceID) + "/subtitles/3/stream.ASS":           testAssBytes,
		"/Videos/" + wire(testMovieID) + "/" + testSourceID + "/Subtitles/4/Stream.subrip":              testSrtBytes,
		"/Videos/" + wire(testMovieID) + "/" + wire(testSourceID) + "/Subtitles/4/0/Stream.srt?api_key": testSrtBytes,
	} {
		w := h.get("/compat"+target, nativeToken)
		if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), want) {
			t.Fatalf("%s: %d %q", target, w.Code, w.Body)
		}
	}
	if head := h.do(http.MethodHead, "/compat"+assURL, authHeader(nativeToken)); head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Content-Length") != strconv.Itoa(len(testAssBytes)) {
		t.Fatalf("head: %d %v", head.Code, head.Header())
	}
	tracks := len(h.resolver.tracks)
	prefix := "/compat/Videos/" + wire(testMovieID) + "/" + wire(testSourceID) + "/Subtitles/"
	for _, target := range []string{"3/0/Stream.srt", "3/Stream.vtt", "4/Stream.ass", "4/0/Stream.js", "3/600000000/Stream.ass", "3/Stream.ass?StartPositionTicks=10",
		"3/Stream.ass?EndPositionTicks=10", "3/Stream.ass?AddVttTimeMap=true", "3/Stream.ass?format=srt", "3/Stream.ass?CopyTimestamps=true",
		// The embedded subtitle would have to be extracted.
		"2/Stream.srt"} {
		assertStatus(t, target, h.get(prefix+target, nativeToken), http.StatusConflict)
	}
	if len(h.resolver.tracks) != tracks {
		t.Fatal("refused subtitle requests reached delivery")
	}
	assertEmpty(t, "unknown index", h.get(prefix+"9/Stream.srt", nativeToken), http.StatusNotFound)
	assertEmpty(t, "other source", h.get("/compat/Videos/"+wire(testMovieID)+"/"+strings.Repeat("ab", 16)+"/Subtitles/3/Stream.ass", nativeToken), http.StatusNotFound)
	assertEmpty(t, "web", h.get("/compat"+assURL, webToken), http.StatusUnauthorized)
	assertGeneric(t, "malformed index", h.get(prefix+"-1/Stream.ass", nativeToken), http.StatusBadRequest)
	assertGeneric(t, "malformed ticks", h.get(prefix+"3/x/Stream.ass", nativeToken), http.StatusBadRequest)
}

func TestStreamRoutesNeedDelivery(t *testing.T) {
	h := newLibraryHarness(t, http.StatusNotFound, true)
	for _, target := range []string{"/compat/Videos/" + wire(testMovieID) + "/stream?static=true", "/compat/Videos/" + wire(testMovieID) + "/stream.mkv",
		"/compat/Audio/" + wire(testMovieID) + "/stream", "/compat/Videos/" + wire(testMovieID) + "/" + wire(testSourceID) + "/Subtitles/3/Stream.ass"} {
		assertEmpty(t, target, h.get(target, nativeToken), http.StatusNotFound)
	}
	// Without delivery the item lists no external subtitle.
	if body := h.get("/compat/Items/"+wire(testMovieID)+"/PlaybackInfo", nativeToken).Body.String(); strings.Contains(body, "DeliveryUrl") || strings.Contains(body, "DeliveryMethod") {
		t.Fatalf("subtitles listed without delivery: %s", body)
	}
}

func TestPlaybackRoutesAreWalkable(t *testing.T) {
	h := newPlaybackHarness(t, http.StatusNotFound)
	seen := map[string]bool{}
	if err := chi.Walk(h.handler.(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		seen[method+" "+route] = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"GET", "HEAD"} {
		for _, route := range []string{"/Videos/{itemId}/stream", "/Videos/{itemId}/stream.{container}", "/Audio/{itemId}/stream", "/Audio/{itemId}/stream.{container}",
			"/Videos/{itemId}/{mediaSourceId}/Subtitles/{index}/Stream.{format}", "/Videos/{itemId}/{mediaSourceId}/Subtitles/{index}/{startPositionTicks}/Stream.{format}"} {
			if !seen[method+" "+route] {
				t.Fatalf("%s %s not walkable: %v", method, route, seen)
			}
		}
	}
	if len(seen) != 24 {
		t.Fatalf("unexpected routes %v", seen)
	}
}

func TestCanonicalLiteralPrefix(t *testing.T) {
	rt := &router{}
	rt.patterns = [][]string{{"", "Videos", "{itemId}", "stream"}, {"", "Videos", "{itemId}", "stream.{container}"}}
	for in, want := range map[string]string{
		"/videos/x/STREAM":     "/Videos/x/stream",
		"/videos/x/STREAM.Mkv": "/Videos/x/stream.Mkv",
		"/videos/x/stream.":    "/videos/x/stream.",
		"/videos/x/streams":    "/videos/x/streams",
	} {
		if got := rt.canonical(in); got != want {
			t.Fatalf("%s: %s, want %s", in, got, want)
		}
	}
}
