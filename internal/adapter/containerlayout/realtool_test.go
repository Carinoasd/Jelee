//go:build jelee_fixture_tools

package containerlayout

import (
	"context"
	"crypto/sha256"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// TestReadRealMuxerOutput classifies files written by ffmpeg with and
// without faststart, fragmented, and Matroska with Cues at either end. The
// tool is only used to produce fixtures; Read never runs anything. Each
// file's SHA-256 must be unchanged by the read (G10.10).
func TestReadRealMuxerOutput(t *testing.T) {
	ffmpeg := os.Getenv("JELEE_FFMPEG")
	if ffmpeg == "" {
		var err error
		if ffmpeg, err = exec.LookPath("ffmpeg"); err != nil {
			if os.Getenv("JELEE_REQUIRE_MEDIA_TOOL_TESTS") == "true" {
				t.Fatal("ffmpeg not found")
			}
			t.Skip("ffmpeg not found")
		}
	}
	dir := t.TempDir()
	input := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y", "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=24", "-t", "3", "-c:v", "mpeg4", "-threads", "1", "-g", "24"}
	for _, tc := range []struct {
		name, contentType string
		args              []string
		want              domain.ContainerLayout
		index             bool
	}{
		{"plain.mp4", "video/mp4", nil, domain.LayoutMoovAtEnd, true},
		{"faststart.mp4", "video/mp4", []string{"-movflags", "+faststart"}, domain.LayoutMoovBeforeMdat, true},
		{"fragmented.mp4", "video/mp4", []string{"-movflags", "frag_keyframe+empty_moov"}, domain.LayoutFragmented, true},
		{"plain.mov", "video/quicktime", nil, domain.LayoutMoovAtEnd, true},
		{"plain.mkv", "video/x-matroska", nil, domain.LayoutCuesAfterClusters, true},
		{"front-cues.mkv", "video/x-matroska", []string{"-reserve_index_space", "4096"}, domain.LayoutCuesBeforeClusters, true},
		{"live.mkv", "video/x-matroska", []string{"-live", "1"}, domain.LayoutNoCues, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := filepath.Join(dir, tc.name)
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			args := append(append(append([]string(nil), input...), tc.args...), target)
			if out, err := exec.CommandContext(ctx, ffmpeg, args...).CombinedOutput(); err != nil {
				t.Skipf("ffmpeg cannot write %s here: %v %s", tc.name, err, out)
			}
			before := digest(t, target)
			file, err := os.Open(target)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			info, err := file.Stat()
			if err != nil {
				t.Fatal(err)
			}
			got, err := Read(ctx, file, info.Size(), tc.contentType)
			if err != nil {
				t.Fatal(err)
			}
			if got.Layout != tc.want || (got.IndexLength > 0) != tc.index {
				t.Fatalf("got %+v, want %s (index %t)", got, tc.want, tc.index)
			}
			if digest(t, target) != before {
				t.Fatal("reading the layout changed the file")
			}
		})
	}
}

func digest(t *testing.T, name string) [32]byte {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(data)
}
