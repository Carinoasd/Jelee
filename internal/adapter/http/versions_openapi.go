package httpapi

import (
	"github.com/MoYuanCN/Jelee/internal/domain"
)

// versionsSpecification documents version decisions (G20.3, G20.5) and track
// preferences (G16.5, G20.4).
func versionsSpecification(paths, schemas map[string]any) {
	uuid := map[string]any{"type": "string", "format": "uuid"}
	instant := map[string]any{"type": "string", "format": "date-time"}
	bearer := []any{map[string]any{"bearer": []string{}}}
	nullable := func(schema map[string]any) map[string]any {
		return map[string]any{"oneOf": []any{schema, map[string]any{"type": "null"}}}
	}
	language := map[string]any{"type": "string", "minLength": 2, "maxLength": 35, "description": "Language tag or code (en, eng, zh-TW, zh-Hant, chs); stored as the canonical BCP 47 tag."}
	track := map[string]any{"type": "string", "pattern": `^(e:(0|[1-9][0-9]{0,4})|x:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$`,
		"description": "One original track of this version: e:<stream index> for an embedded stream, x:<track id> for an external file. Version level only."}
	preference := map[string]any{
		"audioLanguage":    nullable(language),
		"audioCommentary":  nullable(map[string]any{"type": "boolean", "description": "true prefers a commentary track; false or null avoids one."}),
		"audioTrack":       nullable(track),
		"subtitleMode":     nullable(map[string]any{"type": "string", "enum": []string{domain.SubtitleModeAuto, domain.SubtitleModeAlways, domain.SubtitleModeForced, domain.SubtitleModeOff}, "description": "auto (default): subtitles in subtitleLanguage when the chosen audio is in another language, else forced subtitles only; always; forced (forced subtitles only); off."}),
		"subtitleLanguage": nullable(language),
		"subtitleSdh":      nullable(map[string]any{"type": "boolean", "description": "true prefers subtitles for the deaf and hard of hearing."}),
		"subtitleTrack":    nullable(track),
	}
	schemas["TrackPreference"] = map[string]any{"type": "object", "additionalProperties": false, "properties": preference,
		"description": "One preference level. A null or absent member inherits from the broader level (version, then item, then the user's defaults); the account language stands in for an unset subtitle language. Nothing is converted: preferences only choose among the original tracks."}
	inputMembers := map[string]any{"sourceId": nullable(map[string]any{"type": "string", "format": "uuid", "description": "A version of this item; null or absent sets the item level."})}
	for key, value := range preference {
		inputMembers[key] = value
	}
	schemas["TrackPreferenceInput"] = map[string]any{"type": "object", "additionalProperties": false, "properties": inputMembers,
		"description": "Replaces one level. Members left null or absent inherit; a body without any member removes the level."}
	schemas["TrackPreferences"] = objectSchema(map[string]any{
		"itemId": uuid,
		"user":   nullable(schemaRef("TrackPreference")),
		"item":   nullable(schemaRef("TrackPreference")),
		"versions": map[string]any{"type": "array", "items": objectSchema(map[string]any{"sourceId": uuid, "preference": schemaRef("TrackPreference")}, "sourceId", "preference"),
			"description": "Version levels of the item's current versions, by source ID."},
	}, "itemId", "user", "item", "versions")
	schemas["VersionOperation"] = objectSchema(map[string]any{
		"id": uuid, "libraryId": uuid,
		"kind":        map[string]any{"type": "string", "enum": []string{domain.VersionOpSplit, domain.VersionOpMerge, domain.VersionOpPrimary, domain.VersionOpUnexclude}},
		"itemId":      map[string]any{"type": "string", "format": "uuid", "description": "The item that stays: the original of a split, the target of a merge."},
		"otherItemId": map[string]any{"type": "string", "format": "uuid", "description": "The item a split created or a merge absorbed."},
		"sourceIds":   map[string]any{"type": "array", "items": uuid, "description": "Versions the operation moved or chose."},
		"actorId":     uuid, "createdAt": instant,
		"undoUntil":   map[string]any{"type": "string", "format": "date-time", "description": "Undo is possible until this time (30 days)."},
		"undoneAt":    instant,
		"undoable":    map[string]any{"type": "boolean"},
		"undoBlocked": map[string]any{"type": "string", "enum": []string{domain.VersionUndoBlockedUndone, domain.VersionUndoBlockedExpired, domain.VersionUndoBlockedLater}, "description": "Why undo is unavailable; later_operation: a newer split or merge of the same items must be undone first."},
	}, "id", "libraryId", "kind", "itemId", "sourceIds", "createdAt", "undoUntil", "undoable")
	schemas["VersionOverview"] = objectSchema(map[string]any{
		"itemId":          uuid,
		"primarySourceId": map[string]any{"type": "string", "format": "uuid", "description": "The administrator's main version, if any."},
		"exclusions": map[string]any{"type": "array", "items": objectSchema(map[string]any{"id": uuid, "itemId": uuid,
			"fileName": map[string]any{"type": "string", "description": "File name only; never the root or directory."}, "operationId": uuid, "createdAt": instant}, "id", "itemId", "fileName", "createdAt"),
			"description": "Files synchronisation never groups into this item again."},
		"operations": map[string]any{"type": "array", "maxItems": domain.VersionOperationsPage, "items": schemaRef("VersionOperation"), "description": "Newest first."},
	}, "itemId", "exclusions", "operations")
	schemas["SplitVersionInput"] = map[string]any{"type": "object", "additionalProperties": false, "required": []string{"sourceId"}, "properties": map[string]any{
		"sourceId": uuid,
		"exclude":  map[string]any{"type": "boolean", "description": "Also mark the file as not a version of this item, so synchronisation never groups it here again (also after it disappears and comes back)."},
		"title":    map[string]any{"type": "string", "maxLength": domain.VersionTitleMax, "description": "Title of the new item; empty keeps the original's title."},
	}}
	schemas["MergeItemsInput"] = objectSchema(map[string]any{"sourceItemId": map[string]any{"type": "string", "format": "uuid", "description": "The item absorbed into the path item and removed."}}, "sourceItemId")
	schemas["PrimaryVersionInput"] = objectSchema(map[string]any{"sourceId": nullable(map[string]any{"type": "string", "format": "uuid", "description": "A version of the item; null clears the main version."})}, "sourceId")

	type route struct {
		path, method, summary, description, body, result, status string
		admin                                                    bool
		params                                                   []any
	}
	exclusion := map[string]any{"name": "exclusionId", "in": "path", "required": true, "schema": uuid}
	itemParams := []any{idParameter()}
	routes := []route{
		{"/api/v1/items/{id}/versions", "get", "Read an item's main version, exclusions and version operations (G20.3)", "", "", "VersionOverview", "200", true, itemParams},
		{"/api/v1/items/{id}/versions/split", "post", "Split one version off into a new item",
			"Creates an item of the same kind, library and parent with the given or the original title (a manual title) and moves the version with its scan registration and version track preferences; the playback history stays with the original item. The only version cannot be split (409 version_merge_incompatible). exclude keeps catalog synchronisation from grouping the file into the original item again. Audited as item.version_split; undoable for 30 days.", "SplitVersionInput", "VersionOperation", "201", true, itemParams},
		{"/api/v1/items/{id}/versions/merge", "post", "Merge another item into this item",
			"Moves every version of sourceItemId here, with its playback history, and applies the merge policy: user data (played if either, play counts added, resume point and last version from the later play), access rules (a hide on either hides), watch statistics (added per user and day) and item track preferences (copied where none). The absorbed item is removed and its scan group points here, so its files never recreate it. Refused without override: another kind, library or series, container items, items with children (409 version_merge_incompatible); external IDs or episode numbers that disagree (409 version_identity_conflict, G20.5). 409 version_item_busy while either item is played or a job works on it. Audited as item.versions_merged; undoable for 30 days.", "MergeItemsInput", "VersionOperation", "201", true, itemParams},
		{"/api/v1/items/{id}/versions/primary", "put", "Choose or clear the main version",
			"The main version is listed first and used when a client names no version. 409 conflict when unchanged. Audited as item.primary_version_changed.", "PrimaryVersionInput", "VersionOperation", "200", true, itemParams},
		{"/api/v1/items/{id}/versions/exclusions/{exclusionId}", "delete", "Lift an exclusion",
			"Synchronisation may group the file into the item again. Audited as item.version_exclusion_removed.", "", "VersionOperation", "200", true, []any{idParameter(), exclusion}},
		{"/api/v1/version-operations/{id}/undo", "post", "Undo a version operation (G20.5)",
			"Within 30 days. A split or merge waits until every later split or merge of the same items is undone (409 version_undo_unavailable). Undoing a merge recreates the absorbed item with its own rows, moves its versions and playback history back and reverses the transfer where the target's rows were not changed since; undoing a split moves the version back, folds what the new item gathered meanwhile into the original by the merge policy and removes the new item and its exclusion. The administrator must see the item the operation kept. Audited as item.version_operation_undone.", "Empty", "VersionOperation", "200", true, itemParams},
		{"/api/v1/items/{id}/track-preferences", "get", "Read the caller's track preferences for an item (G16.5, G20.4)", "Any signed-in user; their own preferences only.", "", "TrackPreferences", "200", false, itemParams},
		{"/api/v1/items/{id}/track-preferences", "put", "Replace one level of the caller's track preferences for an item",
			"sourceId selects a version level; without it the item level. Named tracks exist on the version level only. Playback information (GET /api/v1/items/{id}/playback, compat PlaybackInfo) starts with the tracks the merged levels pick. Not audited: preferences change which original track a client starts with, nothing else.", "TrackPreferenceInput", "TrackPreferences", "200", false, itemParams},
		{"/api/v1/users/me/track-preferences", "get", "Read the caller's default track preferences", "", "", "UserTrackPreference", "200", false, nil},
		{"/api/v1/users/me/track-preferences", "put", "Replace the caller's default track preferences", "Used for every item without its own level. Named tracks are not allowed here.", "TrackPreference", "UserTrackPreference", "200", false, nil},
	}
	schemas["UserTrackPreference"] = objectSchema(map[string]any{"preference": nullable(schemaRef("TrackPreference"))}, "preference")
	for _, r := range routes {
		op := operation(r.summary, r.status, "400", "401", "403", "404", "408", "409", "413", "415", "503")
		op["security"] = bearer
		if r.description != "" {
			op["description"] = r.description
		}
		if r.admin {
			op["x-jelee-role"] = "administrator"
			op["description"] = "Administrator only; other users get 403 forbidden. An item the administrator may not see is answered 404 like a missing one. " + r.description
		}
		if r.params != nil {
			op["parameters"] = r.params
		}
		if r.body != "" {
			op["requestBody"] = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": schemaRef(r.body)}},
				"description": "Maximum 4 KiB; exactly one object; unknown or duplicate keys rejected; null only where the schema allows it."}
		}
		responses := op["responses"].(map[string]any)
		responses["404"] = map[string]any{"description": "The item, version, exclusion or operation is missing or not visible to the caller; all are answered alike."}
		responses[r.status] = map[string]any{"description": "Successful response", "content": map[string]any{"application/json": map[string]any{"schema": objectSchema(map[string]any{"data": schemaRef(r.result)}, "data")}}}
		item, ok := paths[r.path].(map[string]any)
		if !ok {
			item = map[string]any{}
			paths[r.path] = item
		}
		item[r.method] = op
	}
}
