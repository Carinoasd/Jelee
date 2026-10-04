//go:build linux || windows

package scan

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/probe"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"golang.org/x/text/encoding/japanese"
)

const inspectTrackID = "00000000-0000-4000-8000-0000000000aa"

// shiftJISSubtitle returns an SRT whose Japanese text is Shift_JIS encoded.
func shiftJISSubtitle(t *testing.T) []byte {
	t.Helper()
	lines := []string{"一体何を言っているんだ？全然わからないよ。", "暗くなる前にここを離れなければならない。", "心配しないで、きっと大丈夫だから。", "こんなに美しい景色は初めて見た。"}
	var text bytes.Buffer
	for i, line := range lines {
		fmt.Fprintf(&text, "%d\r\n00:00:0%d,000 --> 00:00:0%d,500\r\n%s\r\n\r\n", i+1, i, i, line)
	}
	encoded, err := japanese.ShiftJIS.NewEncoder().Bytes(text.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func inspectTarget(t *testing.T, root, relative, kind, format string) domain.SidecarInspectionTarget {
	t.Helper()
	info, err := os.Stat(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil {
		t.Fatal(err)
	}
	return domain.SidecarInspectionTarget{ID: inspectTrackID, Kind: kind, Format: format, RootPath: root, RelativePath: relative, Size: info.Size(), ModifiedUnixNano: info.ModTime().UnixNano()}
}

func writeInspectFile(t *testing.T, root, relative string, data []byte) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSidecarInspectorCharsetFingerprintAndUnchangedFile(t *testing.T) {
	root := t.TempDir()
	subtitle := shiftJISSubtitle(t)
	writeInspectFile(t, root, "Movie/Subs/Movie.ja.srt", subtitle)
	writeInspectFile(t, root, "Movie/Movie.en.srt", []byte("1\r\n00:00:01,000 --> 00:00:02,000\r\nHello there.\r\n"))
	writeInspectFile(t, root, "Movie/Movie.ja.flac", bytes.Repeat([]byte{0x80, 0x81, 0x82}, 100000))
	// A VobSub ".sub" is binary: no charset, but still a fingerprint.
	writeInspectFile(t, root, "Movie/Movie.sub", append([]byte{0, 0, 1, 0xba}, bytes.Repeat([]byte{0}, 4096)...))
	before := sha256.Sum256(subtitle)
	target := inspectTarget(t, root, "Movie/Subs/Movie.ja.srt", domain.SidecarKindSubtitle, "srt")
	ctx := context.Background()
	got, err := SidecarInspector{}.InspectSidecar(ctx, target)
	if err != nil || got.Charset != "Shift_JIS" || got.ID != inspectTrackID || got.Size != target.Size || got.ModifiedUnixNano != target.ModifiedUnixNano {
		t.Fatalf("subtitle inspection %v %+v", err, got)
	}
	file, err := os.Open(filepath.Join(root, "Movie/Subs/Movie.ja.srt"))
	if err != nil {
		t.Fatal(err)
	}
	digest, err := probe.EdgeFingerprint(ctx, file, target.Size)
	_ = file.Close()
	if err != nil || hex.EncodeToString(got.Fingerprint) != digest || !domain.ValidSidecarInspection(got) {
		t.Fatal("fingerprint is not the shared edge fingerprint", err)
	}
	if after, err := os.ReadFile(filepath.Join(root, "Movie/Subs/Movie.ja.srt")); err != nil || sha256.Sum256(after) != before {
		t.Fatal("inspection changed the original subtitle", err)
	}
	ascii, err := SidecarInspector{}.InspectSidecar(ctx, inspectTarget(t, root, "Movie/Movie.en.srt", domain.SidecarKindSubtitle, "srt"))
	if err != nil || ascii.Charset != "UTF-8" {
		t.Fatalf("ASCII subtitle %v %q", err, ascii.Charset)
	}
	audio, err := SidecarInspector{}.InspectSidecar(ctx, inspectTarget(t, root, "Movie/Movie.ja.flac", domain.SidecarKindAudio, "flac"))
	if err != nil || audio.Charset != "" || len(audio.Fingerprint) != domain.SidecarFingerprintBytes {
		t.Fatalf("audio inspection %v %+v", err, audio)
	}
	vobsub, err := SidecarInspector{}.InspectSidecar(ctx, inspectTarget(t, root, "Movie/Movie.sub", domain.SidecarKindSubtitle, "sub"))
	if err != nil || vobsub.Charset != "" || len(vobsub.Fingerprint) != domain.SidecarFingerprintBytes {
		t.Fatalf("binary subtitle %v %+v", err, vobsub)
	}
}

// The fingerprint reads at most 64 KiB from each end, so a 1 TiB sparse
// audio track is inspected as fast as a small one.
func TestSidecarInspectorDoesNotReadWholeLargeFile(t *testing.T) {
	root := t.TempDir()
	full := filepath.Join(root, "Big.en.mka")
	file, err := os.Create(full)
	if err != nil {
		t.Fatal(err)
	}
	if err = file.Truncate(1 << 40); err != nil {
		_ = file.Close()
		t.Skip("sparse files unavailable:", err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	target := inspectTarget(t, root, "Big.en.mka", domain.SidecarKindAudio, "mka")
	started := time.Now()
	got, err := SidecarInspector{}.InspectSidecar(context.Background(), target)
	if err != nil || len(got.Fingerprint) != domain.SidecarFingerprintBytes {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("1 TiB sidecar took %v: the whole file was read", elapsed)
	}
}

func TestSidecarInspectorRefusesChangedEscapedAndSpecialFiles(t *testing.T) {
	root := t.TempDir()
	writeInspectFile(t, root, "Movie.en.srt", []byte("1\r\n00:00:01,000 --> 00:00:02,000\r\nHello.\r\n"))
	target := inspectTarget(t, root, "Movie.en.srt", domain.SidecarKindSubtitle, "srt")
	ctx := context.Background()
	changed := target
	changed.Size++
	if _, err := (SidecarInspector{}).InspectSidecar(ctx, changed); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("changed size inspected", err)
	}
	changed = target
	changed.ModifiedUnixNano++
	if _, err := (SidecarInspector{}).InspectSidecar(ctx, changed); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("changed mtime inspected", err)
	}
	gone := target
	gone.RelativePath = "Missing.en.srt"
	if _, err := (SidecarInspector{}).InspectSidecar(ctx, gone); err == nil {
		t.Fatal("missing file inspected")
	}
	outside := t.TempDir()
	writeInspectFile(t, outside, "Secret.en.srt", []byte("secret"))
	if err := os.Symlink(filepath.Join(outside, "Secret.en.srt"), filepath.Join(root, "Link.en.srt")); err == nil {
		escape := target
		escape.RelativePath = "Link.en.srt"
		escape.Size = 6
		if _, err := (SidecarInspector{}).InspectSidecar(ctx, escape); err == nil {
			t.Fatal("symlink out of the root was inspected")
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "Dir.en.srt"), 0o700); err != nil {
		t.Fatal(err)
	}
	directory := target
	directory.RelativePath = "Dir.en.srt"
	if _, err := (SidecarInspector{}).InspectSidecar(ctx, directory); err == nil {
		t.Fatal("directory inspected")
	}
	for name, bad := range map[string]domain.SidecarInspectionTarget{
		"relative root": {ID: inspectTrackID, RootPath: "media", RelativePath: "Movie.en.srt"},
		"dot dot":       {ID: inspectTrackID, RootPath: root, RelativePath: "../Movie.en.srt"},
		"absolute":      {ID: inspectTrackID, RootPath: root, RelativePath: "/etc/passwd"},
		"id":            {ID: "x", RootPath: root, RelativePath: "Movie.en.srt"},
	} {
		if _, err := (SidecarInspector{}).InspectSidecar(ctx, bad); err == nil {
			t.Error("accepted", name)
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := (SidecarInspector{}).InspectSidecar(cancelled, target); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled inspection", err)
	}
}
