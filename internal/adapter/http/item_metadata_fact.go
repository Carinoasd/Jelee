package httpapi

func itemMetadataFactSpecification(paths, schemas map[string]any) {
	year := map[string]any{"type": []string{"integer", "null"}, "minimum": 1, "maximum": 9999}
	field := map[string]any{"type": "string", "enum": []string{"year"}}
	nullRef := func(name string) map[string]any {
		return map[string]any{"oneOf": []any{map[string]any{"$ref": "#/components/schemas/" + name}, map[string]any{"type": "null"}}}
	}
	schemas["ItemMetadataFact"] = objectSchema(map[string]any{"field": field, "value": year, "source": map[string]any{"type": "string", "enum": []string{"existing", "manual", "nfo"}}, "locked": map[string]any{"type": "boolean"}, "updatedAt": map[string]any{"type": []string{"string", "null"}, "format": "date-time"}, "nfoOrigin": nullRef("NFOItemOrigin"), "nfoLockOrigin": nullRef("NFOFieldLockOrigin")}, "field", "value", "source", "locked", "updatedAt", "nfoOrigin", "nfoLockOrigin")
	item := schemas["ItemMetadata"].(map[string]any)
	item["properties"].(map[string]any)["facts"] = map[string]any{"type": "array", "maxItems": 1, "items": map[string]any{"$ref": "#/components/schemas/ItemMetadataFact"}}
	item["required"] = append(item["required"].([]string), "facts")
	patch := objectSchema(map[string]any{"field": field, "value": year, "locked": map[string]any{"type": "boolean"}}, "field")
	patch["anyOf"] = []any{map[string]any{"required": []string{"value"}}, map[string]any{"required": []string{"locked"}}}
	put := paths["/api/v1/items/{id}/metadata"].(map[string]any)["put"].(map[string]any)
	body := put["requestBody"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	body["properties"].(map[string]any)["facts"] = map[string]any{"type": "array", "minItems": 1, "maxItems": 1, "items": patch}
	body["required"] = []string{"expectedRevision"}
	body["anyOf"] = []any{map[string]any{"required": []string{"fields"}}, map[string]any{"required": []string{"facts"}}}
	put["description"] = put["description"].(string) + " Year facts use JSON integers from 1 through 9999; null at facts[].value records an explicit manual clear. Omitted fact value preserves it. Text and fact edits share one revision and transaction; explicit fact values clear NFO value and lock origins. Null anywhere else is rejected."
}
