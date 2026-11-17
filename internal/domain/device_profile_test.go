package domain

import (
	"slices"
	"testing"
)

func i64(v int64) *int64 { return &v }

// profileSource is a probed 4K HDR10 HEVC Main 10 Matroska source with a
// default 5.1 E-AC-3 track.
func profileSource() PlaybackSource {
	return PlaybackSource{
		ID: "s1", Container: "mkv", Probed: true, BitRate: i64(40_000_000),
		Video: []PlaybackVideoTrack{{Index: 0, Codec: "hevc", Profile: "Main 10", Level: i64(153), Width: i64(3840), Height: i64(2160),
			FrameRate: &MediaRational{Numerator: 24000, Denominator: 1001}, HDR: []string{"hdr10"}, Primary: true}},
		Audio: []PlaybackAudioTrack{{Index: 1, Codec: "eac3", Profile: "", Channels: i64(6), SampleRate: i64(48000), Default: true}},
	}
}

func cond(op, property, value string, required bool) ProfileCondition {
	return ProfileCondition{Condition: op, Property: property, Value: value, IsRequired: required}
}

func hevcProfile(conditions ...ProfileCondition) *DeviceProfile {
	return &DeviceProfile{
		DirectPlay: []DirectPlayProfile{{Video: true, Containers: []string{"matroska"}, VideoCodecs: []string{"h265"}, AudioCodecs: []string{"ec-3"}}},
		Codecs:     []CodecProfile{{Type: CodecProfileVideo, Codecs: []string{"hevc"}, Conditions: conditions}},
	}
}

func TestDeviceProfileConditions(t *testing.T) {
	for _, tc := range []struct {
		name string
		c    ProfileCondition
		want bool
	}{
		{"level within", cond("LessThanEqual", "VideoLevel", "153", true), true},
		{"level above", cond("LessThanEqual", "VideoLevel", "150", true), false},
		{"level at least", cond("GreaterThanEqual", "VideoLevel", "153", true), true},
		{"level not equal", cond("NotEquals", "VideoLevel", "153", true), false},
		{"level any", cond("EqualsAny", "VideoLevel", "120|153", true), true},
		{"level unreadable", cond("LessThanEqual", "VideoLevel", "5.1", false), false},
		{"profile case-insensitive", cond("EqualsAny", "VideoProfile", "main|MAIN 10", true), true},
		{"profile missing", cond("EqualsAny", "videoprofile", "main", true), false},
		{"profile not equal", cond("NotEquals", "VideoProfile", "main", true), true},
		{"profile ordering is meaningless", cond("LessThanEqual", "VideoProfile", "main 10", true), false},
		{"bit depth", cond("LessThanEqual", "VideoBitDepth", "8", true), false},
		{"bit depth ten", cond("LessThanEqual", "VideoBitDepth", "10", true), true},
		{"range", cond("EqualsAny", "VideoRangeType", "SDR|HDR10|HLG", true), true},
		{"range sdr only", cond("EqualsAny", "VideoRangeType", "SDR", true), false},
		{"width", cond("LessThanEqual", "Width", "1920", true), false},
		{"height", cond("LessThanEqual", "Height", "2160", true), true},
		{"framerate", cond("LessThanEqual", "VideoFramerate", "23.976", true), false},
		{"framerate above", cond("LessThanEqual", "VideoFramerate", "24", true), true},
		{"framerate unreadable", cond("LessThanEqual", "VideoFramerate", "1e", true), false},
		{"unknown video bitrate optional", cond("LessThanEqual", "VideoBitrate", "1", false), true},
		{"unknown video bitrate required", cond("LessThanEqual", "VideoBitrate", "1", true), false},
		{"unmeasured required", cond("LessThanEqual", "RefFrames", "1", true), true},
		{"unmeasured interlaced", cond("Equals", "IsInterlaced", "true", true), true},
		{"unknown property", cond("Equals", "Sharpness", "11", true), true},
		{"unknown operator", cond("Approximately", "VideoLevel", "153", true), false},
		{"stream count", cond("LessThanEqual", "NumAudioStreams", "1", true), true},
		{"video stream count", cond("Equals", "NumVideoStreams", "2", true), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reasons := DeviceProfileReasons(hevcProfile(tc.c), 0, profileSource())
			if got := len(reasons) == 0; got != tc.want {
				t.Fatalf("holds %v, want %v: %v", got, tc.want, reasons)
			}
		})
	}
}

