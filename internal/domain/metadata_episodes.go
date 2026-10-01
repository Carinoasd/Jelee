package domain

import "time"

const MaxMetadataSeasonEpisodes = 1000

type EpisodeCandidate struct {
	ProviderID    int32     `json:"providerId"`
	SeriesID      int32     `json:"seriesId"`
	SeasonNumber  int32     `json:"seasonNumber"`
	EpisodeNumber int32     `json:"episodeNumber"`
	Source        string    `json:"source"`
	SourceURL     string    `json:"sourceUrl"`
	Language      string    `json:"language"`
	FetchedAt     time.Time `json:"fetchedAt"`
	Title         string    `json:"title"`
	Overview      string    `json:"overview"`
	AirDate       string    `json:"airDate"`
}

type SeasonCandidate struct {
	ProviderID   int32              `json:"providerId"`
	SeriesID     int32              `json:"seriesId"`
	SeasonNumber int32              `json:"seasonNumber"`
	Source       string             `json:"source"`
	SourceURL    string             `json:"sourceUrl"`
	Language     string             `json:"language"`
	FetchedAt    time.Time          `json:"fetchedAt"`
	Title        string             `json:"title"`
	Overview     string             `json:"overview"`
	AirDate      string             `json:"airDate"`
	Episodes     []EpisodeCandidate `json:"episodes"`
}

func (c EpisodeCandidate) FetchedTime() time.Time { return c.FetchedAt }
func (c SeasonCandidate) FetchedTime() time.Time  { return c.FetchedAt }

func ValidSeasonRequest(seriesID, seasonNumber int32, language string) bool {
	return seriesID > 0 && seasonNumber >= 0 && ValidMetadataLanguage(language)
}
func ValidEpisodeRequest(seriesID, seasonNumber, episodeNumber int32, language string) bool {
	return ValidSeasonRequest(seriesID, seasonNumber, language) && episodeNumber > 0
}
