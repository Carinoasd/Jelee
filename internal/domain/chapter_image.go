package domain

import (
	"crypto/sha256"
	"errors"
	"slices"
)

// Chapter image extraction (G40.1) copies the chapter thumbnails a container
// already stores into the image store: a QuickTime/MP4 chapter track whose
// samples are JPEG or PNG pictures (FFmpeg exposes it as a video stream with
// the attached_pic and timed_thumbnails dispositions). It never decodes,
// renders or re-encodes video; generating thumbnails from frames would need
// ffmpeg, which the production image never ships (ADR 0007, G37.1).
const (
	// ChapterImageMaxPerItem bounds the pictures of one file; the sandboxed
	// read stops after this many packets plus the demuxer's cover copy.
	ChapterImageMaxPerItem = 64
	// ChapterImageMaxBytes bounds one picture and ChapterImageMaxTotalBytes
	// all pictures of one file. The read's output limit sits just above the
	// hex dump of the total.
	ChapterImageMaxBytes      = 512 << 10
	ChapterImageMaxTotalBytes = 3 << 20
	// ChapterImageBatch is one candidate page; ChapterImageMaxPerJob caps the
	// files one catalog sync job reads.
	ChapterImageBatch     = 16
	ChapterImageMaxPerJob = 256
	// ChapterImageMaxStreams bounds the picture streams tried per file.
	ChapterImageMaxStreams = 4
)

// Outcomes of one chapter image attempt. Stored, absent, invalid and
// too_large are remembered for the probed fingerprint; changed is not.
const (
	ChapterImageStored   = "stored"
	ChapterImageAbsent   = "absent"
	ChapterImageInvalid  = "invalid"
	ChapterImageTooLarge = "too_large"
	ChapterImageChanged  = "changed"
)

var (
	// ErrChapterImagesAbsent means no tried stream is a chapter picture track
	// or none of its pictures falls inside a chapter.
	ErrChapterImagesAbsent = errors.New("chapter_images_absent")
	// ErrChapterImagesInvalid means malformed output or unsupported bytes.
	ErrChapterImagesInvalid = errors.New("chapter_images_invalid")
	// ErrChapterImagesTooLarge means a picture, the total or the output limit
	// is over its bound.
	ErrChapterImagesTooLarge = errors.New("chapter_images_too_large")
)

// ChapterImageStream is one picture stream to try: its container index and
// its position among the video streams (the sealed sandbox operation).
type ChapterImageStream struct {
	StreamIndex, VideoIndex int
}

// ChapterImageCandidate is one item whose single media file was probed with
// chapters and at least one picture stream. Stamp is the probe cache stamp the
// extraction must still observe.
type ChapterImageCandidate struct {
	ItemID, LibraryID string
	RootID, RootPath  string `json:"-"`
	RelativePath      string `json:"-"`
	Stamp             ProbeStamp
	Streams           []ChapterImageStream
}

// String redacts the media path from diagnostics.
func (ChapterImageCandidate) String() string { return "chapter image candidate (path redacted)" }

// GoString redacts the media path from %#v diagnostics.
func (ChapterImageCandidate) GoString() string { return "chapter image candidate (path redacted)" }

// Source returns the probe source of the candidate's media file.
func (c ChapterImageCandidate) Source() ProbeSource {
	return ProbeSource{RootPath: c.RootPath, RelativePath: c.RelativePath}
}

// ChapterImage is one verified picture: the exact stored sample of the
// chapter at Index (the chapter's position in start order).
type ChapterImage struct {
	Index         int
	Data          []byte `json:"-"`
	SHA256        [32]byte
	Format        string
	Width, Height int
}

// String redacts the picture bytes from diagnostics.
func (ChapterImage) String() string { return "chapter image (data redacted)" }

// GoString redacts the picture bytes from %#v diagnostics.
func (ChapterImage) GoString() string { return "chapter image (data redacted)" }

// ChapterImageContent is one stored picture of an attempt.
type ChapterImageContent struct {
	Index   int
	Content ItemImageContent
}

// ChapterImageResult is what one attempt records for its candidate. Images
// is non-empty exactly for ChapterImageStored.
type ChapterImageResult struct {
	Candidate ChapterImageCandidate
	Outcome   string
	Images    []ChapterImageContent
}

// ChapterImageRecord is the applied result: Stored slots written, Skipped
// slots left to a locked, manual or higher-priority row, and Removed stale
// embedded chapter rows of the file.
type ChapterImageRecord struct {
	Outcome                  string
	Stored, Skipped, Removed int
}

