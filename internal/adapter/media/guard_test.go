package media

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProductionGuardRejectsTransformations(t *testing.T) {
	for _, target := range []string{
		"/Videos/id/master.m3u8", "/VIDEOS/id/STREAM.MPD", "/Videos/id/HLS/0.ts", "/Videos/id/HLS1/main/0.ts", "/Videos/id/dash/0", "/transcode/id", "/Videos/id/segments/1", "/Videos/id/%68%6Cs/0", "/Videos/id/%2568ls/0", "/Videos/id/hls%5C0.ts", "/Videos/id/chunk.m4s",
		"/stream?videoCodec=h264", "/stream?%76ideoCodec=copy", "/stream?AUDIOCODEC=copy", "/stream?maxVideoBitrate=0", "/stream?video_codec=copy", "/stream?SegmentLength=6", "/stream?HlsSegmentLength=5", "/stream?TranscodeReasons=codec", "/stream?protocol=HLS", "/stream?container=mpd", "/stream?Static=false", "/stream?EnableTranscoding=true", "/stream?AllowVideoStreamCopy=false", "/stream?SubtitleMethod=Encode", "/stream?width=1920", "/stream?DeviceProfile=%7B%22TranscodingProfiles%22%3A%5B%7B%22Type%22%3A%22Video%22%7D%5D%7D",
	} {
		t.Run(target, func(t *testing.T) {
			err := GuardProduction(httptest.NewRequest("GET", target, nil))
			if !errors.Is(err, ErrTranscodeDisabled) {
				t.Fatalf("guard error=%v", err)
			}
		})
	}
}

