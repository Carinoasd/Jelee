package compat

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// profileClients are device profiles shaped like the ones the common
// upstream clients post (testdata/profiles): a browser, ExoPlayer on
// Android, the native Apple player, an mpv based player and a player that
// declares nearly everything. They are hand-written from the clients'
// published profile builders, so they test the evaluation, not a client.
var profileClients = []string{"web", "android-exoplayer", "swiftfin-native", "findroid-mpv", "infuse"}

func loadProfile(t testing.TB, name string) *deviceProfile {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "profiles", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var p deviceProfile
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return &p
}

func p64(v int64) *int64 { return &v }

// profileSources are the cases of the matrix: HEVC 10 bit HDR10, Dolby
// Vision 8.1 with TrueHD Atmos, Dolby Vision 5 in MP4, H.264 with PGS
// subtitles, a 4K60 H.264 level 5.2 file and an AV1 stream of unknown depth.
func profileSources() map[string]domain.PlaybackSource {
	video := func(codec, profile string, level, width, height int64, hdr []string, dv *int64) []domain.PlaybackVideoTrack {
		return []domain.PlaybackVideoTrack{{Index: 0, Codec: codec, Profile: profile, Level: p64(level), Width: p64(width), Height: p64(height),
			FrameRate: &domain.MediaRational{Numerator: 24000, Denominator: 1001}, HDR: hdr, DolbyVisionProfile: dv, Default: true, Primary: true}}
	}
	audio := func(codec string, channels int64, depth *int64) []domain.PlaybackAudioTrack {
		return []domain.PlaybackAudioTrack{{Index: 1, Codec: codec, Channels: p64(channels), SampleRate: p64(48000), BitDepth: depth, Default: true}}
	}
	pgs := []domain.PlaybackSubtitleTrack{{Index: 2, Codec: "hdmv_pgs_subtitle", Format: "pgs"}}
	return map[string]domain.PlaybackSource{
		"hevc10_hdr10_mkv_eac3": {ID: "a1", Container: "mkv", Probed: true, BitRate: p64(40_000_000),
			Video: video("hevc", "Main 10", 153, 3840, 2160, []string{"hdr10"}, nil), Audio: audio("eac3", 6, nil)},
		"dv81_mkv_truehd_atmos": {ID: "a2", Container: "mkv", Probed: true, BitRate: p64(60_000_000),
			Video: video("hevc", "Main 10", 153, 3840, 2160, []string{"dolby_vision", "hdr10"}, p64(8)), Audio: audio("truehd", 8, p64(24))},
		"dv5_mp4_eac3": {ID: "a3", Container: "mp4", Probed: true, BitRate: p64(25_000_000),
			Video: video("hevc", "Main 10", 153, 3840, 2160, []string{"dolby_vision"}, p64(5)), Audio: audio("eac3", 6, nil)},
		"h264_mkv_aac_pgs": {ID: "a4", Container: "mkv", Probed: true, BitRate: p64(8_000_000),
			Video: video("h264", "High", 41, 1920, 1080, nil, nil), Audio: audio("aac", 2, nil), Subtitles: pgs},
		"h264_4k60_level52_mp4": {ID: "a5", Container: "mp4", Probed: true, BitRate: p64(50_000_000),
			Video: video("h264", "High", 52, 3840, 2160, nil, nil), Audio: audio("aac", 2, nil)},
		"av1_unknown_depth_mkv_opus": {ID: "a6", Container: "mkv", Probed: true, BitRate: p64(6_000_000),
			Video: video("av1", "Main", 0, 1920, 1080, nil, nil), Audio: audio("opus", 2, nil)},
	}
}

