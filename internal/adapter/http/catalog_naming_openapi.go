package httpapi

func catalogNamingSpecification(paths, schemas map[string]any) {
	boolean := map[string]any{"type": "boolean"}
	date := map[string]any{"type": "boolean", "description": "Read daily-show names such as 2024-03-15 as episodes: season is the year, the episode number is month*100+day."}
	absolute := map[string]any{"type": "boolean", "description": "Read numbers without season context (anime absolute numbering) as episodes of season 1; explicit markers and \" - 01\" / \"[01]\" numbers only."}
	schemas["CatalogNamingInput"] = objectSchema(map[string]any{"dateEpisodes": date, "absoluteEpisodes": absolute}, "dateEpisodes", "absoluteEpisodes")
	schemas["CatalogNaming"] = objectSchema(map[string]any{"libraryId": map[string]any{"type": "string", "format": "uuid"}, "dateEpisodes": boolean, "absoluteEpisodes": boolean,
		"parserVersion": map[string]any{"type": "string", "minLength": 1, "maxLength": 64, "description": "Parser version plus enabled options; tracked files whose stored version differs are planned again by the next synchronisation."}},
		"libraryId", "dateEpisodes", "absoluteEpisodes", "parserVersion")
	operations := map[string]any{}
	for _, route := range []struct{ method, summary, body string }{
		{"get", "Read the library's naming options (G21.3)", ""},
		{"put", "Replace the library's naming options; the next catalog synchronisation plans tracked and pending files again with them, locked identification still wins (G21.5)", "CatalogNamingInput"},
	} {
		op := operation(route.summary, "200", "400", "401", "403", "404", "408", "409", "413", "415", "429", "503")
		op["security"] = []any{map[string]any{"bearer": []string{}}}
		op["x-jelee-role"] = "administrator"
		op["parameters"] = []any{idParameter()}
		if route.body != "" {
			op["requestBody"] = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": schemaRef(route.body)}}, "description": "Maximum 64 KiB; one strict JSON object with both options."}
		}
		op["responses"].(map[string]any)["200"] = map[string]any{"description": "Current naming options", "content": map[string]any{"application/json": map[string]any{"schema": objectSchema(map[string]any{"data": schemaRef("CatalogNaming")}, "data")}}}
		operations[route.method] = op
	}
	paths["/api/v1/libraries/{id}/naming"] = operations
}
