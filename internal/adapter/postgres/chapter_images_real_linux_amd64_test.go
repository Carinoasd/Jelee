package postgres

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/probe"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/process"
	"github.com/MoYuanCN/Jelee/internal/platform/sandbox"
)

// chapterTrackFixture writes an MP4 whose MJPEG track is the QuickTime
// chapter picture track: the pinned ffmpeg muxes the video, the JPEG
// pictures (copied, one packet each) and text chapters, then the test points
// the main track's chapter reference at the picture track and disables the
// other references, which is how chapter-art writers lay the file out.
func chapterTrackFixture(t *testing.T, ffmpeg, scratch, out string, pictures [][]byte) {
	t.Helper()
	var stream bytes.Buffer
	for _, p := range pictures {
		stream.Write(p)
	}
	frames := filepath.Join(scratch, "thumbs.mjpeg")
	meta := filepath.Join(scratch, "chapters.txt")
	text := ";FFMETADATA1\n"
	for i := range pictures {
		text += fmt.Sprintf("[CHAPTER]\nTIMEBASE=1/1000\nSTART=%d\nEND=%d\ntitle=C%d\n", i*2000, i*2000+2000, i)
	}
	if os.WriteFile(frames, stream.Bytes(), 0600) != nil || os.WriteFile(meta, []byte(text), 0600) != nil {
		t.Fatal("write chapter fixture inputs")
	}
	muxed := out + ".muxed.mp4"
	coverFFmpeg(t, ffmpeg, "-f", "lavfi", "-i", fmt.Sprintf("testsrc2=size=160x90:rate=10:duration=%d", 2*len(pictures)), "-f", "mjpeg", "-framerate", "0.5", "-i", frames,
		"-i", meta, "-map", "0:v", "-map", "1:v", "-map_metadata", "2", "-map_chapters", "2", "-c:v:0", "mpeg4", "-c:v:1", "copy", muxed)
	data, err := os.ReadFile(muxed)
	if err != nil {
		t.Fatal(err)
	}
	references := 0
	for at := bytes.Index(data, []byte("chap")); at >= 4; {
		if references == 0 {
			copy(data[at+4:at+8], []byte{0, 0, 0, 2})
		} else {
			copy(data[at:at+4], "xxxx")
		}
		references++
		next := bytes.Index(data[at+4:], []byte("chap"))
		if next < 0 {
			break
		}
		at += 4 + next
	}
	if references == 0 || os.WriteFile(out, data, 0600) != nil || os.Remove(muxed) != nil {
		t.Fatal("chapter reference not found in the muxed fixture")
	}
}