// TestDeviceProfileMatrix is the golden matrix of the client profiles
// against the sources: the PlaybackInfo exclusion codes and the upstream
// reason names a NoCompatibleStream would carry. An empty list is a direct
// play. Rewrite with -update after an intended rule change and review the
// diff case by case.
func TestDeviceProfileMatrix(t *testing.T) {
	type cell struct {
		Codes   []string `json:"codes"`
		Reasons []string `json:"reasons"`
	}
	sources := profileSources()
	names := make([]string, 0, len(sources))
	for name := range sources {
		names = append(names, name)
	}
	slices.Sort(names)
	matrix := map[string]map[string]cell{}
	for _, client := range profileClients {
		wire := loadProfile(t, client)
		profile := wire.declaration()
		matrix[client] = map[string]cell{}
		for _, name := range names {
			codes := domain.DeviceProfileReasons(profile, *wire.MaxStreamingBitrate, sources[name])
			matrix[client][name] = cell{Codes: append([]string{}, codes...), Reasons: append([]string{}, unsupportedReasons([][]string{codes})...)}
		}
	}
	got, err := json.MarshalIndent(matrix, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join("testdata", "golden", "device_profile_matrix.json")
	if *updateGolden {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s differs:\n got %s", path, got)
	}
}

// PlaybackInfo answers a codec condition the source fails with
// NoCompatibleStream, the upstream reason names and an info log line that
// holds only the source ID and the fixed codes; it never offers a
// conversion. The same request without the condition is a direct play.
func TestPlaybackInfoCodecConditions(t *testing.T) {
	fixedPlaySession(t)
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	h := &playbackHarness{libraryHarness: newLibraryHarnessConfig(t, http.StatusNotFound, func(o *LibraryOptions) { o.DirectPlay, o.Logger = true, logger })}
	target := "/compat/Items/" + wire(testMovieID) + "/PlaybackInfo"
	stereo := `[{"Condition":"LessThanEqual","Property":"AudioChannels","Value":"2","IsRequired":false}]`
	profile := func(conditions, audio string) string {
		return `{"DeviceProfile":{"DirectPlayProfiles":[{"Container":"mkv","Type":"Video","VideoCodec":"hevc","AudioCodec":"eac3"}],
		 "CodecProfiles":[{"Type":"Video","Codec":"hevc","Conditions":` + conditions + `},
		  {"Type":"VideoAudio","Codec":"eac3","Conditions":` + audio + `}],
		 "SubtitleProfiles":[{"Format":"ass","Method":"External"}],
		 "TranscodingProfiles":[{"Container":"ts","Type":"Video","VideoCodec":"h264","Protocol":"hls"}]}}`
	}
	w := h.post(target, nativeToken, profile(`[{"Condition":"LessThanEqual","Property":"Width","Value":"1920","IsRequired":false},
		{"Condition":"LessThanEqual","Property":"VideoLevel","Value":"120","IsRequired":false},
		{"Condition":"EqualsAny","Property":"VideoProfile","Value":"main","IsRequired":false},
		{"Condition":"LessThanEqual","Property":"RefFrames","Value":"1","IsRequired":true}]`, stereo))
	checkGolden(t, "playback_info_codec_conditions.json", w)
	assertNoTranscoding(t, w.Body.String())
	var line struct {
		Level, Msg, Component, SourceID string
		Reasons                         []string
	}
	first, _, _ := bytes.Cut(logs.Bytes(), []byte("\n"))
	if err := json.Unmarshal(first, &line); err != nil {
		t.Fatalf("log %q: %v", logs.String(), err)
	}
	if line.Level != "INFO" || line.Msg != "direct play unsupported" || line.Component != "compat" || line.SourceID != testSourceID ||
		!slices.Equal(line.Reasons, []string{"codec_condition_unmet:VideoProfile", "codec_condition_unmet:Width", "codec_condition_unmet:AudioChannels"}) {
		t.Fatalf("log %s", first)
	}
	for _, leak := range []string{"Arrival", "movie", "mkv\"", testMovieID} {
		if strings.Contains(logs.String(), leak) {
			t.Fatalf("log leaks %q: %s", leak, logs.String())
		}
	}

	// The fake source has no level: an unknown value fails only a
	// required condition.
	logs.Reset()
	if body := h.post(target, nativeToken, profile(`[{"Condition":"LessThanEqual","Property":"VideoLevel","Value":"120","IsRequired":true}]`, stereo)).Body.String(); !strings.Contains(body, `"JeleeUnsupportedReasons":["VideoLevelNotSupported","AudioChannelsNotSupported"]`) {
		t.Fatalf("required level: %s", body)
	}

	// Conditions the source meets leave it listed; the embedded SRT the
	// client declared no way to read is only logged.
	logs.Reset()
	w = h.post(target, nativeToken, profile(`[{"Condition":"EqualsAny","Property":"VideoProfile","Value":"main|main 10","IsRequired":true},
		{"Condition":"EqualsAny","Property":"VideoRangeType","Value":"SDR","IsRequired":true}]`, `[{"Condition":"LessThanEqual","Property":"AudioChannels","Value":"6"}]`))
	if body := w.Body.String(); !strings.Contains(body, `"SupportsDirectPlay":true`) || strings.Contains(body, "ErrorCode") || strings.Contains(body, "JeleeUnsupportedReasons") {
		t.Fatalf("not offered: %s", body)
	}
	assertNoTranscoding(t, w.Body.String())
	if s := logs.String(); !strings.Contains(s, `"msg":"subtitle unsupported by client"`) || !strings.Contains(s, `"reason":"subtitle_format_unsupported","streams":[2]`) {
		t.Fatalf("subtitle log: %s", s)
	}

	// Refusals that say nothing about the client's capabilities carry no
	// reasons.
	if body := h.post(target, nativeToken, `{"EnableDirectPlay":false,"DeviceProfile":{"DirectPlayProfiles":[]}}`).Body.String(); body != `{"MediaSources":[],"ErrorCode":"NoCompatibleStream"}` {
		t.Fatalf("direct play off: %s", body)
	}
}

// Malformed profile members are a bad request; scalars in any JSON type
// are read as text, as clients send enums by name or number.
func TestDeviceProfileWireMembers(t *testing.T) {
	h := &playbackHarness{libraryHarness: newLibraryHarnessConfig(t, http.StatusNotFound, func(o *LibraryOptions) { o.DirectPlay = true })}
	target := "/compat/Items/" + wire(testMovieID) + "/PlaybackInfo"
	for _, body := range []string{
		`{"DeviceProfile":{"CodecProfiles":{}}}`,
		`{"DeviceProfile":{"CodecProfiles":[{"Conditions":[{"Value":{"a":1}}]}]}}`,
		`{"DeviceProfile":{"CodecProfiles":[{"Conditions":[{"IsRequired":"perhaps"}]}]}}`,
		`{"DeviceProfile":{"SubtitleProfiles":[{"Format":["srt"]}]}}`,
		`{"DeviceProfile":{"ContainerProfiles":[{"Type":{}}]}}`,
	} {
		assertGeneric(t, body, h.post(target, nativeToken, body), http.StatusBadRequest)
	}
	for _, tc := range []struct{ body, want string }{
		{`{"DeviceProfile":{"DirectPlayProfiles":[{"Type":"Video"}],"CodecProfiles":[{"Type":0,"Conditions":[{"Condition":2,"Property":"VideoLevel","Value":100}]}]}}`, "VideoLevelNotSupported"},
		{`{"DeviceProfile":{"DirectPlayProfiles":[{"Type":"Video"}],"CodecProfiles":[{"Type":"video","Conditions":[{"Condition":"lessthanequal","Property":"width","Value":"1920","IsRequired":"TRUE"}]}]}}`, "VideoResolutionNotSupported"},
		{`{"DeviceProfile":{"DirectPlayProfiles":[{"Type":"Video"}],"CodecProfiles":[{"Type":1,"Conditions":[{"Condition":"Equals","Property":"AudioSampleRate","Value":44100,"IsRequired":null}]}]}}`, "AudioSampleRateNotSupported"},
		{`{"DeviceProfile":{"DirectPlayProfiles":[{"Type":"Video"}],"ContainerProfiles":[{"Type":1,"Container":"mkv","Conditions":[{"Condition":"Equals","Property":"NumAudioStreams","Value":"0"}]}]}}`, "StreamCountExceedsLimit"},
		{`{"DeviceProfile":{"DirectPlayProfiles":[{"Type":"Video"}],"CodecProfiles":[{"Type":"Video","Conditions":[{"Condition":"Equals","Property":"VideoBitDepth","Value":true}]}]}}`, "VideoBitDepthNotSupported"},
	} {
		body := h.post(target, nativeToken, tc.body).Body.String()
		if body != `{"MediaSources":[],"ErrorCode":"NoCompatibleStream","JeleeUnsupportedReasons":["`+tc.want+`"]}` {
			t.Fatalf("%s: %s", tc.body, body)
		}
	}
}

// FuzzDeviceProfileDeclaration feeds arbitrary PlaybackInfo bodies through
// the declaration parser and the evaluation: no input may panic, and a
// declaration may only remove sources (with nothing declared the source is
// always playable).
func FuzzDeviceProfileDeclaration(f *testing.F) {
	for _, client := range profileClients {
		data, err := os.ReadFile(filepath.Join("testdata", "profiles", client+".json"))
		if err != nil {
			f.Fatal(err)
		}
		f.Add(`{"DeviceProfile":` + string(data) + `}`)
	}
	f.Add(`{"DeviceProfile":{"CodecProfiles":[{"Type":1,"Codec":"","Conditions":[{"Condition":4,"Property":"VideoFramerate","Value":"1e999999999|NaN|-0"}]}]}}`)
	f.Add(`{"DeviceProfile":{"SubtitleProfiles":[{"Format":"","Method":99}]}}`)
	sources := profileSources()
	f.Fuzz(func(t *testing.T, body string) {
		var parsed playbackInfoBody
		if err := json.Unmarshal([]byte(body), &parsed); err != nil {
			return
		}
		declared := parsed.DeviceProfile.declaration()
		for _, source := range sources {
			_ = domain.DeviceProfileReasons(declared, 0, source)
			if r := domain.DeviceProfileReasons(nil, 0, source); r != nil {
				t.Fatalf("nothing declared excluded %s: %v", source.ID, r)
			}
			if declared != nil {
				for _, s := range source.Subtitles {
					_ = domain.SubtitleMethod(declared.Subtitles, source.Container, domain.SubtitleCandidate{Format: s.Format})
				}
			}
		}
		_ = unsupportedReasons([][]string{{"codec_condition_unmet:" + body}})
	})
}
