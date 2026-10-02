package domain

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

const NFOItemIdentifierFieldsVersion = "provider-identifiers-v1"
const MaxMetadataUniqueIDs = 128
const MaxMetadataUniqueIDBytes = 16384

type NFOUniqueID struct {
	Type    string `json:"type"`
	Value   string `json:"value"`
	Default bool   `json:"default"`
}

func ValidMetadataUniqueIDs(ids []NFOUniqueID) bool {
	if ids == nil || len(ids) > MaxMetadataUniqueIDs {
		return false
	}
	total := 0
	for _, id := range ids {
		for _, field := range []struct {
			value string
			limit int
		}{{id.Type, 64}, {id.Value, 1024}} {
			if !utf8.ValidString(field.value) || len(field.value) > field.limit || strings.TrimSpace(field.value) == "" || strings.ContainsRune(field.value, 0) {
				return false
			}
			total += len(field.value)
		}
		if total > MaxMetadataUniqueIDBytes {
			return false
		}
	}
	return true
}

func ValidMetadataUniqueIDValue(value json.RawMessage) bool {
	if len(value) > 128<<10 || !utf8.Valid(value) {
		return false
	}
	if string(value) == "null" {
		return true
	}
	var entries []map[string]json.RawMessage
	if json.Unmarshal(value, &entries) != nil || entries == nil {
		return false
	}
	ids := make([]NFOUniqueID, 0, len(entries))
	for _, entry := range entries {
		for key := range entry {
			if key != "type" && key != "value" && key != "default" {
				return false
			}
		}
		var provider, identifier *string
		if json.Unmarshal(entry["type"], &provider) != nil || provider == nil || json.Unmarshal(entry["value"], &identifier) != nil || identifier == nil {
			return false
		}
		id := NFOUniqueID{Type: *provider, Value: *identifier}
		if raw, ok := entry["default"]; ok {
			var flag *bool
			if json.Unmarshal(raw, &flag) != nil || flag == nil {
				return false
			}
			id.Default = *flag
		}
		ids = append(ids, id)
	}
	return ValidMetadataUniqueIDs(ids)
}
