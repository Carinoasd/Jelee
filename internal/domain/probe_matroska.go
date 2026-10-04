package domain

import (
	"math"
	"path"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MediaMatroska is the optional MediaInfo supplement of a Matroska/WebM
// probe (G19.1): chapter titles, attachments and container tags that the
// ffprobe whitelist does not keep. It is absent when MediaInfo is not
// installed, the container is not Matroska, or MediaInfo reported nothing.
// Every text value is untrusted file content: bounded, valid UTF-8 without
// control characters, and only ever returned as JSON data.
type MediaMatroska struct {
	Chapters     []MediaNamedChapter `json:"chapters,omitempty"`
	Attachments  []MediaAttachment   `json:"attachments,omitempty"`
	Tags         []MediaTag          `json:"tags,omitempty"`
	StreamTitles []MediaStreamTitle  `json:"streamTitles,omitempty"`
}

// MediaNamedChapter is one edition chapter start and its title.
type MediaNamedChapter struct {
	StartMicros int64  `json:"startMicros"`
	Title       string `json:"title,omitempty"`
}

// MediaAttachment is one Matroska attachment in file order. ID is its
// 1-based position, the ID mkvtoolnix uses; Font marks font file names.
type MediaAttachment struct {
	ID       int    `json:"id"`
	FileName string `json:"fileName"`
	Font     bool   `json:"font"`
}

// MediaTag is one container-level tag. Names come from a fixed table or are
// plain identifiers; values are short text.
type MediaTag struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// MediaStreamTitle is the track name of the stream with this probe index.
type MediaStreamTitle struct {
	Index int    `json:"index"`
	Title string `json:"title"`
}

// Supplement bounds; larger documents are dropped whole.
const (
	MaxMatroskaChapters    = 256
	MaxMatroskaAttachments = 256
	MaxMatroskaTags        = 32
	MaxMatroskaTextBytes   = 512
	MaxMatroskaFileName    = 255
)

var matroskaTagName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)

// fontExtensions are attachment file name extensions treated as fonts.
var fontExtensions = map[string]bool{".ttf": true, ".otf": true, ".ttc": true, ".otc": true, ".woff": true, ".woff2": true}

// IsFontFileName reports whether an attachment name has a font extension.
func IsFontFileName(name string) bool {
	return fontExtensions[strings.ToLower(path.Ext(name))]
}

// ValidMatroskaText accepts short, valid UTF-8 text without control
// characters; empty text is valid only where the caller allows it.
func ValidMatroskaText(value string, maximum int) bool {
	return len(value) <= maximum && utf8.ValidString(value) && !strings.ContainsFunc(value, unicode.IsControl)
}

// ValidAttachmentFileName accepts a bare file name: no separators, no
// relative components and no control characters.
func ValidAttachmentFileName(name string) bool {
	return name != "" && name != "." && name != ".." && ValidMatroskaText(name, MaxMatroskaFileName) && !strings.ContainsAny(name, "/\\:\x00")
}

// ValidMediaMatroska applies the stored whitelist to a supplement before
// it is attached to probe metadata; streams are the probe stream indices.
func ValidMediaMatroska(v *MediaMatroska, duration *int64, streams map[int]bool) bool {
	return v != nil && validMatroska(v, duration, streams)
}

func validMatroska(v *MediaMatroska, duration *int64, streams map[int]bool) bool {
	if v == nil {
		return true
	}
	if len(v.Chapters) > MaxMatroskaChapters || len(v.Attachments) > MaxMatroskaAttachments || len(v.Tags) > MaxMatroskaTags || len(v.StreamTitles) > 64 {
		return false
	}
	if len(v.Chapters) == 0 && len(v.Attachments) == 0 && len(v.Tags) == 0 && len(v.StreamTitles) == 0 {
		return false
	}
	previous := int64(-1)
	for _, c := range v.Chapters {
		if c.StartMicros < 0 || c.StartMicros > math.MaxInt64/2 || c.StartMicros < previous || duration != nil && c.StartMicros > *duration || !ValidMatroskaText(c.Title, MaxMatroskaTextBytes) {
			return false
		}
		previous = c.StartMicros
	}
	for i, a := range v.Attachments {
		if a.ID != i+1 || !ValidAttachmentFileName(a.FileName) || a.Font != IsFontFileName(a.FileName) {
			return false
		}
	}
	names := make(map[string]bool, len(v.Tags))
	for _, t := range v.Tags {
		if !matroskaTagName.MatchString(t.Name) || names[t.Name] || t.Value == "" || !ValidMatroskaText(t.Value, MaxMatroskaTextBytes) {
			return false
		}
		names[t.Name] = true
	}
	titled := make(map[int]bool, len(v.StreamTitles))
	for _, s := range v.StreamTitles {
		if !streams[s.Index] || titled[s.Index] || s.Title == "" || !ValidMatroskaText(s.Title, MaxMatroskaTextBytes) {
			return false
		}
		titled[s.Index] = true
	}
	return true
}
