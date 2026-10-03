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
	return value, nil
}
