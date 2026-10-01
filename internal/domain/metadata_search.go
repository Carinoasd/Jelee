package domain

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

type MetadataSearchInput struct {
	Query    string `json:"query"`
	Year     int    `json:"year"`
	Language string `json:"language"`
}

type MovieSearchInput = MetadataSearchInput

type MovieMatch struct {
	Movie             MovieCandidate `json:"movie"`
	ExactTitle        bool           `json:"exactTitle"`
	ExactYear         bool           `json:"exactYear"`
	NeedsConfirmation bool           `json:"needsConfirmation"`
}

type MovieMatches struct {
	MovieSearchInput
	Candidates []MovieMatch `json:"candidates"`
}

func NormalizeMetadataSearch(input MetadataSearchInput) (MetadataSearchInput, error) {
	input.Query = strings.TrimSpace(input.Query)
	if input.Query == "" || len(input.Query) > 256 || !utf8.ValidString(input.Query) || strings.ContainsFunc(input.Query, unicode.IsControl) || !ValidMetadataLanguage(input.Language) || (input.Year != 0 && (input.Year < 1000 || input.Year > 9999)) {
		return MovieSearchInput{}, ErrInvalid
	}
	return input, nil
}
