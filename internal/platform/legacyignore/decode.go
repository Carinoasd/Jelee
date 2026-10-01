package legacyignore

import (
	"context"
	"encoding/binary"
	"strings"
	"unicode/utf8"
)

const MaxRawSourceBytes = 256 << 10

// DecodeSource follows the pinned wrapper's ReadAllText BOM detection and
// replacement fallback. Unlike the custom rule decoder, malformed encoded
// units become U+FFFD. It reads no paths and preserves physical line endings.
func DecodeSource(ctx context.Context, raw []byte) (string, error) {
	if ctx == nil || len(raw) > MaxRawSourceBytes {
		return "", ErrBatch
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	encoding, skip := 8, 0
	var order binary.ByteOrder = binary.LittleEndian
	switch {
	case len(raw) >= 4 && raw[0] == 0xff && raw[1] == 0xfe && raw[2] == 0 && raw[3] == 0:
		encoding, skip = 32, 4
	case len(raw) >= 4 && raw[0] == 0 && raw[1] == 0 && raw[2] == 0xfe && raw[3] == 0xff:
		encoding, skip, order = 32, 4, binary.BigEndian
	case len(raw) >= 3 && raw[0] == 0xef && raw[1] == 0xbb && raw[2] == 0xbf:
		skip = 3
	case len(raw) >= 2 && raw[0] == 0xff && raw[1] == 0xfe:
		encoding, skip = 16, 2
	case len(raw) >= 2 && raw[0] == 0xfe && raw[1] == 0xff:
		encoding, skip, order = 16, 2, binary.BigEndian
	}
	var out strings.Builder
	for i := skip; i < len(raw); {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		r, n := utf8.RuneError, 1
		switch encoding {
		case 32:
			n = min(4, len(raw)-i)
			if n == 4 {
				value := order.Uint32(raw[i : i+4])
				if value <= utf8.MaxRune && (value < 0xd800 || value > 0xdfff) {
					r = rune(value)
				}
			}
		case 16:
			n = min(2, len(raw)-i)
			if n == 2 {
				w := order.Uint16(raw[i : i+2])
				r = rune(w)
				if w >= 0xd800 && w <= 0xdbff {
					r = utf8.RuneError
					if len(raw)-i >= 4 {
						low := order.Uint16(raw[i+2 : i+4])
						if low >= 0xdc00 && low <= 0xdfff {
							r = 0x10000 + (rune(w)-0xd800)*1024 + rune(low) - 0xdc00
							n = 4
						}
					}
				} else if w >= 0xdc00 && w <= 0xdfff {
					r = utf8.RuneError
				}
			}
		default:
			r, n = utf8.DecodeRune(raw[i:])
			if r == utf8.RuneError && n == 1 {
				n = invalidUTF8Prefix(raw[i:])
			}
		}
		if out.Len() > MaxBatchSourceBytes-utf8.RuneLen(r) {
			return "", ErrBatch
		}
		out.WriteRune(r)
		i += n
	}
	return out.String(), nil
}

// Replace one maximal valid prefix of an ill-formed UTF-8 sequence. Invalid
// starters and out-of-range second bytes remain separate replacement units.
func invalidUTF8Prefix(b []byte) int {
	want := 1
	switch {
	case b[0] >= 0xc2 && b[0] <= 0xdf:
		want = 2
	case b[0] >= 0xe0 && b[0] <= 0xef:
		want = 3
	case b[0] >= 0xf0 && b[0] <= 0xf4:
		want = 4
	}
	n := 1
	for n < want && n < len(b) {
		c := b[n]
		if c < 0x80 || c > 0xbf {
			break
		}
		if n == 1 && (b[0] == 0xe0 && c < 0xa0 || b[0] == 0xed && c > 0x9f || b[0] == 0xf0 && c < 0x90 || b[0] == 0xf4 && c > 0x8f) {
			break
		}
		n++
	}
	return n
}