func TestDeviceProfileAudioConditions(t *testing.T) {
	src := profileSource()
	src.Audio = append(src.Audio, PlaybackAudioTrack{Index: 2, Codec: "truehd", Channels: i64(8), SampleRate: i64(48000), BitDepth: i64(24)})
	profile := func(conditions ...ProfileCondition) *DeviceProfile {
		p := hevcProfile()
		p.DirectPlay[0].AudioCodecs = nil
		p.Codecs = []CodecProfile{{Type: CodecProfileVideoAudio, Conditions: conditions}}
		return p
	}
	for _, tc := range []struct {
		name   string
		c      ProfileCondition
		reason string
	}{
		{"channels", cond("LessThanEqual", "AudioChannels", "2", true), "codec_condition_unmet:AudioChannels"},
		{"channels ok", cond("LessThanEqual", "AudioChannels", "6", true), ""},
		{"sample rate", cond("LessThanEqual", "AudioSampleRate", "44100", true), "codec_condition_unmet:AudioSampleRate"},
		{"lossy bit depth unknown", cond("LessThanEqual", "AudioBitDepth", "16", false), ""},
		{"lossy bit depth required", cond("LessThanEqual", "AudioBitDepth", "16", true), "codec_condition_unmet:AudioBitDepth"},
		{"profile unknown", cond("Equals", "AudioProfile", "LC", false), ""},
		{"secondary", cond("Equals", "IsSecondaryAudio", "false", true), ""},
		{"secondary refused", cond("Equals", "IsSecondaryAudio", "true", true), "codec_condition_unmet:IsSecondaryAudio"},
		{"secondary unreadable", cond("Equals", "IsSecondaryAudio", "maybe", true), "codec_condition_unmet:IsSecondaryAudio"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reasons := DeviceProfileReasons(profile(tc.c), 0, src)
			if want := []string{tc.reason}; tc.reason == "" && len(reasons) != 0 || tc.reason != "" && !slices.Equal(reasons, want) {
				t.Fatalf("reasons %v, want %q", reasons, tc.reason)
			}
		})
	}
	// The checked track is the default one: the TrueHD track only counts
	// once the user's preferences pick it.
	index := 2
	src.DefaultTracks = &DefaultTracks{Audio: &TrackSelection{Kind: TrackEmbedded, Index: &index}}
	if got := DeviceProfileReasons(profile(cond("LessThanEqual", "AudioBitDepth", "16", true)), 0, src); !slices.Equal(got, []string{"codec_condition_unmet:AudioBitDepth"}) {
		t.Fatalf("preferred track: %v", got)
	}
	if got := DeviceProfileReasons(profile(cond("LessThanEqual", "AudioBitDepth", "24", true)), 0, src); len(got) != 0 {
		t.Fatalf("preferred track within: %v", got)
	}
}