// ChapterImagePage is one candidate page. Next is the cursor of the next page,
// empty when the library has been scanned to its end.
type ChapterImagePage struct {
	Candidates []ChapterImageCandidate
	Next       string
}

// chapterImageFormats are the probe format names whose demuxer exposes
// chapter picture tracks.
var chapterImageFormats = []string{"mov", "mp4", "m4a", "3gp", "3g2", "mj2"}

// ChapterImageStreams picks the streams that can hold chapter pictures:
// JPEG or PNG video streams with the attached_pic disposition in a QuickTime
// family file that has chapters, lowest index first. Probe metadata does not
// record timed_thumbnails, so the sandboxed read checks it on each stream.
func ChapterImageStreams(meta MediaMetadata) []ChapterImageStream {
	if len(meta.Chapters) == 0 || !slices.ContainsFunc(meta.Format.Names, func(name string) bool { return slices.Contains(chapterImageFormats, name) }) {
		return nil
	}
	var out []ChapterImageStream
	video := -1
	streams := slices.Clone(meta.Streams)
	slices.SortFunc(streams, func(a, b MediaStream) int { return a.Index - b.Index })
	for _, s := range streams {
		if s.Kind != "video" {
			continue
		}
		video++
		if s.AttachedPic == nil || !*s.AttachedPic || s.Codec == nil || (*s.Codec != "mjpeg" && *s.Codec != "png") {
			continue
		}
		if video > EmbeddedCoverMaxVideoIndex || s.Index >= 64 || len(out) == ChapterImageMaxStreams {
			break
		}
		out = append(out, ChapterImageStream{StreamIndex: s.Index, VideoIndex: video})
	}
	return out
}

// ValidChapterImageCandidate checks the identifiers, the confined media path,
// the probe stamp and the stream list before any process starts.
func ValidChapterImageCandidate(c ChapterImageCandidate) bool {
	if !ValidEmbeddedCoverCandidate(EmbeddedCoverCandidate{ItemID: c.ItemID, LibraryID: c.LibraryID, RootID: c.RootID, RootPath: c.RootPath,
		RelativePath: c.RelativePath, Stamp: c.Stamp}) || len(c.Streams) == 0 || len(c.Streams) > ChapterImageMaxStreams {
		return false
	}
	for i, s := range c.Streams {
		if s.StreamIndex < 0 || s.StreamIndex >= 64 || s.VideoIndex < 0 || s.VideoIndex > EmbeddedCoverMaxVideoIndex || s.VideoIndex > s.StreamIndex ||
			i > 0 && (s.StreamIndex <= c.Streams[i-1].StreamIndex || s.VideoIndex <= c.Streams[i-1].VideoIndex) {
			return false
		}
	}
	return true
}

// ValidChapterImages checks verified pictures against their digests, the
// per-picture and total bounds, and strictly increasing chapter indexes.
func ValidChapterImages(images []ChapterImage) bool {
	if len(images) == 0 || len(images) > ChapterImageMaxPerItem {
		return false
	}
	total, previous := 0, -1
	for _, image := range images {
		total += len(image.Data)
		if image.Index <= previous || image.Index > ItemImageMaxIndex ||
			len(image.Data) == 0 || len(image.Data) > ChapterImageMaxBytes || sha256.Sum256(image.Data) != image.SHA256 ||
			!ValidEmbeddedCover(EmbeddedCover{Data: image.Data, SHA256: image.SHA256, Format: image.Format, Width: image.Width, Height: image.Height}) {
			return false
		}
		previous = image.Index
	}
	return total <= ChapterImageMaxTotalBytes
}

// ValidChapterImageResult checks the outcome/content pairing.
func ValidChapterImageResult(r ChapterImageResult) bool {
	if !ValidChapterImageCandidate(r.Candidate) {
		return false
	}
	switch r.Outcome {
	case ChapterImageStored:
		if len(r.Images) == 0 || len(r.Images) > ChapterImageMaxPerItem {
			return false
		}
		for i, image := range r.Images {
			c := image.Content
			if !ValidItemImageSlot("Chapter", image.Index) || i > 0 && image.Index <= r.Images[i-1].Index || !validItemImageContent(&c) ||
				c.SHA256 == nil || (c.Format != "jpeg" && c.Format != "png") || c.Width < 1 || c.Bytes > ChapterImageMaxBytes {
				return false
			}
		}
		return true
	case ChapterImageAbsent, ChapterImageInvalid, ChapterImageTooLarge:
		return len(r.Images) == 0
	}
	return false
}
