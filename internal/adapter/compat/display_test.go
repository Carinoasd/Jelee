package compat

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/adapter/media"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// TestDisplayTitleRules pins the upstream DisplayTitle assembly for each
// stream type, the title merge and the locale of the flag words.
func TestDisplayTitleRules(t *testing.T) {
	i := func(n int64) *int64 { return &n }
	for _, c := range []struct {
		name, locale string
		stream       mediaStream
		want         string
	}{
		{"4K HDR video", "en-US", mediaStream{Type: mediaStreamVideo, Codec: "hevc", Width: i(3840), Height: i(2160), VideoRangeType: "HDR10"}, "4K HEVC HDR10"},
		{"Dolby Vision", "en-US", mediaStream{Type: mediaStreamVideo, Codec: "hevc", Width: i(3840), Height: i(1608), VideoRangeType: "DOVI"}, "4K HEVC Dolby Vision"},
		{"1080p SDR", "zh-TW", mediaStream{Type: mediaStreamVideo, Codec: "h264", Width: i(1920), Height: i(1080)}, "1080p H264"},
		{"cropped 720p", "en-US", mediaStream{Type: mediaStreamVideo, Codec: "av1", Width: i(1280), Height: i(536)}, "720p AV1"},
		{"no size", "en-US", mediaStream{Type: mediaStreamVideo, Codec: "vp9"}, "VP9"},
		{"video title", "en-US", mediaStream{Type: mediaStreamVideo, Codec: "h264", Width: i(1920), Height: i(1080), Title: "Main feature 1080p"}, "Main feature 1080p - H264"},
		{"audio layout", "en-US", mediaStream{Type: mediaStreamAudio, Codec: "eac3", Language: "eng", ChannelLayout: "5.1(side)", IsDefault: true}, "English - Dolby Digital+ - 5.1(side) - Default"},
		{"audio profile wins", "en-US", mediaStream{Type: mediaStreamAudio, Codec: "dts", Profile: "DTS-HD MA", Language: "ger", Channels: i(8)}, "German - DTS-HD MA - 8 ch"},
		{"AAC LC shows the codec", "en-US", mediaStream{Type: mediaStreamAudio, Codec: "aac", Profile: "LC", Language: "jpn", ChannelLayout: "stereo"}, "Japanese - AAC - Stereo"},
		{"commentary external", "zh-CN", mediaStream{Type: mediaStreamAudio, Codec: "ac3", Language: "en", IsExternal: true, commentary: true}, "英语 - Dolby Digital - 评论 - 外挂"},
		{"special language", "ja-JP", mediaStream{Type: mediaStreamAudio, Codec: "flac", Language: "und"}, "FLAC"},
		{"unknown language code", "en-US", mediaStream{Type: mediaStreamAudio, Codec: "opus", Language: "tlh"}, "Tlh - OPUS"},
		{"audio title merge", "en-US", mediaStream{Type: mediaStreamAudio, Codec: "ac3", Language: "eng", Channels: i(6), Title: "English Dolby Digital"}, "English Dolby Digital - 6 ch"},
		{"subtitle flags", "zh-TW", mediaStream{Type: mediaStreamSubtitle, Codec: "srt", Language: "chi", IsHearingImpaired: true, IsDefault: true, IsForced: true, IsExternal: true}, "中文 - 聽障 - 預設 - 強制 - SRT - 外掛"},
		{"subtitle script", "en-US", mediaStream{Type: mediaStreamSubtitle, Codec: "ass", Language: "zh-Hant"}, "Chinese (Traditional) - ASS"},
		{"undetermined subtitle", "ja-JP", mediaStream{Type: mediaStreamSubtitle, Codec: "pgssub"}, "言語不明 - PGSSUB"},
		{"subtitle title", "en-US", mediaStream{Type: mediaStreamSubtitle, Codec: "ass", Language: "eng", Title: "Signs & Songs", IsForced: true}, "Signs & Songs - English - Forced - ASS"},
	} {
		if got := displayTitle(c.stream, c.locale); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// The stream members the probe records are sent; those it does not
// record (interlacing, pixel format, aspect ratio) never are.
func TestMediaSourceStreamDetails(t *testing.T) {
	i := func(n int64) *int64 { return &n }
	source := domain.PlaybackSource{
		ID: testSourceID, Container: "mkv", Probed: true,
		Video: []domain.PlaybackVideoTrack{{Index: 0, Codec: "h264", Profile: "High", Level: i(41), Width: i(1920), Height: i(1080), Primary: true,
			AverageFrameRate: &domain.MediaRational{Numerator: 24000, Denominator: 1001}, RealFrameRate: &domain.MediaRational{Numerator: 24, Denominator: 1}}},
		Audio: []domain.PlaybackAudioTrack{{Index: 1, Codec: "flac", Language: "jpn", Channels: i(2), ChannelLayout: "stereo", BitDepth: i(24), SampleRate: i(96000), Title: "Original", Commentary: true}},
	}
	rt := &router{opts: Options{Library: &LibraryOptions{}}}
	info, err := rt.mediaSource(wire(testMovieID), source, "Movie", "en-US")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(info.MediaStreams)
	body := string(raw)
	for _, want := range []string{`"Level":41`, `"AverageFrameRate":23.976`, `"RealFrameRate":24`, `"BitDepth":8`, `"DisplayTitle":"1080p H264"`,
		`"ChannelLayout":"stereo"`, `"BitDepth":24`, `"DisplayTitle":"Original - Japanese - FLAC - Stereo - Commentary"`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s in %s", want, body)
		}
	}
	for _, absent := range []string{"IsInterlaced", "PixelFormat", "AspectRatio", "commentary", "DeliveryUrl"} {
		if strings.Contains(body, absent) {
			t.Errorf("%s sent: %s", absent, body)
		}
	}
	if got := frameRate(&domain.MediaRational{Numerator: 1, Denominator: 0}); got != nil {
		t.Fatal("rate without denominator")
	}
}

// PlaybackInfo labels streams in the locale the request negotiates; an
// external audio file is listed after the subtitles with a DeliveryUrl
// that serves it as it is.
func TestPlaybackInfoDisplayTitlesAndExternalAudio(t *testing.T) {
	h := newPlaybackHarness(t, http.StatusNotFound)
	target := "/compat/Items/" + wire(testMovieID) + "/PlaybackInfo"
	for language, want := range map[string][]string{
		"en-US":          {`"DisplayTitle":"English - Dolby Digital+ - 6 ch - Default"`, `"DisplayTitle":"Japanese - Dolby Digital - External"`, `"DisplayTitle":"Signs - Chinese - Default - ASS - External"`},
		"zh-TW,zh;q=0.8": {`"DisplayTitle":"英語 - Dolby Digital+ - 6 ch - 預設"`, `"DisplayTitle":"日語 - Dolby Digital - 外掛"`, `"DisplayTitle":"英語 - 聽障 - SRT - 外掛"`},
		"ja":             {`"DisplayTitle":"中国語 - 強制 - SRT"`, `"DisplayTitle":"4K HEVC"`},
		"":               {`"DisplayTitle":"日语 - Dolby Digital - 外挂"`},
		"fr-FR":          {`"DisplayTitle":"Japanese - Dolby Digital - External"`},
	} {
		r := h.do(http.MethodGet, target, mergeHeader(authHeader(nativeToken), "Accept-Language", language))
		for _, w := range want {
			if !strings.Contains(r.Body.String(), w) {
				t.Fatalf("%q: missing %s in %s", language, w, r.Body)
			}
		}
	}
	body := h.get(target, nativeToken).Body.String()
	audioURL := "/Videos/" + wire(testMovieID) + "/" + wire(testSourceID) + "/Audio/5/Stream.ac3"
	if !strings.Contains(body, `{"Codec":"ac3","DisplayTitle":"日语 - Dolby Digital - 外挂","Language":"jpn","IsDefault":false,"IsForced":false,"Type":"Audio","Index":5,"IsExternal":true,"SupportsExternalStream":true,"DeliveryUrl":"`+audioURL+`"}`) {
		t.Fatalf("external audio listing: %s", body)
	}
	// The item detail lists it the same way.
	if detail := h.get("/compat/Items/"+wire(testMovieID), nativeToken).Body.String(); !strings.Contains(detail, `"DeliveryUrl":"`+audioURL+`"`) {
		t.Fatalf("item detail: %s", detail)
	}
	// Without direct delivery nothing external is listed.
	plain := newLibraryHarness(t, http.StatusNotFound, true)
	if body := plain.get(target, nativeToken).Body.String(); strings.Contains(body, `"IsExternal":true`) || !strings.Contains(body, `"DisplayTitle":"英语 - Dolby Digital+ - 6 ch - 默认"`) {
		t.Fatalf("without delivery: %s", body)
	}
}

func mergeHeader(h http.Header, key, value string) http.Header {
	if value != "" {
		h.Set(key, value)
	}
	return h
}

// The external audio route serves the file as it is, with Range and HEAD
// from the shared delivery handler; another format, a start offset or an
// embedded stream asks for a conversion and is refused before delivery.
func TestExternalAudioStream(t *testing.T) {
	h := newPlaybackHarness(t, http.StatusNotFound)
	prefix := "/compat/Videos/" + wire(testMovieID) + "/" + wire(testSourceID) + "/Audio/"
	if w := h.get(prefix+"5/Stream.ac3", nativeToken); w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), testAc3Bytes) || w.Header().Get("Content-Type") != "audio/ac3" {
		t.Fatalf("full: %d %v", w.Code, w.Header())
	}
	ranged := h.do(http.MethodGet, prefix+"5/Stream.AC3?api_key=x", mergeHeader(authHeader(nativeToken), "Range", "bytes=4-11"))
	if ranged.Code != http.StatusPartialContent || !bytes.Equal(ranged.Body.Bytes(), testAc3Bytes[4:12]) {
		t.Fatalf("range: %d %q", ranged.Code, ranged.Body)
	}
	if head := h.do(http.MethodHead, prefix+"5/Stream.ac3", authHeader(nativeToken)); head.Code != http.StatusOK || head.Body.Len() != 0 ||
		head.Header().Get("Content-Length") != strconv.Itoa(len(testAc3Bytes)) {
		t.Fatalf("head: %d %v", head.Code, head.Header())
	}
	if got := h.resolver.tracks[len(h.resolver.tracks)-1]; got != testAudioTrackID {
		t.Fatalf("delivered track %s", got)
	}
	tracks := len(h.resolver.tracks)
	rejections := h.rejections
	for _, target := range []string{"5/Stream.aac", "5/Stream.mka", "5/Stream.ac3?StartTimeTicks=600000000", "5/Stream.ac3?AudioCodec=aac",
		"5/Stream.ac3?static=false", "5/Stream.ac3?MaxAudioChannels=2",
		// An embedded audio stream would have to be demuxed.
		"1/Stream.eac3"} {
		assertStatus(t, target, h.get(prefix+target, nativeToken), http.StatusConflict)
	}
	if len(h.resolver.tracks) != tracks || h.rejections == rejections {
		t.Fatal("refused audio requests reached delivery")
	}
	assertStatus(t, "start zero", h.get(prefix+"5/Stream.ac3?StartTimeTicks=0", nativeToken), http.StatusOK)
	// A subtitle index is not audio, and an unknown index is missing.
	assertEmpty(t, "subtitle index", h.get(prefix+"3/Stream.ass", nativeToken), http.StatusNotFound)
	assertEmpty(t, "unknown index", h.get(prefix+"9/Stream.ac3", nativeToken), http.StatusNotFound)
	assertEmpty(t, "other source", h.get("/compat/Videos/"+wire(testMovieID)+"/"+strings.Repeat("ab", 16)+"/Audio/5/Stream.ac3", nativeToken), http.StatusNotFound)
	assertEmpty(t, "web", h.get(prefix+"5/Stream.ac3", webToken), http.StatusUnauthorized)
	for _, malformed := range []string{"-1/Stream.ac3", "05/Stream.ac3", "x/Stream.ac3", "100000/Stream.ac3"} {
		assertGeneric(t, malformed, h.get(prefix+malformed, nativeToken), http.StatusBadRequest)
	}
	assertGeneric(t, "malformed start", h.get(prefix+"5/Stream.ac3?StartTimeTicks=x", nativeToken), http.StatusBadRequest)
	h403 := newPlaybackHarness(t, http.StatusForbidden)
	h403.catalog.sourceErr = domain.ErrNotFound
	assertEmpty(t, "hidden 403", h403.get(prefix+"5/Stream.ac3", nativeToken), http.StatusForbidden)
	// Without direct delivery the route does not exist.
	plain := newLibraryHarness(t, http.StatusNotFound, true)
	assertEmpty(t, "no delivery", plain.get(prefix+"5/Stream.ac3", nativeToken), http.StatusNotFound)
}

