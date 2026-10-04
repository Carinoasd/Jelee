package domain

import (
	"crypto/sha256"
	"strings"
	"testing"
	"time"
)

func coverStream(index int, kind, codec string, attached *bool) MediaStream {
	s := MediaStream{Index: index, Kind: kind, AttachedPic: attached}
	if codec != "" {
		s.Codec = &codec
	}
	return s
}

func TestEmbeddedCoverStreamSelection(t *testing.T) {
	yes, no := true, false
	meta := MediaMetadata{Streams: []MediaStream{
		coverStream(0, "video", "h264", &no), coverStream(1, "audio", "aac", nil),
		coverStream(4, "video", "png", &yes), coverStream(3, "video", "mjpeg", &yes), coverStream(5, "video", "gif", &yes),
	}}
	if stream, video, ok := EmbeddedCoverStream(meta); !ok || stream != 3 || video != 1 {
		t.Fatal("lowest attached picture", stream, video, ok)
	}
	for name, m := range map[string]MediaMetadata{
		"none":       {Streams: []MediaStream{coverStream(0, "video", "h264", nil)}},
		"not_flag":   {Streams: []MediaStream{coverStream(0, "video", "png", &no)}},
		"unknown":    {Streams: []MediaStream{coverStream(0, "video", "", &yes)}},
		"video_only": {Streams: []MediaStream{coverStream(0, "video", "gif", &yes)}},
	} {
		if _, _, ok := EmbeddedCoverStream(m); ok {
			t.Fatal("cover offered for", name)
		}
	}
	var many []MediaStream
	for i := 0; i <= EmbeddedCoverMaxVideoIndex; i++ {
		many = append(many, coverStream(i, "video", "h264", nil))
	}
	many = append(many, coverStream(EmbeddedCoverMaxVideoIndex+1, "video", "png", &yes))
	if _, _, ok := EmbeddedCoverStream(MediaMetadata{Streams: many}); ok {
		t.Fatal("cover beyond the sealed operation range offered")
	}
	if EmbeddedCoverFormat("mjpeg") != "jpeg" || EmbeddedCoverFormat("png") != "png" || EmbeddedCoverFormat("h264") != "" {
		t.Fatal("format mapping")
	}
}

func validCoverCandidate() EmbeddedCoverCandidate {
	return EmbeddedCoverCandidate{ItemID: "11111111-1111-4111-8111-111111111111", LibraryID: "22222222-2222-4222-8222-222222222222", RootID: "33333333-3333-4333-8333-333333333333",
		RootPath: "/media/library", RelativePath: "Movie/Movie.mkv", StreamIndex: 2, VideoIndex: 1,
		Stamp: ProbeStamp{Size: 10, ModifiedUnixNano: 1, Fingerprint: strings.Repeat("a", 64), FingerprintVersion: ProbeFingerprintVersion}}
}

func TestEmbeddedCoverValidation(t *testing.T) {
	c := validCoverCandidate()
	if !ValidEmbeddedCoverCandidate(c) {
		t.Fatal("valid candidate refused")
	}
	windows := c
	windows.RootPath = `C:\media`
	if !ValidEmbeddedCoverCandidate(windows) {
		t.Fatal("windows root refused")
	}
	for name, edit := range map[string]func(*EmbeddedCoverCandidate){
		"item":        func(c *EmbeddedCoverCandidate) { c.ItemID = "x" },
		"root":        func(c *EmbeddedCoverCandidate) { c.RootPath = "relative" },
		"path":        func(c *EmbeddedCoverCandidate) { c.RelativePath = "../escape.mkv" },
		"stamp":       func(c *EmbeddedCoverCandidate) { c.Stamp.Fingerprint = "zz" },
		"video_index": func(c *EmbeddedCoverCandidate) { c.VideoIndex = EmbeddedCoverMaxVideoIndex + 1 },
		"order":       func(c *EmbeddedCoverCandidate) { c.VideoIndex = 3 },
		"stream":      func(c *EmbeddedCoverCandidate) { c.StreamIndex = 64 },
	} {
		bad := validCoverCandidate()
		edit(&bad)
		if ValidEmbeddedCoverCandidate(bad) {
			t.Fatal("invalid candidate accepted:", name)
		}
	}
	if s := c.Source(); s.RootPath != c.RootPath || s.RelativePath != c.RelativePath {
		t.Fatal("source binding")
	}
	if strings.Contains(c.String()+c.GoString(), "Movie") {
		t.Fatal("candidate diagnostics expose paths")
	}
	data := []byte("picture")
	cover := EmbeddedCover{Data: data, SHA256: sha256.Sum256(data), Format: "png", Width: 2, Height: 2}
	if !ValidEmbeddedCover(cover) || strings.Contains(cover.String()+cover.GoString(), "picture") {
		t.Fatal("cover validation or redaction")
	}
	for name, edit := range map[string]func(*EmbeddedCover){
		"digest": func(c *EmbeddedCover) { c.SHA256[0]++ },
		"format": func(c *EmbeddedCover) { c.Format = "gif" },
		"pixels": func(c *EmbeddedCover) { c.Width, c.Height = EmbeddedCoverMaxDimension, EmbeddedCoverMaxDimension },
		"empty":  func(c *EmbeddedCover) { c.Data = nil },
	} {
		bad := cover
		edit(&bad)
		if ValidEmbeddedCover(bad) {
			t.Fatal("invalid cover accepted:", name)
		}
	}
	content := &ItemImageContent{SHA256: cover.SHA256[:], Width: 2, Height: 2, Format: "png", Bytes: 7, FetchedAt: time.Now()}
	if !ValidEmbeddedCoverResult(EmbeddedCoverResult{Candidate: c, Outcome: EmbeddedCoverStored, Content: content}) ||
		!ValidEmbeddedCoverResult(EmbeddedCoverResult{Candidate: c, Outcome: EmbeddedCoverAbsent}) {
		t.Fatal("valid results refused")
	}
	for name, r := range map[string]EmbeddedCoverResult{
		"stored_without_content": {Candidate: c, Outcome: EmbeddedCoverStored},
		"refusal_with_content":   {Candidate: c, Outcome: EmbeddedCoverInvalid, Content: content},
		"transient":              {Candidate: c, Outcome: EmbeddedCoverChanged},
		"bad_candidate":          {Candidate: EmbeddedCoverCandidate{}, Outcome: EmbeddedCoverAbsent},
		"webp":                   {Candidate: c, Outcome: EmbeddedCoverStored, Content: &ItemImageContent{SHA256: cover.SHA256[:], Width: 2, Height: 2, Format: "webp", Bytes: 7, FetchedAt: time.Now()}},
	} {
		if ValidEmbeddedCoverResult(r) {
			t.Fatal("invalid result accepted:", name)
		}
	}
	if value, ok := EmbeddedCoverFingerprint(c.Stamp); !ok || len(value) != 32 {
		t.Fatal("fingerprint decode")
	}
	if _, ok := EmbeddedCoverFingerprint(ProbeStamp{Fingerprint: "abc"}); ok {
		t.Fatal("short fingerprint accepted")
	}
}
