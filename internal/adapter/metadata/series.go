package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/outbound"
)

type providerSeries struct {
	ID           int32  `json:"id"`
	Name         string `json:"name"`
	OriginalName string `json:"original_name"`
	Overview     string `json:"overview"`
	FirstAirDate string `json:"first_air_date"`
}

func (t *TMDB) Series(ctx context.Context, id int32, language string) (domain.SeriesCandidate, error) {
	if err := ctx.Err(); err != nil {
		return domain.SeriesCandidate{}, err
	}
	if id <= 0 || !domain.ValidMetadataLanguage(language) {
		return domain.SeriesCandidate{}, domain.ErrInvalid
	}
	key := candidateKey{id, language}
	if value, ok := t.series.get(key, t.now()); ok {
		return value, nil
	}
	budget, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	query := url.Values{"api_key": {t.key}, "language": {language}}
	r, err := t.providerRequest(budget, "https://api.themoviedb.org/3/tv/"+strconv.FormatInt(int64(id), 10)+"?"+query.Encode(), 1<<20)
	if err := seriesResponseError(r, err, true); err != nil {
		return domain.SeriesCandidate{}, err
	}
	var raw providerSeries
	if json.Unmarshal(r.Body, &raw) != nil || raw.ID != id {
		return domain.SeriesCandidate{}, ErrResponse
	}
	value, err := t.seriesCandidate(raw, language, t.now().UTC())
	if err != nil {
		return domain.SeriesCandidate{}, err
	}
	if err := budget.Err(); err != nil {
		return domain.SeriesCandidate{}, err
	}
	t.series.put(key, value)
	return value, nil
}

func (t *TMDB) SearchSeries(ctx context.Context, input domain.SeriesSearchInput) ([]domain.SeriesCandidate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	input, err := domain.NormalizeMetadataSearch(input)
	if err != nil || strings.Contains(input.Query, t.key) {
		return nil, domain.ErrInvalid
	}
	budget, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	query := url.Values{"api_key": {t.key}, "query": {input.Query}, "language": {input.Language}, "page": {"1"}, "include_adult": {"false"}}
	if input.Year != 0 {
		query.Set("first_air_date_year", strconv.Itoa(input.Year))
	}
	r, err := t.providerRequest(budget, "https://api.themoviedb.org/3/search/tv?"+query.Encode(), 1<<20)
	if err := seriesResponseError(r, err, false); err != nil {
		return nil, err
	}
	var raw struct {
		Page    int               `json:"page"`
		Results *[]providerSeries `json:"results"`
	}
	if json.Unmarshal(r.Body, &raw) != nil || raw.Page != 1 || raw.Results == nil || len(*raw.Results) > 20 {
		return nil, ErrResponse
	}
	result := make([]domain.SeriesCandidate, 0, len(*raw.Results))
	ids := make(map[int32]bool, len(*raw.Results))
	now := t.now().UTC()
	for _, entry := range *raw.Results {
		if ids[entry.ID] {
			return nil, ErrResponse
		}
		candidate, err := t.seriesCandidate(entry, input.Language, now)
		if err != nil {
			return nil, err
		}
		ids[entry.ID] = true
		result = append(result, candidate)
	}
	if err := budget.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (t *TMDB) seriesCandidate(raw providerSeries, language string, at time.Time) (domain.SeriesCandidate, error) {
	// Explicit provider mapping reuses the validated text/date contract.
	checked, err := t.movieCandidate(providerMovie{ID: raw.ID, Title: raw.Name, OriginalTitle: raw.OriginalName, Overview: raw.Overview, ReleaseDate: raw.FirstAirDate}, language, at)
	if err != nil {
		return domain.SeriesCandidate{}, err
	}
	return domain.SeriesCandidate{ProviderID: checked.ProviderID, Source: checked.Source, SourceURL: "https://www.themoviedb.org/tv/" + strconv.FormatInt(int64(raw.ID), 10), Language: checked.Language, FetchedAt: checked.FetchedAt, Title: checked.Title, OriginalTitle: checked.OriginalTitle, Overview: checked.Overview, FirstAirDate: checked.ReleaseDate}, nil
}

func seriesResponseError(r outbound.Response, err error, detail bool) error {
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return context.Canceled
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return context.DeadlineExceeded
		}
		return ErrUnavailable
	}
	switch r.Status {
	case 200:
		return nil
	case 401, 403:
		return ErrCredentials
	case 429:
		return ErrRateLimited
	case 404:
		if detail {
			return domain.ErrNotFound
		}
	}
	return ErrUnavailable
}
