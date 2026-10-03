package domain

import (
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
)

func ValidDirectorySourcePath(value string) bool {
	return utf8.ValidString(value) && len(value) > 0 && len(value) <= 1012 && !strings.HasPrefix(value, "/") && !strings.ContainsAny(value, "\\:") && !strings.ContainsFunc(value, unicode.IsControl) && path.Clean(value) == value && value != ".." && !strings.HasPrefix(value, "../") && strings.Count(value, "/") < 128
}

func DirectoryNFOPath(directory, kind string) (string, bool) {
	if !ValidDirectorySourcePath(directory) || kind != "Series" && kind != "Season" {
		return "", false
	}
	name := "tvshow.nfo"
	if kind == "Season" {
		name = "season.nfo"
	}
	return path.Join(directory, name), true
}

const NFOItemSeasonFieldsVersion = "season-details-v1"

type NFOSeasonDetails struct {
	Number *int
}

func CloneNFOSeasonDetails(value *NFOSeasonDetails) *NFOSeasonDetails {
	if value == nil {
		return nil
	}
	result := *value
	if value.Number != nil {
		number := *value.Number
		result.Number = &number
	}
	return &result
}

func EqualNFOSeasonDetails(a, b *NFOSeasonDetails) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Number == nil && b.Number == nil || a.Number != nil && b.Number != nil && *a.Number == *b.Number
}
