package domain

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func playbackPtr[T any](v T) *T { return &v }

// playbackTestSource is an HEVC/TrueHD+AC3 Matroska file with a PGS stream,
// a cover-art stream and a 20 Mb/s container bit rate.
func playbackTestSource() PlaybackSource {
	meta := MediaMetadata{
		Format: MediaFormat{BitRate: playbackPtr[int64](20_000_000), DurationMicros: playbackPtr[int64](60_000_000)},
		Streams: []MediaStream{
			{Index: 0, Kind: "video", Codec: playbackPtr("mjpeg"), Video: &MediaVideo{Width: playbackPtr[int64](600), Height: playbackPtr[int64](900)}},
			{Index: 1, Kind: "video", Codec: playbackPtr("hevc"), Video: &MediaVideo{Width: playbackPtr[int64](3840), Height: playbackPtr[int64](2160)}},
			{Index: 2, Kind: "audio", Codec: playbackPtr("truehd"), Default: playbackPtr(true), Audio: &MediaAudio{Channels: playbackPtr[int64](8)}},
			{Index: 3, Kind: "audio", Codec: playbackPtr("ac3"), Audio: &MediaAudio{}},
			{Index: 4, Kind: "subtitle", Codec: playbackPtr("hdmv_pgs_subtitle"), Language: playbackPtr("en"), Forced: playbackPtr(true)},
			{Index: 5, Kind: "subtitle", Codec: playbackPtr("subrip")},
		},
	}
	return BuildPlaybackSource(PlaybackSourceRecord{ID: "s", ContentType: "video/x-matroska", FileName: "Film.2160p.mkv", Metadata: &meta,
		Sidecars: []SidecarTrackRecord{
			{ID: "ext-srt", Track: SidecarTrack{Kind: SidecarKindSubtitle, Format: "vtt"}},
			{ID: "ext-sub", Track: SidecarTrack{Kind: SidecarKindSubtitle, Format: "sub"}},
			{ID: "ext-flac", Track: SidecarTrack{Kind: SidecarKindAudio, Format: "flac", Commentary: true}},
			{ID: "ext-mka", Track: SidecarTrack{Kind: SidecarKindAudio, Format: "mka"}},
		}})
}

func playbackFullCaps() ClientCapabilities {
	return ClientCapabilities{Containers: []string{"mkv"}, VideoCodecs: []string{"hevc"}, AudioCodecs: []string{"truehd", "ac3"},
		SubtitleFormats: []string{"pgs"}, MaxBitrate: 20_000_000}
}

