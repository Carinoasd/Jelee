package httpapi

import (
	"strconv"
	"strings"
)

func schemaRef(name string) map[string]any {
	return map[string]any{"$ref": "#/components/schemas/" + name}
}
func stringSchema(max int) map[string]any {
	return map[string]any{"type": "string", "maxLength": max, "description": "The server also enforces the stated maximum in UTF-8 bytes."}
}
func objectSchema(properties map[string]any, required ...string) map[string]any {
	result := map[string]any{"type": "object", "additionalProperties": false, "properties": properties}
	if len(required) > 0 {
		result["required"] = required
	}
	return result
}

// nativeLabel is a client-reported string: UTF-8, no control characters.
func nativeLabel(max int, description string) map[string]any {
	return map[string]any{"type": "string", "maxLength": max, "description": description + " At most " + strconv.Itoa(max) + " UTF-8 bytes; control characters are rejected."}
}
func accountSchemas() map[string]any {
	uuid := map[string]any{"type": "string", "format": "uuid"}
	boolean := map[string]any{"type": "boolean"}
	instant := map[string]any{"type": "string", "format": "date-time"}
	locale := map[string]any{"type": "string", "enum": []string{"zh-CN", "zh-TW", "ja-JP", "en-US"}}
	password := map[string]any{"type": "string", "maxLength": 1024, "writeOnly": true, "description": "Unmodified UTF-8. New passwords must contain 12–1024 bytes; passwords are never trimmed or normalized."}
	settings := map[string]any{"name": stringSchema(128), "displayName": stringSchema(128), "locale": locale, "hidden": boolean, "admin": boolean, "disabled": boolean}
	create := map[string]any{}
	for k, v := range settings {
		create[k] = v
	}
	create["password"] = password
	itemAccessEffect := map[string]any{"type": "string", "enum": []string{"allow", "hide"}, "description": "hide hides the item and its descendants; allow shows them despite blocked tags and the rating ceiling, within the library grants. The nearest rule on the item or an ancestor wins."}
	csrf := map[string]any{"type": "string", "minLength": 43, "maxLength": 43, "description": "Present for web sessions. Send it as the X-Jelee-CSRF header on every POST, PUT, PATCH or DELETE authenticated by the session cookie; it changes when the session is rotated."}
	return map[string]any{
		"Login":        objectSchema(map[string]any{"name": stringSchema(128), "password": password, "deviceName": stringSchema(128)}, "name", "password"),
		"Rotate":       objectSchema(map[string]any{"deviceName": stringSchema(128)}),
		"Empty":        objectSchema(map[string]any{}),
		"CreateUser":   objectSchema(create, "name", "password"),
		"UserSettings": objectSchema(settings, "name", "locale"),
		"Profile":      objectSchema(map[string]any{"displayName": stringSchema(128), "locale": locale, "hidden": boolean}, "locale"),
		"UserPreferences": objectSchema(map[string]any{
			"theme":   map[string]any{"type": "string", "enum": []string{"system", "light", "dark"}, "description": "system follows the browser's color scheme."},
			"density": map[string]any{"type": "string", "enum": []string{"comfortable", "compact"}, "description": "Reserved layout density of the web client."},
			"layout":  map[string]any{"oneOf": []any{schemaRef("UserLayout"), map[string]any{"type": "null"}}, "description": "Page layout and saved presets (G33.5); null until customized, when the site default layout applies. Required on replacement like every field."},
		}, "theme", "density", "layout"),
		"PasswordChange": objectSchema(map[string]any{"oldPassword": password, "newPassword": password}, "oldPassword", "newPassword"),
		"LibraryAccess":  objectSchema(map[string]any{"libraryIds": map[string]any{"type": "array", "items": uuid, "maxItems": 1000, "uniqueItems": true}}, "libraryIds"),
		"User":           objectSchema(map[string]any{"id": uuid, "name": stringSchema(128), "displayName": stringSchema(128), "locale": locale, "hidden": boolean, "admin": boolean, "disabled": boolean, "allowNative": map[string]any{"type": "boolean", "description": "Whether POST /api/v1/auth/login/native may issue native sessions to this user. Changed only through PUT /api/v1/users/{id}/native."}, "createdAt": instant, "deletedAt": instant}, "id", "name", "displayName", "locale", "hidden", "admin", "disabled", "allowNative", "createdAt"),
		"Session": objectSchema(map[string]any{"id": uuid, "userId": uuid, "clientKind": map[string]any{"type": "string", "enum": []string{"web", "native"}}, "deviceName": stringSchema(128),
			"client": nativeLabel(128, "Client application name reported at native login."), "deviceId": nativeLabel(256, "Device identifier reported at native login; a label, not a device proof."), "version": nativeLabel(64, "Client application version reported at native login."),
			"createdAt": instant, "expiresAt": instant, "revokedAt": instant,
			"lastSeenAt": map[string]any{"type": "string", "format": "date-time", "description": "Last authenticated use, recorded at most once per 60 seconds."},
			"lastIp":     map[string]any{"type": "string", "maxLength": 45, "description": "Client address of the last recorded use, as determined by the trusted proxy settings."},
		}, "id", "userId", "clientKind", "deviceName", "createdAt", "expiresAt"),
		"NativeLogin": objectSchema(map[string]any{"name": stringSchema(128), "password": password,
			"client": nativeLabel(128, "Required client application name; no surrounding spaces."), "device": nativeLabel(128, "Optional device name shown in session lists."),
			"deviceId": nativeLabel(256, "Required stable device identifier; no surrounding spaces."), "version": nativeLabel(64, "Optional client application version."),
		}, "name", "password", "client", "deviceId"),
		"NativeAccess": objectSchema(map[string]any{"allowNative": boolean}, "allowNative"),
		"DeliveryLimits": objectSchema(map[string]any{
			"maxStreams": map[string]any{"type": "integer", "minimum": 0, "maximum": 128, "description": "Distinct simultaneous playbacks of the user. Omitted follows the server-wide streaming.maxStreamsPerUser; 0 exempts the user. Applies only while streaming.enableStreamLimit is on."},
			"maxKbps":    map[string]any{"type": "integer", "minimum": 0, "maximum": 10000000, "description": "Bandwidth in kilobits per second shared by the user's streams. Omitted follows the server-wide streaming.maxKbpsPerUser; 0 exempts the user. Applies only while streaming.enableBandwidthLimit is on."},
		}),
		"SessionPage":   objectSchema(map[string]any{"sessions": map[string]any{"type": "array", "maxItems": 100, "items": schemaRef("Session")}, "pagination": objectSchema(map[string]any{"nextCursor": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100}}, "nextCursor", "limit")}, "sessions", "pagination"),
		"SessionGrant":  objectSchema(map[string]any{"user": schemaRef("User"), "session": schemaRef("Session"), "token": map[string]any{"type": "string", "minLength": 43, "maxLength": 43, "description": "One-time returned opaque bearer credential. Store securely; never log."}, "csrf": csrf}, "user", "session", "token"),
		"CSRFToken":     objectSchema(map[string]any{"csrf": csrf}, "csrf"),
		"LibraryGrant":  objectSchema(map[string]any{"libraryId": uuid, "name": map[string]any{"type": "string"}}, "libraryId", "name"),
		"ContentAccess": objectSchema(contentAccessProperties(), "blockedTags"),
		"ContentAccessView": objectSchema(func() map[string]any {
			p := contentAccessProperties()
			p["rules"] = map[string]any{"type": "array", "maxItems": 1000, "items": schemaRef("ItemAccessRule"), "description": "Explicit item rules in creation order."}
			p["windows"] = map[string]any{"type": "array", "maxItems": 20, "items": schemaRef("AccessWindow"), "description": "Restricted time windows in order."}
			return p
		}(), "blockedTags", "blockedKeywords", "rules", "windows"),
		"AccessWindow": objectSchema(map[string]any{
			"weekdays":  map[string]any{"type": "array", "maxItems": 7, "uniqueItems": true, "items": map[string]any{"type": "integer", "minimum": 0, "maximum": 6}, "description": "Days the window opens, 0 is Sunday; empty or omitted means every day."},
			"start":     map[string]any{"type": "string", "pattern": "^([01][0-9]|2[0-3]):[0-5][0-9]$", "description": "Wall-clock start, HH:MM."},
			"end":       map[string]any{"type": "string", "pattern": "^(([01][0-9]|2[0-3]):[0-5][0-9]|24:00)$", "description": "Wall-clock end, HH:MM or 24:00; at or before start crosses midnight and belongs to the day the window opened. Must differ from start."},
			"timeZone":  map[string]any{"type": "string", "minLength": 1, "maxLength": 64, "description": "IANA time zone the times are read in, such as Asia/Taipei; Local is refused. Must be known to both the server and PostgreSQL."},
			"ratingMax": map[string]any{"type": "integer", "minimum": 0, "maximum": 21, "description": "Rating ceiling inside the window, combined with the user's own ceiling (the lower wins); omitted hides every item inside the window."},
		}, "start", "end", "timeZone"),
		"AccessWindowsInput":   objectSchema(map[string]any{"windows": map[string]any{"type": "array", "maxItems": 20, "items": schemaRef("AccessWindow")}}, "windows"),
		"ParentalRatingsInput": objectSchema(map[string]any{"ratings": map[string]any{"type": "array", "maxItems": 500, "items": schemaRef("ParentalRating")}}, "ratings"),
		"AccessGrantMatrix": objectSchema(map[string]any{
			"libraries": map[string]any{"type": "array", "maxItems": 1000, "items": schemaRef("LibraryGrant")},
			"users": map[string]any{"type": "array", "maxItems": 1000, "items": objectSchema(map[string]any{"id": uuid, "name": map[string]any{"type": "string"}, "displayName": map[string]any{"type": "string"},
				"admin": boolean, "disabled": boolean, "libraryIds": map[string]any{"type": "array", "maxItems": 1000, "items": uuid}}, "id", "name", "admin", "disabled", "libraryIds")},
			"truncated": boolean,
		}, "libraries", "users", "truncated"),
		"AccessGrantBulk": objectSchema(map[string]any{
			"operations": map[string]any{"type": "array", "minItems": 1, "maxItems": 200, "items": objectSchema(map[string]any{
				"action":     map[string]any{"type": "string", "enum": []string{"add", "remove"}},
				"userIds":    map[string]any{"type": "array", "minItems": 1, "maxItems": 100, "uniqueItems": true, "items": uuid},
				"libraryIds": map[string]any{"type": "array", "minItems": 1, "maxItems": 1000, "uniqueItems": true, "items": uuid},
			}, "action", "userIds", "libraryIds")},
			"preview": map[string]any{"type": "boolean", "description": "true only reports what the change would do; false applies it."},
		}, "operations", "preview"),
		"AccessTemplateApply": objectSchema(map[string]any{
			"userIds": map[string]any{"type": "array", "minItems": 1, "maxItems": 100, "uniqueItems": true, "items": uuid},
			"preview": map[string]any{"type": "boolean", "description": "true only reports what the application would do; false applies it."},
		}, "userIds", "preview"),
		"AccessTemplateInput": objectSchema(func() map[string]any {
			p := contentAccessProperties()
			p["name"] = map[string]any{"type": "string", "minLength": 1, "maxLength": 64, "description": "Unique case-insensitively; no surrounding spaces."}
			p["libraryIds"] = map[string]any{"type": "array", "maxItems": 1000, "uniqueItems": true, "items": uuid, "description": "The libraries a user receives, replacing the user's grants."}
			return p
		}(), "name", "libraryIds", "blockedTags"),
		"AccessTemplate": objectSchema(func() map[string]any {
			p := contentAccessProperties()
			p["id"], p["createdAt"], p["updatedAt"] = uuid, instant, instant
			p["name"] = map[string]any{"type": "string"}
			p["libraryIds"] = map[string]any{"type": "array", "maxItems": 1000, "items": uuid}
			return p
		}(), "id", "name", "libraryIds", "blockedTags", "blockedKeywords", "createdAt", "updatedAt"),
		"AccessChangePreview": objectSchema(map[string]any{
			"applied": map[string]any{"type": "boolean", "description": "false for a preview, true once written and audited."},
			"users":   map[string]any{"type": "integer", "minimum": 0, "description": "Users whose grants or restrictions change."},
			"items":   map[string]any{"type": "integer", "minimum": 0, "description": "Distinct items whose visibility changes for at least one user."},
			"shown":   map[string]any{"type": "integer", "minimum": 0, "description": "User × item pairs that become visible."},
			"hidden":  map[string]any{"type": "integer", "minimum": 0, "description": "User × item pairs that become hidden."},
			"changes": map[string]any{"type": "array", "maxItems": 100, "items": objectSchema(map[string]any{
				"userId": uuid, "name": map[string]any{"type": "string"},
				"addedLibraryIds": map[string]any{"type": "array", "items": uuid}, "removedLibraryIds": map[string]any{"type": "array", "items": uuid},
				"restrictionsChanged": boolean,
				"shown":               map[string]any{"type": "integer", "minimum": 0}, "hidden": map[string]any{"type": "integer", "minimum": 0},
			}, "userId", "name", "addedLibraryIds", "removedLibraryIds", "restrictionsChanged", "shown", "hidden"), "description": "Users with a change, by name."},
		}, "applied", "users", "items", "shown", "hidden", "changes"),
		"ItemAccessRuleInput": objectSchema(map[string]any{"effect": itemAccessEffect}, "effect"),
		"ItemAccessRule": objectSchema(map[string]any{"itemId": uuid, "libraryId": uuid, "kind": map[string]any{"type": "string"}, "title": map[string]any{"type": "string"},
			"effect": itemAccessEffect, "createdAt": instant}, "itemId", "libraryId", "kind", "title", "effect", "createdAt"),
		"AccessPolicy": objectSchema(map[string]any{
			"restrictAdmins": map[string]any{"type": "boolean", "description": "Apply rating ceilings, blocked tags and item rules to administrators too. Library grants never restrict administrators."},
			"blockUnrated":   map[string]any{"type": "boolean", "description": "Default for items without a recognized rating, for users with a ceiling whose own blockUnrated is null."},
		}, "restrictAdmins", "blockUnrated"),
		"ParentalRating": objectSchema(map[string]any{"code": map[string]any{"type": "string", "maxLength": 32}, "level": map[string]any{"type": "integer", "minimum": 0, "maximum": 21}}, "code", "level"),
		"UserPage":       objectSchema(map[string]any{"users": map[string]any{"type": "array", "maxItems": 100, "items": schemaRef("User")}, "pagination": objectSchema(map[string]any{"nextCursor": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100}}, "nextCursor", "limit")}, "users", "pagination"),
	}
}

