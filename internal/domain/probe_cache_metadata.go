package domain

import (
	"encoding/json"
	"math"
	"math/big"
	"regexp"
	"strings"
)

// MarshalProbeMetadata validates the normalized whitelist before serializing.
// It never accepts raw ffprobe JSON, arbitrary tags, paths, or diagnostic text.
// The typed cardinality/string bounds also bound the allocation before Marshal.
func MarshalProbeMetadata(v MediaMetadata) ([]byte, error) {
	if len(v.Streams) < 1 || len(v.Streams) > 64 || len(v.Chapters) > 256 || len(v.Format.Names) > 16 ||
		!metadataInt(v.Format.DurationMicros, 0, math.MaxInt64) || !metadataInt(v.Format.SizeBytes, 0, math.MaxInt64) || !metadataInt(v.Format.BitRate, 1, math.MaxInt64) {
		return nil, ErrInvalid
	}
	names := make(map[string]bool, len(v.Format.Names))
	for _, name := range v.Format.Names {
		if !metadataFormats[name] || names[name] {
			return nil, ErrInvalid
		}
		names[name] = true
	}
	indices := make(map[int]bool, len(v.Streams))
	for _, s := range v.Streams {
		if !validMetadataStream(s) || indices[s.Index] {
			return nil, ErrInvalid
		}
		indices[s.Index] = true
	}
	chapters := make(map[int64]bool, len(v.Chapters))
	for _, c := range v.Chapters {
		if c.ID < 0 || chapters[c.ID] || !metadataInt(c.StartMicros, 0, math.MaxInt64) || !metadataInt(c.EndMicros, 0, math.MaxInt64) ||
			c.StartMicros != nil && c.EndMicros != nil && *c.StartMicros > *c.EndMicros ||
			v.Format.DurationMicros != nil && (c.StartMicros != nil && *c.StartMicros > *v.Format.DurationMicros || c.EndMicros != nil && *c.EndMicros > *v.Format.DurationMicros) {
			return nil, ErrInvalid
		}
		chapters[c.ID] = true
	}
	if !validMatroska(v.Matroska, v.Format.DurationMicros, indices) {
		return nil, ErrInvalid
	}
	encoded, err := json.Marshal(v)
	if err != nil || len(encoded) > ProbeMetadataMaxBytes {
		return nil, ErrInvalid
	}
	return encoded, nil
}

func metadataInt(v *int64, min, max int64) bool { return v == nil || *v >= min && *v <= max }
func metadataEnum(v *string, allowed map[string]bool) bool {
	return v == nil || len(*v) <= 128 && allowed[*v]
}
func metadataRational(v *MediaRational, positive bool, max int64) bool {
	if v == nil {
		return true
	}
	if v.Numerator < 0 || v.Denominator < 1 || positive && v.Numerator == 0 {
		return false
	}
	if new(big.Int).SetInt64(v.Numerator).Cmp(new(big.Int).Mul(big.NewInt(max), big.NewInt(v.Denominator))) > 0 {
		return false
	}
	// The parser reduces fractions; reject alternative encodings at this boundary.
	a, b := v.Numerator, v.Denominator
	for b != 0 {
		a, b = b, a%b
	}
	return a == 1
}
func metadataRationalLE(a, b *MediaRational) bool {
	return a == nil || b == nil || new(big.Int).Mul(big.NewInt(a.Numerator), big.NewInt(b.Denominator)).Cmp(new(big.Int).Mul(big.NewInt(b.Numerator), big.NewInt(a.Denominator))) <= 0
}
func validMetadataStream(s MediaStream) bool {
	if s.Index < 0 || s.Index > math.MaxInt32 || !metadataEnum(s.Codec, metadataCodecs) || !metadataEnum(s.Profile, metadataProfiles) ||
		!metadataInt(s.DurationMicros, 0, math.MaxInt64) || !metadataInt(s.BitRate, 1, math.MaxInt64) || s.Language != nil && (len(*s.Language) > 35 || !metadataLanguage.MatchString(*s.Language)) {
		return false
	}
	switch s.Kind {
	case "video":
		return s.Video != nil && s.Audio == nil && validMetadataVideo(*s.Video)
	case "audio":
		return s.Audio != nil && s.Video == nil && validMetadataAudio(s)
	case "subtitle", "data", "attachment":
		return s.Video == nil && s.Audio == nil
	default:
		return false
	}
}
func validMetadataAudio(s MediaStream) bool {
	a := s.Audio
	if !metadataInt(a.Channels, 1, 256) || !metadataInt(a.SampleRate, 1, 768000) || !metadataInt(a.BitsPerSample, 1, 64) || !metadataInt(a.BitsPerRawSample, 1, 64) || !metadataEnum(a.ChannelLayout, metadataLayouts) {
		return false
	}
	if a.Atmos != nil {
		if !*a.Atmos || s.Codec == nil || s.Profile == nil {
			return false
		}
		return *s.Codec == "eac3" && *s.Profile == "Dolby Digital Plus + Dolby Atmos" || *s.Codec == "truehd" && *s.Profile == "Dolby TrueHD + Dolby Atmos"
	}
	return true
}
func validMetadataVideo(v MediaVideo) bool {
	if !metadataInt(v.Level, 0, 65535) || !metadataInt(v.Width, 1, 65535) || !metadataInt(v.Height, 1, 65535) ||
		!metadataRational(v.FrameRate, true, 1000000) || !metadataRational(v.AverageFrameRate, true, 1000000) ||
		!metadataEnum(v.ColorRange, metadataColorRange) || !metadataEnum(v.ColorSpace, metadataColorSpace) ||
		!metadataEnum(v.ColorTransfer, metadataColorTransfer) || !metadataEnum(v.ColorPrimaries, metadataColorPrimaries) || v.HDR10Plus != nil && !*v.HDR10Plus {
		return false
	}
	if d := v.MasteringDisplay; d != nil {
		for _, r := range []*MediaRational{d.RedX, d.RedY, d.GreenX, d.GreenY, d.BlueX, d.BlueY, d.WhiteX, d.WhiteY} {
			if !metadataRational(r, false, 1) {
				return false
			}
		}
		if !metadataRational(d.MinLuminance, false, 1000000) || !metadataRational(d.MaxLuminance, false, 1000000) || !metadataRationalLE(d.MinLuminance, d.MaxLuminance) {
			return false
		}
	}
	if c := v.ContentLight; c != nil {
		if !metadataInt(c.MaxContent, 0, 65535) || !metadataInt(c.MaxAverage, 0, 65535) || c.MaxContent != nil && c.MaxAverage != nil && *c.MaxAverage > *c.MaxContent {
			return false
		}
	}
	if d := v.DolbyVision; d != nil {
		if !metadataInt(d.Profile, 0, 255) || !metadataInt(d.Level, 0, 255) || !metadataInt(d.CompatibilityID, 0, 15) {
			return false
		}
	}
	return true
}