func TestDeviceProfileApplicability(t *testing.T) {
	level := cond("LessThanEqual", "VideoLevel", "120", true)
	src := profileSource()
	for _, tc := range []struct {
		name    string
		profile CodecProfile
		applies bool
	}{
		{"codec matches alias", CodecProfile{Type: CodecProfileVideo, Codecs: []string{"H.265"}, Conditions: []ProfileCondition{level}}, true},
		{"every codec", CodecProfile{Type: CodecProfileVideo, Conditions: []ProfileCondition{level}}, true},
		{"other codec", CodecProfile{Type: CodecProfileVideo, Codecs: []string{"h264"}, Conditions: []ProfileCondition{level}}, false},
		{"other container", CodecProfile{Type: CodecProfileVideo, Containers: []string{"mp4"}, Conditions: []ProfileCondition{level}}, false},
		{"container alias", CodecProfile{Type: CodecProfileVideo, Containers: []string{"webm", "matroska"}, Conditions: []ProfileCondition{level}}, true},
		{"apply condition holds", CodecProfile{Type: CodecProfileVideo, Conditions: []ProfileCondition{level}, ApplyConditions: []ProfileCondition{cond("GreaterThanEqual", "Width", "3840", true)}}, true},
		{"apply condition fails", CodecProfile{Type: CodecProfileVideo, Conditions: []ProfileCondition{level}, ApplyConditions: []ProfileCondition{cond("LessThanEqual", "Width", "1920", true)}}, false},
		{"audio-only profile", CodecProfile{Type: CodecProfileAudio, Conditions: []ProfileCondition{cond("LessThanEqual", "AudioChannels", "2", true)}}, false},
		{"unknown type", CodecProfile{Type: "Photo", Conditions: []ProfileCondition{level}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := hevcProfile()
			p.Codecs = []CodecProfile{tc.profile}
			if got := len(DeviceProfileReasons(p, 0, src)) > 0; got != tc.applies {
				t.Fatalf("applied %v, want %v", got, tc.applies)
			}
		})
	}
}

func TestDeviceProfileReasonOrder(t *testing.T) {
	p := &DeviceProfile{
		DirectPlay: []DirectPlayProfile{
			{Video: true, Containers: []string{"mp4"}, VideoCodecs: []string{"h264"}, AudioCodecs: []string{"aac"}},
			{Video: true, Containers: []string{"mkv"}, VideoCodecs: []string{"h264"}},
			{Video: false, Containers: []string{"mkv"}},
		},
		Containers: []ContainerProfile{{Video: true, Containers: []string{"mkv"}, Conditions: []ProfileCondition{cond("LessThanEqual", "NumAudioStreams", "0", true)}}},
		Codecs: []CodecProfile{
			{Type: CodecProfileVideoAudio, Conditions: []ProfileCondition{cond("LessThanEqual", "AudioChannels", "2", true)}},
			{Type: CodecProfileVideo, Conditions: []ProfileCondition{cond("LessThanEqual", "Width", "1920", true), cond("LessThanEqual", "VideoLevel", "120", true)}},
			{Type: CodecProfileVideo, Conditions: []ProfileCondition{cond("LessThanEqual", "VideoLevel", "100", true)}},
		},
	}
	want := []string{
		// The closest direct play profile fails only the video codec.
		"video_codec_unsupported",
		"container_condition_unmet:NumAudioStreams",
		"codec_condition_unmet:VideoLevel", "codec_condition_unmet:Width", "codec_condition_unmet:AudioChannels",
		"bitrate_exceeds_client",
	}
	src := profileSource()
	if got := DeviceProfileReasons(p, 1_000_000, src); !slices.Equal(got, want) {
		t.Fatalf("reasons %v, want %v", got, want)
	}
	// Multi-part: the union over the parts, same order.
	a, b := src, src
	a.ID, b.ID = "a", "b"
	a.Video = []PlaybackVideoTrack{{Codec: "h264", Width: i64(1280), Level: i64(40), Primary: true}}
	a.Audio = []PlaybackAudioTrack{{Codec: "aac", Channels: i64(2), Default: true}}
	b.BitRate = i64(1)
	multi := PlaybackSource{ID: "m", Parts: []PlaybackPart{{PlaybackSource: a}, {PlaybackSource: b}}}
	got := DeviceProfileReasons(p, 1_000_000, multi)
	if wantMulti := []string{"video_codec_unsupported", "container_condition_unmet:NumAudioStreams", "codec_condition_unmet:VideoLevel", "codec_condition_unmet:Width", "codec_condition_unmet:AudioChannels", "bitrate_exceeds_client"}; !slices.Equal(got, wantMulti) {
		t.Fatalf("multi-part %v, want %v", got, wantMulti)
	}
	// No declaration: only the bit rate ceiling.
	if got := DeviceProfileReasons(nil, 0, src); got != nil {
		t.Fatalf("no declaration: %v", got)
	}
	if got := DeviceProfileReasons(nil, 1, src); !slices.Equal(got, []string{PlaybackReasonBitrate}) {
		t.Fatalf("ceiling only: %v", got)
	}
}

