package images

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func imageSourceFixture(t *testing.T) (domain.LocalImageSource, string) {
	t.Helper()
	base := imageSourceTestBase(t)
	root, scratch := filepath.Join(base, "media"), filepath.Join(base, "scratch")
	for _, directory := range []string{root, scratch, filepath.Join(root, "movie")} {
		if err := os.Mkdir(directory, 0700); err != nil {
			t.Fatal("create image source fixture")
		}
	}
	imageWrite(t, filepath.Join(root, "movie", "Film.mkv"), "unchanged original media")
	return domain.LocalImageSource{ItemID: "10000000-0000-4000-8000-000000000001", LibraryID: "10000000-0000-4000-8000-000000000002", SourceID: "10000000-0000-4000-8000-000000000003", RootID: "10000000-0000-4000-8000-000000000004", RootPath: root, MediaPath: "movie/Film.mkv"}, scratch
}

func imageWrite(t *testing.T, filename, text string) {
	t.Helper()
	if err := os.WriteFile(filename, []byte(text), 0600); err != nil {
		t.Fatal("write owned image fixture")
	}
}

func imageScratchEmpty(t *testing.T, scratch string) {
	t.Helper()
	entries, err := os.ReadDir(scratch)
	if err != nil || len(entries) != 0 {
		t.Fatal("image staging did not clean its files")
	}
}

func TestImageSourceStagesSelectedBytesAndCleans(t *testing.T) {
	source, scratch := imageSourceFixture(t)
	parent := filepath.Join(source.RootPath, "movie")
	imageWrite(t, filepath.Join(parent, "POSTER.JPG"), "fallback")
	imageWrite(t, filepath.Join(parent, "fIlM-pOsTeR.PnG"), "preferred")
	imageWrite(t, filepath.Join(parent, "Film-poster.jpeg"), "jpeg preferred over png")
	original, err := os.Stat(filepath.Join(parent, "Film-poster.jpeg"))
	if err != nil {
		t.Fatal("stat original image fixture")
	}
	first, err := stageLocalPrimary(context.Background(), source, scratch, 1024)
	if err != nil {
		t.Fatal("stage selected image", err)
	}
	t.Cleanup(func() { _ = first.Close() })
	data, err := io.ReadAll(first.Reader())
	if err != nil || string(data) != "jpeg preferred over png" || first.Size() != int64(len(data)) || first.Key() == ([32]byte{}) {
		t.Fatal("stage selected wrong image or size")
	}
	if _, err := first.Reader().Seek(0, io.SeekStart); err != nil {
		t.Fatal("rewind staging")
	}
	again, err := io.ReadAll(first.Reader())
	if err != nil || !bytes.Equal(data, again) || first.Verify(context.Background()) != nil {
		t.Fatal("staging did not preserve immutable input")
	}
	info, err := first.file.Stat()
	if err != nil || !info.Mode().IsRegular() || runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatal("staging is not a private regular file")
	}
	second, err := stageLocalPrimary(context.Background(), source, scratch, 1024)
	if err != nil {
		t.Fatal("repeat staging", err)
	}
	if second.Key() != first.Key() {
		t.Error("unchanged source changed cache key")
	}
	if second.Close() != nil || first.Close() != nil || first.Close() != nil {
		t.Fatal("staging close failed or is not idempotent")
	}
	imageScratchEmpty(t, scratch)
	after, err := os.Stat(filepath.Join(parent, "Film-poster.jpeg"))
	if err != nil || !sameImageFile(original, after) {
		t.Fatal("staging changed original image")
	}
	video, err := os.ReadFile(filepath.Join(parent, "Film.mkv"))
	if err != nil || string(video) != "unchanged original media" {
		t.Fatal("staging changed original media")
	}
	if text := fmt.Sprintf("%v %#v", first, source); strings.Contains(text, source.RootPath) || strings.Contains(text, source.MediaPath) {
		t.Fatal("source diagnostics expose private paths")
	}
}

func TestImageSourceSizeBoundsAndMissingCandidate(t *testing.T) {
	source, scratch := imageSourceFixture(t)
	if value, err := stageLocalPrimary(context.Background(), source, scratch, 4); value != nil || err != domain.ErrNotFound {
		t.Fatal("absent poster should not produce a source")
	}
	poster := filepath.Join(source.RootPath, "movie", "poster.jpg")
	imageWrite(t, poster, "1234")
	value, err := stageLocalPrimary(context.Background(), source, scratch, 4)
	if err != nil || value.Size() != 4 {
		t.Fatal("exact source bound rejected", err)
	}
	if value.Close() != nil {
		t.Fatal("close exact source")
	}
	for _, contents := range []string{"12345", ""} {
		imageWrite(t, poster, contents)
		if value, err := stageLocalPrimary(context.Background(), source, scratch, 4); value != nil || err != domain.ErrImageTooLarge {
			t.Fatal("invalid source byte size accepted")
		}
		imageScratchEmpty(t, scratch)
	}
}

