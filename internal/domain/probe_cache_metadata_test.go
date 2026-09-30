package domain

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
)

func metadataPtr[T any](v T) *T { return &v }
func fullProbeMetadata() MediaMetadata {
	return MediaMetadata{
		Format: MediaFormat{Names: []string{"matroska", "webm"}, DurationMicros: metadataPtr(int64(1000000)), SizeBytes: metadataPtr(int64(500)), BitRate: metadataPtr(int64(4000))},
		Streams: []MediaStream{
			{Index: 0, Kind: "video", Codec: metadataPtr("hevc"), Profile: metadataPtr("Main 10"), DurationMicros: metadataPtr(int64(1000000)), BitRate: metadataPtr(int64(1)), Video: &MediaVideo{
				Level: metadataPtr(int64(1)), Width: metadataPtr(int64(320)), Height: metadataPtr(int64(180)), FrameRate: &MediaRational{24, 1}, AverageFrameRate: &MediaRational{24000, 1001},
				ColorRange: metadataPtr("tv"), ColorSpace: metadataPtr("bt2020nc"), ColorTransfer: metadataPtr("smpte2084"), ColorPrimaries: metadataPtr("bt2020"), HDR10Plus: metadataPtr(true),
				MasteringDisplay: &MediaMasteringDisplay{RedX: &MediaRational{1, 2}, RedY: &MediaRational{1, 3}, GreenX: &MediaRational{0, 1}, GreenY: &MediaRational{1, 1}, BlueX: &MediaRational{1, 5}, BlueY: &MediaRational{1, 10}, WhiteX: &MediaRational{1, 4}, WhiteY: &MediaRational{1, 5}, MinLuminance: &MediaRational{1, 1000}, MaxLuminance: &MediaRational{1000, 1}},
				ContentLight:     &MediaContentLight{MaxContent: metadataPtr(int64(1000)), MaxAverage: metadataPtr(int64(200))}, DolbyVision: &MediaDolbyVision{Profile: metadataPtr(int64(8)), Level: metadataPtr(int64(6)), RPU: metadataPtr(true), BaseLayer: metadataPtr(true), EnhancementLayer: metadataPtr(false), CompatibilityID: metadataPtr(int64(1))},
			}},
			{Index: 1, Kind: "audio", Codec: metadataPtr("eac3"), Profile: metadataPtr("Dolby Digital Plus + Dolby Atmos"), Language: metadataPtr("eng"), Default: metadataPtr(true), Forced: metadataPtr(false), Audio: &MediaAudio{Channels: metadataPtr(int64(6)), ChannelLayout: metadataPtr("5.1"), SampleRate: metadataPtr(int64(48000)), BitsPerSample: metadataPtr(int64(24)), BitsPerRawSample: metadataPtr(int64(24)), Atmos: metadataPtr(true)}},
			{Index: 2, Kind: "subtitle", Codec: metadataPtr("subrip"), Language: metadataPtr("zh-hant")},
			{Index: 3, Kind: "data"}, {Index: 4, Kind: "attachment"},
		}, Chapters: []MediaChapter{{ID: 1, StartMicros: metadataPtr(int64(0)), EndMicros: metadataPtr(int64(1000000))}},
	}
}

func TestProbeMetadataWhitelistRoundTrip(t *testing.T) {
	v := fullProbeMetadata()
	before, _ := json.Marshal(v)
	got, err := MarshalProbeMetadata(v)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(before) {
		t.Fatal("marshal changed normalized data")
	}
	var after MediaMetadata
	if err := json.Unmarshal(got, &after); err != nil || !reflect.DeepEqual(v, after) {
		t.Fatal("metadata did not round trip")
	}
	for _, field := range []string{"filename", "title", "vendor", "url", "rootPath", "stderr"} {
		if strings.Contains(string(got), `"`+field+`":`) {
			t.Fatalf("private field %s serialized", field)
		}
	}
	minimal := MediaMetadata{Streams: []MediaStream{{Kind: "audio", Audio: &MediaAudio{}}}}
	got, err = MarshalProbeMetadata(minimal)
	if err != nil || strings.Contains(string(got), "atmos") || strings.Contains(string(got), "sampleRate") {
		t.Fatal("unknown observations became explicit values")
	}
	v = fullProbeMetadata()
	v.Streams[1].Codec = metadataPtr("truehd")
	v.Streams[1].Profile = metadataPtr("Dolby TrueHD + Dolby Atmos")
	if _, err := MarshalProbeMetadata(v); err != nil {
		t.Fatal("explicit truehd Atmos rejected")
	}
}

