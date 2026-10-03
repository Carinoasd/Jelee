package domain

import (
	"slices"
	"strings"
	"time"
)

// NFOItemOrigin exposes stable source identity without exposing local paths.
// Locked records NFO lock intent separately from the manually edited lock flag.
type NFOItemOrigin struct {
	SourceID       string    `json:"sourceId"`
	RootID         string    `json:"rootId"`
	Generation     int64     `json:"generation"`
	SHA256         string    `json:"sha256"`
	IdentityDigest string    `json:"identityDigest"`
	Projection     string    `json:"projection"`
	ReadAt         time.Time `json:"readAt"`
	Locked         bool      `json:"locked"`
}

func ValidNFOItemOrigin(v NFOItemOrigin) bool {
	return ValidID(v.SourceID) && ValidID(v.RootID) && v.Generation >= 1 && probeHex(v.SHA256, 64) && probeHex(v.IdentityDigest, 64) && (v.Projection == NFOItemFieldsVersion || v.Projection == NFOItemSortFieldsVersion || v.Projection == NFOItemTextFieldsVersion || v.Projection == NFOItemYearFieldsVersion || v.Projection == NFOItemNumericFieldsVersion || v.Projection == NFOItemListFieldsVersion || v.Projection == NFOItemActorFieldsVersion || v.Projection == NFOItemIdentifierFieldsVersion || v.Projection == NFOItemRatingFieldsVersion || v.Projection == NFOItemCollectionFieldsVersion || v.Projection == NFOItemMovieFieldsVersion || v.Projection == NFOItemSeriesFieldsVersion || v.Projection == NFOItemEpisodeFieldsVersion || v.Projection == NFOItemSeasonFieldsVersion) && !v.ReadAt.IsZero() && v.ReadAt.Year() >= 1 && v.ReadAt.Year() <= 9999
}

func NFOFieldLocked(fields NFOItemFields, field string) bool {
	version := fields.Version
	// Standalone legacy lock mapping predates compiled observations. Such a
	// capsule is still rejected by ValidNFOItemFields before any persistence.
	if version == "" {
		version = NFOItemFieldsVersion
	}
	if !slices.Contains(NFOItemFieldNames(version), field) {
		return false
	}
	if fields.LockData {
		return true
	}
	for _, name := range fields.LockedFields {
		name = strings.ToLower(strings.TrimSpace(name))
		if (field == "dateAdded" || field == "trailers" || field == "art") && MetadataMovieFieldForLock(name) == field {
			return true
		}
		if fields.Kind == "Season" && field == "seasonNumber" && (name == "season" || name == "seasonnumber" || name == "indexnumber") {
			return true
		}
		if IsItemMetadataEpisodeField(field) && MetadataEpisodeFieldForLock(name) == field {
			return true
		}
		if IsItemMetadataSeriesField(field) && MetadataSeriesFieldForLock(name) == field {
			return true
		}
		if field == "collection" && (name == "collection" || name == "set") {
			return true
		}
		if field == "ratings" && (name == "ratings" || name == "sourceratings") {
			return true
		}
		if field == "uniqueIds" && (name == "uniqueid" || name == "uniqueids" || name == "providerids" || name == "imdbid" || name == "tmdbid" || name == "tvdbid") {
			return true
		}
		if field == "actors" && (name == "actor" || name == "actors" || name == "cast") {
			return true
		}
		if IsItemMetadataListField(field) && MetadataListFieldForLock(name) == field {
			return true
		}
		if (field == "mpaa" || field == "certification") && name == "officialrating" {
			return true
		}
		if field == "title" && name == "name" || field == "originalTitle" && name == "originaltitle" || field == "overview" && name == "overview" || field == "date" && name == "premieredate" || field == "sortTitle" && name == "sortname" || field == "tagline" && name == "tagline" || field == "certification" && name == "certification" || field == "mpaa" && name == "mpaa" || field == "outline" && name == "outline" || field == "year" && (name == "year" || name == "productionyear") || field == "runtimeMinutes" && (name == "runtime" || name == "runtimeminutes") || field == "rating" && (name == "rating" || name == "communityrating") || field == "userRating" && name == "userrating" {
			return true
		}
	}
	return false
}