// DefaultAudioStreamIndex follows a preference for an external audio file
// with its upstream number, and a reported index maps back to the file.
func TestPlaybackInfoDefaultExternalAudio(t *testing.T) {
	h := newPlaybackHarness(t, http.StatusNotFound)
	sources, _ := h.catalog.PlaybackSources(t.Context(), domain.Actor{}, testMovieID)
	source := sources[0]
	source.DefaultTracks = &domain.DefaultTracks{Audio: &domain.TrackSelection{Kind: domain.TrackExternal, ID: testAudioTrackID}}
	h.catalog.sources = []domain.PlaybackSource{source}
	if body := h.get("/compat/Items/"+wire(testMovieID)+"/PlaybackInfo", nativeToken).Body.String(); !strings.Contains(body, `"DefaultAudioStreamIndex":5`) {
		t.Fatalf("external default audio: %s", body)
	}
	if got := defaultAudioIndex(source, false); got == nil || *got != 1 {
		t.Fatal("without delivery the embedded default stays", got)
	}
	index := 5
	if tracks, ok := domain.ResolveTrackReport(source, domain.PlaybackTrackReport{AudioIndex: &index}); !ok || tracks.AudioTrack != "x:"+testAudioTrackID {
		t.Fatalf("reported external audio: %+v %v", tracks, ok)
	}
}

