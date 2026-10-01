package domain

import (
	"strings"
	"time"
	"unicode/utf8"
)

const ItemMetadataRevisionMax int64 = 2147483647

type ItemMetadataField struct {
	Field     string     `json:"field"`
	Value     string     `json:"value"`
	Source    string     `json:"source"`
	Locked    bool       `json:"locked"`
	UpdatedAt *time.Time `json:"updatedAt"`
}

type ItemMetadata struct {
	ItemID    string              `json:"itemId"`
	LibraryID string              `json:"libraryId"`
	Revision  int64               `json:"revision"`
	Fields    []ItemMetadataField `json:"fields"`
}

// Manual patches carry no provider or NFO identity. A nil value preserves the
// current value; a pointer to an empty string explicitly clears an optional field.
type ItemMetadataPatch struct {
	Field  string  `json:"field"`
	Value  *string `json:"value,omitempty"`
	Locked *bool   `json:"locked,omitempty"`
}

func ValidItemMetadataValue(field, value string) bool {
	if !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return false
	}
	switch field {
	case "title":
		return len(value) <= 1024 && strings.TrimSpace(value) != ""
	case "originalTitle":
		return len(value) <= 1024
	case "overview":
		return len(value) <= 16384
	case "date":
		if value == "" {
			return true
		}
		parsed, err := time.Parse("2006-01-02", value)
		return err == nil && len(value) == 10 && parsed.Year() >= 1
	}
	return false
}

func ValidItemMetadataPatches(item string, revision int64, patches []ItemMetadataPatch) bool {
	if !ValidID(item) || revision < 1 || revision >= ItemMetadataRevisionMax || len(patches) < 1 || len(patches) > 4 {
		return false
	}
	seen := map[string]bool{}
	for _, p := range patches {
		if seen[p.Field] || p.Value == nil && p.Locked == nil {
			return false
		}
		seen[p.Field] = true
		if p.Value != nil {
			if !ValidItemMetadataValue(p.Field, *p.Value) {
				return false
			}
		} else if !ValidItemMetadataValue(p.Field, "") && p.Field != "title" {
			return false
		}
	}
	return true
}

func CloneItemMetadata(value ItemMetadata) ItemMetadata {
	value.Fields = append([]ItemMetadataField{}, value.Fields...)
	for i := range value.Fields {
		if value.Fields[i].UpdatedAt != nil {
			stamp := *value.Fields[i].UpdatedAt
			value.Fields[i].UpdatedAt = &stamp
		}
	}
	return value
}
