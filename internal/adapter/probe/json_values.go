package probe

import (
	"encoding/json"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type metadataParser struct{ err error }

func (p *metadataParser) invalid() {
	if p.err == nil {
		p.err = ErrMetadataInvalid
	}
}
func (p *metadataParser) limit() {
	if p.err == nil {
		p.err = ErrMetadataLimit
	}
}
func absent(value any) bool {
	if value == nil {
		return true
	}
	if value, ok := value.(string); ok {
		switch strings.ToLower(value) {
		case "", "n/a", "unknown", "unspecified":
			return true
		}
	}
	return false
}
func (p *metadataParser) object(value any) object {
	if value == nil {
		return nil
	}
	result, ok := value.(object)
	if !ok {
		p.invalid()
	}
	return result
}
func (p *metadataParser) array(value any, max int) []any {
	if value == nil {
		return nil
	}
	result, ok := value.([]any)
	if !ok {
		p.invalid()
		return nil
	}
	if len(result) > max {
		p.limit()
		return nil
	}
	return result
}
func (p *metadataParser) text(value any) *string {
	if absent(value) {
		return nil
	}
	result, ok := value.(string)
	if !ok {
		p.invalid()
		return nil
	}
	return &result
}
func (p *metadataParser) integer(value any, quoted bool, min, max int64) *int64 {
	if absent(value) {
		return nil
	}
	var raw string
	if quoted {
		s, ok := value.(string)
		if !ok {
			p.invalid()
			return nil
		}
		raw = s
	} else {
		n, ok := value.(json.Number)
		if !ok {
			p.invalid()
			return nil
		}
		raw = string(n)
	}
	if len(raw) > 20 || strings.ContainsAny(raw, ".eE+") {
		p.invalid()
		return nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < min || n > max {
		p.invalid()
		return nil
	}
	return &n
}
func (p *metadataParser) positive(value any, quoted bool, max int64) *int64 {
	v := p.integer(value, quoted, 0, max)
	if v != nil && *v == 0 {
		return nil
	}
	return v
}
func (p *metadataParser) flag(value any) *bool {
	v := p.integer(value, false, 0, 1)
	if v == nil {
		return nil
	}
	result := *v == 1
	return &result
}

var decimalSeconds = regexp.MustCompile(`^[0-9]+(?:\.[0-9]{1,6})?$`)

func (p *metadataParser) micros(value any) *int64 {
	s := p.text(value)
	if s == nil {
		return nil
	}
	if len(*s) > 26 || !decimalSeconds.MatchString(*s) {
		p.invalid()
		return nil
	}
	parts := strings.SplitN(*s, ".", 2)
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || whole > math.MaxInt64/1000000 {
		p.invalid()
		return nil
	}
	var fraction int64
	if len(parts) == 2 {
		fraction, _ = strconv.ParseInt(parts[1]+strings.Repeat("0", 6-len(parts[1])), 10, 64)
	}
	if whole*1000000 > math.MaxInt64-fraction {
		p.invalid()
		return nil
	}
	result := whole*1000000 + fraction
	return &result
}
func (p *metadataParser) rational(value any, maxValue int64) *domain.MediaRational {
	s := p.text(value)
	if s == nil {
		return nil
	}
	parts := strings.Split(*s, "/")
	if len(parts) != 2 {
		p.invalid()
		return nil
	}
	if absent(parts[0]) || absent(parts[1]) {
		p.invalid()
		return nil
	}
	n := p.integer(parts[0], true, 0, math.MaxInt64)
	d := p.integer(parts[1], true, 1, math.MaxInt64)
	if n == nil || d == nil {
		return nil
	}
	if new(big.Int).SetInt64(*n).Cmp(new(big.Int).Mul(big.NewInt(maxValue), big.NewInt(*d))) > 0 {
		p.invalid()
		return nil
	}
	a, b := *n, *d
	for b != 0 {
		a, b = b, a%b
	}
	return &domain.MediaRational{Numerator: *n / a, Denominator: *d / a}
}
func (p *metadataParser) rate(value any) *domain.MediaRational {
	if value == "0/0" {
		return nil
	} // ffprobe's explicit unknown frame-rate sentinel.
	v := p.rational(value, 1000000)
	if v != nil && v.Numerator == 0 {
		return nil
	}
	return v
}
func (p *metadataParser) tickTime(ticks *int64, base *domain.MediaRational) *int64 {
	if ticks == nil || base == nil {
		return nil
	}
	v := new(big.Int).Mul(big.NewInt(*ticks), big.NewInt(base.Numerator))
	v.Mul(v, big.NewInt(1000000))
	v.Add(v, big.NewInt(base.Denominator/2))
	v.Quo(v, big.NewInt(base.Denominator))
	if !v.IsInt64() {
		p.invalid()
		return nil
	}
	result := v.Int64()
	return &result
}
func words(value string) map[string]bool {
	result := make(map[string]bool)
	for _, word := range strings.Split(value, "|") {
		result[word] = true
	}
	return result
}
func compareRational(a, b *domain.MediaRational) int {
	left := new(big.Int).Mul(big.NewInt(a.Numerator), big.NewInt(b.Denominator))
	right := new(big.Int).Mul(big.NewInt(b.Numerator), big.NewInt(a.Denominator))
	return left.Cmp(right)
}
func (p *metadataParser) enum(value any, allowed map[string]bool) *string {
	s := p.text(value)
	if s == nil {
		return nil
	}
	if len(*s) > 128 {
		p.invalid()
		return nil
	}
	if !allowed[*s] {
		return nil
	}
	return s
}

var codecNames = words("h264|hevc|av1|vp9|vp8|mpeg4|mpeg2video|mpeg1video|mjpeg|jpeg2000|prores|dnxhd|vc1|wmv3|theora|ffv1|huffyuv|rawvideo|png|apng|gif|webp|bmp|tiff|aac|ac3|eac3|truehd|dts|mp3|mp2|mp1|flac|alac|opus|vorbis|wmav1|wmav2|wmapro|pcm_s16le|pcm_s16be|pcm_s24le|pcm_s24be|pcm_s32le|pcm_s32be|pcm_f32le|pcm_f64le|pcm_u8|subrip|ass|ssa|webvtt|mov_text|hdmv_pgs_subtitle|dvd_subtitle|dvb_subtitle|text|srt|eia_608")
var profiles = words("Baseline|Constrained Baseline|Main|High|High 10|High 4:2:2|High 4:4:4 Predictive|Main 10|Main Still Picture|Rext|Main 12|Main 4:2:2 10|Main 4:2:2 12|Main 4:4:4|Main 4:4:4 10|Main 4:4:4 12|Profile 0|Profile 1|Profile 2|Profile 3|Professional|Simple Profile|Advanced Simple Profile|LC|HE-AAC|HE-AACv2|Main|SSR|LTP|ELD|LD|DTS|DTS-ES|DTS 96/24|DTS-HD HRA|DTS-HD MA|DTS Express|Dolby Digital Plus|Dolby Digital Plus + Dolby Atmos|Dolby TrueHD|Dolby TrueHD + Dolby Atmos")
var formatNames = words("mov|mp4|m4a|3gp|3g2|mj2|matroska|webm|avi|mpegts|mpeg|mpegvideo|flv|ogg|wav|flac|mp3|aac|ac3|eac3|dts|truehd|image2|png_pipe|jpeg_pipe|gif|apng|h264|hevc|av1|ivf")
var channelLayouts = words("mono|stereo|2.1|3.0|3.0(back)|4.0|quad|quad(side)|3.1|4.1|5.0|5.0(side)|5.1|5.1(side)|6.0|6.0(front)|hexagonal|6.1|6.1(back)|6.1(front)|7.0|7.0(front)|7.1|7.1(wide)|7.1(wide-side)|7.1(top)|7.1.2|7.1.4|5.1.2|5.1.4|9.1.4|9.1.6|octagonal|hexadecagonal|downmix|22.2")
var colorsRange = words("tv|pc")
var colorsSpace = words("gbr|bt709|fcc|bt470bg|smpte170m|smpte240m|ycgco|bt2020nc|bt2020c|smpte2085|chroma-derived-nc|chroma-derived-c|ictcp|ipt-c2")
var colorsTransfer = words("bt709|gamma22|gamma28|smpte170m|smpte240m|linear|log|log_sqrt|iec61966-2-4|bt1361e|iec61966-2-1|bt2020-10|bt2020-12|smpte2084|smpte428|arib-std-b67")
var colorsPrimaries = words("bt709|bt470m|bt470bg|smpte170m|smpte240m|film|bt2020|smpte428|smpte431|smpte432|jedec-p22|ebu3213")
var languageTag = regexp.MustCompile(`^[A-Za-z]{2,3}(?:-[A-Za-z0-9]{2,8}){0,3}$`)