func TestImageSourceRejectsOverlappingScratch(t *testing.T) {
	source, scratch := imageSourceFixture(t)
	imageWrite(t, filepath.Join(source.RootPath, "movie", "poster.jpg"), "image")
	for _, target := range []string{source.RootPath, filepath.Join(source.RootPath, "movie"), filepath.Dir(source.RootPath)} {
		value, err := stageLocalPrimary(context.Background(), source, target, 100)
		if value != nil || err != domain.ErrImageUnavailable {
			t.Fatal("overlapping scratch accepted")
		}
	}
	imageScratchEmpty(t, scratch)
	if runtime.GOOS != "windows" {
		if err := os.Chmod(scratch, 0755); err != nil {
			t.Fatal("set test scratch permission")
		}
		if value, err := stageLocalPrimary(context.Background(), source, scratch, 100); value != nil || err != domain.ErrImageUnavailable {
			t.Fatal("nonprivate scratch accepted")
		}
	}
}

func TestImageSourceInvalidInputAndCancellation(t *testing.T) {
	source, scratch := imageSourceFixture(t)
	imageWrite(t, filepath.Join(source.RootPath, "movie", "poster.jpg"), "image")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if value, err := stageLocalPrimary(ctx, source, scratch, 100); value != nil || err != context.Canceled {
		t.Fatal("cancelled source admitted")
	}
	for _, relative := range []string{"../poster.jpg", "/poster.jpg", "movie\\Film.mkv", "movie/../Film.mkv", "movie/Film.mkv\x00"} {
		invalid := source
		invalid.MediaPath = relative
		if value, err := stageLocalPrimary(context.Background(), invalid, scratch, 100); value != nil || err != domain.ErrInvalid {
			t.Fatal("unsafe internal source accepted")
		}
	}
	if value, err := stageLocalPrimary(nil, source, scratch, 100); value != nil || err != domain.ErrInvalid {
		t.Fatal("nil context accepted")
	}
	active, stop := context.WithCancel(context.Background())
	value, err := stageLocalPrimary(active, source, scratch, 100)
	if err != nil {
		t.Fatal("stage before cancellation", err)
	}
	stop()
	var one [1]byte
	if _, err := value.Reader().Read(one[:]); err != context.Canceled {
		t.Fatal("staging read ignored cancellation")
	}
	if value.Verify(active) != context.Canceled || value.Close() != nil {
		t.Fatal("cancelled verify or cleanup differs")
	}
	imageScratchEmpty(t, scratch)
}

func TestImageSourceVerifyRejectsReplacementAndRestoredTimestampWrites(t *testing.T) {
	for _, mode := range []string{"rewrite", "replace", "new_preferred", "media_replace", "parent_replace"} {
		t.Run(mode, func(t *testing.T) {
			source, scratch := imageSourceFixture(t)
			parent := filepath.Join(source.RootPath, "movie")
			poster := filepath.Join(parent, "poster.jpg")
			imageWrite(t, poster, "old bytes")
			value, err := stageLocalPrimary(context.Background(), source, scratch, 100)
			if err != nil {
				t.Fatal("stage before mutation", err)
			}
			t.Cleanup(func() { _ = value.Close() })
			switch mode {
			case "rewrite":
				imageWrite(t, poster, "new bytes")
				if err := os.Chtimes(poster, value.observed.image.ModTime(), value.observed.image.ModTime()); err != nil {
					t.Fatal("restore image timestamp")
				}
			case "replace":
				if err := os.Rename(poster, poster+".old"); err != nil {
					t.Fatal("replace image fixture")
				}
				imageWrite(t, poster, "old bytes")
				if os.Chtimes(poster, value.observed.image.ModTime(), value.observed.image.ModTime()) != nil || os.Chtimes(parent, value.observed.parent.ModTime(), value.observed.parent.ModTime()) != nil {
					t.Fatal("restore replacement timestamps")
				}
			case "new_preferred":
				imageWrite(t, filepath.Join(parent, "Film-poster.png"), "preferred")
			case "media_replace":
				if err := os.Rename(filepath.Join(parent, "Film.mkv"), filepath.Join(parent, "old.mkv")); err != nil {
					t.Fatal("replace media fixture")
				}
				imageWrite(t, filepath.Join(parent, "Film.mkv"), "unchanged original media")
			case "parent_replace":
				if err := os.Rename(parent, parent+"-old"); err != nil {
					t.Fatal("replace parent fixture")
				}
				if err := os.Mkdir(parent, 0700); err != nil {
					t.Fatal("create replacement parent")
				}
				imageWrite(t, filepath.Join(parent, "Film.mkv"), "unchanged original media")
				imageWrite(t, poster, "old bytes")
			}
			if err := value.Verify(context.Background()); err != domain.ErrImageUnavailable {
				t.Fatal("changed source remained valid", err)
			}
			data, err := io.ReadAll(value.Reader())
			if err != nil || string(data) != "old bytes" {
				t.Fatal("source mutation changed private staging bytes")
			}
			if value.Close() != nil {
				t.Fatal("mutation cleanup failed")
			}
			imageScratchEmpty(t, scratch)
		})
	}
}

