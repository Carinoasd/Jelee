package domain

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

const NFOItemEpisodeFieldsVersion = "episode-details-v1"

type NFOEpisodeDetails struct {
	SeasonNumber   *int
	EpisodeNumber  *int
	DisplaySeason  *int
	DisplayEpisode *int
	Aired          string
	ShowTitle      string
}

func ItemMetadataEpisodeFieldNames() []string {
	return []string{"seasonNumber", "episodeNumber", "displaySeason", "displayEpisode", "aired", "showTitle"}
}

func IsItemMetadataEpisodeField(field string) bool {
	switch field {
	case "seasonNumber", "episodeNumber", "displaySeason", "displayEpisode", "aired", "showTitle":
		return true
	}
	return false
}

func ValidMetadataEpisodeValue(field string, raw json.RawMessage) bool {
	if !IsItemMetadataEpisodeField(field) || len(raw) > 8192 || !utf8.Valid(raw) {
		return false
	}
	if string(raw) == "null" {
		return true
	}
	if field != "aired" && field != "showTitle" {
		var number *int
		return json.Unmarshal(raw, &number) == nil && number != nil && *number >= 0 && *number <= 1000000
	}
	var value *string
	if json.Unmarshal(raw, &value) != nil || value == nil {
		return false
	}
	if field == "aired" {
		return ValidMetadataAddedDate(*value)
	}
	return validMetadataReferenceText(*value, 1024, true)
}

func ValidNFOEpisodeDetails(v NFOEpisodeDetails) bool {
	if v.SeasonNumber == nil && v.EpisodeNumber == nil && v.DisplaySeason == nil && v.DisplayEpisode == nil && v.Aired == "" && v.ShowTitle == "" {
		return false
	}
	for _, number := range []*int{v.SeasonNumber, v.EpisodeNumber, v.DisplaySeason, v.DisplayEpisode} {
		if number != nil && (*number < 0 || *number > 1000000) {
			return false
		}
	}
	return (v.Aired == "" || ValidMetadataAddedDate(v.Aired)) && (v.ShowTitle == "" || validMetadataReferenceText(v.ShowTitle, 1024, true))
}

func CloneNFOEpisodeDetails(v *NFOEpisodeDetails) *NFOEpisodeDetails {
	if v == nil {
		return nil
	}
	result := *v
	for _, number := range []**int{&result.SeasonNumber, &result.EpisodeNumber, &result.DisplaySeason, &result.DisplayEpisode} {
		if *number != nil {
			value := **number
			*number = &value
		}
	}
	return &result
}

func EqualNFOEpisodeDetails(a, b *NFOEpisodeDetails) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	equalNumber := func(a, b *int) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }
	return equalNumber(a.SeasonNumber, b.SeasonNumber) && equalNumber(a.EpisodeNumber, b.EpisodeNumber) && equalNumber(a.DisplaySeason, b.DisplaySeason) && equalNumber(a.DisplayEpisode, b.DisplayEpisode) && a.Aired == b.Aired && a.ShowTitle == b.ShowTitle
}

func MetadataEpisodeFieldForLock(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "season", "seasonnumber", "parentindexnumber":
		return "seasonNumber"
	case "episode", "episodenumber", "indexnumber":
		return "episodeNumber"
	case "displayseason":
		return "displaySeason"
	case "displayepisode":
		return "displayEpisode"
	case "aired", "dateaired":
		return "aired"
	case "showtitle", "seriesname":
		return "showTitle"
	}
	return ""
}