func TestProbeMetadataRejectsUnsafeOrInconsistentValues(t *testing.T) {
	cases := map[string]func(*MediaMetadata){
		"empty": func(v *MediaMetadata) { v.Streams = nil }, "streams bound": func(v *MediaMetadata) { v.Streams = make([]MediaStream, 65) }, "chapters bound": func(v *MediaMetadata) { v.Chapters = make([]MediaChapter, 257) }, "formats bound": func(v *MediaMetadata) { v.Format.Names = make([]string, 17) },
		"duplicate formats": func(v *MediaMetadata) { v.Format.Names = []string{"mp4", "mp4"} }, "format path": func(v *MediaMetadata) { v.Format.Names = []string{"/private/movie"} },
		"format negative duration": func(v *MediaMetadata) { v.Format.DurationMicros = metadataPtr(int64(-1)) }, "negative size": func(v *MediaMetadata) { v.Format.SizeBytes = metadataPtr(int64(-1)) }, "zero rate": func(v *MediaMetadata) { v.Format.BitRate = metadataPtr(int64(0)) },
		"duplicate stream": func(v *MediaMetadata) { v.Streams[1].Index = 0 }, "negative index": func(v *MediaMetadata) { v.Streams[0].Index = -1 }, "large index": func(v *MediaMetadata) { v.Streams[0].Index = math.MaxInt32 + 1 }, "kind path": func(v *MediaMetadata) { v.Streams[0].Kind = "/secret" },
		"codec path": func(v *MediaMetadata) { v.Streams[0].Codec = metadataPtr("/private") }, "profile URL": func(v *MediaMetadata) { v.Streams[0].Profile = metadataPtr("https://secret") }, "oversize enum": func(v *MediaMetadata) { v.Streams[0].Profile = metadataPtr(strings.Repeat("s", 129)) }, "invalid language": func(v *MediaMetadata) { v.Streams[1].Language = metadataPtr("private track title") }, "language case": func(v *MediaMetadata) { v.Streams[1].Language = metadataPtr("EN") }, "language bound": func(v *MediaMetadata) { v.Streams[1].Language = metadataPtr(strings.Repeat("a", 36)) },
		"wrong stream body": func(v *MediaMetadata) { v.Streams[0].Audio = &MediaAudio{} }, "missing video": func(v *MediaMetadata) { v.Streams[0].Video = nil }, "missing audio": func(v *MediaMetadata) { v.Streams[1].Audio = nil }, "subtitle video": func(v *MediaMetadata) { v.Streams[2].Video = &MediaVideo{} },
		"width": func(v *MediaMetadata) { v.Streams[0].Video.Width = metadataPtr(int64(0)) }, "height": func(v *MediaMetadata) { v.Streams[0].Video.Height = metadataPtr(int64(65536)) }, "level": func(v *MediaMetadata) { v.Streams[0].Video.Level = metadataPtr(int64(-99)) },
		"zero denominator": func(v *MediaMetadata) { v.Streams[0].Video.FrameRate = &MediaRational{1, 0} }, "negative rate": func(v *MediaMetadata) { v.Streams[0].Video.FrameRate = &MediaRational{-1, 1} }, "zero rate fraction": func(v *MediaMetadata) { v.Streams[0].Video.FrameRate = &MediaRational{0, 1} }, "fraction bound": func(v *MediaMetadata) { v.Streams[0].Video.FrameRate = &MediaRational{math.MaxInt64, 1} }, "unreduced": func(v *MediaMetadata) { v.Streams[0].Video.FrameRate = &MediaRational{48, 2} },
		"range": func(v *MediaMetadata) { v.Streams[0].Video.ColorRange = metadataPtr("private") }, "color": func(v *MediaMetadata) { v.Streams[0].Video.ColorSpace = metadataPtr("private") }, "transfer": func(v *MediaMetadata) { v.Streams[0].Video.ColorTransfer = metadataPtr("private") }, "primary": func(v *MediaMetadata) { v.Streams[0].Video.ColorPrimaries = metadataPtr("private") },
		"HDR false": func(v *MediaMetadata) { v.Streams[0].Video.HDR10Plus = metadataPtr(false) }, "coordinate": func(v *MediaMetadata) { v.Streams[0].Video.MasteringDisplay.RedX = &MediaRational{2, 1} }, "luminance limit": func(v *MediaMetadata) { v.Streams[0].Video.MasteringDisplay.MaxLuminance = &MediaRational{1000001, 1} }, "luminance order": func(v *MediaMetadata) { v.Streams[0].Video.MasteringDisplay.MinLuminance = &MediaRational{1001, 1} },
		"light bound": func(v *MediaMetadata) { v.Streams[0].Video.ContentLight.MaxContent = metadataPtr(int64(65536)) }, "light order": func(v *MediaMetadata) { v.Streams[0].Video.ContentLight.MaxAverage = metadataPtr(int64(1001)) }, "DV": func(v *MediaMetadata) { v.Streams[0].Video.DolbyVision.Profile = metadataPtr(int64(256)) }, "DV compatibility": func(v *MediaMetadata) { v.Streams[0].Video.DolbyVision.CompatibilityID = metadataPtr(int64(16)) },
		"channels": func(v *MediaMetadata) { v.Streams[1].Audio.Channels = metadataPtr(int64(257)) }, "sample rate": func(v *MediaMetadata) { v.Streams[1].Audio.SampleRate = metadataPtr(int64(768001)) }, "bits": func(v *MediaMetadata) { v.Streams[1].Audio.BitsPerSample = metadataPtr(int64(65)) }, "raw bits": func(v *MediaMetadata) { v.Streams[1].Audio.BitsPerRawSample = metadataPtr(int64(0)) }, "layout": func(v *MediaMetadata) { v.Streams[1].Audio.ChannelLayout = metadataPtr("private") },
		"guessed Atmos": func(v *MediaMetadata) { v.Streams[1].Profile = metadataPtr("Main") }, "Atmos no profile": func(v *MediaMetadata) { v.Streams[1].Profile = nil }, "Atmos no codec": func(v *MediaMetadata) { v.Streams[1].Codec = nil }, "Atmos false": func(v *MediaMetadata) { v.Streams[1].Audio.Atmos = metadataPtr(false) },
		"chapter ID": func(v *MediaMetadata) { v.Chapters[0].ID = -1 }, "duplicate chapter": func(v *MediaMetadata) { v.Chapters = append(v.Chapters, v.Chapters[0]) }, "chapter negative": func(v *MediaMetadata) { v.Chapters[0].StartMicros = metadataPtr(int64(-1)) }, "chapter order": func(v *MediaMetadata) { v.Chapters[0].StartMicros = metadataPtr(int64(1000001)) }, "chapter duration": func(v *MediaMetadata) { v.Chapters[0].EndMicros = metadataPtr(int64(1000001)) },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			v := fullProbeMetadata()
			change(&v)
			got, err := MarshalProbeMetadata(v)
			if err != ErrInvalid || got != nil {
				t.Fatal("invalid metadata produced persisted bytes")
			}
		})
	}
}

