package domain

import (
	"math"
	"slices"
	"strings"
	"time"
)

const NFOItemFieldsVersion = "four-text-fields-v1"
const NFOItemLockFieldsVersion = "lock-only-fields-v1"
const NFOItemSortFieldsVersion = "five-field-projection-v1"
const NFOItemTextFieldsVersion = "extended-text-fields-v1"
const NFOItemYearFieldsVersion = "year-fact-v1"
const NFOItemNumericFieldsVersion = "numeric-facts-v1"

// Each projection keeps its published field vocabulary. Returned slices are owned.
func NFOItemFieldNames(version string) []string {
	switch version {
	case NFOItemFieldsVersion, NFOItemLockFieldsVersion:
		return []string{"title", "originalTitle", "overview", "date"}
	case NFOItemSortFieldsVersion:
		return []string{"title", "originalTitle", "overview", "date", "sortTitle"}
	case NFOItemIdentifierFieldsVersion:
		return append(NFOItemFieldNames(NFOItemActorFieldsVersion), "uniqueIds")
	case NFOItemActorFieldsVersion:
		return append(NFOItemFieldNames(NFOItemListFieldsVersion), "actors")
	case NFOItemListFieldsVersion:
		return append(NFOItemFieldNames(NFOItemNumericFieldsVersion), ItemMetadataListFieldNames()...)
	case NFOItemNumericFieldsVersion:
		return []string{"title", "originalTitle", "overview", "date", "sortTitle", "tagline", "outline", "mpaa", "certification", "year", "runtimeMinutes", "rating", "userRating"}
	case NFOItemYearFieldsVersion:
		return []string{"title", "originalTitle", "overview", "date", "sortTitle", "tagline", "outline", "mpaa", "certification", "year"}
	case NFOItemTextFieldsVersion:
		return []string{"title", "originalTitle", "overview", "date", "sortTitle", "tagline", "outline", "mpaa", "certification"}
	}
	return nil
}

func ItemMetadataFieldNames() []string {
	return []string{"title", "originalTitle", "overview", "date", "sortTitle", "tagline", "outline", "mpaa", "certification"}
}

// NFOItemFields is an internal observation, not a caller supplied write intent.
// It contains no filesystem paths. Library/item ownership is checked separately.
type NFOItemFields struct {
	Version      string
	Kind         string
	Identity     NFOIdentity
	Stamp        NFOStamp
	ReadAt       time.Time
	UniqueIDs    []NFOUniqueID
	Actors       []NFOActor
	Lists        []NFOStringList
	NumberFacts  []NFONumberFact
	Facts        []NFOIntegerFact
	Fields       []NFOTextField
	LockData     bool
	LockedFields []string
}

type NFONumberFact struct {
	Field string
	Value float64
}

type NFOIntegerFact struct {
	Field string
	Value int
}

type NFOTextField struct {
	Field string
	Value string
}

func (NFOItemFields) String() string   { return "nfo item fields (data redacted)" }
func (NFOItemFields) GoString() string { return "nfo item fields (data redacted)" }

