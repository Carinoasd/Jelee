package domain

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

const NFOItemSeriesFieldsVersion = "series-details-v1"

// Counts retain the -1 sentinel emitted by the retained SeriesNfoSaver.
// Missing, unknown (-1), and zero are distinct observations.
type NFOSeriesDetails struct {
	SeasonCount   *int
	EpisodeCount  *int
	Status        string
	AirsDayOfWeek string
	AirsTime      string
}

func ItemMetadataSeriesFieldNames() []string {
	return []string{"seasonCount", "episodeCount", "seriesStatus", "airsDayOfWeek", "airsTime"}
}

func IsItemMetadataSeriesField(field string) bool {
	switch field {
	case "seasonCount", "episodeCount", "seriesStatus", "airsDayOfWeek", "airsTime":
		return true
	}
	return false
}

func ValidMetadataSeriesValue(field string, raw json.RawMessage) bool {
	if !IsItemMetadataSeriesField(field) || len(raw) > 1024 || !utf8.Valid(raw) {
		return false
	}
	if string(raw) == "null" {
		return true
	}
	if field == "seasonCount" || field == "episodeCount" {
		var count *int
		return json.Unmarshal(raw, &count) == nil && count != nil && *count >= -1 && *count <= 1000000
	}
	var value *string
	return json.Unmarshal(raw, &value) == nil && value != nil && validMetadataReferenceText(*value, 128, true)
}

func ValidNFOSeriesDetails(v NFOSeriesDetails) bool {
	if v.SeasonCount == nil && v.EpisodeCount == nil && v.Status == "" && v.AirsDayOfWeek == "" && v.AirsTime == "" {
		return false
	}
	for _, count := range []*int{v.SeasonCount, v.EpisodeCount} {
		if count != nil && (*count < -1 || *count > 1000000) {
			return false
		}
	}
	for _, value := range []string{v.Status, v.AirsDayOfWeek, v.AirsTime} {
		if value != "" && !validMetadataReferenceText(value, 128, true) {
			return false
		}
	}
	return true
}

func CloneNFOSeriesDetails(v *NFOSeriesDetails) *NFOSeriesDetails {
	if v == nil {
		return nil
	}
	result := *v
	if v.SeasonCount != nil {
		count := *v.SeasonCount
		result.SeasonCount = &count
	}
	if v.EpisodeCount != nil {
		count := *v.EpisodeCount
		result.EpisodeCount = &count
	}
	return &result
}

func EqualNFOSeriesDetails(a, b *NFOSeriesDetails) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	equalCount := func(a, b *int) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }
	return equalCount(a.SeasonCount, b.SeasonCount) && equalCount(a.EpisodeCount, b.EpisodeCount) && a.Status == b.Status && a.AirsDayOfWeek == b.AirsDayOfWeek && a.AirsTime == b.AirsTime
}

func MetadataSeriesFieldForLock(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "season", "seasoncount":
		return "seasonCount"
	case "episode", "episodecount":
		return "episodeCount"
	case "status", "seriesstatus":
		return "seriesStatus"
	case "airdays", "airs_dayofweek", "airsdayofweek":
		return "airsDayOfWeek"
	case "airtime", "airs_time", "airstime":
		return "airsTime"
	}
	return ""
}
