package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path"
)

// Embedded cover extraction (G40.4) copies the picture a container already
// stores (an attached_pic stream: an MP4 cover atom or a Matroska image
// attachment) into the image store. It never decodes or re-encodes video.
const (
	// EmbeddedCoverMaxBytes bounds one stored picture. The sandboxed read's
	// output limit sits just above the hex dump of this many bytes.
	EmbeddedCoverMaxBytes = 3 << 20
	// EmbeddedCoverMaxDimension and EmbeddedCoverMaxPixels are the header
	// precheck: a larger declared frame is refused before anything decodes it.
	EmbeddedCoverMaxDimension = 16384
	EmbeddedCoverMaxPixels    = 64 << 20
	// EmbeddedCoverBatch is one candidate page; EmbeddedCoverMaxPerJob caps
	// the extractions one catalog sync job starts.
	EmbeddedCoverBatch     = 16
	EmbeddedCoverMaxPerJob = 512
	// EmbeddedCoverMaxVideoIndex mirrors the sandbox's sealed descriptor range.
	EmbeddedCoverMaxVideoIndex = 15
)

// Outcomes of one extraction attempt. Stored, absent, invalid and too_large
// are remembered for the probed fingerprint and not retried until the file
// changes; the skips, changes and transient failures are not remembered.
const (
	EmbeddedCoverStored          = "stored"
	EmbeddedCoverAbsent          = "absent"
	EmbeddedCoverInvalid         = "invalid"
	EmbeddedCoverTooLarge        = "too_large"
	EmbeddedCoverSkippedLocked   = "skipped_locked"
	EmbeddedCoverSkippedPriority = "skipped_priority"
	EmbeddedCoverChanged         = "changed"
)

var (
	// ErrEmbeddedCoverAbsent means the selected stream is not an attached
	// picture.
	ErrEmbeddedCoverAbsent = errors.New("embedded_cover_absent")
	// ErrEmbeddedCoverInvalid means malformed output or bytes that are not a
	// supported picture.
	ErrEmbeddedCoverInvalid = errors.New("embedded_cover_invalid")
	// ErrEmbeddedCoverTooLarge means over EmbeddedCoverMaxBytes, the output
	// limit or the dimension precheck.
	ErrEmbeddedCoverTooLarge = errors.New("embedded_cover_too_large")
)

// EmbeddedCoverCandidate is one item whose single media file was probed with
// an attached picture and has no usable higher-priority or locked Primary
// image. Stamp is the probe cache stamp the extraction must still observe.
type EmbeddedCoverCandidate struct {
	ItemID, LibraryID string
	RootID, RootPath  string `json:"-"`
	RelativePath      string `json:"-"`
	Stamp             ProbeStamp
	// StreamIndex is the container stream index; VideoIndex is its position
	// among the video streams, which selects the sealed sandbox operation.
	StreamIndex, VideoIndex int
}

// String redacts the media path from diagnostics.
func (EmbeddedCoverCandidate) String() string { return "embedded cover candidate (path redacted)" }

// GoString redacts the media path from %#v diagnostics.
func (EmbeddedCoverCandidate) GoString() string { return "embedded cover candidate (path redacted)" }

// Source returns the probe source of the candidate's media file.
func (c EmbeddedCoverCandidate) Source() ProbeSource {
	return ProbeSource{RootPath: c.RootPath, RelativePath: c.RelativePath}
}

// EmbeddedCover is the verified original picture: the exact stream payload.
type EmbeddedCover struct {
	Data          []byte `json:"-"`
	SHA256        [32]byte
	Format        string
	Width, Height int
}

// String redacts the picture bytes from diagnostics.
func (EmbeddedCover) String() string { return "embedded cover (data redacted)" }

// GoString redacts the picture bytes from %#v diagnostics.
func (EmbeddedCover) GoString() string { return "embedded cover (data redacted)" }

// EmbeddedCoverResult is what one attempt records for its candidate. Content
// is set exactly for EmbeddedCoverStored.
type EmbeddedCoverResult struct {
	Candidate EmbeddedCoverCandidate
	Outcome   string
	Content   *ItemImageContent
}

// EmbeddedCoverPage is one candidate page. Next is the cursor of the next
// page, empty when the library has been scanned to its end.
type EmbeddedCoverPage struct {
	Candidates []EmbeddedCoverCandidate
	Next       string
}

