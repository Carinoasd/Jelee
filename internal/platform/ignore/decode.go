package ignore

import (
	"strings"
	"unicode/utf8"
)

func decode(text []byte, m *workMeter) (string, error) {
	if len(text) > MaxSourceBytes {
		return "", ErrLimit
	}
	var out strings.Builder
	appendRune := func(r rune) error {
		n := utf8.RuneLen(r)
		if err := m.spend(n); err != nil {
			return err
		}
		if out.Len() > MaxDecodedSourceBytes-n {
			return ErrLimit
		}
		out.WriteRune(r)
		return nil
	}
	if len(text) >= 2 && (text[0] == 0xff && text[1] == 0xfe || text[0] == 0xfe && text[1] == 0xff) {
		if len(text)%2 != 0 {
			return "", ErrInvalid
		}
		little := text[0] == 0xff
		word := func(i int) uint16 {
			if little {
				return uint16(text[i]) | uint16(text[i+1])<<8
			}
			return uint16(text[i])<<8 | uint16(text[i+1])
		}
		for i := 2; i < len(text); i += 2 {
			if err := m.spend(2); err != nil {
				return "", err
			}
			w := word(i)
			r := rune(w)
			if w >= 0xd800 && w <= 0xdbff {
				if i+3 >= len(text) {
					return "", ErrInvalid
				}
				i += 2
				if err := m.spend(2); err != nil {
					return "", err
				}
				low := word(i)
				if low < 0xdc00 || low > 0xdfff {
					return "", ErrInvalid
				}
				r = 0x10000 + (rune(w)-0xd800)*1024 + rune(low) - 0xdc00
			} else if w >= 0xdc00 && w <= 0xdfff {
				return "", ErrInvalid
			}
			if r == 0 {
				return "", ErrInvalid
			}
			if err := appendRune(r); err != nil {
				return "", err
			}
		}
	} else {
		if len(text) >= 3 && text[0] == 0xef && text[1] == 0xbb && text[2] == 0xbf {
			text = text[3:]
		}
		for i := 0; i < len(text); {
			r, n := utf8.DecodeRune(text[i:])
			if err := m.spend(n); err != nil {
				return "", err
			}
			if r == 0 || r == utf8.RuneError && n == 1 {
				return "", ErrInvalid
			}
			if err := appendRune(r); err != nil {
				return "", err
			}
			i += n
		}
	}
	return out.String(), nil
}
