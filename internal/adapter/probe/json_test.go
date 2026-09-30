package probe

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

const minimalMetadata = `{"streams":[{"index":0,"codec_type":"video"}]}`

func metadataDocument(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func metadataError(t *testing.T, data []byte, want error) {
	t.Helper()
	got, err := ParseJSON(data)
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	if !reflect.DeepEqual(got, domain.MediaMetadata{}) {
		t.Fatal("failure returned partial metadata")
	}
	if err.Error() != want.Error() {
		t.Fatal("error contained extra diagnostic data")
	}
}

func TestParseJSONGoldenPrivacy(t *testing.T) {
	// Field types follow the ffprobe JSON writer. This synthetic sample tests
	// normalization; it is not a claim about a real HDR or Atmos media fixture.
	raw := []byte(`{
  "format":{"filename":"/private/source/movie.mkv","format_name":"matroska,webm,matroska,private-container","duration":"2.125000","size":"1024","bit_rate":"8192","tags":{"title":"PRIVATE_TITLE","url":"https://private.invalid/secret"}},
  "streams":[
    {"index":0,"codec_type":"video","codec_name":"h264","profile":"High","level":40,"width":1920,"height":1080,"r_frame_rate":"60000/2002","avg_frame_rate":"30000/1001","duration":"2.125000","bit_rate":"4096","color_range":"tv","color_space":"bt709","color_transfer":"bt709","color_primaries":"bt709","disposition":{"default":1,"forced":0},"tags":{"language":"EN-us","title":"PRIVATE_TRACK","vendor":"PRIVATE_VENDOR"}},
    {"index":1,"codec_type":"audio","codec_name":"aac","profile":"LC","channels":2,"channel_layout":"stereo","sample_rate":"48000","bits_per_sample":0,"bits_per_raw_sample":"16","r_frame_rate":"0/0"},
    {"index":2,"codec_type":"subtitle","codec_name":"subrip","tags":{"language":"zho"},"disposition":{"default":0,"forced":1}},
    {"index":3,"codec_type":"data","codec_name":"vendor_codec"},
    {"index":4,"codec_type":"attachment","tags":{"filename":"PRIVATE_FONT","mimetype":"PRIVATE_MIME"}}
  ],
  "chapters":[{"id":1,"time_base":"1/1000","start":0,"start_time":"0.000000","end":2000,"end_time":"2.000000","tags":{"title":"PRIVATE_CHAPTER"}}],
  "unknown":{"nested":[true,null,{"untrusted":"PRIVATE_UNKNOWN"}]}
}`)
	got, err := ParseJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	i := func(v int64) *int64 { return &v }
	s := func(v string) *string { return &v }
	b := func(v bool) *bool { return &v }
	want := domain.MediaMetadata{
		Format: domain.MediaFormat{Names: []string{"matroska", "webm"}, DurationMicros: i(2125000), SizeBytes: i(1024), BitRate: i(8192)},
		Streams: []domain.MediaStream{
			{Index: 0, Kind: "video", Codec: s("h264"), Profile: s("High"), DurationMicros: i(2125000), BitRate: i(4096), Language: s("en-us"), Default: b(true), Forced: b(false), Video: &domain.MediaVideo{Level: i(40), Width: i(1920), Height: i(1080), FrameRate: &domain.MediaRational{Numerator: 30000, Denominator: 1001}, AverageFrameRate: &domain.MediaRational{Numerator: 30000, Denominator: 1001}, ColorRange: s("tv"), ColorSpace: s("bt709"), ColorTransfer: s("bt709"), ColorPrimaries: s("bt709")}},
			{Index: 1, Kind: "audio", Codec: s("aac"), Profile: s("LC"), Audio: &domain.MediaAudio{Channels: i(2), ChannelLayout: s("stereo"), SampleRate: i(48000), BitsPerRawSample: i(16)}},
			{Index: 2, Kind: "subtitle", Codec: s("subrip"), Language: s("zho"), Default: b(false), Forced: b(true)},
			{Index: 3, Kind: "data"}, {Index: 4, Kind: "attachment"},
		},
		Chapters: []domain.MediaChapter{{ID: 1, StartMicros: i(0), EndMicros: i(2000000)}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalized result differs\ngot: %#v", got)
	}
	encoded := string(metadataDocument(t, got))
	for _, private := range []string{"PRIVATE", "/private", "private.invalid", `"filename"`, `"title"`, "vendor_codec", "private-container"} {
		if strings.Contains(encoded, private) {
			t.Fatalf("privacy projection retained %q", private)
		}
	}
}

func TestParseJSONExplicitHDRAndAtmos(t *testing.T) {
	raw := []byte(`{"streams":[{"index":0,"codec_type":"video","codec_name":"hevc","profile":"Main 10","color_transfer":"smpte2084","side_data_list":[
 {"side_data_type":"Mastering display metadata","red_x":"34000/50000","red_y":"16000/50000","green_x":"13250/50000","green_y":"34500/50000","blue_x":"7500/50000","blue_y":"3000/50000","white_point_x":"15635/50000","white_point_y":"16450/50000","min_luminance":"1/10000","max_luminance":"10000000/10000"},
 {"side_data_type":"Content light level metadata","max_content":1000,"max_average":400},
 {"side_data_type":"DOVI configuration record","dv_profile":8,"dv_level":6,"rpu_present_flag":1,"el_present_flag":0,"bl_present_flag":1,"dv_bl_signal_compatibility_id":1},
 {"side_data_type":"HDR Dynamic Metadata SMPTE2094-40 (HDR10+)"}]},
 {"index":1,"codec_type":"audio","codec_name":"eac3","profile":"Dolby Digital Plus + Dolby Atmos"},
 {"index":2,"codec_type":"audio","codec_name":"truehd","profile":"Dolby TrueHD + Dolby Atmos"},
 {"index":3,"codec_type":"audio","codec_name":"aac","profile":"Dolby TrueHD + Dolby Atmos"}]}`)
	got, err := ParseJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	v := got.Streams[0].Video
	if v.MasteringDisplay == nil || *v.MasteringDisplay.RedX != (domain.MediaRational{Numerator: 17, Denominator: 25}) || *v.MasteringDisplay.MaxLuminance != (domain.MediaRational{Numerator: 1000, Denominator: 1}) {
		t.Fatal("mastering rational normalization")
	}
	if v.ContentLight == nil || *v.ContentLight.MaxContent != 1000 || *v.ContentLight.MaxAverage != 400 {
		t.Fatal("content light observation")
	}
	if v.DolbyVision == nil || *v.DolbyVision.Profile != 8 || !*v.DolbyVision.RPU || *v.DolbyVision.EnhancementLayer || !*v.DolbyVision.BaseLayer || *v.DolbyVision.CompatibilityID != 1 {
		t.Fatal("DOVI observation")
	}
	if v.HDR10Plus == nil || !*v.HDR10Plus || got.Streams[1].Audio.Atmos == nil || !*got.Streams[1].Audio.Atmos || got.Streams[2].Audio.Atmos == nil || !*got.Streams[2].Audio.Atmos || got.Streams[3].Audio.Atmos != nil {
		t.Fatal("feature inference was not restricted to explicit observations")
	}
	got, err = ParseJSON([]byte(`{"format":{"filename":"HDR10_DolbyVision_Atmos.mkv"},"streams":[{"index":0,"codec_type":"video","codec_name":"hevc","color_transfer":"smpte2084","tags":{"title":"Dolby Vision HDR10+"}},{"index":1,"codec_type":"audio","codec_name":"eac3","tags":{"title":"Atmos"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	v = got.Streams[0].Video
	if v.MasteringDisplay != nil || v.ContentLight != nil || v.DolbyVision != nil || v.HDR10Plus != nil || got.Streams[1].Audio.Atmos != nil {
		t.Fatal("inferred special metadata from names or a transfer characteristic")
	}
}

func TestParseJSONUnknownIsAbsent(t *testing.T) {
	got, err := ParseJSON([]byte(`{"format":{"format_name":"unknown_vendor","duration":"N/A","size":null,"bit_rate":"0"},"streams":[{"index":0,"codec_type":"video","codec_name":"unknown_vendor","profile":"unknown","width":0,"height":0,"level":-99,"r_frame_rate":"0/0","avg_frame_rate":"0/1","bit_rate":"N/A","color_transfer":"unspecified","tags":{"language":"private/path"},"side_data_list":[{"side_data_type":"unknown vendor side data","url":"https://private.invalid"}]},{"index":1,"codec_type":"audio","channels":0,"sample_rate":"0","bits_per_sample":0,"bits_per_raw_sample":"N/A","channel_layout":"unknown"}],"chapters":[{"id":0,"start_time":"N/A","end_time":null}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Format, domain.MediaFormat{}) || !reflect.DeepEqual(*got.Streams[0].Video, domain.MediaVideo{}) || !reflect.DeepEqual(*got.Streams[1].Audio, domain.MediaAudio{}) || got.Streams[0].Codec != nil || got.Streams[0].Profile != nil || got.Streams[0].Language != nil || got.Chapters[0].StartMicros != nil || got.Chapters[0].EndMicros != nil {
		t.Fatal("unknown input became a concrete value")
	}
}

func TestParseJSONCanonicalGBRColorSpace(t *testing.T) {
	// FFmpeg av_color_space_name(AVCOL_SPC_RGB) emits "gbr", not "rgb".
	got, err := ParseJSON([]byte(`{"streams":[{"index":0,"codec_type":"video","color_space":"gbr"},{"index":1,"codec_type":"video","color_space":"rgb"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.Streams[0].Video.ColorSpace == nil || *got.Streams[0].Video.ColorSpace != "gbr" || got.Streams[1].Video.ColorSpace != nil {
		t.Fatal("canonical FFmpeg color-space name was not preserved")
	}
}

func TestParseJSONRejectsInvalidAndUnsafeNumbers(t *testing.T) {
	cases := map[string]string{
		"empty": "", "root_array": "[]", "no_streams": "{}", "empty_streams": `{"streams":[]}`, "null_streams": `{"streams":null}`, "format_only": `{"format":{"format_name":"matroska"}}`,
		"trailing": minimalMetadata + ` {}`, "syntax": `{"streams":[}`, "duplicate_top": `{"streams":[],"streams":[]}`, "duplicate_escaped": `{"streams":[{"index":0,"\u0069ndex":1,"codec_type":"video"}]}`, "duplicate_unknown": `{"streams":[{"index":0,"codec_type":"video"}],"unknown":{"x":1,"x":2}}`,
		"duplicate_index": `{"streams":[{"index":0,"codec_type":"video"},{"index":0,"codec_type":"audio"}]}`,
		"bad_stream":      `{"streams":[null]}`, "bad_kind": `{"streams":[{"index":0,"codec_type":"unknown"}]}`, "missing_index": `{"streams":[{"codec_type":"video"}]}`, "missing_kind": `{"streams":[{"index":0}]}`,
		"numeric_overflow": `{"streams":[{"index":0,"codec_type":"video"}],"unknown":1e999}`,
		"nan_literal":      `{"streams":[{"index":0,"codec_type":"video"}],"unknown":NaN}`,
	}
	for name, extra := range map[string]string{
		"index_string": `"index":"0","codec_type":"video"`, "index_float": `"index":0.0,"codec_type":"video"`, "index_exp": `"index":1e0,"codec_type":"video"`, "index_huge": `"index":2147483648,"codec_type":"video"`,
		"width_type": `"width":"1920"`, "width_negative": `"width":-1`, "width_huge": `"width":65536`, "height_type": `"height":[]`, "bad_level": `"level":-1`, "codec_type": `"codec_name":true`, "profile_type": `"profile":{}`, "codec_long": `"codec_name":"` + strings.Repeat("x", 129) + `"`,
		"fps_zero_denominator": `"r_frame_rate":"1/0"`, "fps_negative": `"r_frame_rate":"-1/1"`, "fps_huge": `"r_frame_rate":"1000001/1"`, "fps_int_overflow": `"r_frame_rate":"9223372036854775808/1"`, "fps_missing_num": `"r_frame_rate":"N/A/1"`, "fps_missing_den": `"r_frame_rate":"1/unknown"`, "fps_bad_shape": `"r_frame_rate":"1/2/3"`, "fps_map": `"r_frame_rate":{}`, "fps_array": `"r_frame_rate":[]`,
		"duration_number": `"duration":1`, "duration_nonfinite": `"duration":"NaN"`, "duration_negative": `"duration":"-0.1"`, "duration_exp": `"duration":"1e3"`, "duration_precision": `"duration":"0.0000001"`, "duration_overflow": `"duration":"9223372036854.775808"`, "duration_whole_overflow": `"duration":"999999999999999999999"`,
		"bitrate_number": `"bit_rate":1`, "bitrate_overflow": `"bit_rate":"9223372036854775808"`, "bitrate_plus": `"bit_rate":"+1"`, "bitrate_decimal": `"bit_rate":"1.0"`,
		"disposition_type": `"disposition":[]`, "default_bool": `"disposition":{"default":true}`, "forced_two": `"disposition":{"forced":2}`, "tags_type": `"tags":"text"`, "language_type": `"tags":{"language":12}`, "side_type": `"side_data_list":{}`, "side_element": `"side_data_list":[false]`,
		"duplicate_side": `"side_data_list":[{"side_data_type":"Content light level metadata"},{"side_data_type":"Content light level metadata"}]`, "light_order": `"side_data_list":[{"side_data_type":"Content light level metadata","max_content":10,"max_average":20}]`,
		"chromaticity_range": `"side_data_list":[{"side_data_type":"Mastering display metadata","red_x":"2/1"}]`, "luminance_order": `"side_data_list":[{"side_data_type":"Mastering display metadata","min_luminance":"2/1","max_luminance":"1/1"}]`, "dovi_flag": `"side_data_list":[{"side_data_type":"DOVI configuration record","rpu_present_flag":2}]`,
	} {
		if strings.HasPrefix(name, "index_") {
			cases[name] = `{"streams":[{` + extra + `}]}`
		} else {
			cases[name] = `{"streams":[{"index":0,"codec_type":"video",` + extra + `}]}`
		}
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) { metadataError(t, []byte(raw), ErrMetadataInvalid) })
	}
	metadataError(t, append([]byte(minimalMetadata), 0xff), ErrMetadataInvalid)
	for name, field := range map[string]string{"channels": `"channels":257`, "sample_rate": `"sample_rate":"768001"`, "bits": `"bits_per_sample":65`, "bits_string": `"bits_per_raw_sample":16`, "layout": `"channel_layout":[]`} {
		t.Run("audio_"+name, func(t *testing.T) {
			metadataError(t, []byte(`{"streams":[{"index":0,"codec_type":"audio",`+field+`}]}`), ErrMetadataInvalid)
		})
	}
}

func TestParseJSONChapterRangesAndOverflow(t *testing.T) {
	got, err := ParseJSON([]byte(`{"format":{"duration":"9223372036854.775807","size":"9223372036854775807"},"streams":[{"index":0,"codec_type":"video"}],"chapters":[{"id":0,"time_base":"1/3","start":1,"end":2,"start_time":"0.333333","end_time":"0.666667"},{"id":1,"time_base":"1/2000000","start":1,"end":1}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if *got.Format.DurationMicros != math.MaxInt64 || *got.Format.SizeBytes != math.MaxInt64 || *got.Chapters[0].StartMicros != 333333 || *got.Chapters[0].EndMicros != 666667 || *got.Chapters[1].StartMicros != 1 {
		t.Fatal("exact integer/rounded tick conversion")
	}
	cases := map[string]string{
		"no_id": `{}`, "negative_id": `{"id":-1}`, "reverse": `{"id":0,"start_time":"2","end_time":"1"}`, "beyond_duration": `{"id":0,"end_time":"3"}`, "inconsistent_ticks": `{"id":0,"time_base":"1/1","start":1,"start_time":"0"}`, "bad_base": `{"id":0,"time_base":"0/1"}`, "zero_den": `{"id":0,"time_base":"1/0"}`, "tick_overflow": `{"id":0,"time_base":"1/1","start":9223372036854775807}`, "negative_ticks": `{"id":0,"start":-1}`, "duplicate_id": `{"id":0},{"id":0}`,
	}
	for name, chapters := range cases {
		t.Run(name, func(t *testing.T) {
			metadataError(t, []byte(`{"streams":[{"index":0,"codec_type":"video"}],"format":{"duration":"2"},"chapters":[`+chapters+`]}`), ErrMetadataInvalid)
		})
	}
}

func TestParseJSONResourceLimits(t *testing.T) {
	minimal := map[string]any{"streams": []any{map[string]any{"index": 0, "codec_type": "video"}}}
	streams := make([]any, MaxStreams)
	for i := range streams {
		streams[i] = map[string]any{"index": i, "codec_type": "data"}
	}
	chapters := make([]any, MaxChapters)
	for i := range chapters {
		chapters[i] = map[string]any{"id": i}
	}
	if _, err := ParseJSON(metadataDocument(t, map[string]any{"streams": streams, "chapters": chapters})); err != nil {
		t.Fatalf("inclusive stream/chapter limits: %v", err)
	}
	metadataError(t, metadataDocument(t, map[string]any{"streams": append(streams, map[string]any{"index": 64, "codec_type": "data"})}), ErrMetadataLimit)
	metadataError(t, metadataDocument(t, map[string]any{"streams": minimal["streams"], "chapters": append(chapters, map[string]any{"id": 256})}), ErrMetadataLimit)
	metadataError(t, []byte(strings.Repeat(" ", MaxJSONBytes+1)), ErrMetadataLimit)
	metadataError(t, metadataDocument(t, map[string]any{"streams": minimal["streams"], "unknown": strings.Repeat("a", MaxJSONStringBytes+1)}), ErrMetadataLimit)
	metadataError(t, metadataDocument(t, map[string]any{"streams": minimal["streams"], strings.Repeat("a", MaxJSONStringBytes+1): 0}), ErrMetadataLimit)
	metadataError(t, []byte(`{"streams":[{"index":0,"codec_type":"video"}],"x":`+strings.Repeat("[", MaxJSONDepth+1)+`0`+strings.Repeat("]", MaxJSONDepth+1)+`}`), ErrMetadataLimit)
	object := make(map[string]any)
	for i := 0; i <= MaxJSONFields; i++ {
		object[fmt.Sprintf("key%d", i)] = 0
	}
	metadataError(t, metadataDocument(t, map[string]any{"streams": minimal["streams"], "x": object}), ErrMetadataLimit)
	metadataError(t, metadataDocument(t, map[string]any{"streams": minimal["streams"], "x": make([]any, MaxJSONFields+1)}), ErrMetadataLimit)
	values := make([]any, 33)
	for i := range values {
		values[i] = make([]any, MaxJSONFields)
	}
	metadataError(t, metadataDocument(t, map[string]any{"streams": minimal["streams"], "x": values}), ErrMetadataLimit)
	sides := make([]any, MaxSideData+1)
	for i := range sides {
		sides[i] = map[string]any{"side_data_type": "unrecognized"}
	}
	metadataError(t, metadataDocument(t, map[string]any{"streams": []any{map[string]any{"index": 0, "codec_type": "video", "side_data_list": sides}}}), ErrMetadataLimit)
	metadataError(t, metadataDocument(t, map[string]any{"streams": minimal["streams"], "format": map[string]any{"format_name": strings.Repeat("x", 1025)}}), ErrMetadataLimit)
	metadataError(t, metadataDocument(t, map[string]any{"streams": minimal["streams"], "format": map[string]any{"format_name": strings.Repeat("mov,", 16) + "mov"}}), ErrMetadataLimit)
	// Byte limit is inclusive; whitespace still counts toward the byte budget.
	data := []byte(minimalMetadata + strings.Repeat(" ", MaxJSONBytes-len(minimalMetadata)))
	if _, err := ParseJSON(data); err != nil {
		t.Fatalf("inclusive byte limit: %v", err)
	}
}

func FuzzParseJSON(f *testing.F) {
	for _, seed := range []string{minimalMetadata, `{}`, `{"streams":[{"index":0,"codec_type":"video","r_frame_rate":{}}]}`, `{"streams":[{"index":0,"codec_type":"audio","sample_rate":"48000"}]}`, `{"streams":[{"index":0,"codec_type":"video","r_frame_rate":"0/0"}]}`} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		got, err := ParseJSON(data)
		if err != nil {
			if err != ErrMetadataInvalid && err != ErrMetadataLimit {
				t.Fatalf("unsafe error: %v", err)
			}
			if !reflect.DeepEqual(got, domain.MediaMetadata{}) {
				t.Fatal("partial result")
			}
			return
		}
		if len(got.Streams) < 1 || len(got.Streams) > MaxStreams || len(got.Chapters) > MaxChapters {
			t.Fatal("result outside bounds")
		}
		if _, err := json.Marshal(got); err != nil {
			t.Fatalf("unserializable metadata: %v", err)
		}
	})
}
