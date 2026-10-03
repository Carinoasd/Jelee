package domain

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

const NFOItemCollectionFieldsVersion = "collection-structure-v1"

type NFOCollection struct {
	Name     string `json:"name"`
	Overview string `json:"overview"`
}

func ValidMetadataCollection(v NFOCollection) bool {
	return strings.TrimSpace(v.Name) != "" && len(v.Name) <= 1024 && len(v.Overview) <= 16384 && utf8.ValidString(v.Name) && utf8.ValidString(v.Overview) && !strings.ContainsRune(v.Name, 0) && !strings.ContainsRune(v.Overview, 0)
}

func ValidMetadataCollectionValue(raw json.RawMessage) bool {
	if len(raw) > 128<<10 || !utf8.Valid(raw) {
		return false
	}
	if string(raw) == "null" {
		return true
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return false
	}
	for key := range fields {
		if key != "name" && key != "overview" {
			return false
		}
	}
	var name *string
	if json.Unmarshal(fields["name"], &name) != nil || name == nil {
		return false
	}
	v := NFOCollection{Name: *name}
	if raw, ok := fields["overview"]; ok {
		var overview *string
		if json.Unmarshal(raw, &overview) != nil || overview == nil {
			return false
		}
		v.Overview = *overview
	}
	return ValidMetadataCollection(v)
}

func CloneNFOCollection(v *NFOCollection) *NFOCollection {
	if v == nil {
		return nil
	}
	copy := *v
	return &copy
}

func EqualNFOCollection(a, b *NFOCollection) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}