func TestDecideDirectPlayMatrix(t *testing.T) {
	source := playbackTestSource()
	unprobed := BuildPlaybackSource(PlaybackSourceRecord{ID: "u", ContentType: "video/mp4", FileName: "Film.1080p.mp4", ScanSize: playbackPtr[int64](5)})
	without := func(edit func(*ClientCapabilities)) ClientCapabilities {
		c := playbackFullCaps()
		edit(&c)
		return c
	}
	for _, tc := range []struct {
		name   string
		caps   ClientCapabilities
		source PlaybackSource
		want   []string
	}{
		{"all supported", playbackFullCaps(), source, nil},
		{"container", without(func(c *ClientCapabilities) { c.Containers = []string{"mp4"} }), source, []string{PlaybackReasonContainer}},
		{"video codec", without(func(c *ClientCapabilities) { c.VideoCodecs = []string{"h264", "mjpeg"} }), source, []string{PlaybackReasonVideoCodec}},
		{"one audio codec is enough", without(func(c *ClientCapabilities) { c.AudioCodecs = []string{"ac3"} }), source, nil},
		{"no audio codec", without(func(c *ClientCapabilities) { c.AudioCodecs = []string{"aac"} }), source, []string{PlaybackReasonAudioCodec}},
		{"bitrate equal is allowed", without(func(c *ClientCapabilities) { c.MaxBitrate = 20_000_000 }), source, nil},
		{"bitrate one below", without(func(c *ClientCapabilities) { c.MaxBitrate = 19_999_999 }), source, []string{PlaybackReasonBitrate}},
		{"no ceiling", without(func(c *ClientCapabilities) { c.MaxBitrate = 0 }), source, nil},
		{"subtitles never block", without(func(c *ClientCapabilities) { c.SubtitleFormats = nil }), source, nil},
		{"multiple in fixed order", ClientCapabilities{Containers: []string{"mp4"}, VideoCodecs: []string{"h264"}, AudioCodecs: []string{"aac"}, MaxBitrate: 1},
			source, []string{PlaybackReasonContainer, PlaybackReasonVideoCodec, PlaybackReasonAudioCodec, PlaybackReasonBitrate}},
		{"empty declaration", ClientCapabilities{}, source, []string{PlaybackReasonContainer, PlaybackReasonVideoCodec, PlaybackReasonAudioCodec}},
		{"unprobed is never confirmed", ClientCapabilities{Containers: []string{"mp4"}, VideoCodecs: []string{"h264"}, AudioCodecs: []string{"aac"}}, unprobed, []string{PlaybackReasonSourceNotProbe}},
		{"unprobed wrong container", playbackFullCaps(), unprobed, []string{PlaybackReasonContainer, PlaybackReasonSourceNotProbe}},
		{"aliases", ClientCapabilities{Containers: []string{"Matroska"}, VideoCodecs: []string{"H265"}, AudioCodecs: []string{"AC-3"}}, source, nil},
		{"invalid declaration supports nothing", ClientCapabilities{Containers: []string{"mkv"}, VideoCodecs: []string{"hevc"}, AudioCodecs: []string{"ac3"}, MaxBitrate: -1},
			source, []string{PlaybackReasonContainer, PlaybackReasonVideoCodec, PlaybackReasonAudioCodec}},
	} {
		ok, reasons := DecideDirectPlay(tc.caps, tc.source)
		if ok != (len(tc.want) == 0) || !slices.Equal(reasons, tc.want) || reasons == nil {
			t.Errorf("%s: ok=%t reasons=%v, want %v", tc.name, ok, reasons, tc.want)
		}
		for _, reason := range reasons {
			if strings.Contains(reason, "transcod") || strings.Contains(reason, "remux") {
				t.Errorf("%s: reason %q suggests a conversion", tc.name, reason)
			}
		}
	}
}

func TestDecideDirectPlayEdgeSources(t *testing.T) {
	caps := ClientCapabilities{Containers: []string{"mp4", "mpegts"}, VideoCodecs: []string{"h264"}, AudioCodecs: []string{"pcm"}, MaxBitrate: 1000}
	n := func(v int64) *int64 { return &v }
	build := func(content string, format MediaFormat, streams ...MediaStream) PlaybackSource {
		return BuildPlaybackSource(PlaybackSourceRecord{ID: "x", ContentType: content, Metadata: &MediaMetadata{Format: format, Streams: streams}})
	}
	video := MediaStream{Index: 0, Kind: "video", Codec: playbackPtr("h264"), Video: &MediaVideo{}}
	// Derived bit rate: 1000 bytes over 8 seconds is exactly 1000 b/s.
	if ok, r := DecideDirectPlay(caps, build("video/mp4", MediaFormat{SizeBytes: n(1000), DurationMicros: n(8_000_000)}, video)); !ok {
		t.Fatal("derived bit rate at the boundary rejected", r)
	}
	if ok, r := DecideDirectPlay(caps, build("video/mp4", MediaFormat{SizeBytes: n(1001), DurationMicros: n(8_000_000)}, video)); ok || !slices.Equal(r, []string{PlaybackReasonBitrate}) {
		t.Fatal("derived bit rate above the ceiling accepted", r)
	}
	// An unknown bit rate is not reported.
	if ok, _ := DecideDirectPlay(caps, build("video/mp4", MediaFormat{}, video)); !ok {
		t.Fatal("unknown bit rate rejected")
	}
	// No audio stream at all is fine; PCM matches every sample format.
	if ok, _ := DecideDirectPlay(caps, build("video/mp2t", MediaFormat{}, video, MediaStream{Index: 1, Kind: "audio", Codec: playbackPtr("pcm_s24le"), Audio: &MediaAudio{}})); !ok {
		t.Fatal("pcm declaration did not cover pcm_s24le")
	}
	// An unrecognized codec is never supported.
	if ok, r := DecideDirectPlay(caps, build("video/mp4", MediaFormat{}, MediaStream{Index: 0, Kind: "video", Video: &MediaVideo{}})); ok || !slices.Equal(r, []string{PlaybackReasonVideoCodec}) {
		t.Fatal("unknown video codec accepted", r)
	}
	// Cover art alone is not a video stream to check; unknown content types
	// have no container.
	if ok, r := DecideDirectPlay(caps, build("video/mp4", MediaFormat{}, MediaStream{Index: 0, Kind: "video", Codec: playbackPtr("png"), Video: &MediaVideo{}})); !ok {
		t.Fatal("cover art checked as video", r)
	}
	if ok, r := DecideDirectPlay(caps, build("application/octet-stream", MediaFormat{}, video)); ok || !slices.Equal(r, []string{PlaybackReasonContainer}) {
		t.Fatal("unknown content type accepted", r)
	}
}

