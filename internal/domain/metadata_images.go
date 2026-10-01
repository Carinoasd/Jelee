package domain

import "time"

const MetadataImageLimit = 1000

type MetadataImageCandidate struct {
	Kind              string  `json:"kind"`
	Language          string  `json:"language"`
	FilePath          string  `json:"filePath"`
	URL               string  `json:"url"`
	Width             int32   `json:"width"`
	Height            int32   `json:"height"`
	VoteAverage       float64 `json:"voteAverage"`
	VoteCount         int32   `json:"voteCount"`
	NeedsConfirmation bool    `json:"needsConfirmation"`
}

type MetadataImages struct {
	Resource       string                   `json:"resource"`
	ProviderID     int32                    `json:"providerId"`
	Source         string                   `json:"source"`
	SourceURL      string                   `json:"sourceUrl"`
	FetchedAt      time.Time                `json:"fetchedAt"`
	ImageLanguages []string                 `json:"imageLanguages"`
	Candidates     []MetadataImageCandidate `json:"candidates"`
}

func (v MetadataImages) FetchedTime() time.Time { return v.FetchedAt }
func ValidMetadataImageResource(resource string) bool {
	return resource == "movie" || resource == "series"
}

func CloneMetadataImages(value MetadataImages) MetadataImages {
	value.ImageLanguages = append([]string(nil), value.ImageLanguages...)
	value.Candidates = append([]MetadataImageCandidate{}, value.Candidates...)
	return value
}
