package domain

import (
	"slices"
	"strings"
	"unicode/utf8"
)

const NFOItemListFieldsVersion = "string-lists-v1"
const MaxMetadataListEntries = 128
const MaxMetadataListValueBytes = 1024
const MaxMetadataListBytes = 16384

type NFOStringList struct {
	Field  string
	Values []string
}

func ItemMetadataListFieldNames() []string {
	return []string{"genres", "tags", "studios", "countries", "languages", "directors", "writers", "producers"}
}

func IsItemMetadataListField(field string) bool {
	return slices.Contains(ItemMetadataListFieldNames(), field)
}

func MetadataListFieldForLock(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "genre", "genres":
		return "genres"
	case "tag", "tags", "style":
		return "tags"
	case "studio", "studios":
		return "studios"
	case "country", "countries", "productionlocations":
		return "countries"
	case "language", "languages":
		return "languages"
	case "director", "directors":
		return "directors"
	case "writer", "writers", "credits":
		return "writers"
	case "producer", "producers":
		return "producers"
	}
	return ""
}

func ValidMetadataStringList(values []string) bool {
	if values == nil || len(values) > MaxMetadataListEntries {
		return false
	}
	total := 0
	for _, value := range values {
		if !utf8.ValidString(value) || len(value) > MaxMetadataListValueBytes || strings.TrimSpace(value) == "" || strings.ContainsRune(value, 0) {
			return false
		}
		total += len(value)
	}
	return total <= MaxMetadataListBytes
}

func CloneNFOStringLists(values []NFOStringList) []NFOStringList {
	result := slices.Clone(values)
	for i := range result {
		result[i].Values = slices.Clone(result[i].Values)
	}
	return result
}