func TestProbeMetadataMaxShapeIsBounded(t *testing.T) {
	v := fullProbeMetadata()
	video := v.Streams[0]
	v.Streams = nil
	for i := 0; i < 64; i++ {
		s := video
		s.Index = i
		v.Streams = append(v.Streams, s)
	}
	v.Chapters = nil
	for i := 0; i < 256; i++ {
		v.Chapters = append(v.Chapters, MediaChapter{ID: int64(i), StartMicros: metadataPtr(int64(0)), EndMicros: metadataPtr(int64(1))})
	}
	got, err := MarshalProbeMetadata(v)
	if err == nil && len(got) > ProbeMetadataMaxBytes {
		t.Fatal("payload limit bypassed")
	}
	if err != nil && got != nil {
		t.Fatal("failed serialization leaked bytes")
	}
	// Cross-products close to int64 overflow remain exact.
	v = fullProbeMetadata()
	v.Streams[0].Video.FrameRate = &MediaRational{math.MaxInt64, math.MaxInt64 - 1}
	v.Streams[0].Video.MasteringDisplay.MinLuminance = &MediaRational{math.MaxInt64 - 2, math.MaxInt64 - 1}
	v.Streams[0].Video.MasteringDisplay.MaxLuminance = &MediaRational{math.MaxInt64 - 1, math.MaxInt64}
	if _, err := MarshalProbeMetadata(v); err != nil {
		t.Fatal("exact large rational rejected", err)
	}
}

func TestProbeMetadataDenseNormalizedPayloadFitsByteBudget(t *testing.T) {
	v := fullProbeMetadata()
	s := v.Streams[0]
	s.Profile = metadataPtr("High 4:4:4 Predictive")
	s.DurationMicros = metadataPtr(int64(math.MaxInt64))
	s.BitRate = metadataPtr(int64(math.MaxInt64))
	r := &MediaRational{math.MaxInt64 - 1, math.MaxInt64}
	s.Video.MasteringDisplay = &MediaMasteringDisplay{RedX: r, RedY: r, GreenX: r, GreenY: r, BlueX: r, BlueY: r, WhiteX: r, WhiteY: r, MinLuminance: r, MaxLuminance: r}
	s.Video.FrameRate = r
	s.Video.AverageFrameRate = r
	v.Streams = nil
	for i := 0; i < 64; i++ {
		copy := s
		copy.Index = i
		v.Streams = append(v.Streams, copy)
	}
	v.Format.DurationMicros = metadataPtr(int64(math.MaxInt64))
	v.Chapters = nil
	for i := 0; i < 256; i++ {
		v.Chapters = append(v.Chapters, MediaChapter{ID: math.MaxInt64 - int64(i), StartMicros: metadataPtr(int64(math.MaxInt64 - 1)), EndMicros: metadataPtr(int64(math.MaxInt64))})
	}
	raw, err := json.Marshal(v)
	if err != nil || len(raw) > ProbeMetadataMaxBytes {
		t.Fatalf("bounded normalized fixture exceeds byte budget: %d", len(raw))
	}
	got, err := MarshalProbeMetadata(v)
	if err != nil || len(got) != len(raw) {
		t.Fatal("bounded dense normalized payload rejected")
	}
}
