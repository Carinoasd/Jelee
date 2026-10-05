package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// Movie uses the same credential, guarded transport, limiter, retry policy and
// total deadline as startup validation. Only selected text fields are exposed.
func (t *TMDB) Movie(ctx context.Context, id int32, language string) (domain.MovieCandidate, error) {
	if err := ctx.Err(); err != nil {
		return domain.MovieCandidate{}, err
	}
	if id <= 0 || !domain.ValidMetadataLanguage(language) {
		return domain.MovieCandidate{}, domain.ErrInvalid
	}
	key := movieKey{id, language}
	if movie, ok := t.movies.get(key, t.now()); ok {
		return movie, nil
	}
	return t.movieFlights.do(ctx, key, providerBudget, func(budget context.Context) (domain.MovieCandidate, error) {
		return t.fetchMovie(budget, key)
	}, func(movie domain.MovieCandidate) { t.movies.put(key, movie) })
}

func (t *TMDB) fetchMovie(budget context.Context, key movieKey) (domain.MovieCandidate, error) {
	// A flight that finished between the caller's lookup and this one has
	// already filled the cache.
	if movie, ok := t.movies.get(key, t.now()); ok {
		return movie, nil
	}
	id, language := key.id, key.language
	query := url.Values{"api_key": {t.key}, "language": {language}}
	r, err := t.providerRequest(budget, "https://api.themoviedb.org/3/movie/"+strconv.FormatInt(int64(id), 10)+"?"+query.Encode(), 1<<20)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return domain.MovieCandidate{}, context.Canceled
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return domain.MovieCandidate{}, context.DeadlineExceeded
		}
		return domain.MovieCandidate{}, ErrUnavailable
	}
	switch r.Status {
	case 404:
		return domain.MovieCandidate{}, domain.ErrNotFound
	case 401, 403:
		return domain.MovieCandidate{}, ErrCredentials
	case 429:
		return domain.MovieCandidate{}, ErrRateLimited
	case 200:
	default:
		return domain.MovieCandidate{}, ErrUnavailable
	}
	var data providerMovie
	if json.Unmarshal(r.Body, &data) != nil || data.ID != id {
		return domain.MovieCandidate{}, ErrResponse
	}
	movie, err := t.movieCandidate(data, language, t.now().UTC())
	if err != nil {
		return domain.MovieCandidate{}, err
	}
	if err := budget.Err(); err != nil {
		return domain.MovieCandidate{}, err
	}
	return movie, nil
}

func (t *TMDB) safeText(value string, maxBytes int) bool {
	return len(value) <= maxBytes && !strings.Contains(value, t.key) && !strings.ContainsFunc(value, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' })
}
