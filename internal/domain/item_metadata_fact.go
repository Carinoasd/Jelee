package domain

import (
	"encoding/json"
	"math"
	"strconv"
	"time"
	"unicode/utf8"
)

// Facts retain JSON value types; they are not serialized into text fields.
type ItemMetadataFact struct {
	Field         string              `json:"field"`
	Value         json.RawMessage     `json:"value"`
	Source        string              `json:"source"`
	Locked        bool                `json:"locked"`
	UpdatedAt     *time.Time          `json:"updatedAt"`
	NFOOrigin     *NFOItemOrigin      `json:"nfoOrigin"`
	NFOLockOrigin *NFOFieldLockOrigin `json:"nfoLockOrigin"`
}

// A missing Value preserves the fact; JSON null records an explicit manual clear.
type ItemMetadataFactPatch struct {
	Field  string          `json:"field"`
	Value  json.RawMessage `json:"value,omitempty"`
	Locked *bool           `json:"locked,omitempty"`
}

func ValidItemMetadataEdit(item string, revision int64, fields []ItemMetadataPatch, facts []ItemMetadataFactPatch) bool {
	if !ValidID(item) || revision < 1 || revision >= ItemMetadataRevisionMax || len(fields)+len(facts) == 0 || len(facts) > 8+len(ItemMetadataListFieldNames()) {
		return false
	}
	if len(fields) > 0 && !ValidItemMetadataPatches(item, revision, fields) {
		return false
	}
	seen := map[string]bool{}
	for _, fact := range facts {
		if seen[fact.Field] {
			return false
		}
		seen[fact.Field] = true
		if (fact.Field != "year" && fact.Field != "runtimeMinutes" && fact.Field != "rating" && fact.Field != "userRating" && fact.Field != "actors" && fact.Field != "uniqueIds" && fact.Field != "ratings" && fact.Field != "collection" && !IsItemMetadataListField(fact.Field)) || fact.Value == nil && fact.Locked == nil || fact.Value != nil && !ValidItemMetadataFactValue(fact.Field, fact.Value) {
			return false
		}
	}
	return true
}

func ValidItemMetadataFactValue(field string, value json.RawMessage) bool {
	if field == "collection" {
		return ValidMetadataCollectionValue(value)
	}
	if field == "ratings" {
		return ValidMetadataRatingValue(value)
	}
	if field == "uniqueIds" {
		return ValidMetadataUniqueIDValue(value)
	}
	if field == "actors" {
		return ValidMetadataActorValue(value)
	}
	if IsItemMetadataListField(field) {
		if len(value) > 128<<10 || !utf8.Valid(value) {
			return false
		}
		if string(value) == "null" {
			return true
		}
		var values []string
		return json.Unmarshal(value, &values) == nil && ValidMetadataStringList(values)
	}
	if field == "rating" || field == "userRating" {
		if len(value) > 1024 {
			return false
		}
		if string(value) == "null" {
			return true
		}
		var n *float64
		return json.Unmarshal(value, &n) == nil && n != nil && !math.IsNaN(*n) && !math.IsInf(*n, 0) && *n >= 0 && *n <= 10
	}
	minimum, maximum, limit := 1, 9999, 4
	if field == "runtimeMinutes" {
		minimum, maximum, limit = 0, 10000000, 8
	} else if field != "year" {
		return false
	}
	if len(value) > limit {
		return false
	}
	if string(value) == "null" {
		return true
	}
	var year int
	return json.Unmarshal(value, &year) == nil && year >= minimum && year <= maximum && strconv.Itoa(year) == string(value)
}

func CloneItemMetadataFact(v ItemMetadataFact) ItemMetadataFact {
	v.Value = append(json.RawMessage(nil), v.Value...)
	if v.UpdatedAt != nil {
		copy := *v.UpdatedAt
		v.UpdatedAt = &copy
	}
	if v.NFOOrigin != nil {
		copy := *v.NFOOrigin
		v.NFOOrigin = &copy
	}
	if v.NFOLockOrigin != nil {
		copy := *v.NFOLockOrigin
		v.NFOLockOrigin = &copy
	}
	return v
}
