package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"
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
	budget, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
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
	t.movies.put(key, movie)
	return movie, nil
}

func (t *TMDB) safeText(value string, maxBytes int) bool {
	return len(value) <= maxBytes && !strings.Contains(value, t.key) && !strings.ContainsFunc(value, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' })
}
