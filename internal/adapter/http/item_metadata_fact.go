package httpapi

import "github.com/MoYuanCN/Jelee/internal/domain"

func itemMetadataFactSpecification(paths, schemas map[string]any) {
	field := map[string]any{"type": "string", "enum": append([]string{"year", "runtimeMinutes", "rating", "userRating", "actors", "uniqueIds", "ratings", "collection", "dateAdded", "trailers", "art", "seasonCount", "episodeCount", "seriesStatus", "airsDayOfWeek", "airsTime", "seasonNumber", "episodeNumber", "displaySeason", "displayEpisode", "aired", "showTitle"}, domain.ItemMetadataListFieldNames()...)}
	artwork := objectSchema(map[string]any{
		"kind":     map[string]any{"type": "string", "minLength": 1, "maxLength": 64},
		"location": map[string]any{"type": "string", "minLength": 1, "maxLength": 4096},
		"preview":  map[string]any{"type": "string", "maxLength": 4096},
		"season":   map[string]any{"type": []string{"integer", "null"}, "minimum": 0, "maximum": 1000000},
	}, "kind", "location")
	artwork["description"] = "Ordered artwork references; saving does not fetch URLs or open files. Kind and location are nonblank. UTF-8 limits are 64 bytes for kind and 4096 for location/preview, with 16384 combined bytes across all artwork. Missing or null season is distinct from zero."
	collection := objectSchema(map[string]any{"name": map[string]any{"type": "string", "minLength": 1, "maxLength": 1024}, "overview": map[string]any{"type": "string", "maxLength": 16384}}, "name")
	collection["description"] = "Collection name and overview are saved together. Name must be nonblank; UTF-8 limits are 1024 bytes for name and 16384 bytes for overview. Null clears the whole collection; omitted overview means empty. No collection entity is created by this metadata edit."
	sourceRating := objectSchema(map[string]any{
		"name":    map[string]any{"type": "string", "maxLength": 1024},
		"value":   map[string]any{"type": "number", "minimum": 0, "maximum": 1000000},
		"max":     map[string]any{"type": []string{"number", "null"}, "exclusiveMinimum": 0, "maximum": 1000000},
		"votes":   map[string]any{"type": []string{"integer", "null"}, "minimum": 0, "maximum": 2147483647},
		"default": map[string]any{"type": "boolean"},
	}, "value")
	sourceRating["description"] = "Value cannot exceed max; absent or null max uses scale 10. Source names, source order, explicit scales, default flags and absent versus zero votes are preserved. Names are at most 1024 UTF-8 bytes each and 16384 combined bytes. Unnamed sources remain unnamed."
	identifier := objectSchema(map[string]any{
		"type":    map[string]any{"type": "string", "minLength": 1, "maxLength": 64},
		"value":   map[string]any{"type": "string", "minLength": 1, "maxLength": 1024},
		"default": map[string]any{"type": "boolean"},
	}, "type", "value")
	actor := objectSchema(map[string]any{
		"name":  map[string]any{"type": "string", "minLength": 1, "maxLength": 1024},
		"role":  map[string]any{"type": "string", "maxLength": 1024},
		"thumb": map[string]any{"type": "string", "maxLength": 4096, "description": "Stored reference only; saving it does not fetch a URL or open a file."},
		"order": map[string]any{"type": []string{"integer", "null"}, "minimum": 0, "maximum": 1000000},
	}, "name")
	variants := func() []any {
		return []any{
			map[string]any{"properties": map[string]any{"field": map[string]any{"enum": []string{"seasonNumber", "episodeNumber", "displaySeason", "displayEpisode"}}, "value": map[string]any{"type": []string{"integer", "null"}, "minimum": 0, "maximum": 1000000, "description": "Episode numbering preserves missing versus zero. Null is an explicit manual clear."}}},
			map[string]any{"properties": map[string]any{"field": map[string]any{"const": "aired"}, "value": map[string]any{"type": []string{"string", "null"}, "minLength": 10, "maxLength": 64, "description": "Real calendar date, legacy date-time or RFC3339 (up to nine fractional digits); original representation and time zone are preserved. Null clears the field."}}},
			map[string]any{"properties": map[string]any{"field": map[string]any{"const": "showTitle"}, "value": map[string]any{"type": []string{"string", "null"}, "minLength": 1, "maxLength": 1024, "description": "Nonblank show title, at most 1024 UTF-8 bytes. Null clears the field; this does not create a parent series."}}},
			map[string]any{"properties": map[string]any{"field": map[string]any{"enum": []string{"seasonCount", "episodeCount"}}, "value": map[string]any{"type": []string{"integer", "null"}, "minimum": -1, "maximum": 1000000, "description": "Series counts retain -1 for unknown; zero and missing are distinct. Null is an explicit manual clear."}}},
			map[string]any{"properties": map[string]any{"field": map[string]any{"enum": []string{"seriesStatus", "airsDayOfWeek", "airsTime"}}, "value": map[string]any{"type": []string{"string", "null"}, "minLength": 1, "maxLength": 128, "description": "Nonblank original text, at most 128 UTF-8 bytes. Status and airing text are preserved without inferring a date, time zone or schedule. Null clears the field."}}},
			map[string]any{"properties": map[string]any{"field": map[string]any{"const": "dateAdded"}, "value": map[string]any{"type": []string{"string", "null"}, "minLength": 10, "maxLength": 64, "description": "A real calendar date in YYYY-MM-DD, YYYY-MM-DD HH:mm:ss or RFC3339 form (up to nine fractional digits). Original representation is preserved without assigning a time zone. Null clears the date."}}},
			map[string]any{"properties": map[string]any{"field": map[string]any{"const": "trailers"}, "value": map[string]any{"type": []string{"array", "null"}, "maxItems": domain.MaxMetadataReferences, "items": map[string]any{"type": "string", "minLength": 1, "maxLength": 4096}, "description": "Ordered nonblank references, at most 4096 UTF-8 bytes each and 16384 combined bytes. Saving does not fetch references. Null and an empty array are manual clears."}}},
			map[string]any{"properties": map[string]any{"field": map[string]any{"const": "art"}, "value": map[string]any{"type": []string{"array", "null"}, "maxItems": domain.MaxMetadataReferences, "items": artwork}}},
			map[string]any{"properties": map[string]any{"field": map[string]any{"const": "collection"}, "value": map[string]any{"oneOf": []any{collection, map[string]any{"type": "null"}}}}},
			map[string]any{"properties": map[string]any{"field": map[string]any{"const": "ratings"}, "value": map[string]any{"type": []string{"array", "null"}, "maxItems": domain.MaxMetadataRatings, "items": sourceRating}}},
			map[string]any{"properties": map[string]any{"field": map[string]any{"const": "uniqueIds"}, "value": map[string]any{"type": []string{"array", "null"}, "maxItems": domain.MaxMetadataUniqueIDs, "items": identifier, "description": "Provider identifiers retain source order and default flags. Type and value are nonblank; UTF-8 limits are 64 and 1024 bytes, with 16384 combined bytes. Null and an empty array explicitly clear manual identifiers."}}},
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
	fact := objectSchema(map[string]any{"field": field, "value": map[string]any{"type": []string{"number", "string", "array", "object", "null"}}, "source": map[string]any{"type": "string", "enum": []string{"existing", "manual", "nfo"}}, "locked": map[string]any{"type": "boolean"}, "updatedAt": map[string]any{"type": []string{"string", "null"}, "format": "date-time"}, "nfoOrigin": nullRef("NFOItemOrigin"), "nfoLockOrigin": nullRef("NFOFieldLockOrigin")}, "field", "value", "source", "locked", "updatedAt", "nfoOrigin", "nfoLockOrigin")
	fact["oneOf"] = variants()
	fact["allOf"] = []any{map[string]any{"if": map[string]any{"properties": map[string]any{"source": map[string]any{"const": "nfo"}, "field": map[string]any{"enum": append([]string{"actors", "uniqueIds", "ratings", "trailers", "art"}, domain.ItemMetadataListFieldNames()...)}}}, "then": map[string]any{"properties": map[string]any{"value": map[string]any{"type": "array", "minItems": 1}}}}}
	fact["allOf"] = append(fact["allOf"].([]any), map[string]any{"if": map[string]any{"properties": map[string]any{"source": map[string]any{"const": "nfo"}, "field": map[string]any{"const": "collection"}}}, "then": map[string]any{"properties": map[string]any{"value": map[string]any{"type": "object"}}}})
	fact["allOf"] = append(fact["allOf"].([]any), map[string]any{"if": map[string]any{"properties": map[string]any{"source": map[string]any{"const": "nfo"}, "field": map[string]any{"const": "dateAdded"}}}, "then": map[string]any{"properties": map[string]any{"value": map[string]any{"type": "string", "minLength": 10}}}})
	fact["allOf"] = append(fact["allOf"].([]any), map[string]any{"if": map[string]any{"properties": map[string]any{"source": map[string]any{"const": "nfo"}, "field": map[string]any{"enum": append(domain.ItemMetadataSeriesFieldNames(), domain.ItemMetadataEpisodeFieldNames()...)}}}, "then": map[string]any{"properties": map[string]any{"value": map[string]any{"not": map[string]any{"type": "null"}}}}})
	schemas["ItemMetadataFact"] = fact
	item := schemas["ItemMetadata"].(map[string]any)
	item["properties"].(map[string]any)["facts"] = map[string]any{"type": "array", "maxItems": 22 + len(domain.ItemMetadataListFieldNames()), "items": map[string]any{"$ref": "#/components/schemas/ItemMetadataFact"}}
	item["required"] = append(item["required"].([]string), "facts")
	patch := objectSchema(map[string]any{"field": field, "value": map[string]any{"type": []string{"number", "string", "array", "object", "null"}}, "locked": map[string]any{"type": "boolean"}}, "field")
	patch["oneOf"] = variants()
	patch["anyOf"] = []any{map[string]any{"required": []string{"value"}}, map[string]any{"required": []string{"locked"}}}
	put := paths["/api/v1/items/{id}/metadata"].(map[string]any)["put"].(map[string]any)
	body := put["requestBody"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	body["properties"].(map[string]any)["facts"] = map[string]any{"type": "array", "minItems": 1, "maxItems": 22 + len(domain.ItemMetadataListFieldNames()), "items": patch}
	body["required"] = []string{"expectedRevision"}
	body["anyOf"] = []any{map[string]any{"required": []string{"fields"}}, map[string]any{"required": []string{"facts"}}}
	put["description"] = put["description"].(string) + " Year facts use JSON integers from 1 through 9999; runtimeMinutes uses integers from 0 through 10000000; rating and userRating use numbers from 0 through 10. String list facts retain order; null and an empty array are explicit manual clears. Entries are bounded to 128, 1024 UTF-8 bytes each and 16384 combined bytes per list. The request body is bounded to 2 MiB. Actor facts retain name, role, thumb reference and optional integer order; all actor text totals at most 16384 UTF-8 bytes. Null and an empty actor array are explicit manual clears. Fact names must be unique. Null at facts[].value records an explicit manual clear. Omitted fact value preserves it. Text and fact edits share one revision and transaction; explicit fact values clear NFO value and lock origins. Optional artwork season, actor order and rating max/votes also accept null to represent a missing value; other null properties are rejected."
}
