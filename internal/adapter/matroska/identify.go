// Package matroska copies embedded text subtitles and font attachments out
// of Matroska files, unconverted, into a rebuildable cache (G15.5, G15.7).
// It drives only the isolated mkvmerge identification and mkvextract
// runners; it never renders, re-encodes or remuxes, and never writes to the
// original file.
package matroska

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// Errors carry fixed codes only; they never wrap tool output or paths.
var (
	ErrNotFound    = errors.New("matroska_item_not_found")
	ErrInvalid     = errors.New("matroska_identification_invalid")
	ErrUnavailable = errors.New("matroska_extraction_unavailable")
	ErrTooLarge    = errors.New("matroska_extraction_too_large")
	ErrChanged     = errors.New("matroska_source_changed")
	ErrBusy        = errors.New("matroska_extraction_busy")
)

// MaxIdentifyOutput bounds the identification JSON this parser accepts.
const MaxIdentifyOutput = 4 << 20

// Track is one Matroska track as mkvmerge identified it. ID is mkvmerge's
// 0-based track ID, which is also the probe stream index of the track.
type Track struct {
	ID      int
	Type    string
	CodecID string
}

// Attachment is one Matroska attachment. ID is mkvmerge's 1-based ID.
type Attachment struct {
	ID          int
	FileName    string
	ContentType string
	Size        int64
}

// Container is the identification result.
type Container struct {
	Tracks      []Track
	Attachments []Attachment
}

// textCodecs are the Matroska text subtitle codec IDs mkvextract writes as
// they are, with the extension of the written file. D_WEBVTT/SUBTITLES (the
// WebM form) is not extractable by mkvextract 102.0 and is not listed.
var textCodecs = map[string]string{"S_TEXT/UTF8": "srt", "S_TEXT/ASS": "ass", "S_TEXT/SSA": "ssa", "S_TEXT/WEBVTT": "vtt"}

// fontTypes are attachment MIME types treated as fonts in addition to font
// file name extensions.
var fontTypes = map[string]bool{
	"font/ttf": true, "font/otf": true, "font/sfnt": true, "font/collection": true, "font/woff": true, "font/woff2": true,
	"application/x-truetype-font": true, "application/x-font-ttf": true, "application/x-font-otf": true,
	"application/vnd.ms-opentype": true, "application/font-sfnt": true, "application/x-font-opentype": true,
}

// ParseIdentify reads `mkvmerge --identify --identification-format json`.
func ParseIdentify(data []byte) (Container, error) {
	if len(data) == 0 || len(data) > MaxIdentifyOutput {
		return Container{}, ErrInvalid
	}
	var document struct {
		Container struct {
			Recognized bool   `json:"recognized"`
			Supported  bool   `json:"supported"`
			Type       string `json:"type"`
		} `json:"container"`
		Tracks []struct {
			ID         *int   `json:"id"`
			Type       string `json:"type"`
			Properties struct {
				CodecID string `json:"codec_id"`
			} `json:"properties"`
		} `json:"tracks"`
		Attachments []struct {
			ID          *int   `json:"id"`
			FileName    string `json:"file_name"`
			ContentType string `json:"content_type"`
			Size        *int64 `json:"size"`
		} `json:"attachments"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if decoder.Decode(&document) != nil || decoder.Decode(new(any)) != io.EOF {
		return Container{}, ErrInvalid
	}
	if !document.Container.Recognized || !document.Container.Supported || document.Container.Type != "Matroska" ||
		len(document.Tracks) > 128 || len(document.Attachments) > domain.MaxMatroskaAttachments {
		return Container{}, ErrInvalid
	}
	var result Container
	for index, track := range document.Tracks {
		// Track IDs are positions; a gap would misalign probe stream indices.
		if track.ID == nil || *track.ID != index || len(track.Type) > 32 || len(track.Properties.CodecID) > 64 {
			return Container{}, ErrInvalid
		}
		result.Tracks = append(result.Tracks, Track{ID: index, Type: track.Type, CodecID: track.Properties.CodecID})
	}
	for index, attachment := range document.Attachments {
		if attachment.ID == nil || *attachment.ID != index+1 || attachment.Size == nil || *attachment.Size < 0 || len(attachment.ContentType) > 128 {
			return Container{}, ErrInvalid
		}
		result.Attachments = append(result.Attachments, Attachment{ID: index + 1, FileName: attachment.FileName, ContentType: attachment.ContentType, Size: *attachment.Size})
	}
	return result, nil
}

// TextFormat returns the extracted file extension of a text subtitle track.
func (t Track) TextFormat() (string, bool) {
	if t.Type != "subtitles" {
		return "", false
	}
	format, ok := textCodecs[t.CodecID]
	return format, ok
}

// Font reports whether an attachment is a font with a usable file name.
func (a Attachment) Font() bool {
	return domain.ValidAttachmentFileName(a.FileName) && (domain.IsFontFileName(a.FileName) || fontTypes[a.ContentType])
}
