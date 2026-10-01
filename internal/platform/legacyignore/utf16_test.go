package legacyignore

import (
	"strings"
	"testing"
)

func TestUTF16Pattern(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`a\d[片]`, `a\d[片]`},
		{`😀`, `\ud83d\ude00`},
		{`\😀`, `\ud83d\ude00`},
		{`\\😀`, `\\\ud83d\ude00`},
		{`\\\😀`, `\\\ud83d\ude00`},
		{`[\😀]`, `[\ud83d\ude00]`},
		{`😀{2}`, `\ud83d\ude00{2}`},
		{`(😀){2}`, `(\ud83d\ude00){2}`},
		{`𐐀\`, `\ud801\udc00\`},
		{`\uD83D\uDE00`, `\uD83D\uDE00`},
	} {
		got, err := UTF16Pattern(tc.in)
		if err != nil || got != tc.want {
			t.Fatalf("%q: got %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	for _, in := range []string{string([]byte{0xff}), "a\x00b", strings.Repeat("x", 16*MaxPatternBytes+1)} {
		if _, err := UTF16Pattern(in); err != ErrInvalid {
			t.Fatal("invalid expression accepted")
		}
	}
}
