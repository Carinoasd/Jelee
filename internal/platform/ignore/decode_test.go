package ignore

import (
	"context"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
	"unicode/utf16"
)

func contractUTF16(text string, bigEndian bool) []byte {
	units := utf16.Encode([]rune(text))
	out := make([]byte, 2+len(units)*2)
	var order binary.ByteOrder = binary.LittleEndian
	out[0], out[1] = 0xff, 0xfe
	if bigEndian {
		out[0], out[1] = 0xfe, 0xff
		order = binary.BigEndian
	}
	for i, unit := range units {
		order.PutUint16(out[2+i*2:], unit)
	}
	return out
}

func TestDecodeEncodingsPreservePhysicalLinesAndUnicode(t *testing.T) {
	const rules = "# first\r\n\r\n*.nfo\r\n!keep.nfo\r\n片🎬.mkv\r\n\\\r\n"
	for name, raw := range map[string][]byte{
		"utf8": []byte(rules), "utf8 BOM": append([]byte{0xef, 0xbb, 0xbf}, []byte(rules)...),
		"utf16le": contractUTF16(rules, false), "utf16be": contractUTF16(rules, true),
	} {
		t.Run(name, func(t *testing.T) {
			p, err := Compile(context.Background(), []Source{{Path: ".jeleeignore", Text: raw}}, Options{})
			if err != nil {
				t.Fatal(err)
			}
			for _, tc := range []struct {
				path    string
				outcome Outcome
				line    int
			}{{"movie.nfo", Exclude, 3}, {"keep.nfo", Include, 4}, {"片🎬.mkv", Exclude, 5}} {
				got := contractMatch(t, p, tc.path, File, tc.outcome)
				if got.Source != ".jeleeignore" || got.Line != tc.line || got.MatchedPath != tc.path || got.ParentBlocked {
					t.Fatal("decoding changed source position", got)
				}
			}
			d := p.Diagnostics()
			if d.Total != 1 || len(d.Items) != 1 || d.Items[0].Line != 6 {
				t.Fatal("invalid-rule line shifted during decoding", d)
			}
		})
	}
}

func TestDecodeRejectsMalformedTextWithoutPartialProgram(t *testing.T) {
	for name, raw := range map[string][]byte{
		"invalid UTF8": {0xff}, "truncated UTF8": {0xe7, 0x89}, "overlong UTF8": {0xc0, 0xaf},
		"UTF8 surrogate": {0xed, 0xa0, 0x80}, "NUL": []byte("*.mkv\n\x00"),
		"isolated CR":  []byte("*.mkv\r*.nfo"),
		"LE odd bytes": {0xff, 0xfe, 0x61}, "BE odd bytes": {0xfe, 0xff, 0x00},
		"LE high surrogate": {0xff, 0xfe, 0x00, 0xd8}, "LE low surrogate": {0xff, 0xfe, 0x00, 0xdc},
		"BE high surrogate then ascii": {0xfe, 0xff, 0xd8, 0x00, 0x00, 0x61},
		"BE high surrogate twice":      {0xfe, 0xff, 0xd8, 0x00, 0xd8, 0x01},
		"UTF16 NUL":                    contractUTF16("*.mkv\n\x00", false),
		"UTF16 without BOM":            {0x61, 0x00, 0x0a, 0x00},
	} {
		t.Run(name, func(t *testing.T) {
			p, err := Compile(context.Background(), []Source{{Path: ".jeleeignore", Text: raw}}, Options{})
			if !errors.Is(err, ErrInvalid) || p != nil {
				t.Fatal("malformed text produced a program", err)
			}
		})
	}
}

func TestDecodeEmptyBOMAndFinalNewlineHaveNoInventedRules(t *testing.T) {
	for _, raw := range [][]byte{nil, {}, {0xef, 0xbb, 0xbf}, {0xff, 0xfe}, {0xfe, 0xff}, []byte("\r\n# comment\n\n")} {
		p, err := Compile(context.Background(), []Source{{Path: ".jeleeignore", Text: raw}}, Options{})
		if err != nil {
			t.Fatal(err)
		}
		contractMatch(t, p, "any.nfo", File, Unmatched)
		if d := p.Diagnostics(); d.Total != 0 {
			t.Fatal("empty text created invalid patterns")
		}
	}
	for _, suffix := range []string{"", "\n", "\r\n"} {
		p := contractCompile(t, "# comment\n*.mkv"+suffix)
		if got := contractMatch(t, p, "film.mkv", File, Exclude); got.Line != 2 {
			t.Fatal("final newline changed physical line")
		}
	}
}

func TestDecodeDiagnosticRetentionIsBoundedButTotalIsExact(t *testing.T) {
	for _, count := range []int{MaxDiagnostics, MaxDiagnostics + 1} {
		p := contractCompile(t, strings.Repeat("\\\n", count))
		d := p.Diagnostics()
		if d.Total != count || len(d.Items) != min(count, MaxDiagnostics) || d.Truncated != (count > MaxDiagnostics) {
			t.Fatal("diagnostic cap erased total or invented continuation", d)
		}
		for i, item := range d.Items {
			if item.Line != i+1 || item.Source != ".jeleeignore" || item.Code != "invalid_pattern" {
				t.Fatal("diagnostic prefix is not stable")
			}
		}
		contractMatch(t, p, "any.mkv", File, Unmatched)
	}
}

func TestDecodeMaximumExpansionWithinRawLimit(t *testing.T) {
	// The UTF-16 BOM consumes two bytes. At the raw cap, the greatest legal
	// expansion is 131071 three-byte BMP runes: 393213 decoded bytes, cap-3.
	// The decoded per-source cap and +1 cannot independently be reached under
	// the current raw cap; this direct decoder test isolates the reachable
	// expansion without the smaller physical-line/token caps masking it.
	want := strings.Repeat("界", (MaxSourceBytes-2)/2)
	if len(want) != MaxDecodedSourceBytes-3 {
		t.Fatal("limits changed: revisit maximum-expansion analysis")
	}
	for _, bigEndian := range []bool{false, true} {
		raw := contractUTF16(want, bigEndian)
		if len(raw) != MaxSourceBytes {
			t.Fatal("fixture does not reach raw boundary")
		}
		got, err := decode(raw, newWorkMeter(context.Background(), MaxCompileWork))
		if err != nil || got != want {
			t.Fatal("bounded expansion failed", err)
		}
		if got, err := decode(append(raw, 0), newWorkMeter(context.Background(), MaxCompileWork)); !errors.Is(err, ErrLimit) || got != "" {
			t.Fatal("oversized raw text returned a partial decode", err)
		}
	}
}