func TestCheckPlaybackTracks(t *testing.T) {
	caps := playbackFullCaps()
	caps.AudioCodecs = []string{"ac3", "flac"}
	caps.SubtitleFormats = []string{"vtt"}
	d := CheckPlayback(caps, playbackTestSource())
	if !d.DirectPlay || d.Code != "" || len(d.Reasons) != 0 {
		t.Fatalf("source verdict changed by tracks: %+v", d)
	}
	type key struct {
		kind, id  string
		index     int
		supported bool
		reason    string
	}
	var got []key
	for _, track := range d.Tracks {
		k := key{kind: track.Kind, id: track.ID, supported: track.Supported, reason: track.Reason, index: -1}
		if track.Index != nil {
			k.index = *track.Index
		}
		if track.External != (track.ID != "") {
			t.Fatalf("external flag differs: %+v", track)
		}
		// G16.4: every unsupported track is marked as not direct playable.
		if (track.Code == PlaybackUnsupportedCode) == track.Supported || track.Code != "" && track.Code != PlaybackUnsupportedCode || track.URL != "" {
			t.Fatalf("track code differs: %+v", track)
		}
		got = append(got, k)
	}
	want := []key{
		{"audio", "", 2, false, PlaybackReasonAudioCodec},
		{"audio", "", 3, true, ""},
		{"subtitle", "", 4, false, PlaybackReasonSubtitleFormat},
		{"subtitle", "", 5, false, PlaybackReasonSubtitleFormat},
		{"subtitle", "ext-srt", -1, true, ""},
		{"subtitle", "ext-sub", -1, false, PlaybackReasonTrackNotProbed},
		{"audio", "ext-flac", -1, true, ""},
		{"audio", "ext-mka", -1, false, PlaybackReasonTrackNotProbed},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("tracks differ:\n got %v\nwant %v", got, want)
	}
	unsupported := CheckPlayback(ClientCapabilities{}, playbackTestSource())
	if unsupported.DirectPlay || unsupported.Code != PlaybackUnsupportedCode {
		t.Fatalf("unsupported decision lacks the code: %+v", unsupported)
	}
	encoded, err := json.Marshal(d)
	if err != nil || !strings.HasPrefix(string(encoded), `{"sourceId":"s","directPlay":true,"reasons":[],"tracks":[`) {
		t.Fatalf("decision encoding differs: %s", encoded)
	}
}

func TestBuildPlaybackSourceProjection(t *testing.T) {
	s := playbackTestSource()
	if s.Container != "mkv" || !s.Probed || len(s.Video) != 1 || s.Video[0].Index != 1 || !s.Video[0].Primary || s.Version.Resolution != "2160p" ||
		len(s.Audio) != 2 || s.Audio[0].Channels == nil || !s.Audio[0].Default || len(s.Subtitles) != 2 || s.Subtitles[0].Format != "pgs" ||
		!s.Subtitles[0].Forced || s.Subtitles[0].Language != "en" || s.Subtitles[1].Format != "srt" || s.BitRate == nil || *s.BitRate != 20_000_000 {
		t.Fatalf("probed projection differs: %+v", s)
	}
	codecs := map[string]string{}
	for _, e := range s.External {
		codecs[e.ID] = e.Codec
	}
	if codecs["ext-srt"] != "webvtt" || codecs["ext-sub"] != "" || codecs["ext-flac"] != "flac" || codecs["ext-mka"] != "" {
		t.Fatalf("external codecs differ: %v", codecs)
	}
	u := BuildPlaybackSource(PlaybackSourceRecord{ID: "u", ContentType: "video/webm", FileName: "Clip.720p.webm", ScanSize: playbackPtr[int64](42)})
	encoded, err := json.Marshal(u)
	if err != nil || u.Probed || u.Container != "webm" || u.SizeBytes == nil || *u.SizeBytes != 42 || u.BitRate != nil || u.Version.Resolution != "720p" ||
		!strings.Contains(string(encoded), `"videoTracks":[]`) || !strings.Contains(string(encoded), `"externalTracks":[]`) {
		t.Fatalf("unprobed projection differs: %s", encoded)
	}
	if text := (PlaybackSourceRecord{FileName: "secret.mkv"}).String(); strings.Contains(text, "secret") {
		t.Fatal("record diagnostics expose the file name")
	}
}

