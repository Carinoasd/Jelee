package subtitles

import (
	"bytes"
	"context"
	"testing"
)

// BenchmarkDetectCharset runs detection over fixed SRT samples (48 dialogue
// lines each) covering the statistical path for CJK legacy encodings plus the
// UTF-8 fast path. Inputs are encoded once outside the timed loop.
func BenchmarkDetectCharset(b *testing.B) {
	samples := []struct {
		name string
		cs   Charset
		lang string
	}{
		{"UTF8", UTF8, "zh-Hans"},
		{"GB18030", GB18030, "zh-Hans"},
		{"Big5", Big5, "zh-Hant"},
		{"ShiftJIS", ShiftJIS, "ja"},
		{"EUCKR", EUCKR, "ko"},
	}
	ctx := context.Background()
	for _, s := range samples {
		data := encodeWith(b, s.cs, srtText(pick(s.lang, 0, 48)))
		b.Run(s.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				got, _, err := DetectCharset(ctx, bytes.NewReader(data), DefaultDetectLimit)
				if err != nil || got != s.cs {
					b.Fatalf("detected %s, want %s (%v)", got, s.cs, err)
				}
			}
		})
	}
}
