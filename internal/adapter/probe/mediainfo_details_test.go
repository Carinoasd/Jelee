package probe

import (
	"reflect"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func i64(v int64) *int64 { return &v }

func TestMediaInfoStreamDetailsClassifyHDRAndAudio(t *testing.T) {
	type want struct {
		hdr      []string
		dvp, dvl *int64
		audio    []string
		profile  string
	}
	cases := []struct {
		name  string
		track string
		want  want
	}{
		{"hdr10", `{"@type":"Video","StreamOrder":"0","Format_Profile":"Main 10","HDR_Format":"SMPTE ST 2086","HDR_Format_Compatibility":"HDR10"}`, want{hdr: []string{"hdr10"}, profile: "Main 10"}},
		{"hdr10plus", `{"@type":"Video","StreamOrder":"0","HDR_Format":"SMPTE ST 2094 App 4","HDR_Format_Compatibility":"HDR10+ Profile B"}`, want{hdr: []string{"hdr10plus"}}},
		{"hlg by transfer", `{"@type":"Video","StreamOrder":"0","transfer_characteristics":"HLG"}`, want{hdr: []string{"hlg"}}},
		{"dolby vision 8.1", `{"@type":"Video","StreamOrder":"0","HDR_Format":"Dolby Vision / SMPTE ST 2086","HDR_Format_Profile":"dvhe.08.06","HDR_Format_Compatibility":"Blu-ray / HDR10"}`, want{hdr: []string{"dolby_vision", "hdr10"}, dvp: i64(8), dvl: i64(6)}},
		{"dolby vision 5 split level", `{"@type":"Video","StreamOrder":"0","HDR_Format":"Dolby Vision","HDR_Format_Profile":"dvhe.05","HDR_Format_Level":"09"}`, want{hdr: []string{"dolby_vision"}, dvp: i64(5), dvl: i64(9)}},
		{"sdr", `{"@type":"Video","StreamOrder":"0","transfer_characteristics":"BT.709"}`, want{}},
		{"atmos truehd", `{"@type":"Audio","StreamOrder":"1","Format":"MLP FBA","Format_Commercial_IfAny":"Dolby TrueHD with Dolby Atmos","Format_AdditionalFeatures":"16-ch"}`, want{audio: []string{"dolby_atmos", "dolby_truehd"}}},
		{"eac3 joc", `{"@type":"Audio","StreamOrder":"1","Format":"E-AC-3","Format_AdditionalFeatures":"JOC"}`, want{audio: []string{"dolby_atmos"}}},
		{"plain eac3", `{"@type":"Audio","StreamOrder":"1","Format":"E-AC-3"}`, want{}},
		{"dts-hd ma", `{"@type":"Audio","StreamOrder":"1","Format":"DTS","Format_Commercial_IfAny":"DTS-HD Master Audio","Format_AdditionalFeatures":"XLL"}`, want{audio: []string{"dts_hd_ma"}}},
		{"dts:x", `{"@type":"Audio","StreamOrder":"1","Format":"DTS","Format_Commercial_IfAny":"DTS-HD Master Audio","Format_AdditionalFeatures":"XLL X"}`, want{audio: []string{"dts_x", "dts_hd_ma"}}},
		{"lossy dts", `{"@type":"Audio","StreamOrder":"1","Format":"DTS","Format_Profile":"Core"}`, want{profile: "Core"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc := `{"media":{"track":[{"@type":"General","Title":"T"},` + c.track + `]}}`
			got := ParseMediaInfo([]byte(doc), map[int]bool{0: true, 1: true}, nil)
			if got == nil {
				t.Fatal("dropped")
			}
			if c.want.hdr == nil && c.want.audio == nil && c.want.profile == "" {
				if len(got.StreamDetails) != 0 {
					t.Fatalf("details %+v", got.StreamDetails)
				}
				return
			}
			if len(got.StreamDetails) != 1 {
				t.Fatalf("details %+v", got.StreamDetails)
			}
			d := got.StreamDetails[0]
			if !reflect.DeepEqual(d.HDR, c.want.hdr) || !reflect.DeepEqual(d.AudioFeatures, c.want.audio) || d.FormatProfile != c.want.profile ||
				!reflect.DeepEqual(d.DolbyVisionProfile, c.want.dvp) || !reflect.DeepEqual(d.DolbyVisionLevel, c.want.dvl) {
				t.Fatalf("detail %+v", d)
			}
		})
	}
}

func TestMediaInfoStreamDetailsIgnoreUnknownStreamsAndBadProfiles(t *testing.T) {
	doc := `{"media":{"track":[{"@type":"Video","StreamOrder":"7","HDR_Format":"SMPTE ST 2086","HDR_Format_Compatibility":"HDR10"},
{"@type":"Video","StreamOrder":"0","Format_Profile":"` + strings.Repeat("x", 65) + `"}]}}`
	if got := ParseMediaInfo([]byte(doc), map[int]bool{0: true}, nil); got != nil {
		t.Fatalf("unprobed stream or oversized profile kept: %+v", got)
	}
}

func TestStreamDetailValidation(t *testing.T) {
	streams := map[int]bool{0: true}
	for name, d := range map[string]domain.MediaStreamDetail{
		"unknown hdr":        {HDR: []string{"sdr"}},
		"duplicate hdr":      {HDR: []string{"hdr10", "hdr10"}},
		"dv without token":   {HDR: []string{"hdr10"}, DolbyVisionProfile: i64(8)},
		"unknown feature":    {AudioFeatures: []string{"dts_es"}},
		"empty":              {},
		"dv profile range":   {HDR: []string{"dolby_vision"}, DolbyVisionProfile: i64(300)},
		"control in profile": {FormatProfile: "a\x00b"},
	} {
		if domain.ValidMediaMatroska(&domain.MediaMatroska{StreamDetails: []domain.MediaStreamDetail{d}}, nil, streams) {
			t.Errorf("%s accepted", name)
		}
	}
}
