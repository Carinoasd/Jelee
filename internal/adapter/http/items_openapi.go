package httpapi

// catalogSpecification documents the catalog reads: the item listing in its
// cursor and offset forms, one item, its details and its file information.
func catalogSpecification(paths, schemas map[string]any) {
	str := map[string]any{"type": "string"}
	uuid := map[string]any{"type": "string", "format": "uuid"}
	kind := map[string]any{"type": "string", "enum": []string{"Movie", "HomeVideo", "Series", "Season", "Episode"}}
	query := func(name, description string, schema map[string]any) map[string]any {
		return map[string]any{"name": name, "in": "query", "description": description, "schema": schema}
	}
	content := func(op map[string]any, schema map[string]any) {
		op["responses"].(map[string]any)["200"].(map[string]any)["content"] = map[string]any{"application/json": map[string]any{"schema": schema}}
	}
	bearer := []any{map[string]any{"bearer": []string{}}}

	item := map[string]any{
		"id": uuid, "libraryId": uuid, "title": str, "kind": kind,
		"parentId":       map[string]any{"type": "string", "format": "uuid", "description": "Present for an explicitly linked season or episode in the same library."},
		"premiereDate":   map[string]any{"type": "string", "format": "date", "description": "Release date; only in the offset form and in details, absent when unknown."},
		"productionYear": map[string]any{"type": "integer", "minimum": 1, "maximum": 9999, "description": "Release year (the year fact, else the year of the release date); only in the offset form and in details, absent when unknown."},
	}
	schemas["CatalogItem"] = objectSchema(item, "id", "libraryId", "title", "kind")
	details := map[string]any{}
	for key, value := range item {
		details[key] = value
	}
	for key, description := range map[string]string{"sortTitle": "Sort title, absent when none is stored.", "originalTitle": "Original title, absent when none is stored.", "tagline": "Tagline, absent when none is stored.", "overview": "Overview, absent when none is stored."} {
		details[key] = map[string]any{"type": "string", "description": description}
	}
	details["genres"] = map[string]any{"type": "array", "items": str, "description": "Genre labels in stored order, without duplicates."}
	details["externalIds"] = map[string]any{"type": "array", "description": "Provider identifiers (for example tmdb, imdb, tvdb) as stored.", "items": objectSchema(map[string]any{"type": str, "value": str, "default": map[string]any{"type": "boolean"}}, "type", "value", "default")}
	details["nfo"] = objectSchema(map[string]any{
		"status": map[string]any{"type": "string", "enum": []string{"unread", "valid", "missing", "nfo_invalid"}, "description": "Last confirmed NFO read of the item: unread when none was confirmed; missing when no NFO file was found; nfo_invalid when the file failed validation."},
		"readAt": map[string]any{"type": "string", "format": "date-time", "description": "When the NFO file was last read; absent when unread."},
		"fields": map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []string{"title", "originalTitle", "sortTitle", "tagline", "overview", "date", "year", "genres", "uniqueIds"}}, "description": "Displayed fields whose current value came from an NFO file."},
	}, "status", "fields")
	schemas["CatalogItemDetails"] = objectSchema(details, "id", "libraryId", "title", "kind", "genres", "externalIds", "nfo")
	schemas["MediaSourceInfo"] = mediaSourceSchema(false)

	limit := map[string]any{"type": "integer", "minimum": 1, "maximum": itemsPageMax, "default": 50}
	list := operation("List visible video items", "200", "400", "401", "408", "503")
	list["security"] = bearer
	list["description"] = "Two forms share this route. Without browse parameters it is the original keyset listing in item ID order: pass the returned nextCursor as cursor to continue; nextCursor is empty after the last page. Any of offset, libraryId, parentId, type, sort, order or q selects the offset form, which filters and sorts and reports total; continue it with offset+limit while that is below total. A cursor together with a browse parameter is 400 invalid_request. Library grants apply in both forms: a library or parent the caller cannot see yields an empty page with total 0, exactly like one that does not exist."
	list["parameters"] = []any{
		query("cursor", "Keyset cursor of the original form; not allowed with browse parameters.", uuid),
		query("limit", "Page size in both forms.", limit),
		query("offset", "Offset form: items to skip.", map[string]any{"type": "integer", "minimum": 0, "maximum": 1000000, "default": 0}),
		query("libraryId", "Offset form: only items of this library, at any level.", uuid),
		query("parentId", "Offset form: only direct children of this library (its top-level items) or of this series or season (its seasons or episodes).", uuid),
		map[string]any{"name": "type", "in": "query", "description": "Offset form: item kinds to include; repeat the parameter or separate kinds with commas. Default every kind.", "style": "form", "explode": true, "schema": map[string]any{"type": "array", "maxItems": 5, "items": kind}},
		query("sort", "Offset form: comma separated sort keys, at most 3 distinct; the item ID breaks remaining ties. name compares the sort title, else the title, case-insensitively.", map[string]any{"type": "string", "pattern": "^(name|premiereDate|productionYear)(,(name|premiereDate|productionYear)){0,2}$", "default": "name"}),
		query("order", "Offset form: direction of every sort key. Items without a date or year come first ascending and last descending.", map[string]any{"type": "string", "enum": []string{"asc", "desc"}, "default": "asc"}),
		query("q", "Offset form: case-insensitive title substring. % and _ match literally.", map[string]any{"type": "string", "maxLength": 128}),
	}
	content(list, objectSchema(map[string]any{
		"data": map[string]any{"type": "array", "maxItems": itemsPageMax, "items": schemaRef("CatalogItem")},
		"pagination": objectSchema(map[string]any{
			"nextCursor": map[string]any{"type": "string", "description": "Original form only; always empty in the offset form."},
			"limit":      map[string]any{"type": "integer", "minimum": 1, "maximum": itemsPageMax},
			"offset":     map[string]any{"type": "integer", "minimum": 0, "description": "Offset form only."},
			"total":      map[string]any{"type": "integer", "minimum": 0, "description": "Offset form only: every visible item matching the filters."},
		}, "nextCursor", "limit"),
	}, "data", "pagination"))
	paths["/api/v1/items"] = map[string]any{"get": list}

	one := operation("Read a visible video item", "200")
	one["security"] = bearer
	one["parameters"] = []any{idParameter()}
	content(one, objectSchema(map[string]any{"data": schemaRef("CatalogItem")}, "data"))
	paths["/api/v1/items/{id}"] = map[string]any{"get": one}

	hidden := func(op map[string]any) {
		op["security"] = bearer
		op["parameters"] = []any{idParameter()}
		op["responses"].(map[string]any)["404"] = map[string]any{"description": "The item is missing or not visible to the caller; both are answered alike (403 forbidden when hidden content is configured as 403)."}
	}
	detail := operation("Read the display metadata of a visible item", "200", "400", "401", "403", "404", "408", "503")
	detail["description"] = "Any session kind. Overview, release date and year, original title, tagline, genres, external IDs and the NFO read state for every user who may see the item. No file paths, roots, provider origins or NFO file identities are returned; the administrator metadata view stays at /api/v1/items/{id}/metadata. No query parameters are accepted."
	hidden(detail)
	content(detail, objectSchema(map[string]any{"data": schemaRef("CatalogItemDetails")}, "data"))
	paths["/api/v1/items/{id}/details"] = map[string]any{"get": detail}

	sources := operation("Describe the media files of a visible item", "200", "400", "401", "403", "404", "408", "503")
	sources["description"] = "Any session kind. File information only: container, size, duration, bit rate, version labels, embedded video, audio and subtitle streams from the current probe result, and external subtitle and audio files (language, flags, format). It carries no file paths and no delivery URLs; delivery stays native-only under /api/v1/items/{id}/playback. Sources without a current probe result are listed with probed=false. No query parameters are accepted."
	hidden(sources)
	content(sources, objectSchema(map[string]any{"data": objectSchema(map[string]any{
		"itemId": uuid, "sources": map[string]any{"type": "array", "items": schemaRef("MediaSourceInfo")},
	}, "itemId", "sources")}, "data"))
	paths["/api/v1/items/{id}/sources"] = map[string]any{"get": sources}
}
