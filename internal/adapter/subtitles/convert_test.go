package subtitles

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
	"unicode/utf8"
)

func TestToUTF8RoundTrip(t *testing.T) {
	for _, tc := range []struct {
		cs   Charset
		lang string
		bom  bool
	}{
		{UTF8, "ko", false}, {UTF8, "ja", true},
		{UTF16LE, "zh-Hans", true}, {UTF16LE, "western", false},
		{UTF16BE, "ko", true}, {UTF16BE, "zh-Hant", false},
		{GB18030, "zh-Hans", false}, {GB18030, "zh-Hans", true},
		{Big5, "zh-Hant", false}, {ShiftJIS, "ja", false}, {EUCJP, "ja", false},
		{EUCKR, "ko", false}, {Windows1252, "western", false},
	} {
		for _, text := range []string{srtText(pick(tc.lang, 0, 12)), assText(pick(tc.lang, 5, 12))} {
			data := encodeWith(t, tc.cs, text)
			if tc.bom {
				data = withBOM(tc.cs, data)
			}
			for _, reader := range []func() io.Reader{
				func() io.Reader { return bytes.NewReader(data) },
				func() io.Reader { return iotest.OneByteReader(bytes.NewReader(data)) },
				func() io.Reader { return iotest.HalfReader(bytes.NewReader(data)) },
			} {
				var out bytes.Buffer
				res, err := ToUTF8(context.Background(), reader(), &out, tc.cs, 0)
				if err != nil {
					t.Fatalf("%s/%s: %v", tc.cs, tc.lang, err)
				}
				if out.String() != text {
					t.Fatalf("%s/%s bom=%v: output differs\n got %q\nwant %q", tc.cs, tc.lang, tc.bom, out.String(), text)
				}
				if res.Read != int64(len(data)) || res.Written != int64(out.Len()) || res.Replaced != 0 || res.BOMStripped != tc.bom {
					t.Fatalf("%s/%s: result %+v (input %d bytes)", tc.cs, tc.lang, res, len(data))
				}
			}
		}
	}
}

func TestToUTF8KeepsLineEndingsAndInteriorBOM(t *testing.T) {
	text := "1\r\n00:00:01,000 --> 00:00:02,000\r\n第一行\n第二行\r\n\ufeff保留\r\n"
	var out bytes.Buffer
	res, err := ToUTF8(context.Background(), bytes.NewReader(encodeWith(t, GB18030, text)), &out, GB18030, 0)
	if err != nil || out.String() != text || res.BOMStripped {
		t.Fatalf("got %q %+v %v", out.String(), res, err)
	}
	// Only the first of two leading marks is a byte order mark.
	out.Reset()
	res, err = ToUTF8(context.Background(), strings.NewReader("\ufeff\ufeffx"), &out, UTF8, 0)
	if err != nil || out.String() != "\ufeffx" || !res.BOMStripped {
		t.Fatalf("double BOM: %q %+v %v", out.String(), res, err)
	}
}

func TestToUTF8ReplacesInvalidSequences(t *testing.T) {
	for _, tc := range []struct {
		name     string
		cs       Charset
		data     []byte
		want     string
		replaced int64
	}{
		{"utf8 stray bytes", UTF8, []byte("a\xffb\xc3(c"), "a\ufffdb\ufffd(c", 2},
		{"utf8 truncated tail", UTF8, []byte("ok \xe4\xb8"), "ok \ufffd\ufffd", 2},
		{"utf8 literal U+FFFD is not counted", UTF8, []byte("x\ufffdy"), "x\ufffdy", 0},
		{"gbk invalid trail", GB18030, []byte("\xc4\xe3\xff\xc4\xe3"), "你\ufffd你", 1},
		{"gbk truncated tail", GB18030, []byte("\xc4\xe3\xc4"), "你\ufffd", 1},
		{"sjis truncated tail", ShiftJIS, []byte("\x82\xa0\x82"), "あ\ufffd", 1},
		{"utf16 odd length", UTF16LE, []byte{'a', 0, 'b'}, "a\ufffd", 1},
		{"utf16 lone surrogate", UTF16BE, []byte{0xd8, 0x00, 0, 'z'}, "\ufffdz", 1},
		{"1252 undefined byte", Windows1252, []byte("caf\xe9\x81"), "café\ufffd", 1},
	} {
		var out bytes.Buffer
		res, err := ToUTF8(context.Background(), bytes.NewReader(tc.data), &out, tc.cs, 0)
		if err != nil || out.String() != tc.want || res.Replaced != tc.replaced || !utf8.Valid(out.Bytes()) {
			t.Errorf("%s: got %q %+v %v, want %q replaced %d", tc.name, out.String(), res, err, tc.want, tc.replaced)
		}
	}
}

