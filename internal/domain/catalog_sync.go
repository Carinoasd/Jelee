package domain

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// JobCatalogSync turns an accepted inventory baseline into catalog structure.
const JobCatalogSync = "catalog_sync"

// CatalogSyncParserVersion is stored with every derived row. Changing the
// parser or the planning rules must change this value so tracked files are
// planned again on the next synchronisation.
const CatalogSyncParserVersion = "medianame-v1"

const (
	CatalogSyncModeSync   = "sync"
	CatalogSyncModeAccept = "accept"
)

// CatalogSyncReport exposes durable counters only; it never contains paths.
type CatalogSyncReport struct {
	JobID         string `json:"jobId"`
	LibraryID     string `json:"libraryId"`
	Mode          string `json:"mode"`
	Phase         string `json:"phase"`
	SourceJobID   string `json:"sourceJobId,omitempty"`
	Examined      int64  `json:"examined"`
	Created       int64  `json:"created"`
	Updated       int64  `json:"updated"`
	Unchanged     int64  `json:"unchanged"`
	Protected     int64  `json:"protected"`
	Pending       int64  `json:"pending"`
	MarkedMissing int64  `json:"markedMissing"`
	Removed       int64  `json:"removed"`
}

type CatalogSyncSettings struct {
	LibraryID string `json:"libraryId"`
	Auto      bool   `json:"auto"`
}

// CatalogPendingEntry is a file whose name did not support automatic
// structure. The relative path is library-local; absolute roots stay private.
type CatalogPendingEntry struct {
	ID         string `json:"id"`
	RootID     string `json:"rootId"`
	Path       string `json:"path"`
	Reason     string `json:"reason"`
	Kind       string `json:"kind"`
	Confidence string `json:"confidence"`
	Title      string `json:"title"`
	Year       int    `json:"year,omitempty"`
	Season     *int   `json:"season,omitempty"`
	Episode    *int   `json:"episode,omitempty"`
	EpisodeEnd *int   `json:"episodeEnd,omitempty"`
	Absolute   int    `json:"absolute,omitempty"`
	Special    string `json:"special"`
}

// SeasonTitle names a season created from file names.
func SeasonTitle(season int) string {
	if season == 0 {
		return "Specials"
	}
	return fmt.Sprintf("Season %d", season)
}

// NormalizeCatalogTitle folds case and separators for grouping only.
func NormalizeCatalogTitle(title string) string {
	var b strings.Builder
	space := false
	for _, r := range title {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteRune(unicode.ToLower(r))
			space = false
			continue
		}
		space = true
	}
	return b.String()
}

// CatalogGroupDigest derives the fixed-size grouping key. Parts are
// length-prefixed so no concatenation of two keys can collide.
func CatalogGroupDigest(parts ...string) []byte {
	h := sha256.New()
	for _, part := range parts {
		fmt.Fprintf(h, "%d:%s;", len(part), part)
	}
	return h.Sum(nil)
}

const catalogTitleMaxBytes = 1024

// ClampCatalogTitle trims a derived title to the catalog limit on a rune boundary.
func ClampCatalogTitle(title string) string {
	title = strings.TrimSpace(title)
	if len(title) <= catalogTitleMaxBytes {
		return title
	}
	cut := catalogTitleMaxBytes
	for cut > 0 && !utf8.RuneStart(title[cut]) {
		cut--
	}
	return strings.TrimSpace(title[:cut])
}

// ScanMetadataSkip reports why a file-name value may not replace a field.
// Scan values are the lowest priority: only an unlocked scan value is
// replaced. Manual, NFO, TMDB and pre-existing values always win.
func ScanMetadataSkip(old ItemMetadataField) string {
	if old.Locked || old.NFOOrigin != nil && old.NFOOrigin.Locked || old.NFOLockOrigin != nil && old.NFOLockOrigin.Locked {
		return "locked"
	}
	switch old.Source {
	case "scan":
		return ""
	case "manual", "nfo", "tmdb":
		return old.Source
	}
	return "existing"
}

// ValidCatalogPendingReason lists the persisted pending reasons.
func ValidCatalogPendingReason(reason string) bool {
	return reason == "confidence" || reason == "rejected" || reason == "unsupported" || reason == "conflict"
}
