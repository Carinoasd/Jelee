package ignoresource

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/platform/ignore"
)

// Both cases include native root/directory/file opening, two full reads and
// hashes per present rule, final revalidation and handle cleanup. Warm means
// only that immutable compiled Programs are already cached.
func BenchmarkResolver(b *testing.B) {
	for _, warm := range []bool{false, true} {
		name := "cold"
		if warm {
			name = "warm"
		}
		b.Run(name, func(b *testing.B) {
			root := filepath.Clean(b.TempDir())
			if err := os.Mkdir(filepath.Join(root, "child"), 0700); err != nil {
				b.Fatal(err)
			}
			for path, text := range map[string]string{".jeleeignore": "*.tmp\n", "child/.jeleeignore": "!keep.tmp\n"} {
				if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(path)), []byte(text), 0600); err != nil {
					b.Fatal(err)
				}
			}
			r := NewResolver()
			if _, err := r.Evaluate(context.Background(), root, "child/keep.tmp", ignore.File, ignore.Options{}); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if !warm {
					r = NewResolver()
				}
				observation, err := r.Evaluate(context.Background(), root, "child/keep.tmp", ignore.File, ignore.Options{})
				if err != nil || observation.Match.Outcome != ignore.Include {
					b.Fatalf("evaluation failed: %v", err)
				}
			}
		})
	}
}
