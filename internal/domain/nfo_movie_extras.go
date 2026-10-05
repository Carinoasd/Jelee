package domain

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const NFOItemMovieFieldsVersion = "movie-extra-fields-v1"
const MaxMetadataReferences = 128
const MaxMetadataReferenceBytes = 16384

type NFOArtwork struct {
	Kind     string `json:"kind"`
	Location string `json:"location"`
	Preview  string `json:"preview,omitempty"`
	Season   *int   `json:"season,omitempty"`
}

var addedDatePattern = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}( ([01][0-9]|2[0-3]):[0-5][0-9]:[0-5][0-9]|T([01][0-9]|2[0-3]):[0-5][0-9]:[0-5][0-9](\.[0-9]{1,9})?(Z|[+-]([01][0-9]|2[0-3]):[0-5][0-9]))?$`)

func ValidMetadataAddedDate(value string) bool {
	if len(value) > 64 || !addedDatePattern.MatchString(value) {
		return false
	}
	for _, layout := range []string{"2006-01-02", "2006-01-02 15:04:05", time.RFC3339Nano} {
		if parsed, err := time.Parse(layout, value); err == nil && parsed.Year() >= 1 && parsed.Year() <= 9999 {
			return true
		}
	}
	return false
}

func validMetadataReferenceText(value string, limit int, required bool) bool {
	return len(value) <= limit && utf8.ValidString(value) && !strings.ContainsRune(value, 0) && (!required || strings.TrimSpace(value) != "")
}

func ValidMetadataTrailers(values []string) bool {
	if values == nil || len(values) > MaxMetadataReferences {
		return false
	}
	total := 0
	for _, value := range values {
		if !validMetadataReferenceText(value, 4096, true) {
			return false
		}
		total += len(value)
	}
	return total <= MaxMetadataReferenceBytes
}

func ValidMetadataArtwork(values []NFOArtwork) bool {
	if values == nil || len(values) > MaxMetadataReferences {
		return false
	}
	total := 0
	for _, value := range values {
		if !validMetadataReferenceText(value.Kind, 64, true) || !validMetadataReferenceText(value.Location, 4096, true) || !validMetadataReferenceText(value.Preview, 4096, false) || value.Season != nil && (*value.Season < 0 || *value.Season > 1000000) {
			return false
		}
		total += len(value.Kind) + len(value.Location) + len(value.Preview)
	}
	return total <= MaxMetadataReferenceBytes
}

func ValidMetadataMovieValue(field string, raw json.RawMessage) bool {
	if len(raw) > 128<<10 || !utf8.Valid(raw) {
		return false
	}
	if string(raw) == "null" {
		return true
	}
	switch field {
	case "dateAdded":
		var value *string
		return json.Unmarshal(raw, &value) == nil && value != nil && ValidMetadataAddedDate(*value)
	case "trailers":
		var values []string
		return json.Unmarshal(raw, &values) == nil && ValidMetadataTrailers(values)
	case "art":
		var entries []map[string]json.RawMessage
		if json.Unmarshal(raw, &entries) != nil || entries == nil {
			return false
		}
		values := make([]NFOArtwork, 0, len(entries))
		for _, entry := range entries {
			for key := range entry {
				if key != "kind" && key != "location" && key != "preview" && key != "season" {
					return false
				}
			}
			var kind, location *string
			if json.Unmarshal(entry["kind"], &kind) != nil || kind == nil || json.Unmarshal(entry["location"], &location) != nil || location == nil {
				return false
			}
			value := NFOArtwork{Kind: *kind, Location: *location}
			if raw, ok := entry["preview"]; ok {
				var preview *string
				if json.Unmarshal(raw, &preview) != nil || preview == nil {
					return false
				}
				value.Preview = *preview
			}
			if raw, ok := entry["season"]; ok {
				if json.Unmarshal(raw, &value.Season) != nil {
					return false
				}
			}
			values = append(values, value)
		}
		return ValidMetadataArtwork(values)
	}
	return false
}

func CloneNFOArtwork(values []NFOArtwork) []NFOArtwork {
	result := slices.Clone(values)
	for i := range result {
		if result[i].Season != nil {
			season := *result[i].Season
			result[i].Season = &season
		}
	}
	return result
}

func EqualNFOArtwork(a, b []NFOArtwork) bool {
	return slices.EqualFunc(a, b, func(a, b NFOArtwork) bool {
		return a.Kind == b.Kind && a.Location == b.Location && a.Preview == b.Preview && (a.Season == nil && b.Season == nil || a.Season != nil && b.Season != nil && *a.Season == *b.Season)
	})
}

func MetadataMovieFieldForLock(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "dateadded", "datecreated":
		return "dateAdded"
	case "trailer", "trailers", "remotetrailers":
		return "trailers"
	case "art", "artwork", "images", "thumb", "poster", "fanart", "banner", "clearart", "clearlogo", "landscape":
		return "art"
	}
	return ""
}
