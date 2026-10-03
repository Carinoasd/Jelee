package nfo

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// BenchmarkReadDocument parses the in-memory golden movie document (BOM, CRLF,
// every mapped field) so the number reflects XML parsing and field mapping
// without disk I/O.
func BenchmarkReadDocument(b *testing.B) {
	original := append([]byte{0xef, 0xbb, 0xbf}, []byte(strings.ReplaceAll(goldenMovie, "\n", "\r\n"))...)
	ctx := context.Background()
	b.ReportAllocs()
	b.SetBytes(int64(len(original)))
	for b.Loop() {
		document, err := Read(ctx, bytes.NewReader(original), DefaultMaxBytes)
		if err != nil || document.Root != "movie" {
			b.Fatal("parse golden movie", err)
		}
	}
}
