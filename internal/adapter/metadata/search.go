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
)

type providerMovie struct {
	ID            int32  `json:"id"`
	Title         string `json:"title"`
	OriginalTitle string `json:"original_title"`
	Overview      string `json:"overview"`
	ReleaseDate   string `json:"release_date"`
}

func (t *TMDB) SearchMovies(ctx context.Context, input domain.MovieSearchInput) ([]domain.MovieCandidate, error) {
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
		query.Set("primary_release_year", strconv.Itoa(input.Year))
	}
	r, err := t.providerRequest(budget, "https://api.themoviedb.org/3/search/movie?"+query.Encode(), 1<<20)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, context.Canceled
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, context.DeadlineExceeded
		}
		return nil, ErrUnavailable
	}
	switch r.Status {
	case 401, 403:
		return nil, ErrCredentials
	case 429:
		return nil, ErrRateLimited
	case 200:
	default:
		return nil, ErrUnavailable
	}
	var data struct {
		Page    int              `json:"page"`
		Results *[]providerMovie `json:"results"`
	}
	if json.Unmarshal(r.Body, &data) != nil || data.Page != 1 || data.Results == nil || len(*data.Results) > 20 {
		return nil, ErrResponse
	}
	now := t.now().UTC()
	result := make([]domain.MovieCandidate, 0, len(*data.Results))
	ids := make(map[int32]struct{}, len(*data.Results))
	for _, raw := range *data.Results {
		if _, exists := ids[raw.ID]; exists {
			return nil, ErrResponse
		}
		movie, err := t.movieCandidate(raw, input.Language, now)
		if err != nil {
			return nil, err
		}
		ids[raw.ID] = struct{}{}
		result = append(result, movie)
	}
	if err := budget.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (t *TMDB) movieCandidate(data providerMovie, language string, at time.Time) (domain.MovieCandidate, error) {
	if data.ID <= 0 || strings.TrimSpace(data.Title) == "" || !t.safeText(data.Title, 1024) || !t.safeText(data.OriginalTitle, 1024) || !t.safeText(data.Overview, 16<<10) {
		return domain.MovieCandidate{}, ErrResponse
	}
	if data.ReleaseDate != "" {
		if _, err := time.Parse("2006-01-02", data.ReleaseDate); err != nil {
			return domain.MovieCandidate{}, ErrResponse
		}
	}
	return domain.MovieCandidate{ProviderID: data.ID, Source: "TMDB", SourceURL: "https://www.themoviedb.org/movie/" + strconv.FormatInt(int64(data.ID), 10), Language: language, FetchedAt: at, Title: data.Title, OriginalTitle: data.OriginalTitle, Overview: data.Overview, ReleaseDate: data.ReleaseDate}, nil
}
