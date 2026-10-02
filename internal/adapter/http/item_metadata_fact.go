package httpapi

func itemMetadataFactSpecification(paths, schemas map[string]any) {
	field := map[string]any{"type": "string", "enum": []string{"year", "runtimeMinutes", "rating", "userRating"}}
	variants := func() []any {
		return []any{
			map[string]any{"properties": map[string]any{"field": map[string]any{"const": "year"}, "value": map[string]any{"type": []string{"integer", "null"}, "minimum": 1, "maximum": 9999}}},
			map[string]any{"properties": map[string]any{"field": map[string]any{"const": "runtimeMinutes"}, "value": map[string]any{"type": []string{"integer", "null"}, "minimum": 0, "maximum": 10000000}}},
			map[string]any{"properties": map[string]any{"field": map[string]any{"enum": []string{"rating", "userRating"}}, "value": map[string]any{"type": []string{"number", "null"}, "minimum": 0, "maximum": 10}}},
		}
	}
	nullRef := func(name string) map[string]any {
		return map[string]any{"oneOf": []any{map[string]any{"$ref": "#/components/schemas/" + name}, map[string]any{"type": "null"}}}
	}
	fact := objectSchema(map[string]any{"field": field, "value": map[string]any{"type": []string{"number", "null"}}, "source": map[string]any{"type": "string", "enum": []string{"existing", "manual", "nfo"}}, "locked": map[string]any{"type": "boolean"}, "updatedAt": map[string]any{"type": []string{"string", "null"}, "format": "date-time"}, "nfoOrigin": nullRef("NFOItemOrigin"), "nfoLockOrigin": nullRef("NFOFieldLockOrigin")}, "field", "value", "source", "locked", "updatedAt", "nfoOrigin", "nfoLockOrigin")
	fact["oneOf"] = variants()
	schemas["ItemMetadataFact"] = fact
	item := schemas["ItemMetadata"].(map[string]any)
	item["properties"].(map[string]any)["facts"] = map[string]any{"type": "array", "maxItems": 4, "items": map[string]any{"$ref": "#/components/schemas/ItemMetadataFact"}}
	item["required"] = append(item["required"].([]string), "facts")
	patch := objectSchema(map[string]any{"field": field, "value": map[string]any{"type": []string{"number", "null"}}, "locked": map[string]any{"type": "boolean"}}, "field")
	patch["oneOf"] = variants()
	patch["anyOf"] = []any{map[string]any{"required": []string{"value"}}, map[string]any{"required": []string{"locked"}}}
	put := paths["/api/v1/items/{id}/metadata"].(map[string]any)["put"].(map[string]any)
	body := put["requestBody"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	body["properties"].(map[string]any)["facts"] = map[string]any{"type": "array", "minItems": 1, "maxItems": 4, "items": patch}
	body["required"] = []string{"expectedRevision"}
	body["anyOf"] = []any{map[string]any{"required": []string{"fields"}}, map[string]any{"required": []string{"facts"}}}
	put["description"] = put["description"].(string) + " Year facts use JSON integers from 1 through 9999; runtimeMinutes uses integers from 0 through 10000000; rating and userRating use numbers from 0 through 10. Fact names must be unique. Null at facts[].value records an explicit manual clear. Omitted fact value preserves it. Text and fact edits share one revision and transaction; explicit fact values clear NFO value and lock origins. Null anywhere else is rejected."
}
