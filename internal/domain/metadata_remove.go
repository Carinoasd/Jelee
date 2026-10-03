package domain

import (
	"path"
	"strings"
)

// ExternalMetadataSource is the only external provider whose values are
// persisted today (G14.7). Facts never carry it; the schema rejects it.
const ExternalMetadataSource = "tmdb"

// MetadataRemoveResult reports one external-metadata removal review.
type MetadataRemoveResult struct {
	Metadata ItemMetadata        `json:"metadata"`
	Removed  []string            `json:"removed"`
	Skipped  []MetadataFieldSkip `json:"skipped"`
}

func CloneMetadataRemoveResult(value MetadataRemoveResult) MetadataRemoveResult {
	value.Metadata = CloneItemMetadata(value.Metadata)
	value.Removed = append([]string{}, value.Removed...)
	value.Skipped = append([]MetadataFieldSkip{}, value.Skipped...)
	return value
}

func ValidMetadataRemoveInput(item string, expected int64) bool {
	return ValidID(item) && expected >= 1 && expected < ItemMetadataRevisionMax
}

// MetadataFieldLocked follows the field lock semantics shared by NFO and TMDB
// applies: a Jelee lock, a persisted NFO value lock or an NFO lock-only intent.
func MetadataFieldLocked(field ItemMetadataField) bool {
	return field.Locked || field.NFOOrigin != nil && field.NFOOrigin.Locked || field.NFOLockOrigin != nil && field.NFOLockOrigin.Locked
}

// ExternalMetadataRemovable reports whether a persisted field is an unlocked
// external value. Locked external values are an explicit administrator
// decision and stay until the lock is released.
func ExternalMetadataRemovable(field ItemMetadataField) bool {
	return field.Source == ExternalMetadataSource && !MetadataFieldLocked(field)
}

// LocalFallbackTitle derives the required catalog title once a provider title
// is removed. Providers only replace an existing title explicitly, so the
// imported value is no longer stored; the library-relative media filename
// (without its extension) or directory name is the local value left.
func LocalFallbackTitle(relative string, directory bool) (string, bool) {
	relative = strings.TrimRight(relative, "/")
	base := path.Base(relative)
	if relative == "" || base == "." || base == "/" {
		return "", false
	}
	title := base
	if !directory {
		if stem := strings.TrimSuffix(base, path.Ext(base)); strings.TrimSpace(stem) != "" {
			title = stem
		}
	}
	if !ValidItemMetadataValue("title", title) {
		return "", false
	}
	return title, true
}
