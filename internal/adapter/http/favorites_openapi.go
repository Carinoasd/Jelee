package httpapi

// favoritesSpecification documents favourites (G24.2): marking, unmarking
// and the caller's own list.
func favoritesSpecification(paths, schemas map[string]any) {
	uuid := map[string]any{"type": "string", "format": "uuid"}
	bearer := []any{map[string]any{"bearer": []string{}}}
	kind := map[string]any{"type": "string", "enum": []string{"Movie", "HomeVideo", "Series", "Season", "Episode"}}

	mark := func(summary, description string, statuses ...string) map[string]any {
		op := operation(summary, statuses...)
		op["security"] = bearer
		op["description"] = description
		op["parameters"] = []any{idParameter()}
		responses := op["responses"].(map[string]any)
		responses["404"] = map[string]any{"description": "The item is missing or not visible to the caller; both are answered alike (403 forbidden when hidden content is configured as 403)."}
		responses["200"].(map[string]any)["content"] = map[string]any{"application/json": map[string]any{"schema": objectSchema(map[string]any{"data": schemaRef("UserItemData")}, "data")}}
		return op
	}
	put := mark("Mark an item as a favourite",
		"Marks a visible item of any kind as a favourite of the caller (G24.2) and answers the caller's data of the item. Marking again keeps the first time. Favourites belong to the logical item, so every version shares one. The body must be an empty JSON object. A share guest is refused.",
		"200", "400", "401", "403", "404", "408", "413", "415", "503")
	put["requestBody"] = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"type": "object", "additionalProperties": false}}}}
	paths["/api/v1/items/{id}/favorite"] = map[string]any{
		"put": put,
		"delete": mark("Remove an item from the favourites",
			"Removes a visible item from the caller's favourites and answers the caller's data of the item; an item that is not a favourite is answered the same way. No body. A share guest is refused.",
			"200", "400", "401", "403", "404", "408", "503"),
	}

	schemas["FavoriteItem"] = objectSchema(map[string]any{
		"id": uuid, "libraryId": uuid, "title": map[string]any{"type": "string"}, "kind": kind,
		"parentId":       map[string]any{"type": "string", "format": "uuid", "description": "Present for an explicitly linked season or episode in the same library."},
		"premiereDate":   map[string]any{"type": "string", "format": "date", "description": "Release date, absent when unknown."},
		"productionYear": map[string]any{"type": "integer", "minimum": 1, "maximum": 9999, "description": "Release year, absent when unknown."},
		"favoritedAt":    map[string]any{"type": "string", "format": "date-time", "description": "When the caller marked the item."},
	}, "id", "libraryId", "title", "kind", "favoritedAt")
	list := operation("List the caller's favourites", "200", "400", "401", "403", "408", "503")
	list["security"] = bearer
	list["description"] = "The caller's own favourites, most recently marked first. Items the caller can no longer see are never listed or counted (G48.3); their marks are kept and come back with access. A share guest is refused."
	list["parameters"] = []any{
		map[string]any{"name": "libraryId", "in": "query", "description": "Only favourites of this library. A library the caller cannot see yields an empty page, exactly like one that does not exist.", "schema": uuid},
		map[string]any{"name": "type", "in": "query", "description": "Comma separated item kinds to include; default every kind.", "schema": map[string]any{"type": "string", "pattern": "^(Movie|HomeVideo|Series|Season|Episode)(,(Movie|HomeVideo|Series|Season|Episode)){0,4}$"}},
	}
	list["responses"].(map[string]any)["200"].(map[string]any)["content"] = map[string]any{"application/json": map[string]any{"schema": objectSchema(map[string]any{"data": objectSchema(map[string]any{
		"items": map[string]any{"type": "array", "maxItems": 500, "items": schemaRef("FavoriteItem")},
		"total": map[string]any{"type": "integer", "minimum": 0}, "offset": map[string]any{"type": "integer", "minimum": 0}, "limit": map[string]any{"type": "integer", "minimum": 1},
		"nextCursor": map[string]any{"type": "string", "description": "Pass as cursor for the next page; empty on the last page."},
	}, "items", "total", "offset", "limit", "nextCursor")}, "data")}}
	paths["/api/v1/users/me/favorites"] = map[string]any{"get": list}
}
