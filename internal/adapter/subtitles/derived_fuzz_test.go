package subtitles

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"unicode/utf8"
)

// FuzzDetectThenConvert drives the decision the derived cache makes on
// arbitrary bytes: detection never panics or reads past its budget, and a
// detected charset always converts into valid UTF-8 that starts with a
// byte order mark only when the input had two, and whose replacement
// count, which decides the 1% rule, matches the output.
func FuzzDetectThenConvert(f *testing.F) {
	f.Add([]byte("1\r\n00:00:01,000 --> 00:00:02,000\r\nhello\r\n"))
	f.Add([]byte{0xff, 0xfe, '1', 0, '\n', 0})
	f.Add([]byte{0xef, 0xbb, 0xbf, 0xe4, 0xbd, 0xa0})
	f.Add([]byte{0xc4, 0xe3, 0xba, 0xc3, '\n', 0xa7, 0x41})
	f.Add([]byte{0x82, 0xa0, 0x82, 0xa2, '\n', 0xff, 0xfe, 0x00})
	f.Fuzz(func(t *testing.T, data []byte) {
		ctx := context.Background()
		cs, confidence, err := DetectCharset(ctx, bytes.NewReader(data), 0)
		if confidence.Scanned > int64(len(data)) {
			t.Fatalf("scanned %d of %d bytes", confidence.Scanned, len(data))
		}
		if err != nil {
			if cs != CharsetUnknown {
				t.Fatalf("charset %q with error %v", cs, err)
			}
			if !errors.Is(err, ErrNotText) && !errors.Is(err, ErrUnsupportedCharset) {
				t.Fatalf("unexpected error %v", err)
			}
			return
		}
		if _, ok := cs.Encoding(); !ok {
			t.Fatalf("detected unsupported charset %q", cs)
		}
		again, _, _ := DetectCharset(ctx, bytes.NewReader(data), 0)
		if again != cs {
			t.Fatalf("detection not deterministic: %q then %q", cs, again)
		}
		var out bytes.Buffer
		result, err := ToUTF8(ctx, bytes.NewReader(data), &out, cs, 0)
		if err != nil {
			t.Fatalf("convert %q: %v", cs, err)
		}
		// Only the first byte order mark is dropped; a second one is text.
		if !utf8.Valid(out.Bytes()) || bytes.HasPrefix(out.Bytes(), []byte{0xef, 0xbb, 0xbf}) && !result.BOMStripped {
			t.Fatalf("%q produced invalid or BOM-led output", cs)
		}
		if result.Read != int64(len(data)) || result.Written != int64(out.Len()) || result.Replaced < 0 {
			t.Fatalf("result %+v for %d bytes", result, len(data))
		}
		if replaced := int64(bytes.Count(out.Bytes(), []byte("�"))); cs != UTF8 && result.Replaced != replaced {
			t.Fatalf("%q counted %d replacements, output holds %d", cs, result.Replaced, replaced)
		}
	})
}
