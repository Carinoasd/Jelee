package domain

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// SidecarTracksPerSource bounds one source's replacement set so a single
	// directory full of look-alike names cannot grow one transaction without
	// limit.
	SidecarTracksPerSource = 256
	// SidecarLanguagesMax bounds a multi-language marker such as "chs&eng".
	SidecarLanguagesMax = 8
	// SidecarFingerprintBytes is the SHA-256 size of an optional fingerprint.
	SidecarFingerprintBytes = 32
)

// SidecarCharsets mirrors the subtitle charset names (IANA preferred names)
// that detection records. The database check holds the same list.
var SidecarCharsets = []string{"UTF-8", "UTF-16LE", "UTF-16BE", "GB18030", "Big5", "Shift_JIS", "EUC-JP", "EUC-KR", "windows-1252"}

// SidecarTrackInput is one external file attached to a media source by a
// scan. Track is the parsed name; its Subdir is not stored because the
// relative path already holds it. Charset is the detected charset of a text
// subtitle or empty. Fingerprint is optional.
type SidecarTrackInput struct {
	Track            SidecarTrack
	RootID           string
	RelativePath     string
	Charset          string
	Size             int64
	ModifiedUnixNano int64
	Fingerprint      []byte
}

// SidecarTrackRecord is a stored sidecar row. It carries the library
// relative path but never the absolute root, and is redacted from
// diagnostics.
type SidecarTrackRecord struct {
	ID, SourceID, LibraryID, RootID string
	RelativePath                    string
	Track                           SidecarTrack
	Charset                         string
	Size, ModifiedUnixNano          int64
	Fingerprint                     []byte
	CreatedAt, UpdatedAt            time.Time
}

func (SidecarTrackRecord) String() string   { return "sidecar track (redacted)" }
func (SidecarTrackRecord) GoString() string { return "sidecar track (redacted)" }

// SidecarTrackLocation is what direct delivery needs to open one sidecar
// file: the absolute library root, the confined relative path and the
// format. It is redacted from diagnostics.
type SidecarTrackLocation struct {
	ID, SourceID, Kind, Format string
	RootPath, RelativePath     string
	Charset                    string
	Size, ModifiedUnixNano     int64
}

func (SidecarTrackLocation) String() string   { return "sidecar location (redacted)" }
func (SidecarTrackLocation) GoString() string { return "sidecar location (redacted)" }

// SidecarTrackChanges counts what one replacement did.
type SidecarTrackChanges struct {
	Added, Updated, Removed int
}

func (c SidecarTrackChanges) Total() int { return c.Added + c.Updated + c.Removed }

// ValidSidecarTrackInput mirrors the media_sidecar_tracks checks so a bad
// observation is rejected before any statement runs.
func ValidSidecarTrackInput(in SidecarTrackInput) bool {
	t := in.Track
	switch t.Kind {
	case SidecarKindSubtitle:
		if !sidecarSubtitleFormats[t.Format] {
			return false
		}
	case SidecarKindAudio:
		if !sidecarAudioFormats[t.Format] || in.Charset != "" {
			return false
		}
	default:
		return false
	}
	if !ValidID(in.RootID) || !ValidItemImageRelativePath(in.RelativePath) ||
		!strings.HasSuffix(strings.ToLower(in.RelativePath), "."+t.Format) {
		return false
	}
	if in.Size < 0 || in.Fingerprint != nil && len(in.Fingerprint) != SidecarFingerprintBytes {
		return false
	}
	if in.Charset != "" && !validSidecarCharset(in.Charset) {
		return false
	}
	if t.Title != "" && !validSidecarTitle(t.Title) {
		return false
	}
	if t.Language == "" {
		return len(t.Languages) == 0
	}
	if len(t.Languages) == 0 || len(t.Languages) > SidecarLanguagesMax || t.Languages[0] != t.Language {
		return false
	}
	for _, tag := range t.Languages {
		if !ValidSidecarLanguageTag(tag) {
			return false
		}
	}
	return true
}

// ValidSidecarLanguageTag accepts the canonical BCP 47 shape the name
// parser emits: "en", "yue", "zh-Hans", "pt-BR", "es-419", "zh-Hant-TW".
func ValidSidecarLanguageTag(tag string) bool {
	parts := strings.Split(tag, "-")
	if len(parts) > 3 || len(parts[0]) < 2 || len(parts[0]) > 3 || !sidecarAlpha(parts[0]) {
		return false
	}
	rest := parts[1:]
	if len(rest) > 0 && len(rest[0]) == 4 {
		script := rest[0]
		if script[0] < 'A' || script[0] > 'Z' || !sidecarAlpha(script[1:]) {
			return false
		}
		rest = rest[1:]
	}
	if len(rest) > 0 {
		region := rest[0]
		switch {
		case len(region) == 2 && region[0] >= 'A' && region[0] <= 'Z' && region[1] >= 'A' && region[1] <= 'Z':
		case len(region) == 3 && strings.Trim(region, "0123456789") == "":
		default:
			return false
		}
		rest = rest[1:]
	}
	return len(rest) == 0
}

func validSidecarCharset(value string) bool {
	for _, name := range SidecarCharsets {
		if value == name {
			return true
		}
	}
	return false
}

func validSidecarTitle(value string) bool {
	if len(value) > SidecarNameMaxBytes || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
