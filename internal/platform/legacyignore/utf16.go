package legacyignore

import (
	"fmt"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// UTF16Pattern spells supplementary characters as two regex unicode escapes.
// It bridges the fixed upstream's UTF-16 character model to a rune-based regex
// parser. It does not validate .NET syntax or authorize a regex engine.
func UTF16Pattern(pattern string) (string, error) {
	if !utf8.ValidString(pattern) || len(pattern) > 16*MaxPatternBytes || strings.IndexByte(pattern, 0) >= 0 {
		return "", ErrInvalid
	}
	var out strings.Builder
	slashes := 0
	for _, ch := range pattern {
		if ch == '\\' {
			slashes++
			continue
		}
		if ch > 0xffff {
			// .NET treats a backslash before a surrogate as a literal escape. The
			// generated \u escape replaces that odd backslash, rather than doubling it.
			out.WriteString(strings.Repeat(`\`, slashes-slashes%2))
			hi, lo := utf16.EncodeRune(ch)
			fmt.Fprintf(&out, `\u%04x\u%04x`, hi, lo)
		} else {
			out.WriteString(strings.Repeat(`\`, slashes))
			out.WriteRune(ch)
		}
		slashes = 0
	}
	out.WriteString(strings.Repeat(`\`, slashes))
	return out.String(), nil
}