// TestChapterImageRealFFprobeExtraction runs the sealed chapter reads of the
// pinned ffprobe in the real sandbox: the stored pictures are the chapter
// track samples byte for byte, an ordinary cover and a Matroska file yield no
// chapter picture, and an unchanged file starts no process again.
func TestChapterImageRealFFprobeExtraction(t *testing.T) {
	profile, policy, ffmpeg := coverRealTool(t)
	f := coverRealFixture{probeFixture: newProbeFixture(t)}
	f.root = syncRootPath(t, f.jobFixture)
	scratch := t.TempDir()
	var pictures [][]byte
	for i := range 3 {
		var b bytes.Buffer
		if err := jpeg.Encode(&b, coverGradient(64, 36, uint8(40*i)), &jpeg.Options{Quality: 90}); err != nil {
			t.Fatal(err)
		}
		pictures = append(pictures, b.Bytes())
	}
	for _, dir := range []string{"Chapters", "Cover", "Attachment"} {
		if err := os.MkdirAll(filepath.Join(f.root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	chapterTrackFixture(t, ffmpeg, scratch, filepath.Join(f.root, "Chapters", "Chapters.mp4"), pictures)
	cover := filepath.Join(scratch, "cover.jpg")
	if os.WriteFile(cover, pictures[0], 0600) != nil || os.WriteFile(filepath.Join(scratch, "c.txt"), []byte(";FFMETADATA1\n[CHAPTER]\nTIMEBASE=1/1000\nSTART=0\nEND=1000\n"), 0600) != nil {
		t.Fatal("write cover fixture")
	}
	coverFFmpeg(t, ffmpeg, "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=10:duration=1", "-i", cover, "-i", filepath.Join(scratch, "c.txt"), "-map", "0:v", "-map", "1:v",
		"-map_chapters", "2", "-c:v:0", "mpeg4", "-c:v:1", "copy", "-disposition:v:1", "attached_pic", filepath.Join(f.root, "Cover", "Cover.mp4"))
	coverFFmpeg(t, ffmpeg, "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=10:duration=1", "-i", filepath.Join(scratch, "c.txt"), "-map_chapters", "1", "-c:v", "mpeg4",
		"-attach", cover, "-metadata:s:t", "mimetype=image/jpeg", "-metadata:s:t", "filename=cover.jpg", filepath.Join(f.root, "Attachment", "Attachment.mkv"))

	temp := t.TempDir()
	policyData, err := json.Marshal(policy)
	if err != nil || os.WriteFile(filepath.Join(temp, coverHelperPolicyFile), policyData, 0600) != nil {
		t.Fatal("write helper policy")
	}
	launcher, err := sandbox.New(f.ctx, profile, policy)
	if err != nil {
		t.Fatal("real launcher", err)
	}
	config := process.Config{MaxConcurrent: 1, Timeout: 30 * time.Second, MaxStdoutBytes: 4 << 20, MaxStderrBytes: 64 << 10, TempRoot: temp}
	metadataRunner, err := process.NewIsolatedFFprobe(config, launcher)
	if err != nil {
		t.Fatal(err)
	}
	if f.adapter, err = probe.NewAdapter(metadataRunner, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	config.MaxStdoutBytes = 16 << 20
	chapterRunner, err := process.NewIsolatedFFprobeChapters(config, launcher)
	if err != nil {
		t.Fatal(err)
	}
	extractor, err := probe.NewChapterAdapter(chapterRunner)
	if err != nil {
		t.Fatal(err)
	}
	items := map[string]string{}
	digests := map[string]string{}
	for _, relative := range []string{"Chapters/Chapters.mp4", "Cover/Cover.mp4", "Attachment/Attachment.mkv"} {
		f.probeAndSeed(t, relative)
		items[relative] = metadataItem(t, f.jobFixture)
		sidecarSource(t, f.jobFixture, items[relative], relative)
		digests[relative] = coverFileDigest(t, filepath.Join(f.root, filepath.FromSlash(relative)))
	}
	l := coverSyncLease(t, f.probeFixture, "chapters-real")
	pass := func() map[string]domain.ChapterImageRecord {
		t.Helper()
		out := map[string]domain.ChapterImageRecord{}
		for _, c := range chapterCandidates(t, f.probeFixture, l) {
			result := domain.ChapterImageResult{Candidate: c}
			images, err := extractor.ExtractChapterImages(f.ctx, c)
			switch {
			case err == nil:
				result.Outcome = domain.ChapterImageStored
				for _, image := range images {
					result.Images = append(result.Images, domain.ChapterImageContent{Index: image.Index, Content: domain.ItemImageContent{SHA256: image.SHA256[:],
						Width: image.Width, Height: image.Height, Format: image.Format, Bytes: int64(len(image.Data)), FetchedAt: time.Now().UTC().Truncate(time.Microsecond)}})
				}
			case errors.Is(err, domain.ErrChapterImagesAbsent):
				result.Outcome = domain.ChapterImageAbsent
			default:
				t.Fatal("real chapter read", c.RelativePath, err)
			}
			record, err := f.s.RecordChapterImages(f.ctx, l, result)
			if err != nil {
				t.Fatal(err)
			}
			out[c.RelativePath] = record
		}
		return out
	}
	records := pass()
	if records["Chapters/Chapters.mp4"].Stored != 3 || records["Cover/Cover.mp4"].Outcome != domain.ChapterImageAbsent {
		t.Fatal("real pass", records)
	}
	if _, ok := records["Attachment/Attachment.mkv"]; ok {
		t.Fatal("Matroska attachment offered as chapter track")
	}
	rows := chapterRows(t, f.probeFixture, items["Chapters/Chapters.mp4"])
	for i, p := range pictures {
		sum := sha256.Sum256(p)
		if rows[i] != fmt.Sprintf("embedded:%x;", sum) {
			t.Fatal("stored chapter picture is not the track sample", i, rows)
		}
	}
	for relative, digest := range digests {
		if coverFileDigest(t, filepath.Join(f.root, filepath.FromSlash(relative))) != digest {
			t.Fatal("extraction changed a media file", relative)
		}
	}
	started := chapterRunner.Stats().Started
	if again := pass(); len(again) != 0 || chapterRunner.Stats().Started != started {
		t.Fatal("unchanged files read again", again)
	}
}