func metadataWords(s string) map[string]bool {
	result := make(map[string]bool)
	for _, v := range strings.Split(s, "|") {
		result[v] = true
	}
	return result
}

var metadataLanguage = regexp.MustCompile(`^[a-z]{2,3}(?:-[a-z0-9]{2,8}){0,3}$`)
var metadataCodecs = metadataWords("h264|hevc|av1|vp9|vp8|mpeg4|mpeg2video|mpeg1video|mjpeg|jpeg2000|prores|dnxhd|vc1|wmv3|theora|ffv1|huffyuv|rawvideo|png|apng|gif|webp|bmp|tiff|aac|ac3|eac3|truehd|dts|mp3|mp2|mp1|flac|alac|opus|vorbis|wmav1|wmav2|wmapro|pcm_s16le|pcm_s16be|pcm_s24le|pcm_s24be|pcm_s32le|pcm_s32be|pcm_f32le|pcm_f64le|pcm_u8|subrip|ass|ssa|webvtt|mov_text|hdmv_pgs_subtitle|dvd_subtitle|dvb_subtitle|text|srt|eia_608")
var metadataProfiles = metadataWords("Baseline|Constrained Baseline|Main|High|High 10|High 4:2:2|High 4:4:4 Predictive|Main 10|Main Still Picture|Rext|Main 12|Main 4:2:2 10|Main 4:2:2 12|Main 4:4:4|Main 4:4:4 10|Main 4:4:4 12|Profile 0|Profile 1|Profile 2|Profile 3|Professional|Simple Profile|Advanced Simple Profile|LC|HE-AAC|HE-AACv2|Main|SSR|LTP|ELD|LD|DTS|DTS-ES|DTS 96/24|DTS-HD HRA|DTS-HD MA|DTS Express|Dolby Digital Plus|Dolby Digital Plus + Dolby Atmos|Dolby TrueHD|Dolby TrueHD + Dolby Atmos")
var metadataFormats = metadataWords("mov|mp4|m4a|3gp|3g2|mj2|matroska|webm|avi|mpegts|mpeg|mpegvideo|flv|ogg|wav|flac|mp3|aac|ac3|eac3|dts|truehd|image2|png_pipe|jpeg_pipe|gif|apng|h264|hevc|av1|ivf")
var metadataLayouts = metadataWords("mono|stereo|2.1|3.0|3.0(back)|4.0|quad|quad(side)|3.1|4.1|5.0|5.0(side)|5.1|5.1(side)|6.0|6.0(front)|hexagonal|6.1|6.1(back)|6.1(front)|7.0|7.0(front)|7.1|7.1(wide)|7.1(wide-side)|7.1(top)|7.1.2|7.1.4|5.1.2|5.1.4|9.1.4|9.1.6|octagonal|hexadecagonal|downmix|22.2")
var metadataColorRange = metadataWords("tv|pc")
var metadataColorSpace = metadataWords("gbr|bt709|fcc|bt470bg|smpte170m|smpte240m|ycgco|bt2020nc|bt2020c|smpte2085|chroma-derived-nc|chroma-derived-c|ictcp|ipt-c2")
var metadataColorTransfer = metadataWords("bt709|gamma22|gamma28|smpte170m|smpte240m|linear|log|log_sqrt|iec61966-2-4|bt1361e|iec61966-2-1|bt2020-10|bt2020-12|smpte2084|smpte428|arib-std-b67")
var metadataColorPrimaries = metadataWords("bt709|bt470m|bt470bg|smpte170m|smpte240m|film|bt2020|smpte428|smpte431|smpte432|jedec-p22|ebu3213")
