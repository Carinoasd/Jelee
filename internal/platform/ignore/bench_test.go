package ignore

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func benchmarkRules(count int) string {
	var text strings.Builder
	for i := range count - 1 {
		fmt.Fprintf(&text, "/unmatched-%04d.nfo\n", i)
	}
	text.WriteString("*.mkv\n")
	return text.String()
}

func BenchmarkCompile(b *testing.B) {
	for _, count := range []int{1, 100, 1000} {
		b.Run(fmt.Sprintf("rules-%d", count), func(b *testing.B) {
			text := []byte(benchmarkRules(count))
			sources := []Source{{Path: ".jeleeignore", Text: text}}
			b.SetBytes(int64(len(text)))
			b.ReportAllocs()
			for b.Loop() {
				if p, err := Compile(context.Background(), sources, Options{}); err != nil || p == nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkEvaluate(b *testing.B) {
	for _, count := range []int{1, 100, 1000} {
		b.Run(fmt.Sprintf("rules-%d", count), func(b *testing.B) {
			p := contractCompile(b, benchmarkRules(count))
			b.ReportAllocs()
			for b.Loop() {
				got, err := p.Evaluate(context.Background(), "movie.mkv", File)
				if err != nil || got.Outcome != Exclude || got.Line != count {
					b.Fatal("unexpected match", err)
				}
			}
		})
	}
	b.Run("maximum-path-bytes", func(b *testing.B) {
		p := contractCompile(b, "*.mkv")
		name := strings.Repeat("a", MaxPathBytes-len(".mkv")) + ".mkv"
		b.ReportAllocs()
		for b.Loop() {
			got, err := p.Evaluate(context.Background(), name, File)
			if err != nil || got.Outcome != Exclude {
				b.Fatal("unexpected match", err)
			}
		}
	})
	b.Run("128-sources-one-active", func(b *testing.B) {
		sources := contractSources(MaxSources, "*.mkv")
		p, err := Compile(context.Background(), sources, Options{})
		if err != nil {
			b.Fatal(err)
		}
		b.ReportAllocs()
		for b.Loop() {
			got, err := p.Evaluate(context.Background(), "d064/movie.mkv", File)
			if err != nil || got.Outcome != Exclude || got.Source != "d064/.jeleeignore" {
				b.Fatal("unexpected source activation", err)
			}
		}
	})
	b.Run("maximum-depth", func(b *testing.B) {
		p := contractCompile(b, "**/*.mkv")
		name := strings.Repeat("a/", MaxPathComponents-1) + "movie.mkv"
		b.ReportAllocs()
		for b.Loop() {
			got, err := p.Evaluate(context.Background(), name, File)
			if err != nil || got.Outcome != Exclude {
				b.Fatal("unexpected match", err)
			}
		}
	})
}