// A declaration only removes sources: satisfied conditions never make a
// source playable that the direct play profiles refuse.
func TestDeviceProfileConditionsNeverAdmit(t *testing.T) {
	p := hevcProfile(cond("LessThanEqual", "VideoLevel", "999", true))
	p.DirectPlay[0].Containers = []string{"mp4"}
	if got := DeviceProfileReasons(p, 0, profileSource()); !slices.Equal(got, []string{PlaybackReasonContainer}) {
		t.Fatalf("reasons %v", got)
	}
}

func TestDeviceProfileUnprobedSource(t *testing.T) {
	src := PlaybackSource{ID: "u", Container: "mkv", Video: []PlaybackVideoTrack{}, Audio: []PlaybackAudioTrack{}}
	p := hevcProfile(cond("LessThanEqual", "VideoLevel", "1", true))
	p.Codecs[0].Codecs = nil
	p.Containers = []ContainerProfile{{Video: true, Conditions: []ProfileCondition{cond("Equals", "NumVideoStreams", "9", true)}}}
	if got := DeviceProfileReasons(p, 0, src); got != nil {
		t.Fatalf("unprobed source held against: %v", got)
	}
}

func TestVideoRangeTypesAndBitDepth(t *testing.T) {
	for _, tc := range []struct {
		track PlaybackVideoTrack
		want  []string
		depth int64
	}{
		{PlaybackVideoTrack{Codec: "h264", Profile: "High"}, []string{"SDR"}, 8},
		{PlaybackVideoTrack{Codec: "h264", Profile: "High 10"}, []string{"SDR"}, 10},
		{PlaybackVideoTrack{Codec: "hevc", Profile: "Main"}, []string{"SDR"}, 8},
		{PlaybackVideoTrack{Codec: "hevc", Profile: "Main 12"}, []string{"SDR"}, 12},
		{PlaybackVideoTrack{Codec: "av1", Profile: "Main"}, []string{"SDR"}, 0},
		{PlaybackVideoTrack{Codec: "av1", HDR: []string{"hdr10"}}, []string{"HDR10"}, 10},
		{PlaybackVideoTrack{Codec: "hevc", HDR: []string{"hdr10plus", "hdr10"}}, []string{"HDR10Plus", "HDR10"}, 10},
		{PlaybackVideoTrack{Codec: "hevc", HDR: []string{"hlg"}}, []string{"HLG"}, 10},
		{PlaybackVideoTrack{Codec: "hevc", HDR: []string{"dolby_vision"}, DolbyVisionProfile: i64(5)}, []string{"DOVI"}, 10},
		{PlaybackVideoTrack{Codec: "hevc", HDR: []string{"dolby_vision", "hdr10"}, DolbyVisionProfile: i64(8)}, []string{"DOVIWithHDR10", "HDR10"}, 10},
		{PlaybackVideoTrack{Codec: "hevc", HDR: []string{"dolby_vision", "hdr10plus", "hdr10"}, DolbyVisionProfile: i64(8)}, []string{"DOVIWithHDR10Plus", "HDR10Plus", "HDR10"}, 10},
		{PlaybackVideoTrack{Codec: "hevc", HDR: []string{"dolby_vision", "hlg"}, DolbyVisionProfile: i64(8)}, []string{"DOVIWithHLG", "HLG"}, 10},
		{PlaybackVideoTrack{Codec: "hevc", HDR: []string{"dolby_vision"}, DolbyVisionProfile: i64(8)}, []string{"DOVIWithSDR", "SDR"}, 10},
		{PlaybackVideoTrack{Codec: "hevc", HDR: []string{"dolby_vision", "hdr10"}, DolbyVisionProfile: i64(7)}, []string{"DOVIWithEL", "HDR10"}, 10},
		{PlaybackVideoTrack{Codec: "hevc", HDR: []string{"dolby_vision"}}, []string{"DOVI"}, 10},
	} {
		if got := VideoRangeTypes(tc.track); !slices.Equal(got, tc.want) {
			t.Errorf("%+v: range %v, want %v", tc.track, got, tc.want)
		}
		got := VideoBitDepth(tc.track)
		if tc.depth == 0 && got != nil || tc.depth != 0 && (got == nil || *got != tc.depth) {
			t.Errorf("%+v: depth %v, want %d", tc.track, got, tc.depth)
		}
	}
	// A Dolby Vision 8.1 stream satisfies an HDR10 declaration through its
	// base layer, a profile 5 stream does not.
	src := profileSource()
	src.Video[0].HDR, src.Video[0].DolbyVisionProfile = []string{"dolby_vision", "hdr10"}, i64(8)
	if got := DeviceProfileReasons(hevcProfile(cond("EqualsAny", "VideoRangeType", "SDR|HDR10", true)), 0, src); got != nil {
		t.Fatalf("8.1 on HDR10: %v", got)
	}
	src.Video[0].HDR, src.Video[0].DolbyVisionProfile = []string{"dolby_vision"}, i64(5)
	if got := DeviceProfileReasons(hevcProfile(cond("EqualsAny", "VideoRangeType", "SDR|HDR10", true)), 0, src); !slices.Equal(got, []string{"codec_condition_unmet:VideoRangeType"}) {
		t.Fatalf("profile 5 on HDR10: %v", got)
	}
}

