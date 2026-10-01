package nfo

import (
	"bytes"
	"encoding/binary"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	textunicode "golang.org/x/text/encoding/unicode"
)

var declarationEncoding = regexp.MustCompile(`^\s*<\?xml\s+[^?]*\bencoding\s*=\s*["']([^"']+)["']`)
var validDeclaration = regexp.MustCompile(`^version\s*=\s*("1\.0"|'1\.0')(\s+encoding\s*=\s*("[A-Za-z][A-Za-z0-9._-]*"|'[A-Za-z][A-Za-z0-9._-]*'))?(\s+standalone\s*=\s*("yes"|'yes'|"no"|'no'))?\s*$`)

func declaredEncoding(data []byte) (string, error) {
	trimmed := bytes.TrimSpace(data)
	if !bytes.HasPrefix(trimmed, []byte("<?xml")) {
		return "", nil
	}
	if len(trimmed) > 5 && !strings.ContainsRune(" \t\r\n", rune(trimmed[5])) {
		return "", nil
	}
	probe := trimmed[:min(len(trimmed), 1025)]
	end := bytes.Index(probe, []byte("?>"))
	if end < 0 || end+2 > 1024 {
		if len(trimmed) > 1024 {
			return "", ErrTooComplex
		}
		return "", ErrInvalidXML
	}
	match := declarationEncoding.FindSubmatch(probe[:end+2])
	if len(match) == 2 {
		return strings.ToLower(string(match[1])), nil
	}
	return "", nil
}

func canonicalEncoding(label string) string {
	switch strings.ToLower(label) {
	case "", "utf-8", "utf8":
		return "UTF-8"
	case "utf-16":
		return "UTF-16"
	case "utf-16le":
		return "UTF-16LE"
	case "utf-16be":
		return "UTF-16BE"
	case "gbk", "cp936", "gb2312":
		return "GBK"
	}
	return ""
}

func decodeEncoding(original []byte) ([]byte, string, bool, error) {
	data := original
	encoding, guessed := "", false
	switch {
	case bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}):
		encoding, data = "UTF-8", data[3:]
	case bytes.HasPrefix(data, []byte{0xff, 0xfe}):
		encoding, data = "UTF-16LE", data[2:]
	case bytes.HasPrefix(data, []byte{0xfe, 0xff}):
		encoding, data = "UTF-16BE", data[2:]
	case bytes.HasPrefix(data, []byte{'<', 0, '?', 0}) || bytes.HasPrefix(data, []byte{'<', 0}):
		encoding, guessed = "UTF-16LE", true
	case bytes.HasPrefix(data, []byte{0, '<', 0, '?'}) || bytes.HasPrefix(data, []byte{0, '<'}):
		encoding, guessed = "UTF-16BE", true
	default:
		declared, err := declaredEncoding(data)
		if err != nil {
			return nil, "", false, err
		}
		if declared != "" {
			encoding = canonicalEncoding(declared)
			if encoding == "" {
				return nil, "", false, ErrUnsupportedEncoding
			}
		} else if utf8.Valid(data) {
			encoding = "UTF-8"
		} else {
			encoding, guessed = "GBK", true
		}
	}
	var decoded []byte
	var err error
	switch encoding {
	case "UTF-8":
		if !utf8.Valid(data) {
			return nil, "", false, ErrInvalidEncoding
		}
		decoded = data
	case "GBK":
		decoded, err = simplifiedchinese.GBK.NewDecoder().Bytes(data)
		if err != nil || bytes.Contains(decoded, []byte("\ufffd")) {
			return nil, "", false, ErrInvalidEncoding
		}
	case "UTF-16LE", "UTF-16BE":
		little := encoding == "UTF-16LE"
		if !validUTF16(data, little) {
			return nil, "", false, ErrInvalidEncoding
		}
		endian := textunicode.BigEndian
		if little {
			endian = textunicode.LittleEndian
		}
		decoded, err = textunicode.UTF16(endian, textunicode.IgnoreBOM).NewDecoder().Bytes(data)
		if err != nil {
			return nil, "", false, ErrInvalidEncoding
		}
	default:
		return nil, "", false, ErrInvalidEncoding
	}
	declaration, err := declaredEncoding(decoded)
	if err != nil {
		return nil, "", false, err
	}
	if declaration != "" {
		declared := canonicalEncoding(declaration)
		if declared == "" {
			return nil, "", false, ErrUnsupportedEncoding
		}
		if declared != encoding && !(declared == "UTF-16" && strings.HasPrefix(encoding, "UTF-16")) {
			return nil, "", false, ErrInvalidEncoding
		}
	}
	return decoded, encoding, guessed, nil
}

func validUTF16(data []byte, little bool) bool {
	if len(data)%2 != 0 {
		return false
	}
	var order binary.ByteOrder = binary.BigEndian
	if little {
		order = binary.LittleEndian
	}
	for i := 0; i < len(data); i += 2 {
		unit := order.Uint16(data[i : i+2])
		if unit >= 0xd800 && unit <= 0xdbff {
			i += 2
			if i >= len(data) {
				return false
			}
			low := order.Uint16(data[i : i+2])
			if low < 0xdc00 || low > 0xdfff {
				return false
			}
		} else if unit >= 0xdc00 && unit <= 0xdfff {
			return false
		}
	}
	return true
}

// The input has already been decoded and its declaration checked. XML asks for
// a charset reader when the preserved declaration names a non-UTF-8 encoding.
func decodedCharset(actual string) func(string, io.Reader) (io.Reader, error) {
	return func(label string, input io.Reader) (io.Reader, error) {
		declared := canonicalEncoding(label)
		if declared == "" {
			return nil, ErrUnsupportedEncoding
		}
		if declared != actual && !(declared == "UTF-16" && strings.HasPrefix(actual, "UTF-16")) {
			return nil, ErrInvalidEncoding
		}
		return input, nil
	}
}
