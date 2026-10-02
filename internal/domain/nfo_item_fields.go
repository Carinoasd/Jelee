package domain

import (
	"slices"
	"strings"
	"time"
)

const NFOItemFieldsVersion = "four-text-fields-v1"
const NFOItemLockFieldsVersion = "lock-only-fields-v1"
const NFOItemSortFieldsVersion = "five-field-projection-v1"
const NFOItemTextFieldsVersion = "extended-text-fields-v1"
const NFOItemYearFieldsVersion = "year-fact-v1"

// Each projection keeps its published field vocabulary. Returned slices are owned.
func NFOItemFieldNames(version string) []string {
	switch version {
	case NFOItemFieldsVersion, NFOItemLockFieldsVersion:
		return []string{"title", "originalTitle", "overview", "date"}
	case NFOItemSortFieldsVersion:
		return []string{"title", "originalTitle", "overview", "date", "sortTitle"}
	case NFOItemYearFieldsVersion:
		return []string{"title", "originalTitle", "overview", "date", "sortTitle", "tagline", "outline", "mpaa", "certification", "year"}
	case NFOItemTextFieldsVersion:
		return []string{"title", "originalTitle", "overview", "date", "sortTitle", "tagline", "outline", "mpaa", "certification"}
	}
	return nil
}

func ItemMetadataFieldNames() []string {
	return []string{"title", "originalTitle", "overview", "date", "sortTitle", "tagline", "outline", "mpaa", "certification"}
}

// NFOItemFields is an internal observation, not a caller supplied write intent.
// It contains no filesystem paths. Library/item ownership is checked separately.
type NFOItemFields struct {
	Version      string
	Kind         string
	Identity     NFOIdentity
	Stamp        NFOStamp
	ReadAt       time.Time
	Facts        []NFOIntegerFact
	Fields       []NFOTextField
	LockData     bool
	LockedFields []string
}

type NFOIntegerFact struct {
	Field string
	Value int
}

type NFOTextField struct {
	Field string
	Value string
}

func (NFOItemFields) String() string   { return "nfo item fields (data redacted)" }
func (NFOItemFields) GoString() string { return "nfo item fields (data redacted)" }

func ValidNFOItemFields(v NFOItemFields) bool {
	if (v.Kind != "Movie" && v.Kind != "Series") || ValidateNFOIdentity(v.Identity) != nil || ValidateNFOStamp(v.Stamp) != nil || v.Stamp.Size > v.Identity.MaxSourceBytes || v.ReadAt.IsZero() || v.ReadAt.Year() < 1 || v.ReadAt.Year() > 9999 || len(v.Fields) > len(NFOItemFieldNames(v.Version)) || len(v.LockedFields) > 128 {
		return false
	}
	if len(v.Facts) > 1 || len(v.Facts) > 0 && v.Version != NFOItemYearFieldsVersion {
		return false
	}
	for _, fact := range v.Facts {
		if fact.Field != "year" || fact.Value < 1 || fact.Value > 9999 {
			return false
		}
	}
	switch v.Version {
	case NFOItemFieldsVersion:
		if len(v.Fields) == 0 || len(v.Fields) > 4 {
			return false
		}
	case NFOItemLockFieldsVersion:
		if len(v.Fields) != 0 || !HasNFOItemFieldLock(v) {
			return false
		}
	case NFOItemSortFieldsVersion, NFOItemTextFieldsVersion, NFOItemYearFieldsVersion:
		if len(v.Fields) == 0 && len(v.Facts) == 0 && !HasNFOItemFieldLock(v) {
			return false
		}
	default:
		return false
	}
	seen := map[string]bool{}
	for _, field := range v.Fields {
		if seen[field.Field] || !slices.Contains(NFOItemFieldNames(v.Version), field.Field) || !ValidItemMetadataValue(field.Field, field.Value) || strings.TrimSpace(field.Value) == "" {
			return false
		}
		seen[field.Field] = true
	}
	for _, field := range v.LockedFields {
		if len(field) > 128 || strings.ContainsRune(field, 0) {
			return false
		}
	}
	return true
}

func HasNFOItemFieldLock(fields NFOItemFields) bool {
	for _, field := range NFOItemFieldNames(fields.Version) {
		if NFOFieldLocked(fields, field) {
			return true
		}
	}
	return false
}
