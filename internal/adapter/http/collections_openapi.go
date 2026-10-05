package httpapi

import "github.com/MoYuanCN/Jelee/internal/domain"

// collectionsSpecification documents collections and playlists (G02.1).
func collectionsSpecification(paths, schemas map[string]any) {
	uuid := map[string]any{"type": "string", "format": "uuid"}
	instant := map[string]any{"type": "string", "format": "date-time"}
	bearer := []any{map[string]any{"bearer": []string{}}}
	name := map[string]any{"type": "string", "minLength": 1, "maxLength": domain.CollectionNameMax, "description": "Leading and trailing spaces are trimmed; must not be blank or hold control characters; at most 1024 bytes."}
	count := map[string]any{"type": "integer", "minimum": 0, "description": "Members visible to the caller only."}
	cover := map[string]any{"type": "string", "format": "uuid", "description": "The first member visible to the caller, for a poster; absent without one."}
	nfoName := map[string]any{"oneOf": []any{name, map[string]any{"type": "null"}}, "description": "Every item whose collection metadata (NFO <set> or <collection>) carries this name, trimmed and compared case-insensitively, is a member as long as it does. At most one collection may use a name (409 conflict). Null for a manual-only collection."}

	schemas["Collection"] = objectSchema(map[string]any{
		"id": uuid, "name": name, "overview": map[string]any{"type": "string", "maxLength": domain.CollectionOverviewMax},
		"nfoName": nfoName, "itemCount": count, "coverItemId": cover, "createdAt": instant, "updatedAt": instant,
	}, "id", "name", "overview", "nfoName", "itemCount", "createdAt", "updatedAt")
	member := map[string]any{}
	for key, value := range schemas["CatalogItem"].(map[string]any)["properties"].(map[string]any) {
		member[key] = value
	}
	member["manual"] = map[string]any{"type": "boolean", "description": "Added by an administrator; only manual members can be removed."}
	member["fromNfo"] = map[string]any{"type": "boolean", "description": "A member through the NFO name."}
	schemas["CollectionMember"] = objectSchema(member, "id", "libraryId", "title", "kind", "manual", "fromNfo")
	schemas["CollectionView"] = objectSchema(map[string]any{
		"collection": schemaRef("Collection"),
		"items":      map[string]any{"type": "array", "maxItems": domain.CollectionViewItemsMax, "items": schemaRef("CollectionMember"), "description": "Members visible to the caller in title order."},
		"truncated":  map[string]any{"type": "boolean", "description": "More visible members than the 2000 listed."},
	}, "collection", "items", "truncated")
	schemas["CollectionPage"] = objectSchema(map[string]any{
		"collections": map[string]any{"type": "array", "items": schemaRef("Collection")},
		"nextCursor":  map[string]any{"type": "string", "description": "Pass as cursor for the next page; empty on the last page."},
	}, "collections", "nextCursor")
	schemas["CollectionInput"] = map[string]any{"type": "object", "additionalProperties": false, "required": []string{"name"}, "properties": map[string]any{
		"name": name, "overview": map[string]any{"type": "string", "maxLength": domain.CollectionOverviewMax, "description": "Plain text; line breaks and tabs allowed; at most 16384 bytes."}, "nfoName": nfoName,
	}}
	schemas["CollectionNfoSync"] = objectSchema(map[string]any{"created": map[string]any{"type": "integer", "minimum": 0}}, "created")
	schemas["MembershipInput"] = objectSchema(map[string]any{"itemIds": map[string]any{"type": "array", "minItems": 1, "maxItems": domain.MembershipBatchMax, "items": uuid}}, "itemIds")

	schemas["Playlist"] = objectSchema(map[string]any{
		"id": uuid, "name": name, "public": map[string]any{"type": "boolean", "description": "Readable by every other user, each seeing only the entries they may see."},
		"ownerId": uuid, "ownerName": map[string]any{"type": "string", "description": "The owner's display name, else the account name."},
		"owned":     map[string]any{"type": "boolean", "description": "The caller's own playlist; only the owner changes a playlist."},
		"itemCount": count, "coverItemId": cover, "createdAt": instant, "updatedAt": instant,
	}, "id", "name", "public", "ownerId", "ownerName", "owned", "itemCount", "createdAt", "updatedAt")
	schemas["PlaylistEntry"] = objectSchema(map[string]any{"entryId": uuid, "item": schemaRef("CatalogItem")}, "entryId", "item")
	schemas["PlaylistView"] = objectSchema(map[string]any{
		"playlist": schemaRef("Playlist"),
		"entries":  map[string]any{"type": "array", "maxItems": domain.PlaylistEntriesMax, "items": schemaRef("PlaylistEntry"), "description": "Entries visible to the caller in playlist order; an item may repeat."},
	}, "playlist", "entries")
	schemas["PlaylistPage"] = objectSchema(map[string]any{
		"playlists":  map[string]any{"type": "array", "items": schemaRef("Playlist")},
		"nextCursor": map[string]any{"type": "string", "description": "Pass as cursor for the next page; empty on the last page."},
	}, "playlists", "nextCursor")
	schemas["PlaylistInput"] = map[string]any{"type": "object", "additionalProperties": false, "required": []string{"name"}, "properties": map[string]any{
		"name": name, "public": map[string]any{"type": "boolean", "description": "Absent means private."},
	}}
	schemas["PlaylistMoveInput"] = objectSchema(map[string]any{"beforeEntryId": map[string]any{"oneOf": []any{uuid, map[string]any{"type": "null"}}, "description": "The entry to move in front of; null moves the entry to the end."}}, "beforeEntryId")

	page := []any{
		map[string]any{"name": "cursor", "in": "query", "schema": uuid, "description": "nextCursor of the previous page."},
		map[string]any{"name": "limit", "in": "query", "schema": map[string]any{"type": "integer", "minimum": 1, "maximum": domain.CollectionPageMax, "default": collectionPageDefault}},
	}
	itemParam := map[string]any{"name": "itemId", "in": "path", "required": true, "schema": uuid}
	entryParam := map[string]any{"name": "entryId", "in": "path", "required": true, "schema": uuid}
	type route struct {
		path, method, summary, description, body, result, status string
		admin                                                    bool
		params                                                   []any
	}
	id := []any{idParameter()}
	routes := []route{
		{"/api/v1/collections", "get", "List collections (G02.1)", "In name order. Administrators get every collection; other users only collections with at least one member they may see. Counts and covers include visible members only.", "", "CollectionPage", "200", false, page},
		{"/api/v1/collections", "post", "Create a collection", "At most 10000 collections (409 conflict). Audited as collection.created.", "CollectionInput", "CollectionView", "201", true, nil},
		{"/api/v1/collections/nfo-sync", "post", "Create collections from NFO collection names",
			"Creates an NFO-linked collection for every collection name found in item metadata (NFO <set> or <collection>) that no collection uses yet. New items carrying a used name join that collection without a sync. Audited as collection.nfo_synced when anything was created.", "Empty", "CollectionNfoSync", "200", true, nil},
		{"/api/v1/collections/{id}", "get", "Read a collection with its visible members", "A collection without a member visible to a non-administrator caller is answered like a missing one.", "", "CollectionView", "200", false, id},
		{"/api/v1/collections/{id}", "put", "Rename a collection or change its overview or NFO name", "Audited as collection.updated.", "CollectionInput", "CollectionView", "200", true, id},
		{"/api/v1/collections/{id}", "delete", "Delete a collection", "Its items stay. Audited as collection.deleted.", "", "", "204", true, id},
		{"/api/v1/collections/{id}/items", "post", "Add items to a collection",
			"Movies, series and home videos the administrator may see; an invisible or missing item fails the whole request (404), another kind 400. Items already members stay. At most 10000 manual members (409 conflict). Audited as collection.items_added.", "MembershipInput", "CollectionView", "200", true, id},
		{"/api/v1/collections/{id}/items/{itemId}", "delete", "Remove a manual member", "A member only through the NFO name is 409 conflict: change the item's collection metadata or the collection's NFO name instead. Audited as collection.item_removed.", "", "CollectionView", "200", true, []any{idParameter(), itemParam}},
		{"/api/v1/playlists", "get", "List playlists (G02.1)", "The caller's own playlists and other users' public playlists with at least one entry the caller may see, in name order. Counts and covers include visible entries only.", "", "PlaylistPage", "200", false, page},
		{"/api/v1/playlists", "post", "Create a playlist of the caller", "At most 1000 playlists per user (409 conflict). Not audited: playlists are the user's own data and are deleted with the account.", "PlaylistInput", "PlaylistView", "201", false, nil},
		{"/api/v1/playlists/{id}", "get", "Read a playlist with its visible entries", "The caller's own playlist, or another user's public playlist with a visible entry; anything else is answered like a missing playlist.", "", "PlaylistView", "200", false, id},
		{"/api/v1/playlists/{id}", "put", "Rename a playlist or change whether it is public", "Owner only: another user's readable playlist is 403 forbidden, any other 404.", "PlaylistInput", "PlaylistView", "200", false, id},
		{"/api/v1/playlists/{id}", "delete", "Delete a playlist", "Owner only, like PUT.", "", "", "204", false, id},
		{"/api/v1/playlists/{id}/items", "post", "Append items to a playlist",
			"Owner only. Movies, episodes and home videos the caller may see, appended in the given order, repeats included; an invisible or missing item fails the whole request (404), another kind 400. At most 5000 entries (409 conflict). Concurrent changes of one playlist apply one after another.", "MembershipInput", "PlaylistView", "200", false, id},
		{"/api/v1/playlists/{id}/entries/{entryId}", "delete", "Remove one entry of a playlist", "Owner only.", "", "PlaylistView", "200", false, []any{idParameter(), entryParam}},
		{"/api/v1/playlists/{id}/entries/{entryId}/move", "post", "Move one entry of a playlist", "Owner only. Moves the entry in front of beforeEntryId, or to the end; the order of every other entry, visible or not, is kept.", "PlaylistMoveInput", "PlaylistView", "200", false, []any{idParameter(), entryParam}},
	}
	for _, r := range routes {
		op := operation(r.summary, r.status, "400", "401", "403", "404", "408", "409", "413", "415", "503")
		op["security"] = bearer
		op["description"] = r.description
		if r.admin {
			op["x-jelee-role"] = "administrator"
			op["description"] = "Administrator only; other users get 403 forbidden. " + r.description
		}
		if r.params != nil {
			op["parameters"] = r.params
		}
		if r.body != "" {
			op["requestBody"] = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": schemaRef(r.body)}},
				"description": "Maximum 24 KiB; exactly one object; unknown or duplicate keys rejected; null only where the schema allows it."}
		}
		responses := op["responses"].(map[string]any)
		responses["404"] = map[string]any{"description": "The collection, playlist, entry or item is missing or not visible to the caller; all are answered alike."}
		if r.result != "" {
			responses[r.status] = map[string]any{"description": "Successful response", "content": map[string]any{"application/json": map[string]any{"schema": objectSchema(map[string]any{"data": schemaRef(r.result)}, "data")}}}
		} else {
			responses[r.status] = map[string]any{"description": "Deleted"}
		}
		item, ok := paths[r.path].(map[string]any)
		if !ok {
			item = map[string]any{}
			paths[r.path] = item
		}
		item[r.method] = op
	}
}
