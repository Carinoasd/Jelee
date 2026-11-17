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
	Resource   string `json:"resource"`
	ProviderID int32  `json:"providerId"`
	// SeriesID, SeasonNumber and EpisodeNumber locate season and episode
	// images; ProviderID is then the upstream season or episode identifier.
	SeriesID       int32                    `json:"seriesId,omitempty"`
	SeasonNumber   *int32                   `json:"seasonNumber,omitempty"`
	EpisodeNumber  int32                    `json:"episodeNumber,omitempty"`
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

// ValidMetadataEpisodeImageTarget accepts a season (episode 0) or an episode
// image request of one series. Season 0 holds specials.
func ValidMetadataEpisodeImageTarget(series, season, episode int32) bool {
	return series > 0 && season >= 0 && episode >= 0
}

// MetadataEpisodeImageResource names the image resource of a season or
// episode request.
func MetadataEpisodeImageResource(episode int32) string {
	if episode > 0 {
		return "episode"
	}
	return "season"
}

func CloneMetadataImages(value MetadataImages) MetadataImages {
	if value.SeasonNumber != nil {
		season := *value.SeasonNumber
		value.SeasonNumber = &season
	}
	value.ImageLanguages = append([]string(nil), value.ImageLanguages...)
	value.Candidates = append([]MetadataImageCandidate{}, value.Candidates...)
	return value
}