func TestNormalizeClientCapabilities(t *testing.T) {
	c, err := NormalizeClientCapabilities(ClientCapabilities{Containers: []string{"MKV", "matroska", "TS"}, VideoCodecs: []string{"HVC1", "x264"},
		AudioCodecs: []string{"E-AC-3", "dca"}, SubtitleFormats: []string{"SubRip", "SUP", "idx"}, MaxBitrate: ClientCapabilityBitrateMax})
	if err != nil || !slices.Equal(c.Containers, []string{"mkv", "mpegts"}) || !slices.Equal(c.VideoCodecs, []string{"hevc", "h264"}) ||
		!slices.Equal(c.AudioCodecs, []string{"eac3", "dts"}) || !slices.Equal(c.SubtitleFormats, []string{"srt", "pgs", "vobsub"}) {
		t.Fatalf("normalized declaration differs: %+v %v", c, err)
	}
	for _, bad := range []ClientCapabilities{
		{MaxBitrate: -1}, {MaxBitrate: ClientCapabilityBitrateMax + 1},
		{Containers: []string{""}}, {VideoCodecs: []string{strings.Repeat("a", 33)}}, {AudioCodecs: []string{"a b"}},
		{SubtitleFormats: []string{"é"}}, {Containers: make([]string, 33)},
	} {
		if _, err := NormalizeClientCapabilities(bad); err != ErrInvalid {
			t.Errorf("accepted %+v", bad)
		}
	}
}

func TestPlaybackMergesMediaInfoWithoutOverridingFFprobe(t *testing.T) {
	str := func(s string) *string { return &s }
	yes := true
	meta := MediaMetadata{
		Streams: []MediaStream{
			{Index: 0, Kind: "video", Codec: str("hevc"), Profile: str("Main 10"), Video: &MediaVideo{ColorTransfer: str("smpte2084"), HDR10Plus: &yes,
				DolbyVision: &MediaDolbyVision{Profile: func() *int64 { v := int64(8); return &v }(), Level: func() *int64 { v := int64(6); return &v }()}}},
			{Index: 1, Kind: "video", Codec: str("h264"), Video: &MediaVideo{}},
			{Index: 2, Kind: "audio", Codec: str("truehd"), Audio: &MediaAudio{}},
			{Index: 3, Kind: "audio", Codec: str("dts"), Profile: str("DTS-HD MA"), Audio: &MediaAudio{}},
		},
		Matroska: &MediaMatroska{StreamDetails: []MediaStreamDetail{
			{Index: 0, HDR: []string{"hlg", "hdr10"}, FormatProfile: "Ignored"},
			{Index: 1, HDR: []string{"hdr10"}, FormatProfile: "High"},
			{Index: 2, AudioFeatures: []string{AudioFeatureAtmos, AudioFeatureTrueHD}},
			{Index: 3, AudioFeatures: []string{AudioFeatureDTSX, AudioFeatureDTSHD}},
		}},
	}
	s := BuildPlaybackSource(PlaybackSourceRecord{ID: "s", ContentType: "video/x-matroska", FileName: "a.mkv", Metadata: &meta})
	if got := strings.Join(s.Video[0].HDR, ","); got != "dolby_vision,hdr10plus,hdr10,hlg" || s.Video[0].Profile != "Main 10" ||
		s.Video[0].DolbyVisionProfile == nil || *s.Video[0].DolbyVisionProfile != 8 || *s.Video[0].DolbyVisionLevel != 6 {
		t.Fatalf("ffprobe values must lead: %+v", s.Video[0])
	}
	if s.Video[1].Profile != "High" || strings.Join(s.Video[1].HDR, ",") != "hdr10" {
		t.Fatalf("gaps are filled from MediaInfo: %+v", s.Video[1])
	}
	if a := s.Audio[0]; !a.Atmos || strings.Join(a.Features, ",") != "dolby_truehd,dolby_atmos" {
		t.Fatalf("truehd %+v", a)
	}
	if a := s.Audio[1]; strings.Join(a.Features, ",") != "dts_hd_ma,dts_x" || a.Atmos {
		t.Fatalf("dts %+v", a)
	}
}