// EmbeddedCoverStats summarizes one pass; it carries no paths.
type EmbeddedCoverStats struct {
	Candidates, Stored, Remembered, Skipped, Failed int
}

// EmbeddedCoverStream picks the attached picture of probe metadata: the
// lowest-index video stream with the attached_pic disposition and a JPEG or
// PNG codec. It also returns its video-relative index. Streams beyond the
// sealed index range are not selectable.
func EmbeddedCoverStream(meta MediaMetadata) (stream, videoIndex int, ok bool) {
	best, bestVideo := -1, -1
	for _, s := range meta.Streams {
		if s.Kind != "video" || s.AttachedPic == nil || !*s.AttachedPic || s.Codec == nil || (*s.Codec != "mjpeg" && *s.Codec != "png") {
			continue
		}
		if best < 0 || s.Index < best {
			best = s.Index
		}
	}
	if best < 0 {
		return 0, 0, false
	}
	for _, s := range meta.Streams {
		if s.Kind == "video" && s.Index < best {
			bestVideo++
		}
	}
	bestVideo++
	if bestVideo > EmbeddedCoverMaxVideoIndex {
		return 0, 0, false
	}
	return best, bestVideo, true
}

// EmbeddedCoverFormat maps a stream codec to the stored image format.
func EmbeddedCoverFormat(codec string) string {
	switch codec {
	case "mjpeg":
		return "jpeg"
	case "png":
		return "png"
	}
	return ""
}

// ValidEmbeddedCoverCandidate checks the identifiers, the confined media path
// and the probe stamp before any process starts.
func ValidEmbeddedCoverCandidate(c EmbeddedCoverCandidate) bool {
	return ValidID(c.ItemID) && ValidID(c.LibraryID) && ValidID(c.RootID) && c.RootPath != "" && path.IsAbs(toSlash(c.RootPath)) &&
		ValidItemImageRelativePath(c.RelativePath) && ValidateProbeStamp(c.Stamp) == nil &&
		c.StreamIndex >= 0 && c.StreamIndex < 64 && c.VideoIndex >= 0 && c.VideoIndex <= EmbeddedCoverMaxVideoIndex && c.VideoIndex <= c.StreamIndex
}

// ValidEmbeddedCover checks a verified picture against its own digest and
// the configured bounds.
func ValidEmbeddedCover(c EmbeddedCover) bool {
	return len(c.Data) > 0 && len(c.Data) <= EmbeddedCoverMaxBytes && sha256.Sum256(c.Data) == c.SHA256 &&
		(c.Format == "jpeg" || c.Format == "png") && c.Width >= 1 && c.Height >= 1 &&
		c.Width <= EmbeddedCoverMaxDimension && c.Height <= EmbeddedCoverMaxDimension && int64(c.Width)*int64(c.Height) <= EmbeddedCoverMaxPixels
}

// ValidEmbeddedCoverResult checks the outcome/content pairing.
func ValidEmbeddedCoverResult(r EmbeddedCoverResult) bool {
	if !ValidEmbeddedCoverCandidate(r.Candidate) {
		return false
	}
	switch r.Outcome {
	case EmbeddedCoverStored:
		return r.Content != nil && validItemImageContent(r.Content) && (r.Content.Format == "jpeg" || r.Content.Format == "png") &&
			r.Content.Width > 0 && r.Content.Bytes <= EmbeddedCoverMaxBytes
	case EmbeddedCoverAbsent, EmbeddedCoverInvalid, EmbeddedCoverTooLarge:
		return r.Content == nil
	}
	return false
}

// EmbeddedCoverFingerprint decodes the probe stamp's hex fingerprint.
func EmbeddedCoverFingerprint(stamp ProbeStamp) ([]byte, bool) {
	value, err := hex.DecodeString(stamp.Fingerprint)
	return value, err == nil && len(value) == sha256.Size
}

func toSlash(value string) string {
	out := []byte(value)
	for i, c := range out {
		if c == '\\' {
			out[i] = '/'
		}
	}
	// A Windows volume path like C:/media is absolute for the scanner.
	if len(out) >= 3 && out[1] == ':' && out[2] == '/' {
		return string(out[2:])
	}
	return string(out)
}
