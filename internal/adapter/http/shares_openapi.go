package httpapi

import "strings"

// shareSpecification documents share links, guest sessions (G48.6) and
// library network rules (G48.5). See docs/access-control.md.
func shareSpecification(paths, schemas map[string]any) {
	uuid := map[string]any{"type": "string", "format": "uuid"}
	instant := map[string]any{"type": "string", "format": "date-time"}
	boolean := map[string]any{"type": "boolean"}
	enum := func(description string, values ...string) map[string]any {
		return map[string]any{"type": "string", "enum": values, "description": description}
	}
	streams := map[string]any{"type": "integer", "minimum": 1, "maximum": 16, "description": "Concurrent direct playbacks of all the share's guests together; applies whatever the server-wide stream limit switch."}
	note := map[string]any{"type": "string", "maxLength": 512}
	schemas["ShareInput"] = objectSchema(map[string]any{
		"libraryId":     map[string]any{"type": "string", "format": "uuid", "description": "Share a whole library; exactly one of libraryId and itemId."},
		"itemId":        map[string]any{"type": "string", "format": "uuid", "description": "Share an item and its descendants (a series with its seasons and episodes); exactly one of libraryId and itemId."},
		"expiresAt":     map[string]any{"type": "string", "format": "date-time", "description": "At least 5 minutes and at most 90 days ahead."},
		"readOnly":      map[string]any{"type": "boolean", "description": "Refuse every write of the guests (playback reports, played marks) with 403 share_read_only."},
		"allowPlayback": map[string]any{"type": "boolean", "description": "Let the link issue native guest sessions (POST /api/v1/shares/redeem/native), which may direct play the original files under the delivery rules. Web guest sessions never play; nothing is transcoded."},
		"maxStreams":    streams,
		"note":          note,
	}, "expiresAt", "readOnly", "allowPlayback")
	share := map[string]any{
		"id": uuid, "libraryId": uuid, "libraryName": map[string]any{"type": "string"}, "itemId": uuid, "itemTitle": map[string]any{"type": "string"}, "itemKind": map[string]any{"type": "string"},
		"expiresAt": instant, "readOnly": boolean, "allowPlayback": boolean, "maxStreams": streams, "note": note, "createdBy": uuid, "createdAt": instant, "revokedAt": instant,
		"state":          enum("active, expired or revoked.", "active", "expired", "revoked"),
		"activeSessions": map[string]any{"type": "integer", "minimum": 0, "description": "Live guest sessions; 0 once the share is no longer active."},
		"lastUsedAt":     map[string]any{"type": "string", "format": "date-time", "description": "Latest recorded use of a guest session, recorded at most once per 60 seconds."},
	}
	schemas["Share"] = objectSchema(share, "id", "libraryId", "libraryName", "expiresAt", "readOnly", "allowPlayback", "maxStreams", "note", "createdAt", "state", "activeSessions")
	schemas["ShareGrant"] = objectSchema(map[string]any{"share": schemaRef("Share"), "token": map[string]any{"type": "string", "minLength": 43, "maxLength": 43,
		"description": "The link secret, returned only here; only its digest is stored. The web link is /share#<token>: the fragment never reaches the server or its logs."}}, "share", "token")
	schemas["GuestShare"] = objectSchema(map[string]any{"id": uuid, "libraryId": uuid, "libraryName": map[string]any{"type": "string"}, "itemId": uuid, "itemTitle": map[string]any{"type": "string"},
		"itemKind": map[string]any{"type": "string"}, "expiresAt": instant, "readOnly": boolean, "allowPlayback": boolean}, "id", "libraryId", "libraryName", "expiresAt", "readOnly", "allowPlayback")
	schemas["ShareRedeem"] = objectSchema(map[string]any{"token": map[string]any{"type": "string", "maxLength": 64}, "deviceName": stringSchema(128)}, "token")
	schemas["ShareRedeemNative"] = objectSchema(map[string]any{"token": map[string]any{"type": "string", "maxLength": 64},
		"client": nativeLabel(128, "Required client application name; no surrounding spaces."), "device": nativeLabel(128, "Optional device name shown in session lists."),
		"deviceId": nativeLabel(256, "Required stable device identifier; no surrounding spaces."), "version": nativeLabel(64, "Optional client application version.")}, "token", "client", "deviceId")
	schemas["ShareAccessRecord"] = objectSchema(map[string]any{
		"id":         map[string]any{"type": "integer"},
		"event":      enum("share.created, share.revoked, share.redeemed, share.redeem_refused, share.accessed (one record per guest session, route and minute) or share.access_refused (a route the share does not allow).", "share.created", "share.revoked", "share.redeemed", "share.redeem_refused", "share.accessed", "share.access_refused"),
		"occurredAt": instant, "actorId": uuid, "ip": map[string]any{"type": "string", "description": "Client address as resolved through the trusted proxies."},
		"sessionId": uuid, "clientKind": enum("Session kind of the guest.", "web", "native"), "deviceName": map[string]any{"type": "string"},
		"route":  map[string]any{"type": "string", "description": "Method and route pattern of an accessed or refused request, such as GET /api/v1/items/{id}; never a path, query or credential."},
		"reason": enum("Why a redemption was refused.", "revoked", "expired", "playback", "session_limit"),
	}, "id", "event", "occurredAt")
	pagination := objectSchema(map[string]any{"nextCursor": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100}}, "nextCursor", "limit")
	schemas["ShareAccessPage"] = objectSchema(map[string]any{"records": map[string]any{"type": "array", "maxItems": 100, "items": schemaRef("ShareAccessRecord")}, "pagination": pagination}, "records", "pagination")
	ruleInput := map[string]any{
		"libraryId":     uuid,
		"network":       enum("any (no condition), lan (the client address, as resolved through the trusted proxies, is private RFC 1918, unique local fc00::/7 or loopback) or wan (any other address).", "any", "lan", "wan"),
		"cidrs":         map[string]any{"type": "array", "maxItems": 64, "items": map[string]any{"type": "string", "maxLength": 64}, "description": "Addresses or prefixes, stored masked; empty means any address."},
		"clientKinds":   map[string]any{"type": "array", "maxItems": 2, "uniqueItems": true, "items": enum("Server-issued session kind.", "web", "native"), "description": "Empty means any session kind. The client-reported device_type has no source, so the session kind stands for the device type."},
		"includeAdmins": map[string]any{"type": "boolean", "description": "Subject administrators to the rule too; by default administrators are subject only to rules that include them."},
		"enabled":       boolean,
		"note":          map[string]any{"type": "string", "maxLength": 2048},
	}
	schemas["NetworkRuleInput"] = objectSchema(ruleInput, "libraryId", "network", "enabled")
	rule := map[string]any{"id": uuid, "libraryName": map[string]any{"type": "string"}, "createdAt": instant, "updatedAt": instant}
	for k, v := range ruleInput {
		rule[k] = v
	}
	schemas["NetworkRule"] = objectSchema(rule, "id", "libraryId", "libraryName", "network", "cidrs", "clientKinds", "includeAdmins", "enabled", "note", "createdAt", "updatedAt")

	cursor := map[string]any{"name": "cursor", "in": "query", "schema": map[string]any{"type": "string", "maxLength": 32}}
	limit := map[string]any{"name": "limit", "in": "query", "schema": map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "default": 50}}
	routes := []struct {
		path, method, summary, body, result, status, role string
		params                                            []any
	}{
		{"/shares", "get", "List the newest 200 share links with their state, live guest sessions and last use", "", "Share[]", "200", "administrator", nil},
		{"/shares", "post", "Create a share link of a library or of an item and its descendants; returns the token once; at most 1000 live shares (409 conflict); audited as share.created", "ShareInput", "ShareGrant", "201", "administrator", nil},
		{"/shares/{id}", "get", "Read a share link", "", "Share", "200", "administrator", nil},
		{"/shares/{id}/revoke", "post", "Revoke a share link and every guest session it issued, in one transaction; running direct streams of those sessions end at their next session check; a revoked share is returned unchanged; audited as share.revoked", "Empty", "Share", "200", "administrator", nil},
		{"/shares/{id}/access", "get", "Page the audited events of a share link newest first: creation, revocation, redemptions, refusals and guest accesses", "", "ShareAccessPage", "200", "administrator", []any{cursor, limit}},
		{"/shares/current", "get", "Read the share of the calling guest session; any other session gets 404", "", "GuestShare", "200", "guest", nil},
		{"/shares/redeem", "post", "Exchange a share token for a web guest session: sets the session cookie and returns the CSRF token like /auth/login. The guest sees only the share's scope through every catalog route, may use only the catalog, image, own-data and logout routes (others 403 share_forbidden) and never plays. Unknown, revoked and expired tokens alike get 404 share_unavailable. Shares the login rate limit; at most the per-user session limit of live guest sessions (429 session_limit); a session never outlives its share; audited as share.redeemed or share.redeem_refused", "ShareRedeem", "SessionGrant", "200", "", nil},
		{"/shares/redeem/native", "post", "Exchange a share token for a native guest session, returned in the body only, for shares that allow playback (otherwise 403 share_playback_disabled). It may direct play the original files under the delivery rules and the share's stream cap; the compatibility layer refuses guest sessions. Requests carrying Origin, Sec-Fetch-Site or Sec-Fetch-Mode are refused with 403 forbidden", "ShareRedeemNative", "SessionGrant", "200", "", nil},
		{"/access/network-rules", "get", "List the library network rules (G48.5)", "", "NetworkRule[]", "200", "administrator", nil},
		{"/access/network-rules", "post", "Create a library network rule; a library with enabled rules is visible only to requests at least one of them matches; applies to the next request; at most 1000 rules (409 conflict); audited as access.network_rule_created", "NetworkRuleInput", "NetworkRule", "201", "administrator", nil},
		{"/access/network-rules/{id}", "put", "Replace a library network rule; audited as access.network_rule_updated unless unchanged", "NetworkRuleInput", "NetworkRule", "200", "administrator", nil},
		{"/access/network-rules/{id}", "delete", "Delete a library network rule; audited as access.network_rule_deleted; empty body", "", "", "204", "administrator", nil},
	}
	for _, route := range routes {
		path := "/api/v1" + route.path
		op := operation(route.summary, route.status, "400", "401", "403", "404", "409", "413", "415", "429", "503")
		if !strings.HasPrefix(route.path, "/shares/redeem") {
			op["security"] = []any{map[string]any{"bearer": []string{}}}
		}
		if route.role != "" {
			op["x-jelee-role"] = route.role
		}
		params := append([]any{}, route.params...)
		if strings.Contains(path, "{id}") {
			params = append(params, idParameter())
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
			op["responses"].(map[string]any)[route.status] = map[string]any{"description": "Successful response; Cache-Control: no-store", "content": map[string]any{"application/json": map[string]any{"schema": objectSchema(map[string]any{"data": data}, "data")}}}
		}
		item, ok := paths[path].(map[string]any)
		if !ok {
			item = map[string]any{}
			paths[path] = item
		}
		item[route.method] = op
	}
}
