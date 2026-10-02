package httpapi

import "github.com/MoYuanCN/Jelee/internal/domain"

func nfoFieldLockSpecification(schemas map[string]any) {
	schemas["NFOFieldLockOrigin"] = objectSchema(map[string]any{
		"sourceId":       map[string]any{"type": "string", "format": "uuid"},
		"rootId":         map[string]any{"type": "string", "format": "uuid"},
		"generation":     map[string]any{"type": "integer", "format": "int64", "minimum": 1},
		"stamp":          map[string]any{"$ref": "#/components/schemas/NFOItemObservationStamp"},
		"identityDigest": map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"},
		"projection":     map[string]any{"type": "string", "enum": []string{domain.NFOItemFieldsVersion, domain.NFOItemLockFieldsVersion, domain.NFOItemSortFieldsVersion, domain.NFOItemTextFieldsVersion, domain.NFOItemYearFieldsVersion}},
		"readAt":         map[string]any{"type": "string", "format": "date-time"},
		"locked":         map[string]any{"type": "boolean", "const": true},
	}, "sourceId", "rootId", "generation", "stamp", "identityDigest", "projection", "readAt", "locked")
	schemas["NFOFieldLockOrigin"].(map[string]any)["description"] = "Positive historical NFO lock intent, independent of the text value's source. Manual text takeover clears this field's intent; changing only the manual lock flag preserves it."
	field := schemas["ItemMetadataField"].(map[string]any)
	field["properties"].(map[string]any)["nfoLockOrigin"] = map[string]any{"oneOf": []any{map[string]any{"$ref": "#/components/schemas/NFOFieldLockOrigin"}, map[string]any{"type": "null"}}}
	field["required"] = append(field["required"].([]string), "nfoLockOrigin")
}
