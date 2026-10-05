package subtitles

import (
	"errors"
	"strings"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	textunicode "golang.org/x/text/encoding/unicode"
)

// Charset names a supported subtitle character set. Values are the IANA
// preferred names, which is also what the subtitle metadata records.
type Charset string

const (
	CharsetUnknown Charset = ""
	UTF8           Charset = "UTF-8"
	UTF16LE        Charset = "UTF-16LE"
	UTF16BE        Charset = "UTF-16BE"
	// GB18030 also covers GBK, CP936 and GB2312, which are subsets of it.
	GB18030 Charset = "GB18030"
	// Big5 is decoded with the WHATWG table, which includes the CP950 and
	// HKSCS extensions.
	Big5 Charset = "Big5"
	// ShiftJIS is decoded as Windows-31J (CP932).
	ShiftJIS Charset = "Shift_JIS"
	EUCJP    Charset = "EUC-JP"
	// EUCKR is decoded as its CP949 (Unified Hangul Code) superset.
	EUCKR       Charset = "EUC-KR"
	Windows1252 Charset = "windows-1252"
)

var (
	// ErrNotText reports binary input such as VobSub or PGS data.
	ErrNotText = errors.New("subtitles: input is not text")
	// ErrUnsupportedCharset reports a charset this package cannot convert.
	ErrUnsupportedCharset = errors.New("subtitles: unsupported charset")
	// ErrTooLarge reports input beyond the conversion limit.
	ErrTooLarge = errors.New("subtitles: input exceeds size limit")
)

// Charsets lists every supported charset in a stable order.
func Charsets() []Charset {
	return []Charset{UTF8, UTF16LE, UTF16BE, GB18030, Big5, ShiftJIS, EUCJP, EUCKR, Windows1252}
}

// Encoding returns the decoder family for cs. UTF-16 ignores byte order
// marks here; ToUTF8 strips a leading one itself.
func (cs Charset) Encoding() (encoding.Encoding, bool) {
	switch cs {
	case UTF8:
		return textunicode.UTF8, true
	case UTF16LE:
		return textunicode.UTF16(textunicode.LittleEndian, textunicode.IgnoreBOM), true
	case UTF16BE:
		return textunicode.UTF16(textunicode.BigEndian, textunicode.IgnoreBOM), true
	case GB18030:
		return simplifiedchinese.GB18030, true
	case Big5:
		return traditionalchinese.Big5, true
	case ShiftJIS:
		return japanese.ShiftJIS, true
	case EUCJP:
		return japanese.EUCJP, true
	case EUCKR:
		return korean.EUCKR, true
	case Windows1252:
		return charmap.Windows1252, true
	}
	return nil, false
}

// ParseCharset maps a user or file supplied label to a supported charset,
// so that an explicit override can bypass detection.
func ParseCharset(label string) (Charset, bool) {
	key := strings.ToLower(strings.TrimSpace(label))
	key = strings.NewReplacer("_", "-", " ", "-").Replace(key)
	switch key {
	case "utf-8", "utf8", "us-ascii", "ascii":
		return UTF8, true
	case "utf-16le", "utf16le":
		return UTF16LE, true
	case "utf-16be", "utf16be":
		return UTF16BE, true
	case "gb18030", "gbk", "gb2312", "cp936", "ms936", "windows-936", "euc-cn":
		return GB18030, true
	case "big5", "big5-hkscs", "cp950", "ms950":
		return Big5, true
	case "shift-jis", "sjis", "cp932", "ms932", "windows-31j":
		return ShiftJIS, true
	case "euc-jp", "eucjp":
		return EUCJP, true
	case "euc-kr", "euckr", "cp949", "ms949", "uhc":
		return EUCKR, true
	case "windows-1252", "cp1252", "iso-8859-1", "latin1", "latin-1":
		return Windows1252, true
	}
	return CharsetUnknown, false
}
