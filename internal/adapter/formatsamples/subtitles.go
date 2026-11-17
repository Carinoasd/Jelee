package formatsamples

import (
	"bytes"
	"image"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/bitmapsub/bitmapsubtest"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// Subtitle is one external subtitle sample and what every stage must make of
// it. The zero Charset means the content is not text.
type Subtitle struct {
	// File is the sample's own name, used as the free title of its sidecar
	// file name and to tell samples apart.
	File string
	// Format is the sidecar extension the sample is delivered under.
	Format string
	// Language is the BCP 47 tag used in the sidecar file name.
	Language string
	// Charset is the character set detection must record (an entry of
	// domain.SidecarCharsets), empty for binary content.
	Charset string
	// SubtitleKind is the content kind the scan must find in a ".sub".
	SubtitleKind string
	// Codec is the codec the playback information reports.
	Codec string
	// ProbeCodec is the ffprobe codec name of a probed sample (".sub",
	// ".sup"); empty when the extension already names the format.
	ProbeCodec string
	Data       []byte
}

// Name returns the sidecar file name of s next to a video called videoBase:
// the language, then the sample's own name as a free title so that no two
// samples of one format collide.
func (s Subtitle) Name(videoBase string) string {
	title := strings.TrimSuffix(s.File, path.Ext(s.File))
	if s.Language == "" {
		return videoBase + "." + title + "." + s.Format
	}
	return videoBase + "." + s.Language + "." + title + "." + s.Format
}

var (
	textOnce  sync.Once
	textBytes map[string][]byte
)

func read(name string) []byte {
	textOnce.Do(func() { textBytes = textFiles() })
	data, ok := textBytes[name]
	if !ok {
		panic("formatsamples: missing sample " + name)
	}
	return data
}

// textSamples lists the plain English file of every text format in UTF-8
// (the extension names the format), then one SubRip story per legacy
// charset and a legacy charset under the other text formats.
var textSamples = []Subtitle{
	{File: "plain.srt", Format: "srt", Language: "en", Charset: "UTF-8", Codec: "srt"},
	{File: "plain.ass", Format: "ass", Language: "en", Charset: "UTF-8", Codec: "ass"},
	{File: "plain.ssa", Format: "ssa", Language: "en", Charset: "UTF-8", Codec: "ssa"},
	{File: "plain.vtt", Format: "vtt", Language: "en", Charset: "UTF-8", Codec: "webvtt"},
	{File: "plain.vtt", Format: "webvtt", Language: "en", Charset: "UTF-8", Codec: "webvtt"},
	{File: "plain.ttml", Format: "ttml", Language: "en", Charset: "UTF-8", Codec: "ttml"},
	{File: "plain.ttml", Format: "dfxp", Language: "en", Charset: "UTF-8", Codec: "ttml"},
	{File: "plain.smi", Format: "smi", Language: "en", Charset: "UTF-8", Codec: "sami"},
	{File: "plain.smi", Format: "sami", Language: "en", Charset: "UTF-8", Codec: "sami"},
	{File: "microdvd.sub", Format: "sub", Language: "en", Charset: "UTF-8", SubtitleKind: domain.SidecarSubtitleMicroDVD, Codec: domain.SidecarSubtitleMicroDVD, ProbeCodec: "microdvd"},
	// Encodings of one SubRip story per language.
	{File: "utf8-bom.srt", Format: "srt", Language: "zh-Hans", Charset: "UTF-8", Codec: "srt"},
	{File: "utf16le.srt", Format: "srt", Language: "zh-Hans", Charset: "UTF-16LE", Codec: "srt"},
	{File: "utf16be.srt", Format: "srt", Language: "zh-Hans", Charset: "UTF-16BE", Codec: "srt"},
	{File: "gb18030.srt", Format: "srt", Language: "zh-Hans", Charset: "GB18030", Codec: "srt"},
	{File: "big5.srt", Format: "srt", Language: "zh-Hant", Charset: "Big5", Codec: "srt"},
	{File: "shift_jis.srt", Format: "srt", Language: "ja", Charset: "Shift_JIS", Codec: "srt"},
	{File: "euc-jp.srt", Format: "srt", Language: "ja", Charset: "EUC-JP", Codec: "srt"},
	{File: "euc-kr.srt", Format: "srt", Language: "ko", Charset: "EUC-KR", Codec: "srt"},
	{File: "windows-1252.srt", Format: "srt", Language: "fr", Charset: "windows-1252", Codec: "srt"},
	// A legacy charset under the other text formats.
	{File: "gb18030.ass", Format: "ass", Language: "zh-Hans", Charset: "GB18030", Codec: "ass"},
	{File: "big5.ssa", Format: "ssa", Language: "zh-Hant", Charset: "Big5", Codec: "ssa"},
	{File: "euc-kr.smi", Format: "smi", Language: "ko", Charset: "EUC-KR", Codec: "sami"},
	{File: "shift_jis-microdvd.sub", Format: "sub", Language: "ja", Charset: "Shift_JIS", SubtitleKind: domain.SidecarSubtitleMicroDVD, Codec: domain.SidecarSubtitleMicroDVD, ProbeCodec: "microdvd"},
}

var (
	bitmapOnce       sync.Once
	vobsubIdx        []byte
	vobsubSub        []byte
	pgsSup           []byte
	bitmapBuildError error
)

func buildBitmaps() {
	img := image.NewNRGBA(image.Rect(0, 0, 8, 4))
	for i := range img.Pix {
		img.Pix[i] = 0xFF
	}
	cues := []bitmapsubtest.Cue{{Start: time.Second, End: 2 * time.Second, Image: img}}
	var idx, sub, sup bytes.Buffer
	if err := bitmapsubtest.EncodeVobSub(&idx, &sub, 64, 32, cues); err != nil {
		bitmapBuildError = err
		return
	}
	if err := bitmapsubtest.EncodePGS(&sup, 64, 32, cues); err != nil {
		bitmapBuildError = err
		return
	}
	vobsubIdx, vobsubSub, pgsSup = idx.Bytes(), sub.Bytes(), sup.Bytes()
}

// Subtitles returns every subtitle sample: the text files, the VobSub pair
// and the PGS stream. The slice is fresh; the data is shared and read-only.
func Subtitles() []Subtitle {
	out := make([]Subtitle, 0, len(textSamples)+3)
	for _, s := range textSamples {
		s.Data = read(s.File)
		out = append(out, s)
	}
	bitmapOnce.Do(buildBitmaps)
	if bitmapBuildError != nil {
		panic("formatsamples: " + bitmapBuildError.Error())
	}
	out = append(out,
		Subtitle{File: "vobsub-synthetic.idx", Format: "idx", Language: "ja", Charset: "UTF-8", Codec: "vobsub", Data: vobsubIdx},
		Subtitle{File: "vobsub-synthetic.sub", Format: "sub", Language: "ja", SubtitleKind: domain.SidecarSubtitleVobSub, Codec: domain.SidecarSubtitleVobSub, ProbeCodec: "dvd_subtitle", Data: vobsubSub},
		Subtitle{File: "pgs-synthetic.sup", Format: "sup", Language: "ja", Codec: "pgs", ProbeCodec: "hdmv_pgs_subtitle", Data: pgsSup},
	)
	return out
}