func ValidNFOItemFields(v NFOItemFields) bool {
	if (v.Kind != "Movie" && v.Kind != "Series") || ValidateNFOIdentity(v.Identity) != nil || ValidateNFOStamp(v.Stamp) != nil || v.Stamp.Size > v.Identity.MaxSourceBytes || v.ReadAt.IsZero() || v.ReadAt.Year() < 1 || v.ReadAt.Year() > 9999 || len(v.Fields) > len(NFOItemFieldNames(v.Version)) || len(v.LockedFields) > 128 {
		return false
	}
	if len(v.Facts) > 2 || len(v.Facts) > 0 && v.Version != NFOItemYearFieldsVersion && v.Version != NFOItemNumericFieldsVersion && v.Version != NFOItemListFieldsVersion && v.Version != NFOItemActorFieldsVersion && v.Version != NFOItemIdentifierFieldsVersion || v.Version == NFOItemYearFieldsVersion && len(v.Facts) > 1 {
		return false
	}
	if len(v.NumberFacts) > 2 || len(v.NumberFacts) > 0 && v.Version != NFOItemNumericFieldsVersion && v.Version != NFOItemListFieldsVersion && v.Version != NFOItemActorFieldsVersion && v.Version != NFOItemIdentifierFieldsVersion {
		return false
	}
	if len(v.Lists) > len(ItemMetadataListFieldNames()) || len(v.Lists) > 0 && v.Version != NFOItemListFieldsVersion && v.Version != NFOItemActorFieldsVersion && v.Version != NFOItemIdentifierFieldsVersion {
		return false
	}
	if len(v.Actors) > 0 && (v.Version != NFOItemActorFieldsVersion && v.Version != NFOItemIdentifierFieldsVersion || !ValidMetadataActors(v.Actors)) {
		return false
	}
	if len(v.UniqueIDs) > 0 && (v.Version != NFOItemIdentifierFieldsVersion || !ValidMetadataUniqueIDs(v.UniqueIDs)) {
		return false
	}
	seenFacts := map[string]bool{}
	for _, list := range v.Lists {
		if seenFacts[list.Field] || !IsItemMetadataListField(list.Field) || len(list.Values) == 0 || !ValidMetadataStringList(list.Values) {
			return false
		}
		seenFacts[list.Field] = true
	}
	for _, fact := range v.Facts {
		if seenFacts[fact.Field] {
			return false
		}
		seenFacts[fact.Field] = true
		switch fact.Field {
		case "year":
			if fact.Value < 1 || fact.Value > 9999 {
				return false
			}
		case "runtimeMinutes":
			if v.Version != NFOItemNumericFieldsVersion && v.Version != NFOItemListFieldsVersion && v.Version != NFOItemActorFieldsVersion && v.Version != NFOItemIdentifierFieldsVersion || fact.Value < 0 || fact.Value > 10000000 {
				return false
			}
		default:
			return false
		}
	}
	for _, fact := range v.NumberFacts {
		if seenFacts[fact.Field] || (fact.Field != "rating" && fact.Field != "userRating") || math.IsNaN(fact.Value) || math.IsInf(fact.Value, 0) || fact.Value < 0 || fact.Value > 10 {
			return false
		}
		seenFacts[fact.Field] = true
	}
	switch v.Version {
	case NFOItemFieldsVersion:
		if len(v.Fields) == 0 || len(v.Fields) > 4 {
			return false
		}
	case NFOItemLockFieldsVersion:
		if len(v.Fields) != 0 || !HasNFOItemFieldLock(v) {
			return false
		}
	case NFOItemSortFieldsVersion, NFOItemTextFieldsVersion, NFOItemYearFieldsVersion, NFOItemNumericFieldsVersion, NFOItemListFieldsVersion, NFOItemActorFieldsVersion, NFOItemIdentifierFieldsVersion:
		if len(v.Fields) == 0 && len(v.Facts) == 0 && len(v.NumberFacts) == 0 && len(v.Lists) == 0 && len(v.Actors) == 0 && len(v.UniqueIDs) == 0 && !HasNFOItemFieldLock(v) {
			return false
		}
	default:
		return false
	}
	seen := map[string]bool{}
	for _, field := range v.Fields {
		if seen[field.Field] || !slices.Contains(NFOItemFieldNames(v.Version), field.Field) || !ValidItemMetadataValue(field.Field, field.Value) || strings.TrimSpace(field.Value) == "" {
			return false
		}
		seen[field.Field] = true
	}
	for _, field := range v.LockedFields {
		if len(field) > 128 || strings.ContainsRune(field, 0) {
			return false
		}
	}
	return true
}

func HasNFOItemFieldLock(fields NFOItemFields) bool {
	for _, field := range NFOItemFieldNames(fields.Version) {
		if NFOFieldLocked(fields, field) {
			return true
		}
	}
	return false
}
