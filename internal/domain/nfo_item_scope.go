package domain

import (
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
)

// NFOItemScope is repository-owned resolver state. Paths are private and must
// never be accepted in an HTTP request or copied into audit metadata.
type NFOItemScope struct {
	ItemID     string
	LibraryID  string
	SourceID   string
	RootID     string
	Kind       string
	Revision   int64
	Generation int64
	// Zero is historical data without a root generation observation.
	RootGeneration int64
	DirectoryPath  string
	MediaPath      string
	Source         NFOSource
}

func (NFOItemScope) String() string   { return "nfo item scope (paths redacted)" }
func (NFOItemScope) GoString() string { return "nfo item scope (paths redacted)" }

func AdjacentNFOPath(media string) (string, bool) {
	if !utf8.ValidString(media) || len(media) < 1 || len(media) > 1024 || strings.HasPrefix(media, "/") || strings.ContainsAny(media, "\\:") || strings.ContainsFunc(media, unicode.IsControl) || path.Clean(media) != media || media == "." || strings.HasPrefix(media, "../") || strings.Count(media, "/") >= 128 {
		return "", false
	}
	ext := path.Ext(media)
	base := strings.TrimSuffix(media, ext)
	if ext == "" || ext == "." || strings.EqualFold(ext, ".nfo") || path.Base(base) == "." || path.Base(base) == "" {
		return "", false
	}
	nfo := base + ".nfo"
	return nfo, len(nfo) <= 1024
}

func ValidNFOItemScope(v NFOItemScope) bool {
	relative, ok := AdjacentNFOPath(v.MediaPath)
	if v.DirectoryPath != "" {
		relative, ok = DirectoryNFOPath(v.DirectoryPath, v.Kind)
		ok = ok && v.MediaPath == ""
	}
	if v.Kind == "Season" && v.DirectoryPath == "" {
		return false
	}
	return ok && ValidID(v.ItemID) && ValidID(v.LibraryID) && ValidID(v.SourceID) && ValidID(v.RootID) && v.Revision >= 1 && v.Revision < ItemMetadataRevisionMax && v.Generation >= 1 && v.RootGeneration >= 0 && (v.Kind == "Movie" || v.Kind == "HomeVideo" || v.Kind == "Series" || v.Kind == "Episode" || v.Kind == "Season") && v.Source.RootPath != "" && v.Source.RelativePath == relative
}
