package formatsamples

import (
	"bytes"
	"testing"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	"golang.org/x/text/encoding/unicode"
)

// TestEncodedSamplesDecodeBackToTheirStory proves each legacy-encoded sample
// is really in the charset it claims, with a decoder that is independent of
// the detector under test: decoding gives the template text again.
func TestEncodedSamplesDecodeBackToTheirStory(t *testing.T) {
	files := textFiles()
	decoders := map[string]struct {
		enc  encoding.Encoding
		text string
	}{
		"gb18030.srt":            {simplifiedchinese.GB18030, srt(stories["zh-Hans"])},
		"big5.srt":               {traditionalchinese.Big5, srt(stories["zh-Hant"])},
		"shift_jis.srt":          {japanese.ShiftJIS, srt(stories["ja"])},
		"euc-jp.srt":             {japanese.EUCJP, srt(stories["ja"])},
		"euc-kr.srt":             {korean.EUCKR, srt(stories["ko"])},
		"windows-1252.srt":       {charmap.Windows1252, srt(stories["fr"])},
		"utf16le.srt":            {unicode.UTF16(unicode.LittleEndian, unicode.UseBOM), srt(stories["zh-Hans"])},
		"utf16be.srt":            {unicode.UTF16(unicode.BigEndian, unicode.UseBOM), srt(stories["zh-Hans"])},
		"gb18030.ass":            {simplifiedchinese.GB18030, ass(stories["zh-Hans"], true)},
		"big5.ssa":               {traditionalchinese.Big5, ass(stories["zh-Hant"], false)},
		"euc-kr.smi":             {korean.EUCKR, smi(stories["ko"], "KRCC")},
		"shift_jis-microdvd.sub": {japanese.ShiftJIS, microdvd(stories["ja"])},
	}
	for name, c := range decoders {
		got, err := c.enc.NewDecoder().Bytes(files[name])
		if err != nil || string(got) != c.text && !bytes.Equal(bytes.TrimPrefix(got, []byte("\ufeff")), []byte(c.text)) {
			t.Errorf("%s does not decode back to its story (%v)", name, err)
		}
		if bytes.Equal(files[name], []byte(c.text)) {
			t.Errorf("%s is not encoded at all", name)
		}
	}
	if !bytes.HasPrefix(files["utf8-bom.srt"], []byte{0xEF, 0xBB, 0xBF}) || !bytes.HasPrefix(files["utf16le.srt"], []byte{0xFF, 0xFE}) || !bytes.HasPrefix(files["utf16be.srt"], []byte{0xFE, 0xFF}) {
		t.Error("a byte order mark is missing")
	}
	var total int
	for name, data := range files {
		total += len(data)
		if len(data) > 2048 {
			t.Errorf("%s is %d bytes; samples stay in the KB range", name, len(data))
		}
	}
	if total > 32<<10 {
		t.Errorf("text samples total %d bytes", total)
	}
	again := textFiles()
	for name, data := range files {
		if !bytes.Equal(data, again[name]) {
			t.Errorf("%s is not deterministic", name)
		}
	}
}