func accountSpecification(paths map[string]any) {
	type route struct {
		path, method, summary, body, result string
		status                              string
		admin                               bool
	}
	routes := []route{
		{"/auth/login", "post", "Password login; issues a web session and sets the HttpOnly __Host-jelee_session cookie", "Login", "SessionGrant", "200", false},
		{"/auth/login/native", "post", "Password login for an installed native client; issues a playable native session only when an administrator allowed the user native devices (otherwise 403 native_login_disabled after the password is verified). Returns the token in the body only: no cookie, no CSRF token. Requests carrying Origin, Sec-Fetch-Site or Sec-Fetch-Mode, which only browsers send, are refused with 403 forbidden. Shares the login rate limit, lockout and audit with /auth/login", "NativeLogin", "SessionGrant", "200", false},
		{"/auth/logout", "post", "Revoke the current session and expire its browser cookie", "Empty", "", "204", false},
		{"/auth/rotate", "post", "Atomically replace the current token and preserve its client kind; a web session's cookie and CSRF token move to the replacement", "Rotate", "SessionGrant", "200", false},
		{"/auth/csrf", "get", "Read the CSRF token of the authenticating session, for example after a page reload", "", "CSRFToken", "200", false},
		{"/users/me", "get", "Read own account", "", "User", "200", false},
		{"/users/me/profile", "put", "Replace own profile fields; omitted optional fields reset", "Profile", "User", "200", false},
		{"/users/me/preferences", "get", "Read own interface preferences (G33.3, G33.5); until first saved: the site default theme, comfortable density and no layout", "", "UserPreferences", "200", false},
		{"/users/me/preferences", "put", "Replace own interface preferences; every field is required; presentation only, not audited", "UserPreferences", "UserPreferences", "200", false},
		{"/users/me/password", "put", "Verify old password, replace password and revoke all sessions. A wrong old password is 400 invalid_password (the session stays valid, unlike 401); every attempt first draws from the per-user and per-address password-change budget (429 auth_rate_limited)", "PasswordChange", "", "204", false},
		{"/users", "get", "List accounts, including hidden accounts; cursor pagination", "", "UserPage", "200", true},
		{"/users", "post", "Create account; same actor/key replays original result even if payload differs", "CreateUser", "User", "201", true},
		{"/users/{id}", "get", "Read own account or any account as administrator", "", "User", "200", false},
		{"/users/{id}", "put", "Replace account settings; omitted optional fields reset", "UserSettings", "User", "200", true},
		{"/users/{id}", "delete", "Soft delete account and revoke sessions; request body must be empty", "", "", "204", true},
		{"/users/{id}/restore", "post", "Restore a soft-deleted account; old sessions stay revoked", "Empty", "User", "200", true},
		{"/users/{id}/unlock", "post", "Reset login failure count and lock", "Empty", "", "204", true},
		{"/users/{id}/delivery-limits", "get", "Read a user's direct delivery overrides; omitted fields follow the server-wide limits", "", "DeliveryLimits", "200", true},
		{"/users/{id}/delivery-limits", "put", "Replace a user's direct delivery overrides; omitted fields follow the server-wide limits; new values apply to streams started afterwards; audited", "DeliveryLimits", "DeliveryLimits", "200", true},
		{"/users/{id}/native", "put", "Allow or withdraw native-device login for a user; withdrawing revokes the user's active native sessions; audited", "NativeAccess", "User", "200", true},
		{"/sessions", "get", "List active sessions of all users with client, device and last use; cursor pagination by session ID", "", "SessionPage", "200", true},
		{"/users/{id}/sessions", "get", "List active sessions with client, device and last use for self or as administrator; over 1000 returns conflict", "", "Session[]", "200", false},
		{"/users/{id}/sessions", "delete", "Revoke all target sessions; self or administrator; empty body", "", "", "204", false},
		{"/users/{id}/sessions/{sessionID}", "delete", "Revoke target session; self or administrator; empty body", "", "", "204", false},
		{"/users/{id}/libraries", "get", "Read explicit library grants for self or as administrator; max 1000", "", "LibraryGrant[]", "200", false},
		{"/users/{id}/libraries", "put", "Atomically replace explicit library grants; empty array removes grants", "LibraryAccess", "", "204", true},
		{"/users/{id}/content-access", "get", "Read a user's rating ceiling, unrated override, blocked tags and item rules", "", "ContentAccessView", "200", true},
		{"/users/{id}/content-access", "put", "Replace a user's rating ceiling, unrated override, blocked tags and blocked keywords; item rules and time windows are kept; applies to the user's next request; audited as user.content_access_changed unless unchanged", "ContentAccess", "ContentAccessView", "200", true},
		{"/users/{id}/content-access/windows", "put", "Replace a user's restricted time windows (at most 20; an empty array removes them). While the request time falls into a window, read in the window's IANA time zone, the window caps the rating ceiling (ratingMax) or, without ratingMax, hides every item; item allow rules do not lift it. Applies to the user's next request; audited as user.access_windows_changed unless unchanged", "AccessWindowsInput", "ContentAccessView", "200", true},
		{"/users/{id}/content-access/items/{itemId}", "put", "Create or replace a user's rule on an item and its descendants; at most 1000 rules per user (409 conflict); audited as user.item_access_rule_set unless unchanged", "ItemAccessRuleInput", "ItemAccessRule", "200", true},
		{"/users/{id}/content-access/items/{itemId}", "delete", "Remove a user's rule on an item; 404 when there is none; audited as user.item_access_rule_removed; empty body", "", "", "204", true},
		{"/access/policy", "get", "Read the server-wide content access policy", "", "AccessPolicy", "200", true},
		{"/access/policy", "put", "Replace the server-wide content access policy; audited as access.policy_changed unless unchanged", "AccessPolicy", "AccessPolicy", "200", true},
		{"/access/parental-ratings", "get", "List the recognized parental rating codes and the level (minimum age) each stands for; a bare age such as 16 or 16+ is also recognized", "", "ParentalRating[]", "200", true},
		{"/access/parental-ratings", "put", "Replace the rating code table (at most 500 codes). Codes are stored trimmed and upper case; a code with a leading \"Rated \" or a two-letter country prefix (which item values lose before the lookup) and two codes that normalize alike are 400 invalid_request. Applies to the next request; audited as access.rating_codes_changed unless unchanged", "ParentalRatingsInput", "ParentalRating[]", "200", true},
		{"/access/library-grants", "get", "Read the user × library grant matrix: every library and every live account except share guests with its granted library IDs (at most 1000 users; truncated says more exist). Administrators see every library whatever their grants", "", "AccessGrantMatrix", "200", true},
		{"/access/library-grants/bulk", "post", "Add or remove library grants of many users in one transaction, operations in order (at most 200 operations and 100 distinct users). With preview true nothing is written; the result counts the users whose grants change and the items that become visible or hidden, judged by the unified filter at the request time without the request's network restriction. Applied, changed users are audited as user.library_access_replaced and the change as access.grants_bulk_applied with the same counts. A missing user, share guest or library is 404", "AccessGrantBulk", "AccessChangePreview", "200", true},
		{"/access/templates", "get", "List the access templates by name", "", "AccessTemplate[]", "200", true},
		{"/access/templates", "post", "Create an access template: libraries and restrictions applied together. At most 100 templates; a name taken case-insensitively is 409 conflict; audited as access.template_created", "AccessTemplateInput", "AccessTemplate", "201", true},
		{"/access/templates/{id}", "put", "Replace an access template; users it was applied to keep what they received; audited as access.template_updated unless unchanged", "AccessTemplateInput", "AccessTemplate", "200", true},
		{"/access/templates/{id}", "delete", "Delete an access template; users it was applied to keep their settings; audited as access.template_deleted; empty body", "", "", "204", true},
		{"/access/templates/{id}/apply", "post", "Give each user (at most 100) exactly the template's libraries, rating ceiling, unrated override, blocked tags and keywords; item rules and time windows are kept. With preview true nothing is written and the result reports what would change. Applied, changed users are audited as user.library_access_replaced and user.content_access_changed and the application as access.template_applied with the counts", "AccessTemplateApply", "AccessChangePreview", "200", true},
	}
	for _, route := range routes {
		path := "/api/v1" + route.path
		op := operation(route.summary, route.status, "400", "401", "403", "404", "409", "413", "415", "429", "503")
		if route.path != "/auth/login" && route.path != "/auth/login/native" {
			op["security"] = []any{map[string]any{"bearer": []string{}}}
		}
		if route.admin {
			op["x-jelee-role"] = "administrator"
		}
		params := []any{}
		if strings.Contains(path, "{id}") {
			params = append(params, idParameter())
		}
		if strings.Contains(path, "{itemId}") {
			p := idParameter()
			p["name"] = "itemId"
			params = append(params, p)
		}
		if strings.Contains(path, "{sessionID}") {
			p := idParameter()
			p["name"] = "sessionID"
			params = append(params, p)
		}
		if route.path == "/sessions" {
			params = append(params, map[string]any{"name": "cursor", "in": "query", "schema": map[string]any{"type": "string", "format": "uuid"}}, map[string]any{"name": "limit", "in": "query", "schema": map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "default": 50}})
		}
		if route.path == "/users" && route.method == "get" {
			params = append(params, map[string]any{"name": "cursor", "in": "query", "schema": map[string]any{"type": "string", "format": "uuid"}}, map[string]any{"name": "limit", "in": "query", "schema": map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "default": 50}}, map[string]any{"name": "includeDeleted", "in": "query", "schema": map[string]any{"type": "boolean", "default": false}})
		}
		if route.path == "/users" && route.method == "post" {
			params = append(params, map[string]any{"name": "Idempotency-Key", "in": "header", "required": true, "schema": map[string]any{"type": "string", "pattern": "^[!-~]{1,128}$"}})
			op["responses"].(map[string]any)["200"] = map[string]any{"description": "Original result replayed; Idempotency-Replayed: true"}
		}
		if len(params) > 0 {
			op["parameters"] = params
		}
		if route.body != "" {
			limit := "Maximum 64 KiB"
			if route.path == "/access/library-grants/bulk" {
				limit = "Maximum 1 MiB"
			}
			op["requestBody"] = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": schemaRef(route.body)}}, "description": limit + "; exactly one object; unknown or duplicate keys rejected."}
		}
		if route.result != "" {
			data := schemaRef(route.result)
			if strings.HasSuffix(route.result, "[]") {
				data = map[string]any{"type": "array", "maxItems": 1000, "items": schemaRef(strings.TrimSuffix(route.result, "[]"))}
			}
			response := map[string]any{"description": "Successful response; Cache-Control: no-store", "content": map[string]any{"application/json": map[string]any{"schema": objectSchema(map[string]any{"data": data}, "data")}}}
			op["responses"].(map[string]any)[route.status] = response
			if route.path == "/users" && route.method == "post" {
				op["responses"].(map[string]any)["200"] = response
			}
		}
		item, ok := paths[path].(map[string]any)
		if !ok {
			item = map[string]any{}
			paths[path] = item
		}
		item[route.method] = op
	}
}

// contentAccessProperties are the replaceable per-user content restrictions.
func contentAccessProperties() map[string]any {
	return map[string]any{
		"parentalRatingMax": map[string]any{"type": "integer", "minimum": 0, "maximum": 21, "description": "Highest allowed rating level, a minimum age; omitted means no ceiling. See GET /api/v1/access/parental-ratings."},
		"blockUnrated":      map[string]any{"type": "boolean", "description": "Under a ceiling, whether items without a recognized rating are hidden; omitted follows the server-wide policy."},
		"blockedTags": map[string]any{"type": "array", "maxItems": 100, "items": map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
			"description": "Tags and genres that hide an item when the item or an ancestor carries one; compared trimmed and case-insensitively; empty array removes the blocks."},
		"blockedKeywords": map[string]any{"type": "array", "maxItems": 100, "items": map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
			"description": "Keywords that hide an item when its title, metadata title or original title, or an ancestor's, contains one; compared NFKC-normalized (full and half width fold together), trimmed and case-insensitively; overviews are not searched. Omitted or empty means none."},
	}
}
