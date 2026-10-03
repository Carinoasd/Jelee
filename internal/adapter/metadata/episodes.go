package metadata

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type episodeKey struct {
	seriesID, seasonNumber, episodeNumber int32
	language                              string
}
type seasonCache = candidateCache[episodeKey, domain.SeasonCandidate]
type episodeCache = candidateCache[episodeKey, domain.EpisodeCandidate]
type providerEpisode struct {
	ID            int32  `json:"id"`
	ShowID        *int32 `json:"show_id"`
	SeasonNumber  *int32 `json:"season_number"`
	EpisodeNumber int32  `json:"episode_number"`
	Name          string `json:"name"`
	Overview      string `json:"overview"`
	AirDate       string `json:"air_date"`
}

func (t *TMDB) Season(ctx context.Context, seriesID, seasonNumber int32, language string) (domain.SeasonCandidate, error) {
	if err := ctx.Err(); err != nil {
		return domain.SeasonCandidate{}, err
	}
	if !domain.ValidSeasonRequest(seriesID, seasonNumber, language) {
		return domain.SeasonCandidate{}, domain.ErrInvalid
	}
	key := episodeKey{seriesID, seasonNumber, 0, language}
	if value, ok := t.seasons.get(key, t.now()); ok {
		return cloneSeason(value), nil
	}
	budget, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	query := url.Values{"api_key": {t.key}, "language": {language}}
	r, err := t.providerRequest(budget, "https://api.themoviedb.org/3"+seasonPath(seriesID, seasonNumber)+"?"+query.Encode(), 1<<20)
	if err := seriesResponseError(r, err, true); err != nil {
		return domain.SeasonCandidate{}, err
	}
	var raw struct {
		ID           int32              `json:"id"`
		ShowID       *int32             `json:"show_id"`
		SeasonNumber *int32             `json:"season_number"`
		Name         string             `json:"name"`
		Overview     string             `json:"overview"`
		AirDate      string             `json:"air_date"`
		Episodes     *[]providerEpisode `json:"episodes"`
	}
	if json.Unmarshal(r.Body, &raw) != nil || raw.SeasonNumber == nil || *raw.SeasonNumber != seasonNumber || (raw.ShowID != nil && *raw.ShowID != seriesID) || raw.Episodes == nil || len(*raw.Episodes) > domain.MaxMetadataSeasonEpisodes {
		return domain.SeasonCandidate{}, ErrResponse
	}
	now := t.now().UTC()
	checked, err := t.movieCandidate(providerMovie{ID: raw.ID, Title: raw.Name, Overview: raw.Overview, ReleaseDate: raw.AirDate}, language, now)
	if err != nil {
		return domain.SeasonCandidate{}, err
	}
	value := domain.SeasonCandidate{ProviderID: checked.ProviderID, SeriesID: seriesID, SeasonNumber: seasonNumber, Source: checked.Source, SourceURL: "https://www.themoviedb.org" + seasonPath(seriesID, seasonNumber), Language: language, FetchedAt: now, Title: checked.Title, Overview: checked.Overview, AirDate: checked.ReleaseDate, Episodes: make([]domain.EpisodeCandidate, 0, len(*raw.Episodes))}
	ids, numbers := make(map[int32]bool), make(map[int32]bool)
	for _, episode := range *raw.Episodes {
		if ids[episode.ID] || numbers[episode.EpisodeNumber] {
			return domain.SeasonCandidate{}, ErrResponse
		}
		candidate, err := t.episodeCandidate(episode, seriesID, seasonNumber, language, now)
		if err != nil {
			return domain.SeasonCandidate{}, err
		}
		ids[episode.ID] = true
		numbers[episode.EpisodeNumber] = true
		value.Episodes = append(value.Episodes, candidate)
	}
	if err := budget.Err(); err != nil {
		return domain.SeasonCandidate{}, err
	}
	t.seasons.put(key, cloneSeason(value))
	return value, nil
}

func (t *TMDB) Episode(ctx context.Context, seriesID, seasonNumber, episodeNumber int32, language string) (domain.EpisodeCandidate, error) {
	if err := ctx.Err(); err != nil {
		return domain.EpisodeCandidate{}, err
	}
	if !domain.ValidEpisodeRequest(seriesID, seasonNumber, episodeNumber, language) {
		return domain.EpisodeCandidate{}, domain.ErrInvalid
	}
	key := episodeKey{seriesID, seasonNumber, episodeNumber, language}
	if value, ok := t.episodes.get(key, t.now()); ok {
		return value, nil
	}
	budget, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	query := url.Values{"api_key": {t.key}, "language": {language}}
	r, err := t.providerRequest(budget, "https://api.themoviedb.org/3"+episodePath(seriesID, seasonNumber, episodeNumber)+"?"+query.Encode(), 1<<20)
	if err := seriesResponseError(r, err, true); err != nil {
		return domain.EpisodeCandidate{}, err
	}
	var raw providerEpisode
	if json.Unmarshal(r.Body, &raw) != nil || raw.EpisodeNumber != episodeNumber {
		return domain.EpisodeCandidate{}, ErrResponse
	}
	value, err := t.episodeCandidate(raw, seriesID, seasonNumber, language, t.now().UTC())
	if err != nil {
		return domain.EpisodeCandidate{}, err
	}
	if err := budget.Err(); err != nil {
		return domain.EpisodeCandidate{}, err
	}
	t.episodes.put(key, value)
	return value, nil
}

func (t *TMDB) episodeCandidate(raw providerEpisode, seriesID, seasonNumber int32, language string, at time.Time) (domain.EpisodeCandidate, error) {
	if raw.SeasonNumber == nil || *raw.SeasonNumber != seasonNumber || raw.EpisodeNumber <= 0 || (raw.ShowID != nil && *raw.ShowID != seriesID) {
		return domain.EpisodeCandidate{}, ErrResponse
	}
	checked, err := t.movieCandidate(providerMovie{ID: raw.ID, Title: raw.Name, Overview: raw.Overview, ReleaseDate: raw.AirDate}, language, at)
	if err != nil {
		return domain.EpisodeCandidate{}, err
	}
	return domain.EpisodeCandidate{ProviderID: checked.ProviderID, SeriesID: seriesID, SeasonNumber: seasonNumber, EpisodeNumber: raw.EpisodeNumber, Source: checked.Source, SourceURL: "https://www.themoviedb.org" + episodePath(seriesID, seasonNumber, raw.EpisodeNumber), Language: language, FetchedAt: at, Title: checked.Title, Overview: checked.Overview, AirDate: checked.ReleaseDate}, nil
}

func seasonPath(seriesID, seasonNumber int32) string {
	return "/tv/" + strconv.FormatInt(int64(seriesID), 10) + "/season/" + strconv.FormatInt(int64(seasonNumber), 10)
}
func episodePath(seriesID, seasonNumber, episodeNumber int32) string {
	return seasonPath(seriesID, seasonNumber) + "/episode/" + strconv.FormatInt(int64(episodeNumber), 10)
}
func cloneSeason(value domain.SeasonCandidate) domain.SeasonCandidate {
	episodes := make([]domain.EpisodeCandidate, len(value.Episodes))
	copy(episodes, value.Episodes)
	value.Episodes = episodes
	return value
}
