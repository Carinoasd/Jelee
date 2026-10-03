package domain

import "time"

// External identifier namespaces accepted by a provider ID lookup. Only
// sources that resolve to movies or series are exposed.
const (
	ExternalSourceIMDb     = "imdb_id"
	ExternalSourceTVDB     = "tvdb_id"
	ExternalSourceWikidata = "wikidata_id"
)

// MaxExternalIDResults bounds each candidate list of one lookup.
const MaxExternalIDResults = 20

// ExternalIDMatches lists provider candidates for one external identifier.
// Every candidate still needs confirmation before it is applied.
type ExternalIDMatches struct {
	ExternalSource string            `json:"externalSource"`
	ExternalID     string            `json:"externalId"`
	Language       string            `json:"language"`
	FetchedAt      time.Time         `json:"fetchedAt"`
	Movies         []MovieCandidate  `json:"movies"`
	Series         []SeriesCandidate `json:"series"`
}

func (v ExternalIDMatches) FetchedTime() time.Time { return v.FetchedAt }

func CloneExternalIDMatches(value ExternalIDMatches) ExternalIDMatches {
	value.Movies = append([]MovieCandidate{}, value.Movies...)
	value.Series = append([]SeriesCandidate{}, value.Series...)
	return value
}

// ValidExternalID accepts only the canonical form of each source, so the
// value is safe as a single URL path segment without escaping.
func ValidExternalID(source, id string) bool {
	switch source {
	case ExternalSourceIMDb:
		// Title identifiers only: "tt" and 7 to 10 digits.
		return len(id) >= 9 && len(id) <= 12 && id[:2] == "tt" && asciiDigits(id[2:])
	case ExternalSourceTVDB:
		return positiveInt32Decimal(id)
	case ExternalSourceWikidata:
		return len(id) >= 2 && len(id) <= 11 && id[0] == 'Q' && id[1] != '0' && asciiDigits(id[1:])
	}
	return false
}

func ValidExternalIDRequest(source, id, language string) bool {
	return ValidExternalID(source, id) && ValidMetadataLanguage(language)
}

func asciiDigits(value string) bool {
	if value == "" {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}

func positiveInt32Decimal(value string) bool {
	if !asciiDigits(value) || value[0] == '0' || len(value) > 10 {
		return false
	}
	var n int64
	for i := 0; i < len(value); i++ {
		n = n*10 + int64(value[i]-'0')
	}
	return n <= 1<<31-1
}
