package domain

import (
	"strings"
	"time"
)

const NFOItemFieldsVersion = "four-text-fields-v1"

// NFOItemFields is an internal observation, not a caller supplied write intent.
// It contains no filesystem paths. Library/item ownership is checked separately.
type NFOItemFields struct {
	Version      string
	Kind         string
	Identity     NFOIdentity
	Stamp        NFOStamp
	ReadAt       time.Time
	Fields       []NFOTextField
	LockData     bool
	LockedFields []string
}

type NFOTextField struct {
	Field string
	Value string
}

func (NFOItemFields) String() string   { return "nfo item fields (data redacted)" }
func (NFOItemFields) GoString() string { return "nfo item fields (data redacted)" }

func ValidNFOItemFields(v NFOItemFields) bool {
	if v.Version != NFOItemFieldsVersion || (v.Kind != "Movie" && v.Kind != "Series") || ValidateNFOIdentity(v.Identity) != nil || ValidateNFOStamp(v.Stamp) != nil || v.Stamp.Size > v.Identity.MaxSourceBytes || v.ReadAt.IsZero() || v.ReadAt.Year() < 1 || v.ReadAt.Year() > 9999 || len(v.Fields) < 1 || len(v.Fields) > 4 || len(v.LockedFields) > 128 {
		return false
	}
	seen := map[string]bool{}
	for _, field := range v.Fields {
		if seen[field.Field] || !ValidItemMetadataValue(field.Field, field.Value) || strings.TrimSpace(field.Value) == "" {
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
