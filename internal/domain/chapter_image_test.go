package domain

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
	"time"
)

func chapterMeta(format string, chapters int, streams ...MediaStream) MediaMetadata {
	meta := MediaMetadata{Format: MediaFormat{Names: []string{format}}, Streams: streams}
	for i := range chapters {
		meta.Chapters = append(meta.Chapters, MediaChapter{ID: int64(i)})
	}
	return meta
}

func chapterStream(index int, kind, codec string, attached bool) MediaStream {
	return MediaStream{Index: index, Kind: kind, Codec: &codec, AttachedPic: &attached}
}

func TestChapterImageStreamsSelectsPictureStreamsOfQuickTimeFiles(t *testing.T) {
	streams := []MediaStream{chapterStream(4, "video", "png", true), chapterStream(0, "video", "h264", false), chapterStream(1, "audio", "aac", false),
		chapterStream(2, "video", "mjpeg", true), chapterStream(3, "video", "hevc", true)}
	got := ChapterImageStreams(chapterMeta("mov", 3, streams...))
	if fmt.Sprint(got) != "[{2 1} {4 3}]" {
		t.Fatal("selected streams", got)
	}
	for name, meta := range map[string]MediaMetadata{
		"no_chapters": chapterMeta("mp4", 0, streams...),
		"matroska":    chapterMeta("matroska", 3, streams...),
		"no_picture":  chapterMeta("mp4", 3, chapterStream(0, "video", "h264", false), chapterStream(1, "video", "png", false)),
	} {
		if got := ChapterImageStreams(meta); len(got) != 0 {
			t.Fatal("unexpected streams for", name, got)
		}
	}
	var many []MediaStream
	for i := range 20 {
		many = append(many, chapterStream(i, "video", "mjpeg", true))
	}
	if got := ChapterImageStreams(chapterMeta("mp4", 1, many...)); len(got) != ChapterImageMaxStreams || got[3].VideoIndex != 3 {
		t.Fatal("stream bound", got)
	}
	// Streams past the sealed video index range are not selectable.
	var late []MediaStream
	for i := range 17 {
		late = append(late, chapterStream(i, "video", "h264", false))
	}
	late = append(late, chapterStream(17, "video", "png", true))
	if got := ChapterImageStreams(chapterMeta("mp4", 1, late...)); len(got) != 0 {
		t.Fatal("out-of-range video index selected", got)
	}
}

func chapterCandidateFixture() ChapterImageCandidate {
	return ChapterImageCandidate{ItemID: "11111111-1111-4111-8111-111111111111", LibraryID: "22222222-2222-4222-8222-222222222222",
		RootID: "33333333-3333-4333-8333-333333333333", RootPath: "/media", RelativePath: "Film/Film.mp4",
		Stamp:   ProbeStamp{Size: 10, Fingerprint: strings.Repeat("a", 64), FingerprintVersion: ProbeFingerprintVersion},
		Streams: []ChapterImageStream{{StreamIndex: 1, VideoIndex: 1}, {StreamIndex: 3, VideoIndex: 2}}}
}

func TestChapterImageValidation(t *testing.T) {
	valid := chapterCandidateFixture()
	if !ValidChapterImageCandidate(valid) {
		t.Fatal("valid candidate refused")
	}
	if s := fmt.Sprintf("%v %#v", valid, valid); strings.Contains(s, "Film") {
		t.Fatal("candidate diagnostics expose the path")
	}
	for name, edit := range map[string]func(*ChapterImageCandidate){
		"no_streams":  func(c *ChapterImageCandidate) { c.Streams = nil },
		"too_many":    func(c *ChapterImageCandidate) { c.Streams = make([]ChapterImageStream, ChapterImageMaxStreams+1) },
		"unordered":   func(c *ChapterImageCandidate) { c.Streams[0], c.Streams[1] = c.Streams[1], c.Streams[0] },
		"video_range": func(c *ChapterImageCandidate) { c.Streams[1] = ChapterImageStream{StreamIndex: 30, VideoIndex: 16} },
		"video_above": func(c *ChapterImageCandidate) { c.Streams[0] = ChapterImageStream{StreamIndex: 0, VideoIndex: 1} },
		"path":        func(c *ChapterImageCandidate) { c.RelativePath = "../x.mp4" },
		"item":        func(c *ChapterImageCandidate) { c.ItemID = "x" },
	} {
		c := chapterCandidateFixture()
		edit(&c)
		if ValidChapterImageCandidate(c) {
			t.Fatal("invalid candidate accepted:", name)
		}
	}
	picture := func(index int, data string) ChapterImage {
		return ChapterImage{Index: index, Data: []byte(data), SHA256: sha256.Sum256([]byte(data)), Format: "jpeg", Width: 4, Height: 3}
	}
	if !ValidChapterImages([]ChapterImage{picture(0, "a"), picture(5, "b")}) {
		t.Fatal("valid pictures refused")
	}
	tampered := picture(1, "c")
	tampered.SHA256[0] ^= 1
	big := strings.Repeat("x", ChapterImageMaxBytes)
	var total []ChapterImage
	for i := range ChapterImageMaxTotalBytes/ChapterImageMaxBytes + 1 {
		total = append(total, picture(i, big+fmt.Sprint(i)[:0]))
	}
	for name, images := range map[string][]ChapterImage{
		"empty":     nil,
		"duplicate": {picture(1, "a"), picture(1, "b")},
		"digest":    {tampered},
		"index":     {picture(ItemImageMaxIndex+1, "a")},
		"format":    {{Index: 0, Data: []byte("a"), SHA256: sha256.Sum256([]byte("a")), Format: "gif", Width: 1, Height: 1}},
		"total":     total,
	} {
		if ValidChapterImages(images) {
			t.Fatal("invalid pictures accepted:", name)
		}
	}
	content := func(index int) ChapterImageContent {
		return ChapterImageContent{Index: index, Content: ItemImageContent{SHA256: make([]byte, 32), Width: 4, Height: 3, Format: "png", Bytes: 10, FetchedAt: time.Unix(1, 0)}}
	}
	if !ValidChapterImageResult(ChapterImageResult{Candidate: valid, Outcome: ChapterImageStored, Images: []ChapterImageContent{content(0), content(2)}}) {
		t.Fatal("valid stored result refused")
	}
	for _, outcome := range []string{ChapterImageAbsent, ChapterImageInvalid, ChapterImageTooLarge} {
		if !ValidChapterImageResult(ChapterImageResult{Candidate: valid, Outcome: outcome}) ||
			ValidChapterImageResult(ChapterImageResult{Candidate: valid, Outcome: outcome, Images: []ChapterImageContent{content(0)}}) {
			t.Fatal("refusal pairing", outcome)
		}
	}
	for name, r := range map[string]ChapterImageResult{
		"stored_empty": {Candidate: valid, Outcome: ChapterImageStored},
		"changed":      {Candidate: valid, Outcome: ChapterImageChanged},
		"unordered":    {Candidate: valid, Outcome: ChapterImageStored, Images: []ChapterImageContent{content(2), content(0)}},
		"slot":         {Candidate: valid, Outcome: ChapterImageStored, Images: []ChapterImageContent{content(ItemImageMaxIndex + 1)}},
		"candidate":    {Candidate: ChapterImageCandidate{}, Outcome: ChapterImageAbsent},
	} {
		if ValidChapterImageResult(r) {
			t.Fatal("invalid result accepted:", name)
		}
	}
	if s := fmt.Sprintf("%v %#v", picture(0, "secret"), picture(0, "secret")); strings.Contains(s, "secret") {
		t.Fatal("picture diagnostics expose data")
	}
}
