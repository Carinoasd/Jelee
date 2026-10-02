package domain

import (
	"encoding/json"
	"strconv"
	"time"
)

// Facts retain JSON scalar types; they are not serialized into text fields.
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
	if !ValidID(item) || revision < 1 || revision >= ItemMetadataRevisionMax || len(fields)+len(facts) == 0 || len(facts) > 1 {
		return false
	}
	if len(fields) > 0 && !ValidItemMetadataPatches(item, revision, fields) {
		return false
	}
	for _, fact := range facts {
		if fact.Field != "year" || fact.Value == nil && fact.Locked == nil || fact.Value != nil && !ValidItemMetadataFactValue(fact.Field, fact.Value) {
			return false
		}
	}
	return true
}

func ValidItemMetadataFactValue(field string, value json.RawMessage) bool {
	if field != "year" || len(value) > 4 {
		return false
	}
	if string(value) == "null" {
		return true
	}
	var year int
	return json.Unmarshal(value, &year) == nil && year >= 1 && year <= 9999 && strconv.Itoa(year) == string(value)
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