func TestProductionGuardInspectsFormAndJSON(t *testing.T) {
	for _, tc := range []struct{ name, contentType, body string }{
		{"form", "application/x-www-form-urlencoded", "MaxVideoBitrate=120000"},
		{"encoded_form", "application/x-www-form-urlencoded; charset=utf-8", "%61udioCodec=aac"},
		{"json", "application/json", `{"VideoCodec":"h264"}`},
		{"nested", "application/json", `{"DeviceProfile":{"TranscodingProfiles":[{"Container":"ts"}]}}`},
		{"nested_codec", "application/json", `{"Options":[{"audioCodec":"aac"}]}`},
		{"escaped_key", "application/json", `{"\u0076ideoCodec":"h264"}`},
		{"duplicate_key", "application/json", `{"options":{"videoCodec":"h264"},"options":{}}`},
		{"duplicate_toggle", "application/json", `{"EnableTranscoding":true,"EnableTranscoding":false}`},
		{"burnin", "application/json", `{"SubtitleMethod":"BurnIn"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/PlaybackInfo", strings.NewReader(tc.body))
			r.Header.Set("Content-Type", tc.contentType)
			if err := GuardProduction(r); !errors.Is(err, ErrTranscodeDisabled) {
				t.Fatalf("guard error=%v", err)
			}
		})
	}
}

func TestProductionGuardPreservesDirectRequests(t *testing.T) {
	for _, target := range []string{"/Videos/id/stream.ts?Static=true", "/stream?EnableTranscoding=false", "/stream?AllowVideoStreamCopy=true", "/stream?SubtitleMethod=External", "/stream?MediaSourceId=source&StartTimeTicks=0", "/stream?Container=mkv"} {
		if err := GuardProduction(httptest.NewRequest("GET", target, nil)); err != nil {
			t.Fatalf("direct request %s: %v", target, err)
		}
	}
	for _, tc := range []struct{ kind, body string }{
		{"application/json", `{"EnableTranscoding":false,"DeviceProfile":{"TranscodingProfiles":[]},"MediaSourceId":"source","SubtitleMethod":"External"}`},
		{"application/x-www-form-urlencoded", "Static=true&MediaSourceId=source"},
	} {
		r := httptest.NewRequest("POST", "/PlaybackInfo", strings.NewReader(tc.body))
		r.Header.Set("Content-Type", tc.kind)
		if err := GuardProduction(r); err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != tc.body {
			t.Fatal("accepted request body was consumed or rewritten")
		}
	}
}

func TestGuardRejectsInvalidOrUnboundedBodies(t *testing.T) {
	for _, tc := range []struct {
		name, kind, body string
		want             error
	}{
		{"malformed", "application/json", `{"x":`, ErrInvalidRequest},
		{"extra_document", "application/json", `{} {}`, ErrInvalidRequest},
		{"depth", "application/json", strings.Repeat("[", 34) + "0" + strings.Repeat("]", 34), ErrInvalidRequest},
		{"size", "application/json", strings.Repeat(" ", maxPlaybackBody+1), ErrBodyTooLarge},
		{"untyped", "", `{"videoCodec":"h264"}`, ErrUnsupportedMediaType},
		{"multipart", "multipart/form-data; boundary=x", "data", ErrUnsupportedMediaType},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/PlaybackInfo", strings.NewReader(tc.body))
			r.Header.Set("Content-Type", tc.kind)
			if err := GuardProduction(r); !errors.Is(err, tc.want) {
				t.Fatalf("error=%v want=%v", err, tc.want)
			}
		})
	}
	r := httptest.NewRequest("GET", "/stream?key=%ZZ", nil)
	if err := GuardProduction(r); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("invalid query error=%v", err)
	}
	r = httptest.NewRequest("POST", "/PlaybackInfo", strings.NewReader(strings.Repeat("x", maxPlaybackBody+1)))
	r.ContentLength = -1
	if err := GuardProduction(r); !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("chunked body size bypass: %v", err)
	}
}

func TestRouteGuardDoesNotTreatMetadataValuesAsTranscode(t *testing.T) {
	for _, route := range []string{"/api/v1/items", "/api/v1/items/hlstory", "/Videos/id/stream.ts", "/api/v1/search", "/api/v1/metadata/videoCodec"} {
		if IsForbiddenDeliveryRoute(route) {
			t.Fatalf("non-transcoding route blocked: %s", route)
		}
	}
}

func FuzzProductionGuard(f *testing.F) {
	for _, seed := range []string{`{"videoCodec":"h264"}`, `{}`, `{"DeviceProfile":{"TranscodingProfiles":[]}}`, `{"a":{"b":1}}`} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, body string) {
		r := httptest.NewRequest("POST", "/PlaybackInfo", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		_ = GuardProduction(r)
	})
}

// GuardPlaybackInfo reads the documented PlaybackInfo members and the
// DeviceProfile as the client's declaration, and nothing else.
func TestPlaybackInfoGuardSeparatesDeclarations(t *testing.T) {
	declaration := `{"UserId":"u","MaxStreamingBitrate":140000000,"StartTimeTicks":0,"AudioStreamIndex":1,"SubtitleStreamIndex":2,"MaxAudioChannels":6,
"MediaSourceId":"s","LiveStreamId":null,"AutoOpenLiveStream":true,"EnableDirectPlay":false,"EnableDirectStream":false,"EnableTranscoding":true,
"AllowVideoStreamCopy":false,"AllowAudioStreamCopy":false,"AlwaysBurnInSubtitleWhenTranscoding":true,
"DeviceProfile":{"MaxStreamingBitrate":1,"DirectPlayProfiles":[{"Container":"mkv","Type":"Video","VideoCodec":"hevc","AudioCodec":"eac3"}],
"TranscodingProfiles":[{"Container":"ts","Protocol":"hls","VideoCodec":"h264","AudioCodec":"aac","SegmentLength":6,"BreakOnNonKeyFrames":true}],
"SubtitleProfiles":[{"Format":"ass","Method":"Encode"}],"CodecProfiles":[{"Codec":"h264","Conditions":[{"Property":"Width","Value":"1920"}]}]}}`
	post := func(target, body string) *http.Request {
		r := httptest.NewRequest("POST", target, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		return r
	}
	query := "?MaxStreamingBitrate=1&EnableTranscoding=true&AllowVideoStreamCopy=false&EnableDirectPlay=false&AlwaysBurnInSubtitleWhenTranscoding=true&MaxAudioChannels=2"
	if err := GuardPlaybackInfo(post("/Items/x/PlaybackInfo"+query, declaration)); err != nil {
		t.Fatalf("declaration refused: %v", err)
	}
	r := post("/Items/x/PlaybackInfo", declaration)
	if err := GuardPlaybackInfo(r); err != nil {
		t.Fatal(err)
	}
	if body, _ := io.ReadAll(r.Body); string(body) != declaration {
		t.Fatal("body not restored")
	}
	// The production guard itself is unchanged.
	if err := GuardProduction(post("/Items/x/PlaybackInfo", declaration)); !errors.Is(err, ErrTranscodeDisabled) {
		t.Fatalf("production guard: %v", err)
	}
	if err := GuardProduction(httptest.NewRequest("GET", "/stream"+query, nil)); !errors.Is(err, ErrTranscodeDisabled) {
		t.Fatalf("production guard query: %v", err)
	}
	// Every transformation parameter outside the declaration is refused,
	// in the query and at any position of the body except the profile.
	for _, name := range append(append([]string{}, transformParams...), "segmentContainer", "transcodingProtocol", "hlsSegmentLength", "h264-profile") {
		if playbackInfoDeclarations[normalizeKey(name)] {
			continue
		}
		if err := GuardPlaybackInfo(httptest.NewRequest("GET", "/Items/x/PlaybackInfo?"+name+"=1", nil)); !errors.Is(err, ErrTranscodeDisabled) {
			t.Errorf("query %s: %v", name, err)
		}
		for _, body := range []string{`{"` + name + `":1}`, `{"Options":{"` + name + `":1}}`, `{"MaxStreamingBitrate":{"` + name + `":1}}`, `[{"` + name + `":1}]`} {
			if err := GuardPlaybackInfo(post("/Items/x/PlaybackInfo", body)); !errors.Is(err, ErrTranscodeDisabled) {
				t.Errorf("body %s: %v", body, err)
			}
		}
	}
	for _, target := range []string{"/Items/x/PlaybackInfo?Static=false", "/Items/x/PlaybackInfo?Container=hls", "/Items/x/PlaybackInfo?SubtitleMethod=Encode",
		"/Items/x/PlaybackInfo?EnableAutoStreamCopy=false", "/Items/x/master.m3u8", "/Items/x/hls/PlaybackInfo"} {
		if err := GuardPlaybackInfo(httptest.NewRequest("GET", target, nil)); !errors.Is(err, ErrTranscodeDisabled) {
			t.Errorf("%s: %v", target, err)
		}
	}
	// The profile is data but still parsed: syntax, depth and size hold.
	for _, body := range []string{`{"DeviceProfile":` + strings.Repeat("[", 40) + strings.Repeat("]", 40) + `}`, `{"DeviceProfile":{"a":}}`, `{"DeviceProfile":{}} x`} {
		if err := GuardPlaybackInfo(post("/Items/x/PlaybackInfo", body)); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("%s: %v", body, err)
		}
	}
	big := post("/Items/x/PlaybackInfo", `{"DeviceProfile":"`+strings.Repeat("a", maxPlaybackBody)+`"}`)
	if err := GuardPlaybackInfo(big); !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("oversized: %v", err)
	}
	if err := GuardPlaybackInfo(httptest.NewRequest("GET", "/Items/x/PlaybackInfo?DeviceProfile=%7B%22TranscodingProfiles%22%3A%5B%7B%7D%5D%7D", nil)); err != nil {
		t.Fatalf("query profile: %v", err)
	}
	if err := GuardPlaybackInfo(httptest.NewRequest("GET", "/Items/x/PlaybackInfo?DeviceProfile=%7B", nil)); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("malformed query profile: %v", err)
	}
}

// The declaration list is reviewed: adding a member widens what
// PlaybackInfo accepts and must be a deliberate change here.
func TestPlaybackInfoDeclarationsAreFixed(t *testing.T) {
	want := []string{"allowaudiostreamcopy", "allowvideostreamcopy", "alwaysburninsubtitlewhentranscoding", "audiostreamindex", "autoopenlivestream", "deviceprofile",
		"enabledirectplay", "enabledirectstream", "enabletranscoding", "livestreamid", "maxaudiochannels", "maxstreamingbitrate", "mediasourceid", "starttimeticks",
		"subtitlestreamindex", "userid"}
	if len(playbackInfoDeclarations) != len(want) {
		t.Fatalf("declarations %v", playbackInfoDeclarations)
	}
	for _, name := range want {
		if !playbackInfoDeclarations[name] {
			t.Fatalf("missing %s in %v", name, playbackInfoDeclarations)
		}
	}
}