func TestSubtitleMethod(t *testing.T) {
	profiles := []SubtitleProfile{
		{Format: "vtt", Method: "External"}, {Format: "subrip", Method: "External"}, {Format: "ass", Method: "Embed"},
		{Format: "pgssub", Method: "Encode"}, {Format: "dvdsub", Method: "Hls"}, {Format: "dvbsub", Method: "Embed", Containers: []string{"ts"}},
		{Format: "ssa", Method: "Embed", Languages: []string{"jpn"}},
	}
	for _, tc := range []struct {
		sub  SubtitleCandidate
		want string
	}{
		{SubtitleCandidate{Format: "srt", External: true, Deliverable: true}, SubtitleMethodExternal},
		{SubtitleCandidate{Format: "webvtt", External: true, Deliverable: true}, SubtitleMethodExternal},
		{SubtitleCandidate{Format: "srt", Deliverable: true}, SubtitleMethodExternal},
		{SubtitleCandidate{Format: "srt"}, ""},
		{SubtitleCandidate{Format: "ass", External: true, Deliverable: true}, ""},
		{SubtitleCandidate{Format: "ass"}, SubtitleMethodEmbed},
		{SubtitleCandidate{Format: "pgs"}, ""},
		{SubtitleCandidate{Format: "vobsub"}, ""},
		{SubtitleCandidate{Format: "dvb"}, ""},
		{SubtitleCandidate{Format: "ssa", Language: "eng"}, ""},
		{SubtitleCandidate{Format: "ssa", Language: "JPN"}, SubtitleMethodEmbed},
	} {
		if got := SubtitleMethod(profiles, "mkv", tc.sub); got != tc.want {
			t.Errorf("%+v: %q, want %q", tc.sub, got, tc.want)
		}
	}
}
