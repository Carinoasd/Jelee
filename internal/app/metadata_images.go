package app

import (
	"context"
	"slices"
	"sort"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type MetadataImageProvider interface {
	Images(context.Context, string, int32, []string) (domain.MetadataImages, error)
}

// MetadataEpisodeImageProvider lists season or episode images of a series.
type MetadataEpisodeImageProvider interface {
	EpisodeImages(ctx context.Context, series, season, episode int32, languages []string) (domain.MetadataImages, error)
}

func (m *Metadata) Images(ctx context.Context, resource string, id int32, languages []string) (domain.MetadataImages, error) {
	if err := ctx.Err(); err != nil {
		return domain.MetadataImages{}, err
	}
	if id <= 0 || !domain.ValidMetadataImageResource(resource) || !domain.ValidMetadataImageLanguages(languages) {
		return domain.MetadataImages{}, domain.ErrInvalid
	}
	provider, ok := m.provider.(MetadataImageProvider)
	if !ok {
		return domain.MetadataImages{}, domain.ErrMetadataUnavailable
	}
	languages = append([]string(nil), languages...)
	value, err := provider.Images(ctx, resource, id, append([]string(nil), languages...))
	if err != nil {
		return domain.MetadataImages{}, safeEpisodeError(err)
	}
	if err := ctx.Err(); err != nil {
		return domain.MetadataImages{}, err
	}
	if value.Resource != resource || value.ProviderID != id || !slices.Equal(value.ImageLanguages, languages) || len(value.Candidates) > domain.MetadataImageLimit {
		return domain.MetadataImages{}, domain.ErrMetadataUnavailable
	}
	return preferredImages(value, languages), nil
}

// EpisodeImages lists season posters (episode 0) or episode stills in the
// ordered language preference; every image needs confirmation.
func (m *Metadata) EpisodeImages(ctx context.Context, series, season, episode int32, languages []string) (domain.MetadataImages, error) {
	if err := ctx.Err(); err != nil {
		return domain.MetadataImages{}, err
	}
	if !domain.ValidMetadataEpisodeImageTarget(series, season, episode) || !domain.ValidMetadataImageLanguages(languages) {
		return domain.MetadataImages{}, domain.ErrInvalid
	}
	provider, ok := m.provider.(MetadataEpisodeImageProvider)
	if !ok {
		return domain.MetadataImages{}, domain.ErrMetadataUnavailable
	}
	languages = append([]string(nil), languages...)
	value, err := provider.EpisodeImages(ctx, series, season, episode, append([]string(nil), languages...))
	if err != nil {
		return domain.MetadataImages{}, safeEpisodeError(err)
	}
	if err := ctx.Err(); err != nil {
		return domain.MetadataImages{}, err
	}
	kind := "poster"
	if episode > 0 {
		kind = "still"
	}
	if value.Resource != domain.MetadataEpisodeImageResource(episode) || value.ProviderID <= 0 || value.SeriesID != series || value.SeasonNumber == nil || *value.SeasonNumber != season || value.EpisodeNumber != episode || !slices.Equal(value.ImageLanguages, languages) || len(value.Candidates) > domain.MetadataImageLimit {
		return domain.MetadataImages{}, domain.ErrMetadataUnavailable
	}
	for _, image := range value.Candidates {
		if image.Kind != kind {
			return domain.MetadataImages{}, domain.ErrMetadataUnavailable
		}
	}
	return preferredImages(value, languages), nil
}

// preferredImages copies the provider value, keeps the images in the ordered
// language preference and sorts them by kind, preference, score and path.
func preferredImages(value domain.MetadataImages, languages []string) domain.MetadataImages {
	value = domain.CloneMetadataImages(value)
	order := make(map[string]int, len(languages))
	for i, language := range languages {
		order[language] = i
	}
	selected := value.Candidates[:0]
	for _, image := range value.Candidates {
		if _, exists := order[image.Language]; !exists {
			continue
		}
		image.NeedsConfirmation = true
		selected = append(selected, image)
	}
	value.Candidates = selected
	sort.Slice(value.Candidates, func(i, j int) bool {
		a, b := value.Candidates[i], value.Candidates[j]
		if a.Kind != b.Kind {
			return a.Kind == "poster"
		}
		if order[a.Language] != order[b.Language] {
			return order[a.Language] < order[b.Language]
		}
		if a.VoteAverage != b.VoteAverage {
			return a.VoteAverage > b.VoteAverage
		}
		if a.VoteCount != b.VoteCount {
			return a.VoteCount > b.VoteCount
		}
		return a.FilePath < b.FilePath
	})
	return value
}
