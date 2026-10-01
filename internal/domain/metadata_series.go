package domain

import "time"

type SeriesCandidate struct {
	ProviderID    int32     `json:"providerId"`
	Source        string    `json:"source"`
	SourceURL     string    `json:"sourceUrl"`
	Language      string    `json:"language"`
	FetchedAt     time.Time `json:"fetchedAt"`
	Title         string    `json:"title"`
	OriginalTitle string    `json:"originalTitle"`
	Overview      string    `json:"overview"`
	FirstAirDate  string    `json:"firstAirDate"`
}

func (c MovieCandidate) FetchedTime() time.Time  { return c.FetchedAt }
func (c SeriesCandidate) FetchedTime() time.Time { return c.FetchedAt }

type SeriesSearchInput = MetadataSearchInput
type SeriesMatch struct {
	Series            SeriesCandidate `json:"series"`
	ExactTitle        bool            `json:"exactTitle"`
	ExactYear         bool            `json:"exactYear"`
	NeedsConfirmation bool            `json:"needsConfirmation"`
}
type SeriesMatches struct {
	SeriesSearchInput
	Candidates []SeriesMatch `json:"candidates"`
}
