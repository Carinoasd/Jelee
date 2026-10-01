package nfo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// One operation visits all 100 distinct on-disk files. Preparation and final
// byte/hash verification are outside the timed loop. This measures local reads
// and parsing, not DB lookup or the production worker's final second read.
func BenchmarkObservedHundredFiles(b *testing.B) {
	root := b.TempDir()
	sources := make([]domain.NFOSource, 100)
	hashes := make([]string, 100)
	var total int64
	for i := range sources {
		body := []byte(fmt.Sprintf("<movie><title>Fixture %03d</title><plot>%s</plot></movie>", i, strings.Repeat("safe generated text ", 820)))
		name := fmt.Sprintf("fixture-%03d.nfo", i)
		if err := os.WriteFile(filepath.Join(root, name), body, 0600); err != nil {
			b.Fatal(err)
		}
		sum := sha256.Sum256(body)
		hashes[i] = hex.EncodeToString(sum[:])
		total += int64(len(body))
		sources[i] = domain.NFOSource{RootPath: root, RelativePath: name}
	}
	for _, parse := range []bool{false, true} {
		name := "ReadHash"
		if parse {
			name = "ReadHashParse"
		}
		b.Run(name, func(b *testing.B) {
			base, err := NewSummaryReader(domain.NFODefaultSourceBytes)
			if err != nil {
				b.Fatal(err)
			}
			reader, err := NewObservedReader(base)
			if err != nil {
				b.Fatal(err)
			}
			b.SetBytes(total)
			b.ReportAllocs()
			b.ResetTimer()
			for n := 0; n < b.N; n++ {
				for i, source := range sources {
					read, err := reader.Read(context.Background(), source)
					if err != nil || read.Stamp().SHA256 != hashes[i] {
						b.Fatal("file read/full hash mismatch")
					}
					if parse {
						summary, err := read.Parse(context.Background())
						if err != nil || summary.Status != domain.NFOStatusValid {
							b.Fatal("file parse failure")
						}
					}
				}
			}
			b.StopTimer()
			stats := reader.Stats()
			want := uint64(b.N) * 100
			wantParse := uint64(0)
			if parse {
				wantParse = want
			}
			if stats.ReadCalls != want || stats.CompletedReads != want || stats.HashCompletions != want || stats.CompletedReadBytes != uint64(b.N)*uint64(total) || stats.ParseCalls != wantParse || stats.ActiveCalls != 0 {
				b.Fatal("actual file operation counts differ")
			}
			b.ReportMetric(100, "files/op")
			b.ReportMetric(100, "hashes/op")
			b.ReportMetric(100, "reads/op")
			b.ReportMetric(float64(wantParse)/float64(b.N), "parses/op")
		})
	}
	for i, source := range sources {
		body, err := os.ReadFile(filepath.Join(root, source.RelativePath))
		if err != nil {
			b.Fatal(err)
		}
		sum := sha256.Sum256(body)
		if hex.EncodeToString(sum[:]) != hashes[i] {
			b.Fatal("benchmark changed original file")
		}
	}
}