func TestImageSourceRejectsAmbiguityAndNonregularCandidates(t *testing.T) {
	source, scratch := imageSourceFixture(t)
	parent := filepath.Join(source.RootPath, "movie")
	if err := os.Mkdir(filepath.Join(parent, "poster.jpg"), 0700); err != nil {
		t.Fatal("create nonregular candidate")
	}
	if value, err := stageLocalPrimary(context.Background(), source, scratch, 100); value != nil || err != domain.ErrImageUnavailable {
		t.Fatal("nonregular candidate accepted")
	}
	if err := os.Remove(filepath.Join(parent, "poster.jpg")); err != nil {
		t.Fatal("remove owned empty candidate directory")
	}
	if runtime.GOOS == "windows" {
		t.Skip("case-distinct filesystem ambiguity is covered by the native Linux run")
	}
	imageWrite(t, filepath.Join(parent, "poster.jpg"), "one")
	imageWrite(t, filepath.Join(parent, "POSTER.JPG"), "two")
	imageWrite(t, filepath.Join(parent, "Film-poster.jpg"), "preferred")
	if value, err := stageLocalPrimary(context.Background(), source, scratch, 100); value != nil || err != domain.ErrImageUnavailable {
		t.Fatal("ambiguous lower-priority candidate accepted")
	}
	imageScratchEmpty(t, scratch)
}

func TestImageSourceRejectsSymlinkAndBoundsStreamingCopy(t *testing.T) {
	source, scratch := imageSourceFixture(t)
	parent := filepath.Join(source.RootPath, "movie")
	original := filepath.Join(parent, "original.png")
	imageWrite(t, original, "12345")
	if err := os.Symlink(original, filepath.Join(parent, "poster.jpg")); err == nil {
		if value, err := stageLocalPrimary(context.Background(), source, scratch, 100); value != nil || err != domain.ErrImageUnavailable {
			t.Fatal("symlink candidate accepted")
		}
	} else if runtime.GOOS != "windows" {
		t.Fatal("create symlink fixture")
	} else {
		t.Log("Windows symlink creation unavailable; native Linux case is required")
	}
	input, err := os.Open(original)
	if err != nil {
		t.Fatal("open bounded copy fixture")
	}
	defer input.Close()
	var output bytes.Buffer
	if n, err := copyImageBytes(context.Background(), &output, input, 4); n != 5 || err != domain.ErrImageTooLarge || output.Len() != 5 {
		t.Fatal("copy did not stop at limit plus one")
	}
	if _, err := input.Seek(0, io.SeekStart); err != nil {
		t.Fatal("rewind bounded copy fixture")
	}
	if _, err := copyImageBytes(context.Background(), imageShortWriter{}, input, 5); err != domain.ErrImageUnavailable {
		t.Fatal("short write was not rejected safely")
	}
	if err := input.Close(); err != nil {
		t.Fatal("close bounded copy fixture")
	}
	var one [1]byte
	_, err = (imageContextFile{context.Background(), input}).Read(one[:])
	if err != domain.ErrImageUnavailable || errors.As(err, new(*os.PathError)) {
		t.Fatal("closed file error exposed private path")
	}
	imageScratchEmpty(t, scratch)
}

type imageShortWriter struct{}

func (imageShortWriter) Write(data []byte) (int, error) { return len(data) - 1, nil }

func TestImageSourceDirectoryLimitRejectsIncompleteSelection(t *testing.T) {
	source, scratch := imageSourceFixture(t)
	parent := filepath.Join(source.RootPath, "movie")
	imageWrite(t, filepath.Join(parent, "poster.jpg"), "image")
	for i := 0; i < imageDirectoryEntries-1; i++ {
		imageWrite(t, filepath.Join(parent, fmt.Sprintf("entry-%05d", i)), "")
	}
	if value, err := stageLocalPrimary(context.Background(), source, scratch, 100); value != nil || err != domain.ErrImageTooLarge {
		t.Fatal("partial oversized directory was treated as a complete selection")
	}
	imageScratchEmpty(t, scratch)
}
