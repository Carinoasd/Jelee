package domain

import (
	"strconv"
	"strings"
	"time"
)

type MetadataProviderOrigin struct {
	Resource          string    `json:"resource"`
	ProviderID        int32     `json:"providerId"`
	SourceURL         string    `json:"sourceUrl"`
	RequestedLanguage string    `json:"requestedLanguage"`
	FetchedAt         time.Time `json:"fetchedAt"`
}

type TMDBMetadataApplyInput struct {
	Resource             string `json:"resource"`
	ProviderID           int32  `json:"providerId"`
	ExpectedRevision     int64  `json:"expectedRevision"`
	Confirmed            bool   `json:"confirmed"`
	ReplaceExistingTitle bool   `json:"replaceExistingTitle"`
	Language             string `json:"language"`
}

type MetadataProviderField struct {
	Field  string
	Value  string
	Origin MetadataProviderOrigin
}

type TMDBMetadataUpdate struct {
	Resource             string
	ProviderID           int32
	ReplaceExistingTitle bool
	Fields               []MetadataProviderField
}

type MetadataFieldSkip struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}

type MetadataApplyResult struct {
	Metadata ItemMetadata              `json:"metadata"`
	Applied  []string                  `json:"applied"`
	Skipped  []MetadataFieldSkip       `json:"skipped"`
	NFO      *MetadataFieldApplyReport `json:"nfo,omitempty"`
	TMDB     *MetadataFieldApplyReport `json:"tmdb,omitempty"`
}

type MetadataFieldApplyReport struct {
	Status  string              `json:"status,omitempty"`
	Applied []string            `json:"applied"`
	Skipped []MetadataFieldSkip `json:"skipped"`
}

func CloneMetadataApplyResult(value MetadataApplyResult) MetadataApplyResult {
	value.Metadata = CloneItemMetadata(value.Metadata)
	value.Applied = append([]string{}, value.Applied...)
	value.Skipped = append([]MetadataFieldSkip{}, value.Skipped...)
	for _, report := range []**MetadataFieldApplyReport{&value.NFO, &value.TMDB} {
		if *report != nil {
			copy := **report
			copy.Applied = append([]string{}, copy.Applied...)
			copy.Skipped = append([]MetadataFieldSkip{}, copy.Skipped...)
			*report = &copy
		}
	}
	return value
}

func TMDBSourceURL(resource string, id int32) string {
	path := "movie"
	if resource == "series" {
		path = "tv"
	}
	return "https://www.themoviedb.org/" + path + "/" + strconv.FormatInt(int64(id), 10)
}

func ValidMetadataProviderOrigin(origin MetadataProviderOrigin) bool {
	return ValidMetadataImageResource(origin.Resource) && origin.ProviderID > 0 && origin.SourceURL == TMDBSourceURL(origin.Resource, origin.ProviderID) && ValidMetadataLanguage(origin.RequestedLanguage) && !origin.FetchedAt.IsZero() && origin.FetchedAt.Year() >= 1 && origin.FetchedAt.Year() <= 9999
}

func ValidTMDBMetadataInput(item string, input TMDBMetadataApplyInput) bool {
	return ValidID(item) && input.ExpectedRevision >= 1 && input.ExpectedRevision < ItemMetadataRevisionMax && input.Confirmed && input.ProviderID > 0 && ValidMetadataImageResource(input.Resource) && (input.Language == "" || ValidMetadataLanguage(input.Language))
}

func ValidTMDBMetadataUpdate(update TMDBMetadataUpdate) bool {
	if !ValidMetadataImageResource(update.Resource) || update.ProviderID <= 0 || len(update.Fields) < 1 || len(update.Fields) > 4 {
		return false
	}
	seen := map[string]bool{}
	for _, f := range update.Fields {
		switch f.Field {
		case "title", "originalTitle", "overview", "date":
		default:
			return false
		}
		if seen[f.Field] || !ValidItemMetadataValue(f.Field, f.Value) || strings.TrimSpace(f.Value) == "" || !ValidMetadataProviderOrigin(f.Origin) || f.Origin.Resource != update.Resource || f.Origin.ProviderID != update.ProviderID {
			return false
		}
		seen[f.Field] = true
	}
	return true
}

func MetadataResourceMatchesKind(resource, kind string) bool {
	return resource == "movie" && (kind == "Movie" || kind == "HomeVideo") || resource == "series" && kind == "Series"
}

// Explicit legacy-title replacement never overrides a lock or known local source.
func TMDBMetadataSkip(old ItemMetadataField, replaceExistingTitle bool) string {
	if old.Locked || old.NFOOrigin != nil && old.NFOOrigin.Locked || old.NFOLockOrigin != nil && old.NFOLockOrigin.Locked {
		return "locked"
	}
	if old.Source == "manual" {
		return "manual"
	}
	if old.Source == "nfo" {
		return "nfo"
	}
	// File-name values (source scan) have the lowest priority and are replaced.
	if old.Source != "" && old.Source != "existing" && old.Source != "tmdb" && old.Source != "scan" {
		return "existing"
	}
	if old.Source == "existing" && strings.TrimSpace(old.Value) != "" && !(old.Field == "title" && replaceExistingTitle) {
		return "existing"
	}
	return ""
}
