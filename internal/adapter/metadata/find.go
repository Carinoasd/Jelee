package metadata

import (
	"context"
	"encoding/json"
	"net/url"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type externalKey struct {
	source, id, language string
}
type externalCache = candidateCache[externalKey, domain.ExternalIDMatches]

// FindByExternalID resolves an IMDb, TVDB or Wikidata identifier to TMDB movie
// and series candidates. It shares the credential, governor, retry policy,
// total deadline, cache lifetime and miss coalescing of the detail lookups.
// An empty result is a valid answer and is cached like any other.
func (t *TMDB) FindByExternalID(ctx context.Context, source, id, language string) (domain.ExternalIDMatches, error) {
	if err := ctx.Err(); err != nil {
		return domain.ExternalIDMatches{}, err
	}
	if !domain.ValidExternalIDRequest(source, id, language) {
		return domain.ExternalIDMatches{}, domain.ErrInvalid
	}
	key := externalKey{source, id, language}
	if value, ok := t.external.get(key, t.now()); ok {
		return domain.CloneExternalIDMatches(value), nil
	}
	value, err := t.externalFlights.do(ctx, key, providerBudget, func(budget context.Context) (domain.ExternalIDMatches, error) {
		return t.fetchExternal(budget, key)
	}, func(value domain.ExternalIDMatches) { t.external.put(key, domain.CloneExternalIDMatches(value)) })
	if err != nil {
		return domain.ExternalIDMatches{}, err
	}
	return domain.CloneExternalIDMatches(value), nil
}

func (t *TMDB) fetchExternal(budget context.Context, key externalKey) (domain.ExternalIDMatches, error) {
	if value, ok := t.external.get(key, t.now()); ok {
		return value, nil
	}
	query := url.Values{"api_key": {t.key}, "external_source": {key.source}, "language": {key.language}}
	// The identifier was validated to a canonical [A-Za-z0-9] form; escaping
	// keeps the fixed host and single path segment even if that ever changes.
	r, err := t.providerRequest(budget, "https://api.themoviedb.org/3/find/"+url.PathEscape(key.id)+"?"+query.Encode(), 1<<20)
	// The find endpoint answers an unknown identifier with empty lists, so a
	// 404 is not a meaningful not-found result here.
	if err := seriesResponseError(r, err, false); err != nil {
		return domain.ExternalIDMatches{}, err
	}
	var raw struct {
		Movies *[]providerMovie  `json:"movie_results"`
		Series *[]providerSeries `json:"tv_results"`
	}
	if json.Unmarshal(r.Body, &raw) != nil || raw.Movies == nil || raw.Series == nil || len(*raw.Movies) > domain.MaxExternalIDResults || len(*raw.Series) > domain.MaxExternalIDResults {
		return domain.ExternalIDMatches{}, ErrResponse
	}
	now := t.now().UTC()
	value := domain.ExternalIDMatches{ExternalSource: key.source, ExternalID: key.id, Language: key.language, FetchedAt: now, Movies: make([]domain.MovieCandidate, 0, len(*raw.Movies)), Series: make([]domain.SeriesCandidate, 0, len(*raw.Series))}
	movieIDs := make(map[int32]bool, len(*raw.Movies))
	for _, entry := range *raw.Movies {
		if movieIDs[entry.ID] {
			return domain.ExternalIDMatches{}, ErrResponse
		}
		candidate, err := t.movieCandidate(entry, key.language, now)
		if err != nil {
			return domain.ExternalIDMatches{}, err
		}
		movieIDs[entry.ID] = true
		value.Movies = append(value.Movies, candidate)
	}
	seriesIDs := make(map[int32]bool, len(*raw.Series))
	for _, entry := range *raw.Series {
		if seriesIDs[entry.ID] {
			return domain.ExternalIDMatches{}, ErrResponse
		}
		candidate, err := t.seriesCandidate(entry, key.language, now)
		if err != nil {
			return domain.ExternalIDMatches{}, err
		}
		seriesIDs[entry.ID] = true
		value.Series = append(value.Series, candidate)
	}
	if err := budget.Err(); err != nil {
		return domain.ExternalIDMatches{}, err
	}
	return value, nil
}
