//go:build linux || windows

package scan

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func mp4Box(kind string, payload int) []byte {
	out := make([]byte, 8+payload)
	binary.BigEndian.PutUint32(out, uint32(8+payload))
	copy(out[4:], kind)
	return out
}

func layoutTarget(t *testing.T, root, relative, contentType string) domain.SourceLayoutTarget {
	t.Helper()
	info, err := os.Stat(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil {
		t.Fatal(err)
	}
	return domain.SourceLayoutTarget{SourceID: inspectTrackID, ContentType: contentType, RootPath: root, RelativePath: relative, Size: info.Size(), ModifiedUnixNano: info.ModTime().UnixNano()}
}

func TestLayoutInspectorReadsWithoutChangingTheFile(t *testing.T) {
	root := t.TempDir()
	data := append(append(mp4Box("ftyp", 16), mp4Box("mdat", 4096)...), mp4Box("moov", 64)...)
	writeInspectFile(t, root, "Movie/Movie.mp4", data)
	target := layoutTarget(t, root, "Movie/Movie.mp4", "video/mp4")
	before := sha256.Sum256(data)
	got, err := LayoutInspector{}.InspectSourceLayout(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	want := domain.SourceLayoutInspection{SourceID: inspectTrackID, Size: target.Size, ModifiedUnixNano: target.ModifiedUnixNano,
		SourceLayout: domain.SourceLayout{Layout: domain.LayoutMoovAtEnd, IndexOffset: 24 + 4104, IndexLength: 72}}
	if got != want || !domain.ValidSourceLayoutInspection(got) {
		t.Fatalf("got %+v", got)
	}
	after, err := os.ReadFile(filepath.Join(root, "Movie", "Movie.mp4"))
	if err != nil || sha256.Sum256(after) != before {
		t.Fatal("inspection changed the file")
	}
	info, err := os.Stat(filepath.Join(root, "Movie", "Movie.mp4"))
	if err != nil || info.ModTime().UnixNano() != target.ModifiedUnixNano {
		t.Fatal("inspection touched the file")
	}
}

func TestLayoutInspectorLargeSparseFileReadsHeadersOnly(t *testing.T) {
	root := t.TempDir()
	file, err := os.Create(filepath.Join(root, "Big.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	// ftyp, then an mdat of almost 1 TiB with a 64-bit size, then moov.
	const mdat = int64(1) << 40
	header := append(mp4Box("ftyp", 16), 0, 0, 0, 1, 'm', 'd', 'a', 't')
	header = binary.BigEndian.AppendUint64(header, uint64(mdat))
	if _, err = file.Write(header); err != nil {
		t.Fatal(err)
	}
	if _, err = file.WriteAt(mp4Box("moov", 32), 24+mdat); err != nil {
		_ = file.Close()
		t.Skip("sparse files unavailable:", err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	got, err := LayoutInspector{}.InspectSourceLayout(context.Background(), layoutTarget(t, root, "Big.mp4", "video/mp4"))
	if err != nil || got.Layout != domain.LayoutMoovAtEnd || got.IndexOffset != 24+mdat || got.IndexLength != 40 {
		t.Fatal(got, err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("1 TiB source took %v: the media data was read", elapsed)
	}
}

func TestLayoutInspectorRefusesChangedEscapedAndUnsupported(t *testing.T) {
	root := t.TempDir()
	writeInspectFile(t, root, "Movie.mkv", []byte("not matroska at all"))
	target := layoutTarget(t, root, "Movie.mkv", "video/x-matroska")
	ctx := context.Background()
	// Unreadable structure is a result, not an error.
	if got, err := (LayoutInspector{}).InspectSourceLayout(ctx, target); err != nil || got.Layout != domain.LayoutUnknown {
		t.Fatal("garbage file", got, err)
	}
	changed := target
	changed.Size++
	if _, err := (LayoutInspector{}).InspectSourceLayout(ctx, changed); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("changed size inspected", err)
	}
	changed = target
	changed.ModifiedUnixNano++
	if _, err := (LayoutInspector{}).InspectSourceLayout(ctx, changed); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("changed mtime inspected", err)
	}
	gone := target
	gone.RelativePath = "Missing.mkv"
	if _, err := (LayoutInspector{}).InspectSourceLayout(ctx, gone); err == nil {
		t.Fatal("missing file inspected")
	}
	outside := t.TempDir()
	writeInspectFile(t, outside, "Secret.mkv", []byte("secret"))
	if err := os.Symlink(filepath.Join(outside, "Secret.mkv"), filepath.Join(root, "Link.mkv")); err == nil {
		escape := target
		escape.RelativePath, escape.Size = "Link.mkv", 6
		if _, err := (LayoutInspector{}).InspectSourceLayout(ctx, escape); err == nil {
			t.Fatal("symlink out of the root was inspected")
		}
	}
	for name, bad := range map[string]domain.SourceLayoutTarget{
		"relative root": {SourceID: inspectTrackID, ContentType: "video/mp4", RootPath: "media", RelativePath: "Movie.mkv"},
		"dot dot":       {SourceID: inspectTrackID, ContentType: "video/mp4", RootPath: root, RelativePath: "../Movie.mkv"},
		"absolute":      {SourceID: inspectTrackID, ContentType: "video/mp4", RootPath: root, RelativePath: "/etc/passwd"},
		"id":            {SourceID: "x", ContentType: "video/mp4", RootPath: root, RelativePath: "Movie.mkv"},
		"avi":           {SourceID: inspectTrackID, ContentType: "video/x-msvideo", RootPath: root, RelativePath: "Movie.mkv"},
	} {
		if _, err := (LayoutInspector{}).InspectSourceLayout(ctx, bad); err == nil {
			t.Error(name, "inspected")
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := (LayoutInspector{}).InspectSourceLayout(cancelled, target); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled inspection", err)
	}
}
