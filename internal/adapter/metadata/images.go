package metadata

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type imageKey struct {
	resource        string
	id              int32
	season, episode int32
	languages       string
}
type imageCache = candidateCache[imageKey, domain.MetadataImages]
type providerImage struct {
	Language    json.RawMessage `json:"iso_639_1"`
	FilePath    string          `json:"file_path"`
	Width       int32           `json:"width"`
	Height      int32           `json:"height"`
	VoteAverage *float64        `json:"vote_average"`
	VoteCount   *int32          `json:"vote_count"`
}

func (t *TMDB) Images(ctx context.Context, resource string, id int32, languages []string) (domain.MetadataImages, error) {
	if err := ctx.Err(); err != nil {
		return domain.MetadataImages{}, err
	}
	if id <= 0 || !domain.ValidMetadataImageResource(resource) || !domain.ValidMetadataImageLanguages(languages) {
		return domain.MetadataImages{}, domain.ErrInvalid
	}
	path := "movie"
	if resource == "series" {
		path = "tv"
	}
	base := path + "/" + strconv.FormatInt(int64(id), 10)
	key := imageKey{resource: resource, id: id, languages: strings.Join(languages, ",")}
	return t.fetchImages(ctx, key, base, domain.MetadataImages{Resource: resource, ProviderID: id}, []imageGroup{{"poster", "posters"}, {"backdrop", "backdrops"}})
}

// EpisodeImages lists season posters (episode 0) or episode stills of one
// series with the same governance, validation and cache as Images.
func (t *TMDB) EpisodeImages(ctx context.Context, series, season, episode int32, languages []string) (domain.MetadataImages, error) {
	if err := ctx.Err(); err != nil {
		return domain.MetadataImages{}, err
	}
	if !domain.ValidMetadataEpisodeImageTarget(series, season, episode) || !domain.ValidMetadataImageLanguages(languages) {
		return domain.MetadataImages{}, domain.ErrInvalid
	}
	resource := domain.MetadataEpisodeImageResource(episode)
	base := "tv/" + strconv.FormatInt(int64(series), 10) + "/season/" + strconv.FormatInt(int64(season), 10)
	groups := []imageGroup{{"poster", "posters"}}
	if episode > 0 {
		base += "/episode/" + strconv.FormatInt(int64(episode), 10)
		groups = []imageGroup{{"still", "stills"}}
	}
	seasonNumber := season
	template := domain.MetadataImages{Resource: resource, SeriesID: series, SeasonNumber: &seasonNumber, EpisodeNumber: episode}
	key := imageKey{resource: resource, id: series, season: season, episode: episode, languages: strings.Join(languages, ",")}
	return t.fetchImages(ctx, key, base, template, groups)
}

type imageGroup struct{ kind, field string }

// fetchImages requests one TMDB images endpoint. For movies and series the
// response ID must equal the requested ID; for seasons and episodes it is the
// upstream season or episode ID and only needs to be a positive int32.
func (t *TMDB) fetchImages(ctx context.Context, key imageKey, base string, template domain.MetadataImages, groups []imageGroup) (domain.MetadataImages, error) {
	languages := strings.Split(key.languages, ",")
	if value, ok := t.images.get(key, t.now()); ok {
		return domain.CloneMetadataImages(value), nil
	}
	budget, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	query := url.Values{"api_key": {t.key}, "include_image_language": {key.languages}}
	r, err := t.providerRequest(budget, "https://api.themoviedb.org/3/"+base+"/images?"+query.Encode(), 1<<20)
	if err := seriesResponseError(r, err, true); err != nil {
		return domain.MetadataImages{}, err
	}
	var raw map[string]json.RawMessage
	var id int32
	if json.Unmarshal(r.Body, &raw) != nil || json.Unmarshal(raw["id"], &id) != nil || id <= 0 || (template.ProviderID != 0 && id != template.ProviderID) {
		return domain.MetadataImages{}, ErrResponse
	}
	value := template
	value.ProviderID = id
	value.Source = "TMDB"
	value.SourceURL = "https://www.themoviedb.org/" + base
	value.FetchedAt = t.now().UTC()
	value.ImageLanguages = languages
	value.Candidates = []domain.MetadataImageCandidate{}
	for _, group := range groups {
		decoder := json.NewDecoder(bytes.NewReader(raw[group.field]))
		token, err := decoder.Token()
		if err != nil || token != json.Delim('[') {
			return domain.MetadataImages{}, ErrResponse
		}
		seen := make(map[string]bool)
		for decoder.More() {
			if err := budget.Err(); err != nil {
				return domain.MetadataImages{}, err
			}
			if len(value.Candidates) >= domain.MetadataImageLimit {
				return domain.MetadataImages{}, ErrResponse
			}
			var raw providerImage
			if decoder.Decode(&raw) != nil {
				return domain.MetadataImages{}, ErrResponse
			}
			language := "null"
			if len(raw.Language) == 0 {
				return domain.MetadataImages{}, ErrResponse
			}
			if strings.TrimSpace(string(raw.Language)) != "null" {
				if json.Unmarshal(raw.Language, &language) != nil || len(language) != 2 || language[0] < 'a' || language[0] > 'z' || language[1] < 'a' || language[1] > 'z' {
					return domain.MetadataImages{}, ErrResponse
				}
			}
			if !validImagePath(raw.FilePath) || strings.Contains(raw.FilePath, t.key) || seen[raw.FilePath] || raw.Width < 1 || raw.Width > 32768 || raw.Height < 1 || raw.Height > 32768 || raw.VoteAverage == nil || *raw.VoteAverage < 0 || *raw.VoteAverage > 10 || raw.VoteCount == nil || *raw.VoteCount < 0 {
				return domain.MetadataImages{}, ErrResponse
			}
			seen[raw.FilePath] = true
			value.Candidates = append(value.Candidates, domain.MetadataImageCandidate{Kind: group.kind, Language: language, FilePath: raw.FilePath, URL: "https://image.tmdb.org/t/p/original" + raw.FilePath, Width: raw.Width, Height: raw.Height, VoteAverage: *raw.VoteAverage, VoteCount: *raw.VoteCount})
		}
		if token, err := decoder.Token(); err != nil || token != json.Delim(']') {
			return domain.MetadataImages{}, ErrResponse
		}
	}
	if err := budget.Err(); err != nil {
		return domain.MetadataImages{}, err
	}
	t.images.put(key, domain.CloneMetadataImages(value))
	return value, nil
}

func validImagePath(path string) bool {
	if len(path) < 6 || len(path) > 256 || path[0] != '/' {
		return false
	}
	name := path[1:]
	index := strings.LastIndexByte(name, '.')
	if index < 1 || index > 192 {
		return false
	}
	ext := name[index:]
	if ext != ".jpg" && ext != ".png" && ext != ".webp" {
		return false
	}
	for _, ch := range name[:index] {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '-') {
			return false
		}
	}
	return true
}
