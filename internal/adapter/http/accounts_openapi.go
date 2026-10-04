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
	csrf := map[string]any{"type": "string", "minLength": 43, "maxLength": 43, "description": "Present for web sessions. Send it as the X-Jelee-CSRF header on every POST, PUT, PATCH or DELETE authenticated by the session cookie; it changes when the session is rotated."}
	return map[string]any{
		"Login":          objectSchema(map[string]any{"name": stringSchema(128), "password": password, "deviceName": stringSchema(128)}, "name", "password"),
		"Rotate":         objectSchema(map[string]any{"deviceName": stringSchema(128)}),
		"Empty":          objectSchema(map[string]any{}),
		"CreateUser":     objectSchema(create, "name", "password"),
		"UserSettings":   objectSchema(settings, "name", "locale"),
		"Profile":        objectSchema(map[string]any{"displayName": stringSchema(128), "locale": locale, "hidden": boolean}, "locale"),
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
		"SessionPage":  objectSchema(map[string]any{"sessions": map[string]any{"type": "array", "maxItems": 100, "items": schemaRef("Session")}, "pagination": objectSchema(map[string]any{"nextCursor": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100}}, "nextCursor", "limit")}, "sessions", "pagination"),
		"SessionGrant": objectSchema(map[string]any{"user": schemaRef("User"), "session": schemaRef("Session"), "token": map[string]any{"type": "string", "minLength": 43, "maxLength": 43, "description": "One-time returned opaque bearer credential. Store securely; never log."}, "csrf": csrf}, "user", "session", "token"),
		"CSRFToken":    objectSchema(map[string]any{"csrf": csrf}, "csrf"),
		"LibraryGrant": objectSchema(map[string]any{"libraryId": uuid, "name": map[string]any{"type": "string"}}, "libraryId", "name"),
		"UserPage":     objectSchema(map[string]any{"users": map[string]any{"type": "array", "maxItems": 100, "items": schemaRef("User")}, "pagination": objectSchema(map[string]any{"nextCursor": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100}}, "nextCursor", "limit")}, "users", "pagination"),
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
		{"/users/me/password", "put", "Verify old password, replace password and revoke all sessions", "PasswordChange", "", "204", false},
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
			op["requestBody"] = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": schemaRef(route.body)}}, "description": "Maximum 64 KiB; exactly one object; unknown or duplicate keys rejected."}
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