func TestPlaybackChapters(t *testing.T) {
	v := func(n int64) *int64 { return &n }
	probe := []MediaChapter{{ID: 0, StartMicros: v(0)}, {ID: 1, StartMicros: v(60_000_000)}, {ID: 2}}
	named := []MediaNamedChapter{{StartMicros: 0, Title: "Intro"}, {StartMicros: 60_000_000, Title: "Main"}}
	cases := []struct {
		name string
		meta MediaMetadata
		want []PlaybackChapter
	}{
		{"none", MediaMetadata{}, nil},
		{"probe only", MediaMetadata{Chapters: probe[:2]}, []PlaybackChapter{{0, 0, ""}, {1, 60_000_000, ""}}},
		{"unknown start skipped", MediaMetadata{Chapters: probe}, []PlaybackChapter{{0, 0, ""}, {1, 60_000_000, ""}}},
		{"titles by position", MediaMetadata{Chapters: probe[:2], Matroska: &MediaMatroska{Chapters: named}}, []PlaybackChapter{{0, 0, "Intro"}, {1, 60_000_000, "Main"}}},
		{"supplement only", MediaMetadata{Matroska: &MediaMatroska{Chapters: named}}, []PlaybackChapter{{0, 0, "Intro"}, {1, 60_000_000, "Main"}}},
		{"count mismatch drops titles", MediaMetadata{Chapters: probe[:1], Matroska: &MediaMatroska{Chapters: named}}, []PlaybackChapter{{0, 0, ""}}},
		{"time mismatch drops titles", MediaMetadata{Chapters: []MediaChapter{{ID: 0, StartMicros: v(0)}, {ID: 1, StartMicros: v(61_000_000)}}, Matroska: &MediaMatroska{Chapters: named}}, []PlaybackChapter{{0, 0, ""}, {1, 61_000_000, ""}}},
	}
	for _, c := range cases {
		if got := playbackChapters(c.meta); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
}

func TestPlaybackEmbeddedTrackLabels(t *testing.T) {
	str := func(s string) *string { return &s }
	yes := true
	meta := MediaMetadata{
		Format: MediaFormat{Names: []string{"matroska", "webm"}},
		Streams: []MediaStream{
			{Index: 0, Kind: "video", Codec: str("h264"), Video: &MediaVideo{}},
			{Index: 1, Kind: "audio", Codec: str("aac"), Audio: &MediaAudio{}, Title: str("Main mix")},
			{Index: 2, Kind: "audio", Codec: str("aac"), Audio: &MediaAudio{}, Comment: &yes},
			{Index: 3, Kind: "audio", Codec: str("aac"), Audio: &MediaAudio{}, Descriptions: &yes},
			{Index: 4, Kind: "audio", Codec: str("aac"), Audio: &MediaAudio{}, VisualImpaired: &yes},
			{Index: 5, Kind: "subtitle", Codec: str("subrip"), HearingImpaired: &yes, Title: str("File title")},
			{Index: 6, Kind: "subtitle", Codec: str("ass"), Captions: &yes},
			{Index: 7, Kind: "subtitle", Codec: str("hdmv_pgs_subtitle")},
			{Index: 8, Kind: "subtitle", Codec: str("mov_text")},
			{Index: 9, Kind: "subtitle", Codec: str("eia_608")},
		},
		// MediaInfo names every track but may only fill a missing title.
		Matroska: &MediaMatroska{StreamTitles: []MediaStreamTitle{{Index: 1, Title: "MediaInfo title"}, {Index: 2, Title: "Matroska comment"}, {Index: 5, Title: "MediaInfo SDH"}}},
	}
	s := BuildPlaybackSource(PlaybackSourceRecord{ID: "s", ContentType: "video/x-matroska", FileName: "a.mkv", Metadata: &meta})
	type audio struct {
		title                 string
		commentary, visualImp bool
	}
	var gotAudio []audio
	for _, a := range s.Audio {
		gotAudio = append(gotAudio, audio{a.Title, a.Commentary, a.VisualImpaired})
	}
	if want := []audio{{"Main mix", false, false}, {"Matroska comment", true, false}, {"", false, true}, {"", false, true}}; !reflect.DeepEqual(gotAudio, want) {
		t.Fatalf("audio labels = %+v, want %+v", gotAudio, want)
	}
	type sub struct {
		title, charset string
		sdh            bool
	}
	var gotSubs []sub
	for _, st := range s.Subtitles {
		gotSubs = append(gotSubs, sub{st.Title, st.Charset, st.SDH})
	}
	want := []sub{{"File title", "UTF-8", true}, {"", "UTF-8", true}, {"", "", false}, {"", "UTF-8", false}, {"", "", false}}
	if !reflect.DeepEqual(gotSubs, want) {
		t.Fatalf("subtitle labels = %+v, want %+v", gotSubs, want)
	}
	// A source without labels serializes none of the optional members.
	plain := BuildPlaybackSource(PlaybackSourceRecord{ID: "s", ContentType: "video/mp4", FileName: "a.mp4", Metadata: &MediaMetadata{Streams: []MediaStream{
		{Index: 0, Kind: "audio", Codec: str("aac"), Audio: &MediaAudio{}}, {Index: 1, Kind: "subtitle", Codec: str("eia_608")}}}})
	raw, err := json.Marshal(plain)
	if err != nil || strings.Contains(string(raw), `"title"`) || strings.Contains(string(raw), `"charset"`) ||
		!strings.Contains(string(raw), `"commentary":false`) || !strings.Contains(string(raw), `"sdh":false`) {
		t.Fatalf("unlabelled tracks: %s %v", raw, err)
	}
}

func TestStreamTitleAndLabelValidation(t *testing.T) {
	for title, ok := range map[string]bool{"English SDH": true, "導演評論": true, "": false, " x": false, "x ": false, "a\nb": false, "a\u202Eb": false, "a\u200Bb": false,
		"a\u2028b": false, "bad\xff": false, "a\ufffdb": false, strings.Repeat("a", MaxStreamTitleBytes): true, strings.Repeat("a", MaxStreamTitleBytes+1): false} {
		if ValidStreamTitle(title) != ok {
			t.Errorf("ValidStreamTitle(%q) = %v, want %v", title, !ok, ok)
		}
	}
	str := func(s string) *string { return &s }
	yes, no := true, false
	base := func(kind string) MediaMetadata {
		s := MediaStream{Index: 0, Kind: kind}
		if kind == "audio" {
			s.Audio = &MediaAudio{}
		}
		if kind == "video" {
			s.Video = &MediaVideo{}
		}
		return MediaMetadata{Streams: []MediaStream{s}}
	}
	cases := map[string]struct {
		kind   string
		mutate func(*MediaStream)
		ok     bool
	}{
		"audio labels": {"audio", func(s *MediaStream) {
			s.Title, s.Comment, s.VisualImpaired, s.Descriptions = str("Commentary"), &yes, &yes, &yes
		}, true},
		"subtitle labels":      {"subtitle", func(s *MediaStream) { s.Title, s.HearingImpaired, s.Captions = str("SDH"), &yes, &yes }, true},
		"video title":          {"video", func(s *MediaStream) { s.Title = str("x") }, false},
		"video flag":           {"video", func(s *MediaStream) { s.Comment = &yes }, false},
		"attachment title":     {"attachment", func(s *MediaStream) { s.Title = str("font") }, false},
		"newline title":        {"audio", func(s *MediaStream) { s.Title = str("a\nb") }, false},
		"empty title":          {"subtitle", func(s *MediaStream) { s.Title = str("") }, false},
		"oversize title":       {"subtitle", func(s *MediaStream) { s.Title = str(strings.Repeat("a", MaxStreamTitleBytes+1)) }, false},
		"explicit false flag":  {"audio", func(s *MediaStream) { s.Comment = &no }, false},
		"explicit false flag2": {"subtitle", func(s *MediaStream) { s.HearingImpaired = &no }, false},
	}
	for name, c := range cases {
		meta := base(c.kind)
		c.mutate(&meta.Streams[0])
		if _, err := MarshalProbeMetadata(meta); (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok=%v", name, err, c.ok)
		}
	}
}

// The parser version is shared by every stage 10 change to the stored probe
// shape (B3 labels now, B4 on-demand sidecar probing later). A second bump
// would make the owner re-probe the whole library twice.
func TestProbeParserVersionIsTheSingleStage10Bump(t *testing.T) {
	if ProbeParserVersion != "media-metadata-v5" || ProbeMetadataSchemaVersion != 2 {
		t.Fatalf("parser %s schema %d: stage 10 shares one bump (see probeParserVersionStage10)", ProbeParserVersion, ProbeMetadataSchemaVersion)
	}
}