func TestToUTF8Limit(t *testing.T) {
	data := encodeWith(t, ShiftJIS, srtText(pick("ja", 0, 12)))
	var out bytes.Buffer
	if res, err := ToUTF8(context.Background(), bytes.NewReader(data), &out, ShiftJIS, int64(len(data))); err != nil || res.Read != int64(len(data)) {
		t.Fatalf("exact limit: %+v %v", res, err)
	}
	out.Reset()
	res, err := ToUTF8(context.Background(), bytes.NewReader(data), &out, ShiftJIS, int64(len(data)-1))
	if !errors.Is(err, ErrTooLarge) || res.Read != int64(len(data)-1) {
		t.Fatalf("one byte over: %+v %v", res, err)
	}
	// An endless source stops at the limit instead of exhausting memory.
	src := &endless{line: encodeWith(t, EUCKR, "안녕하세요\r\n")}
	res, err = ToUTF8(context.Background(), src, io.Discard, EUCKR, 1<<20)
	if !errors.Is(err, ErrTooLarge) || src.read > 1<<20+readChunk {
		t.Fatalf("endless: %+v %v read=%d", res, err, src.read)
	}
}

func TestToUTF8Cancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ToUTF8(ctx, strings.NewReader("abc"), io.Discard, UTF8, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled: %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	src := &cancelAfter{r: &endless{line: encodeWith(t, Big5, "測試\r\n")}, n: 3, cancel: cancel}
	res, err := ToUTF8(ctx, src, io.Discard, Big5, 0)
	if !errors.Is(err, context.Canceled) || res.Read > 5*readChunk {
		t.Fatalf("mid-stream: %+v %v", res, err)
	}
}

type failingWriter struct{ after int }

func (w *failingWriter) Write(p []byte) (int, error) {
	if w.after -= len(p); w.after < 0 {
		return 0, errors.New("disk full")
	}
	return len(p), nil
}

func TestToUTF8Errors(t *testing.T) {
	if _, err := ToUTF8(context.Background(), strings.NewReader("x"), io.Discard, CharsetUnknown, 0); !errors.Is(err, ErrUnsupportedCharset) {
		t.Fatalf("unknown charset: %v", err)
	}
	if _, err := ToUTF8(context.Background(), strings.NewReader("x"), io.Discard, "KOI8-R", 0); !errors.Is(err, ErrUnsupportedCharset) {
		t.Fatalf("unsupported charset: %v", err)
	}
	boom := errors.New("boom")
	r := io.MultiReader(strings.NewReader("abc"), iotest.ErrReader(boom))
	if _, err := ToUTF8(context.Background(), r, io.Discard, UTF8, 0); !errors.Is(err, boom) {
		t.Fatalf("read error: %v", err)
	}
	data := bytes.Repeat([]byte("line\n"), 40000)
	if _, err := ToUTF8(context.Background(), bytes.NewReader(data), &failingWriter{after: 1000}, UTF8, 0); err == nil {
		t.Fatal("write error swallowed")
	}
}

// Detection and conversion together: what a cache builder will do.
func TestDetectThenConvert(t *testing.T) {
	for _, cs := range []Charset{GB18030, Big5, ShiftJIS, EUCJP, EUCKR, Windows1252, UTF16LE} {
		lang := map[Charset]string{GB18030: "zh-Hans", Big5: "zh-Hant", ShiftJIS: "ja", EUCJP: "ja", EUCKR: "ko", Windows1252: "western", UTF16LE: "ko"}[cs]
		text := assText(pick(lang, 2, 12))
		data := encodeWith(t, cs, text)
		got, _, err := DetectCharset(context.Background(), bytes.NewReader(data), 0)
		if err != nil || got != cs {
			t.Fatalf("%s: detected %s %v", cs, got, err)
		}
		var out bytes.Buffer
		if _, err := ToUTF8(context.Background(), bytes.NewReader(data), &out, got, 0); err != nil || out.String() != text {
			t.Fatalf("%s: convert %v", cs, err)
		}
	}
}
