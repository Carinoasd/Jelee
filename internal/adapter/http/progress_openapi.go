package httpapi

func progressSpecification(paths, schemas map[string]any) {
	uuid := map[string]any{"type": "string", "format": "uuid"}
	ticks := map[string]any{"type": "integer", "minimum": 0, "maximum": 86400000000000, "description": "Position in 100-nanosecond ticks."}
	playKey := map[string]any{"type": "string", "minLength": 1, "maxLength": 128, "pattern": "^[A-Za-z0-9._:-]+$"}
	bearer := []any{map[string]any{"bearer": []string{}}}

	schemas["UserItemData"] = objectSchema(map[string]any{
		"itemId":       uuid,
		"resumeTicks":  map[string]any{"type": "integer", "minimum": 0, "description": "Resume point of the logical item in ticks; 0 starts from the beginning. Shared by every version of the item (G20.4)."},
		"played":       map[string]any{"type": "boolean"},
		"playCount":    map[string]any{"type": "integer", "minimum": 0, "description": "Completed playbacks plus explicit played marks."},
		"lastPlayedAt": map[string]any{"type": "string", "format": "date-time"},
	}, "itemId", "resumeTicks", "played", "playCount")
	schemas["PlaybackReport"] = map[string]any{
		"type": "object", "additionalProperties": false,
		"description": "A playback report. playSessionId is the identifier returned by start (or any client chosen key); without it the session is one per authenticated session and itemId. Reports are buffered and written in batches; repeated reports between two writes keep only the latest state.",
		"properties": map[string]any{
			"playSessionId": playKey,
			"itemId":        uuid,
			"sourceId":      map[string]any{"type": "string", "format": "uuid", "description": "Version being played; it must belong to the item. Start only; defaults to the best version."},
			"positionTicks": ticks,
			"paused":        map[string]any{"type": "boolean"},
			"failed":        map[string]any{"type": "boolean", "description": "Stop only: the playback failed. A failed playback never counts as watched."},
			"failureReason": map[string]any{"type": "string", "enum": []string{"transcode_disabled", "codec_unsupported", "client_blocked", "permission_denied", "playback_error"}, "description": "Stop with failed only; playback_error when omitted."},
		},
	}
	schemas["ActivePlayback"] = objectSchema(map[string]any{
		"id": uuid, "userId": uuid, "userName": map[string]any{"type": "string"},
		"deviceId":   map[string]any{"type": "string", "description": "Client-supplied label from the native login, not a proof."},
		"clientName": map[string]any{"type": "string", "description": "Client-supplied label from the native login, not a proof."},
		"itemId":     uuid, "itemTitle": map[string]any{"type": "string"}, "sourceId": uuid,
		"delivery":  map[string]any{"const": "direct"},
		"startedAt": map[string]any{"type": "string", "format": "date-time"}, "lastReportAt": map[string]any{"type": "string", "format": "date-time"},
		"positionTicks": ticks, "runtimeTicks": ticks, "paused": map[string]any{"type": "boolean"},
	}, "id", "userId", "userName", "itemId", "itemTitle", "delivery", "startedAt", "lastReportAt", "positionTicks", "paused")

	report := func(summary, description string, statuses ...string) map[string]any {
		op := operation(summary, statuses...)
		op["description"] = "Native sessions only; web sessions get 403 web_playback_disabled. " + description
		op["security"] = bearer
		op["x-jelee-session"] = "native"
		op["requestBody"] = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": schemaRef("PlaybackReport")}}}
		responses := op["responses"].(map[string]any)
		responses["404"] = map[string]any{"description": "The item is missing or not visible to the caller, or the source does not belong to it; all are answered alike."}
		responses["409"] = map[string]any{"description": "conflict: the play session already ended or names another item; transcode_disabled for a transformation parameter (G10.3)."}
		responses["503"] = map[string]any{"description": "playback_busy: the progress buffer holds its configured number of sessions; retry after Retry-After."}
		return op
	}
	start := report("Report that playback started",
		"Opens a playback session for a visible item, or rejoins the session of the same playSessionId. Requires itemId. The response names the session and the report interval the client should keep; faster reports are accepted and coalesced.",
		"200", "400", "401", "403", "404", "408", "409", "413", "415", "503")
	start["responses"].(map[string]any)["200"].(map[string]any)["content"] = map[string]any{"application/json": map[string]any{"schema": objectSchema(map[string]any{"data": objectSchema(map[string]any{
		"playSessionId": playKey, "itemId": uuid, "reportIntervalSeconds": map[string]any{"type": "integer", "minimum": 1},
	}, "playSessionId", "itemId", "reportIntervalSeconds")}, "data")}}
	paths["/api/v1/playback/start"] = map[string]any{"post": start}
	paths["/api/v1/playback/progress"] = map[string]any{"post": report("Report playback progress",
		"Updates the buffered state of a session (position, paused). Nothing is written per report: the server writes every buffered session in one batch per flush interval. An unknown session with itemId is opened as by start.",
		"204", "400", "401", "403", "404", "408", "409", "413", "415", "503")}
	paths["/api/v1/playback/stop"] = map[string]any{"post": report("Report that playback stopped",
		"Ends a session and writes it at once: past the completion share of the runtime the item becomes played with one more play and no resume point; otherwise the position becomes the resume point. Repeating a stop is accepted and changes nothing.",
		"204", "400", "401", "403", "404", "408", "409", "413", "415", "503")}

	sessions := operation("List active playback sessions", "200", "400", "401", "403", "408", "503")
	sessions["security"] = bearer
	sessions["x-jelee-role"] = "administrator"
	sessions["description"] = "Administrators only. At most 500 sessions, most recently reporting first. Positions buffered on this instance are included."
	sessions["responses"].(map[string]any)["200"].(map[string]any)["content"] = map[string]any{"application/json": map[string]any{"schema": objectSchema(map[string]any{"data": map[string]any{"type": "array", "maxItems": 500, "items": schemaRef("ActivePlayback")}}, "data")}}
	paths["/api/v1/playback/sessions"] = map[string]any{"get": sessions}

	userData := func(summary, description string, statuses ...string) map[string]any {
		op := operation(summary, statuses...)
		op["security"] = bearer
		op["description"] = description
		op["parameters"] = []any{idParameter()}
		responses := op["responses"].(map[string]any)
		responses["404"] = map[string]any{"description": "The item is missing or not visible to the caller; both are answered alike."}
		responses["200"].(map[string]any)["content"] = map[string]any{"application/json": map[string]any{"schema": objectSchema(map[string]any{"data": schemaRef("UserItemData")}, "data")}}
		return op
	}
	paths["/api/v1/items/{id}/user-data"] = map[string]any{"get": userData("Read the caller's progress on an item",
		"Resume point, played state and play count of a visible item. An item the caller cannot see is answered like a missing one, also when it was played before access was withdrawn (G48.3).",
		"200", "400", "401", "403", "404", "408", "503")}
	played := userData("Mark an item played", "Marks a visible item played: one more play, no resume point. The body must be an empty JSON object.", "200", "400", "401", "403", "404", "408", "413", "415", "503")
	played["requestBody"] = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"type": "object", "additionalProperties": false}}}}
	paths["/api/v1/items/{id}/played"] = map[string]any{
		"put":    played,
		"delete": userData("Mark an item unplayed", "Marks a visible item unplayed: no plays, no resume point. No body.", "200", "400", "401", "403", "404", "408", "503"),
	}

	resume := operation("List items to continue watching", "200", "400", "401", "408", "503")
	resume["security"] = bearer
	resume["description"] = "Visible movies, episodes and home videos with a resume point that are not played, most recently played first. Items in libraries the caller can no longer see are never listed (G48.3). Progress buffered but not yet written appears after the next flush."
	resume["parameters"] = []any{
		map[string]any{"name": "offset", "in": "query", "schema": map[string]any{"type": "integer", "minimum": 0, "maximum": 1000000, "default": 0}},
		map[string]any{"name": "limit", "in": "query", "schema": map[string]any{"type": "integer", "minimum": 1, "maximum": 500, "default": 50}},
	}
	resume["responses"].(map[string]any)["200"].(map[string]any)["content"] = map[string]any{"application/json": map[string]any{"schema": objectSchema(map[string]any{"data": objectSchema(map[string]any{
		"items": map[string]any{"type": "array", "maxItems": 500, "items": objectSchema(map[string]any{
			"id": uuid, "libraryId": uuid, "parentId": uuid, "title": map[string]any{"type": "string"},
			"kind":     map[string]any{"type": "string", "enum": []string{"Movie", "Episode", "HomeVideo"}},
			"userData": schemaRef("UserItemData"),
		}, "id", "libraryId", "kind", "title", "userData")},
		"total": map[string]any{"type": "integer", "minimum": 0}, "offset": map[string]any{"type": "integer", "minimum": 0}, "limit": map[string]any{"type": "integer", "minimum": 1},
	}, "items", "total", "offset", "limit")}, "data")}}
	paths["/api/v1/users/me/resume"] = map[string]any{"get": resume}

	clear := operation("Clear the caller's playback history", "204", "400", "401", "408", "503")
	clear["security"] = bearer
	clear["description"] = "Deletes every playback session, stored sample, resume point, played state, play count and watch statistics of the caller (G23.4) and records the audit event playback.history_cleared with counts only. No body. Sessions still playing start a new history with their next report."
	paths["/api/v1/users/me/playback-history"] = map[string]any{"delete": clear}
}
