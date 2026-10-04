package httpapi

func watchStatsSpecification(paths, schemas map[string]any) {
	uuid := map[string]any{"type": "string", "format": "uuid"}
	count := map[string]any{"type": "integer", "minimum": 0}
	date := map[string]any{"type": "string", "format": "date"}
	bearer := []any{map[string]any{"bearer": []string{}}}

	totals := map[string]any{
		"effectiveSeconds": map[string]any{"type": "integer", "minimum": 0, "description": "Effective watch time: played wall time without pauses, stalls, seeks and fast-forward, counted once across devices (docs/watch-statistics.md)."},
		"sessions":         map[string]any{"type": "integer", "minimum": 0, "description": "Playback sessions that reached the view threshold."},
		"views":            map[string]any{"type": "integer", "minimum": 0, "description": "Watch-throughs: first plays plus re-watches."},
		"firstPlays":       count,
		"rewatches":        count,
		"completions":      map[string]any{"type": "integer", "minimum": 0, "description": "Counted sessions that covered the completion share of the runtime."},
		"completionRate":   map[string]any{"type": "number", "minimum": 0, "maximum": 1, "description": "Average completion rate of the counted sessions; 0 without sessions."},
	}
	totalKeys := []string{"effectiveSeconds", "sessions", "views", "firstPlays", "rewatches", "completions", "completionRate"}
	withTotals := func(extra map[string]any, required ...string) map[string]any {
		props := map[string]any{}
		for k, v := range totals {
			props[k] = v
		}
		for k, v := range extra {
			props[k] = v
		}
		return objectSchema(props, append(append([]string{}, required...), totalKeys...)...)
	}
	schemas["WatchStatsTotals"] = withTotals(nil)
	schemas["WatchStatsReport"] = objectSchema(map[string]any{
		"userId":    map[string]any{"type": "string", "format": "uuid", "description": "The user the statistics belong to; absent for statistics of every user."},
		"from":      date,
		"to":        date,
		"period":    map[string]any{"type": "string", "enum": []string{"day", "week", "month", "year"}},
		"timeZone":  map[string]any{"type": "string", "description": "IANA zone whose midnight cuts days (stats.timeZone)."},
		"weekStart": map[string]any{"type": "string", "enum": []string{"monday", "sunday"}},
		"totals":    schemaRef("WatchStatsTotals"),
		"periods": map[string]any{"type": "array", "description": "One entry per period with data, oldest first; start is the first day of the period.",
			"items": withTotals(map[string]any{"start": date}, "start")},
		"topItems": map[string]any{"type": "array", "maxItems": 100, "description": "Items with the most effective time.",
			"items": withTotals(map[string]any{"itemId": uuid, "libraryId": uuid, "kind": map[string]any{"type": "string"}, "title": map[string]any{"type": "string"},
				"userData": schemaRef("UserItemData")}, "itemId", "libraryId", "kind", "title")},
		"libraries": map[string]any{"type": "array", "maxItems": 200, "items": withTotals(map[string]any{"libraryId": uuid, "name": map[string]any{"type": "string"}}, "libraryId", "name")},
		"kinds":     map[string]any{"type": "array", "items": withTotals(map[string]any{"kind": map[string]any{"type": "string"}}, "kind")},
		"topUsers": map[string]any{"type": "array", "maxItems": 100, "description": "Statistics of every user only: users with the most effective time.",
			"items": withTotals(map[string]any{"userId": uuid, "userName": map[string]any{"type": "string"}}, "userId", "userName")},
	}, "from", "to", "period", "timeZone", "weekStart", "totals", "periods", "topItems", "libraries", "kinds")

	rangeParameters := []any{
		map[string]any{"name": "from", "in": "query", "schema": date, "description": "First day (inclusive) in the reporting time zone; defaults to 29 days before to."},
		map[string]any{"name": "to", "in": "query", "schema": date, "description": "Last day (inclusive); defaults to today in the reporting time zone. At most 3660 days, 400 per day."},
		map[string]any{"name": "period", "in": "query", "schema": map[string]any{"type": "string", "enum": []string{"day", "week", "month", "year"}, "default": "day"}},
		map[string]any{"name": "top", "in": "query", "schema": map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "default": 10}},
	}
	reportContent := map[string]any{"application/json": map[string]any{"schema": objectSchema(map[string]any{"data": schemaRef("WatchStatsReport")}, "data")}}
	report := func(summary, description string, admin bool, parameters []any, statuses ...string) map[string]any {
		op := operation(summary, statuses...)
		op["security"] = bearer
		op["description"] = description + " Read from the daily roll-up only; sessions are counted after they end and the next aggregation (stats.aggregateSeconds). No response carries a delivery URL."
		op["parameters"] = parameters
		if admin {
			op["x-jelee-role"] = "administrator"
		}
		op["responses"].(map[string]any)["200"].(map[string]any)["content"] = reportContent
		return op
	}
	paths["/api/v1/users/me/watch-stats"] = map[string]any{"get": report("Read the caller's watch statistics",
		"Totals, periods, top items with the caller's current progress, and per library and kind breakdowns of the caller's own viewing (G23.3, G23.4). Items in libraries the caller can no longer see are left out of every figure (G48.3).",
		false, rangeParameters, "200", "400", "401", "408", "503")}
	paths["/api/v1/users/{id}/watch-stats"] = map[string]any{"get": report("Read a user's watch statistics",
		"Administrators only. The user's statistics as an administrator sees them: every library, including libraries the user can no longer see.",
		true, append([]any{idParameter()}, rangeParameters...), "200", "400", "401", "403", "404", "408", "503")}
	paths["/api/v1/watch-stats"] = map[string]any{"get": report("Read the watch statistics of every user",
		"Administrators only. Statistics of every user, with the users with the most effective time.",
		true, rangeParameters, "200", "400", "401", "403", "408", "503")}

	export := operation("Export the watch statistics roll-up", "200", "400", "401", "403", "408", "409", "503")
	export["security"] = bearer
	export["x-jelee-role"] = "administrator"
	export["description"] = "Administrators only (G23.4). Streams the daily rows (day, user, item) of a range in day, user and item order, as CSV (default) or NDJSON. The export is counted before anything is sent: more rows than limit (at most stats.exportMaxRows) is refused with 409 stats_export_limit; otherwise the audit event watch_stats.exported (range, format, rows, user) is recorded first. X-Jelee-Export-Rows announces the row count and the X-Jelee-Export-Complete trailer reports whether every row was sent. CSV text cells starting with =, +, -, @, tab or carriage return are prefixed with an apostrophe."
	export["parameters"] = []any{
		map[string]any{"name": "from", "in": "query", "schema": date, "description": "First day (inclusive); defaults to 29 days before to."},
		map[string]any{"name": "to", "in": "query", "schema": date, "description": "Last day (inclusive); defaults to today. At most 3660 days."},
		map[string]any{"name": "format", "in": "query", "schema": map[string]any{"type": "string", "enum": []string{"csv", "ndjson"}, "default": "csv"}},
		map[string]any{"name": "limit", "in": "query", "schema": map[string]any{"type": "integer", "minimum": 1}, "description": "Lowers the row limit; at most stats.exportMaxRows (default 100000)."},
		map[string]any{"name": "userId", "in": "query", "schema": uuid, "description": "Export only this user's rows."},
	}
	responses := export["responses"].(map[string]any)
	responses["200"] = map[string]any{
		"description": "The rows, streamed.",
		"headers": map[string]any{
			"X-Jelee-Export-Rows":     map[string]any{"schema": count, "description": "Rows the export holds."},
			"X-Jelee-Export-Complete": map[string]any{"schema": map[string]any{"type": "string", "enum": []string{"true", "false"}}, "description": "Trailer: false when the stream ended early."},
		},
		"content": map[string]any{
			"text/csv": map[string]any{"schema": map[string]any{"type": "string", "description": "Header row day,user_id,user_name,item_id,library_id,item_kind,item_title,effective_seconds,sessions,views,first_plays,rewatches,completions,completion_rate."}},
			"application/x-ndjson": map[string]any{"schema": objectSchema(map[string]any{
				"day": date, "userId": uuid, "userName": map[string]any{"type": "string"}, "itemId": uuid, "libraryId": uuid,
				"kind": map[string]any{"type": "string"}, "title": map[string]any{"type": "string"}, "effectiveMillis": count,
				"sessions": count, "views": count, "firstPlays": count, "rewatches": count, "completions": count,
				"completionRate": map[string]any{"type": "number", "minimum": 0, "maximum": 1},
			}, "day", "userId", "userName", "itemId", "libraryId", "kind", "title", "effectiveMillis", "sessions", "views", "firstPlays", "rewatches", "completions", "completionRate")},
		},
	}
	responses["409"] = map[string]any{"description": "stats_export_limit: the range holds more rows than the limit; narrow it."}
	paths["/api/v1/watch-stats/export"] = map[string]any{"get": export}
}