// stubDelivery lists like the real delivery handler; nothing is served.
type stubDelivery struct{}

func (stubDelivery) ServeSource(http.ResponseWriter, *http.Request, string) {}
func (stubDelivery) ServeTrack(http.ResponseWriter, *http.Request, string, media.TrackKind, string) {
}
func (stubDelivery) ServeExtracted(http.ResponseWriter, *http.Request, media.ExtractedResolver, string, media.ExtractedKind, int) {
}
func (stubDelivery) ServeUTF8Track(http.ResponseWriter, *http.Request, media.UTF8Resolver, string, string, bool) {
}

func (stubDelivery) ServeSidecarOCR(http.ResponseWriter, *http.Request, media.SidecarOCRResolver, string, string) {
}

// A probed external audio file the declaration cannot decode is logged
// with the B7 audio codec code and stays listed with its DeliveryUrl; an
// unprobed one is not judged.
func TestExternalAudioProfileLog(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	h := &playbackHarness{libraryHarness: newLibraryHarnessConfig(t, http.StatusNotFound, func(o *LibraryOptions) {
		o.DirectPlay, o.Logger, o.Delivery = true, logger, stubDelivery{}
	})}
	sources, _ := h.catalog.PlaybackSources(t.Context(), domain.Actor{}, testMovieID)
	source := sources[0]
	source.External = append([]domain.PlaybackExternalTrack(nil), source.External...)
	source.External[2].ProbeState = domain.SidecarProbeReady
	source.External[2].Channels = 6
	source.External = append(source.External, domain.PlaybackExternalTrack{ID: "f1000000-0000-4000-8000-000000000004", Kind: "audio", Format: "dts", Codec: "dts", ProbeState: "pending"})
	h.catalog.sources = []domain.PlaybackSource{source}
	target := "/compat/Items/" + wire(testMovieID) + "/PlaybackInfo"
	profile := func(audio, conditions string) string {
		return `{"DeviceProfile":{"DirectPlayProfiles":[{"Container":"mkv","Type":"Video","VideoCodec":"hevc","AudioCodec":"` + audio + `"}],
		 "CodecProfiles":[{"Type":"VideoAudio","Codec":"ac3","Conditions":` + conditions + `}]}}`
	}
	body := h.post(target, nativeToken, profile("eac3", `[]`)).Body.String()
	if !strings.Contains(body, `/Audio/5/Stream.ac3"`) || !strings.Contains(body, `/Audio/6/Stream.dts"`) {
		t.Fatalf("listing: %s", body)
	}
	var line struct {
		Msg, Reason, SourceID string
		Streams               []int
	}
	if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &line); err != nil || line.Msg != "external audio unsupported by client" ||
		line.Reason != domain.PlaybackReasonAudioCodec || line.SourceID != testSourceID || len(line.Streams) != 1 || line.Streams[0] != 5 {
		t.Fatalf("log %q: %v", logs.String(), err)
	}
	// A channel condition is the same B7 evaluation.
	logs.Reset()
	h.post(target, nativeToken, profile("eac3,ac3", `[{"Condition":"LessThanEqual","Property":"AudioChannels","Value":"2","IsRequired":false}]`))
	if !strings.Contains(logs.String(), "external audio unsupported by client") {
		t.Fatalf("channel condition not logged: %s", logs.String())
	}
	logs.Reset()
	h.post(target, nativeToken, profile("eac3,ac3", `[]`))
	if strings.Contains(logs.String(), "external audio") {
		t.Fatalf("accepted audio logged: %s", logs.String())
	}
}
