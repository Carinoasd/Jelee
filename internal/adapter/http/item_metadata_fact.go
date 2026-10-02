package httpapi

import "github.com/MoYuanCN/Jelee/internal/domain"

func itemMetadataFactSpecification(paths, schemas map[string]any) {
	field := map[string]any{"type": "string", "enum": append([]string{"year", "runtimeMinutes", "rating", "userRating", "actors"}, domain.ItemMetadataListFieldNames()...)}
	actor := objectSchema(map[string]any{
		"name":  map[string]any{"type": "string", "minLength": 1, "maxLength": 1024},
		"role":  map[string]any{"type": "string", "maxLength": 1024},
		"thumb": map[string]any{"type": "string", "maxLength": 4096, "description": "Stored reference only; saving it does not fetch a URL or open a file."},
		"order": map[string]any{"type": []string{"integer", "null"}, "minimum": 0, "maximum": 1000000},
	}, "name")
	variants := func() []any {
		return []any{
			map[string]any{"properties": map[string]any{"field": map[string]any{"const": "actors"}, "value": map[string]any{"type": []string{"array", "null"}, "maxItems": domain.MaxMetadataActors, "items": actor, "description": "Actors retain source order and repetitions. Name is nonblank. UTF-8 limits: name/role 1024 bytes, thumb 4096 bytes, all actor strings combined 16384 bytes. Missing order remains missing; zero is an explicit order."}}},
			map[string]any{"properties": map[string]any{"field": map[string]any{"enum": domain.ItemMetadataListFieldNames()}, "value": map[string]any{"type": []string{"array", "null"}, "maxItems": domain.MaxMetadataListEntries, "items": map[string]any{"type": "string", "minLength": 1, "maxLength": domain.MaxMetadataListValueBytes}, "description": "Ordered nonblank strings; each entry is at most 1024 UTF-8 bytes and their combined size is at most 16384 bytes."}}},
			map[string]any{"properties": map[string]any{"field": map[string]any{"const": "year"}, "value": map[string]any{"type": []string{"integer", "null"}, "minimum": 1, "maximum": 9999}}},
			map[string]any{"properties": map[string]any{"field": map[string]any{"const": "runtimeMinutes"}, "value": map[string]any{"type": []string{"integer", "null"}, "minimum": 0, "maximum": 10000000}}},
			map[string]any{"properties": map[string]any{"field": map[string]any{"enum": []string{"rating", "userRating"}}, "value": map[string]any{"type": []string{"number", "null"}, "minimum": 0, "maximum": 10}}},
		}
	}
	nullRef := func(name string) map[string]any {
		return map[string]any{"oneOf": []any{map[string]any{"$ref": "#/components/schemas/" + name}, map[string]any{"type": "null"}}}
	}
	fact := objectSchema(map[string]any{"field": field, "value": map[string]any{"type": []string{"number", "array", "null"}}, "source": map[string]any{"type": "string", "enum": []string{"existing", "manual", "nfo"}}, "locked": map[string]any{"type": "boolean"}, "updatedAt": map[string]any{"type": []string{"string", "null"}, "format": "date-time"}, "nfoOrigin": nullRef("NFOItemOrigin"), "nfoLockOrigin": nullRef("NFOFieldLockOrigin")}, "field", "value", "source", "locked", "updatedAt", "nfoOrigin", "nfoLockOrigin")
	fact["oneOf"] = variants()
	fact["allOf"] = []any{map[string]any{"if": map[string]any{"properties": map[string]any{"source": map[string]any{"const": "nfo"}, "field": map[string]any{"enum": append([]string{"actors"}, domain.ItemMetadataListFieldNames()...)}}}, "then": map[string]any{"properties": map[string]any{"value": map[string]any{"type": "array", "minItems": 1}}}}}
	schemas["ItemMetadataFact"] = fact
	item := schemas["ItemMetadata"].(map[string]any)
	item["properties"].(map[string]any)["facts"] = map[string]any{"type": "array", "maxItems": 5 + len(domain.ItemMetadataListFieldNames()), "items": map[string]any{"$ref": "#/components/schemas/ItemMetadataFact"}}
	item["required"] = append(item["required"].([]string), "facts")
	patch := objectSchema(map[string]any{"field": field, "value": map[string]any{"type": []string{"number", "array", "null"}}, "locked": map[string]any{"type": "boolean"}}, "field")
	patch["oneOf"] = variants()
	patch["anyOf"] = []any{map[string]any{"required": []string{"value"}}, map[string]any{"required": []string{"locked"}}}
	put := paths["/api/v1/items/{id}/metadata"].(map[string]any)["put"].(map[string]any)
	body := put["requestBody"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	body["properties"].(map[string]any)["facts"] = map[string]any{"type": "array", "minItems": 1, "maxItems": 5 + len(domain.ItemMetadataListFieldNames()), "items": patch}
	body["required"] = []string{"expectedRevision"}
	body["anyOf"] = []any{map[string]any{"required": []string{"fields"}}, map[string]any{"required": []string{"facts"}}}
	put["description"] = put["description"].(string) + " Year facts use JSON integers from 1 through 9999; runtimeMinutes uses integers from 0 through 10000000; rating and userRating use numbers from 0 through 10. String list facts retain order; null and an empty array are explicit manual clears. Entries are bounded to 128, 1024 UTF-8 bytes each and 16384 combined bytes per list. The request body is bounded to 2 MiB. Actor facts retain name, role, thumb reference and optional integer order; all actor text totals at most 16384 UTF-8 bytes. Null and an empty actor array are explicit manual clears. Fact names must be unique. Null at facts[].value records an explicit manual clear. Omitted fact value preserves it. Text and fact edits share one revision and transaction; explicit fact values clear NFO value and lock origins. Null anywhere else is rejected."
}
