package httpapi

import "strings"

// clientControlSpecification documents the administrator client control
// API (G47). See docs/client-control.md for the model.
func clientControlSpecification(paths, schemas map[string]any) {
	uuid := map[string]any{"type": "string", "format": "uuid"}
	instant := map[string]any{"type": "string", "format": "date-time"}
	enum := func(description string, values ...string) map[string]any {
		return map[string]any{"type": "string", "enum": values, "description": description}
	}
	dimension := enum("Request attribute the rule inspects. Everything except ip and api_key_fingerprint is reported by the client and can be forged; app_name, app_version, device_id and device_name prefer the labels recorded with the session at login over the request's own headers.",
		"user_agent", "app_name", "app_version", "device_id", "device_name", "device_type", "ip", "api_key_fingerprint", "header")
	match := enum("exact, prefix, glob (* and ?, backslash escapes, whole value), regex (RE2, unanchored, at most 4000 compiled instructions, at most 1000 enabled regex rules), cidr (ip only) or absent (matches when the value is missing; pattern must be empty).",
		"exact", "prefix", "glob", "regex", "cidr", "absent")
	action := enum("allow (allow list), deny (403 client_blocked), read_only (unsafe methods get 403 client_read_only, except logout, rotate and read-shaped POSTs), rate_limit (429 client_rate_limited with Retry-After), force_relogin (revokes sessions issued before the rule last changed; 401), observe (records hits in statistics, never enforces) or shadow (records hits only in the hit log).",
		"allow", "deny", "read_only", "rate_limit", "force_relogin", "observe", "shadow")
	intent := enum("For observe and shadow rules only (required there): the action the rule enforces after POST .../enforce.", "allow", "deny", "read_only", "rate_limit", "force_relogin")
	rate := objectSchema(map[string]any{
		"requests":      map[string]any{"type": "integer", "minimum": 1, "maximum": 1000000},
		"periodSeconds": map[string]any{"type": "integer", "minimum": 1, "maximum": 86400},
	}, "requests", "periodSeconds")
	window := objectSchema(map[string]any{
		"from":       instant,
		"until":      instant,
		"dailyStart": map[string]any{"type": "string", "pattern": "^[0-2][0-9]:[0-5][0-9]$", "description": "HH:MM in timeZone; with dailyEnd (exclusive, 24:00 allowed). A start after the end wraps past midnight."},
		"dailyEnd":   map[string]any{"type": "string", "pattern": "^[0-2][0-9]:[0-5][0-9]$"},
		"weekdays":   map[string]any{"type": "array", "maxItems": 7, "items": map[string]any{"type": "integer", "minimum": 0, "maximum": 6}, "description": "0 is Sunday; the day the daily window opened."},
		"timeZone":   map[string]any{"type": "string", "maxLength": 64, "description": "IANA name; empty means UTC. Only with a daily window."},
	})
	input := map[string]any{
		"dimension":   dimension,
		"header":      map[string]any{"type": "string", "maxLength": 256, "description": "Header name; required for and only valid with the header dimension."},
		"match":       match,
		"pattern":     map[string]any{"type": "string", "maxLength": 1024},
		"caseFold":    map[string]any{"type": "boolean", "description": "Compare case-insensitively."},
		"priority":    map[string]any{"type": "integer", "minimum": -1000000, "maximum": 1000000, "description": "Larger wins. Within one priority a deny beats an allow; an allow stops lower priorities; restrictions of the same or higher priority still apply."},
		"action":      action,
		"intent":      intent,
		"rateLimit":   rate,
		"scopeKind":   enum("global (default), user (scopeValues are user IDs) or client_kind (web or native, the server-issued session kind).", "global", "user", "client_kind"),
		"scopeValues": map[string]any{"type": "array", "maxItems": 1000, "items": map[string]any{"type": "string", "minLength": 1, "maxLength": 256}},
		"window":      window,
		"enabled":     map[string]any{"type": "boolean"},
		"note":        map[string]any{"type": "string", "maxLength": 2048},
	}
	schemas["ClientRuleInput"] = objectSchema(input, "dimension", "match", "pattern", "action", "enabled")
	rule := map[string]any{"id": uuid, "hitCount": map[string]any{"type": "integer", "minimum": 0}, "lastHitAt": instant, "createdAt": instant, "updatedAt": instant}
	for k, v := range input {
		rule[k] = v
	}
	schemas["ClientRule"] = objectSchema(rule, "id", "dimension", "match", "pattern", "caseFold", "priority", "action", "scopeKind", "scopeValues", "enabled", "note", "hitCount", "createdAt", "updatedAt")
	policy := map[string]any{
		"unknownClients": enum("Authenticated requests of clients that are not trusted and that no allow rule covers: allow, read_only, deny (403 client_blocked) or pending_approval (403 client_pending_approval until an administrator trusts the client). Logins are not subject to it, so a new client can log in and appear in the known clients.", "allow", "read_only", "deny", "pending_approval"),
		"exemptAdmins":   map[string]any{"type": "boolean", "description": "Administrator sessions are never refused; hits are still recorded."},
		"exemptLoopback": map[string]any{"type": "boolean", "description": "Loopback clients without forwarding headers are never refused."},
	}
	schemas["ClientPolicyInput"] = objectSchema(policy, "unknownClients", "exemptAdmins", "exemptLoopback")
	policyView := map[string]any{"version": map[string]any{"type": "integer", "minimum": 1}, "updatedAt": instant}
	for k, v := range policy {
		policyView[k] = v
	}
	schemas["ClientPolicy"] = objectSchema(policyView, "unknownClients", "exemptAdmins", "exemptLoopback", "version", "updatedAt")
	mode := enum("enforced (applied), exempt (matched an exempt administrator or loopback request), observe, shadow, or default (the unknown-client policy).", "enforced", "exempt", "observe", "shadow", "default")
	schemas["ClientHit"] = objectSchema(map[string]any{
		"id":        map[string]any{"type": "integer"},
		"bucket":    map[string]any{"type": "string", "format": "date-time", "description": "Start of the minute the hits are aggregated in."},
		"ruleId":    map[string]any{"type": "string", "format": "uuid", "description": "Absent for the unknown-client policy and for deleted rules."},
		"mode":      mode,
		"action":    enum("Action applied or, for observe and shadow, intended.", "allow", "deny", "read_only", "rate_limit", "force_relogin", "pending_approval"),
		"surface":   enum("API that received the requests.", "native", "compat"),
		"userId":    uuid,
		"network":   map[string]any{"type": "string", "description": "Client address masked to its /24 (IPv4) or /48 (IPv6) network."},
		"userAgent": map[string]any{"type": "string", "maxLength": 256, "description": "Truncated user agent; no path, query or credential is recorded."},
		"appName":   map[string]any{"type": "string", "maxLength": 256},
		"hits":      map[string]any{"type": "integer", "minimum": 1},
	}, "id", "bucket", "mode", "action", "surface", "hits")
	pagination := objectSchema(map[string]any{"nextCursor": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100}}, "nextCursor", "limit")
	schemas["ClientHitPage"] = objectSchema(map[string]any{"hits": map[string]any{"type": "array", "maxItems": 100, "items": schemaRef("ClientHit")}, "pagination": pagination}, "hits", "pagination")
	schemas["ClientHitExport"] = objectSchema(map[string]any{"hits": map[string]any{"type": "array", "maxItems": 10000, "items": schemaRef("ClientHit")}, "count": map[string]any{"type": "integer", "minimum": 0}}, "hits", "count")
	count := objectSchema(map[string]any{"value": map[string]any{"type": "string"}, "hits": map[string]any{"type": "integer"}}, "value", "hits")
	counts := map[string]any{"type": "array", "maxItems": 50, "items": count}
	schemas["ClientHitStats"] = objectSchema(map[string]any{
		"since": instant, "total": map[string]any{"type": "integer"}, "blocked": map[string]any{"type": "integer"}, "observed": map[string]any{"type": "integer"},
		"byAction": counts, "topUserAgents": counts, "topIps": counts, "topRules": counts,
	}, "since", "total", "blocked", "observed", "byAction", "topUserAgents", "topIps", "topRules")
	schemas["KnownClient"] = objectSchema(map[string]any{
		"id": uuid, "appName": map[string]any{"type": "string"}, "appVersion": map[string]any{"type": "string"}, "userAgent": map[string]any{"type": "string"},
		"deviceId": map[string]any{"type": "string"}, "deviceName": map[string]any{"type": "string"}, "clientKind": enum("Session kind it was last seen with.", "web", "native"),
		"alias": map[string]any{"type": "string", "maxLength": 128}, "trusted": map[string]any{"type": "boolean", "description": "Approved by an administrator; escapes the unknown-client policy."},
		"firstSeenAt": instant, "lastSeenAt": instant, "lastIp": map[string]any{"type": "string"}, "lastUserId": uuid,
		"activeSessions": map[string]any{"type": "integer", "minimum": 0},
	}, "id", "trusted", "firstSeenAt", "lastSeenAt", "activeSessions")
	schemas["KnownClientPage"] = objectSchema(map[string]any{"clients": map[string]any{"type": "array", "maxItems": 100, "items": schemaRef("KnownClient")}, "pagination": pagination}, "clients", "pagination")
	schemas["KnownClientUpdate"] = objectSchema(map[string]any{
		"alias":   map[string]any{"type": "string", "maxLength": 128, "description": "Empty removes the alias; omitted keeps it."},
		"trusted": map[string]any{"type": "boolean", "description": "Omitted keeps it."},
	})
	schemas["ClientKickResult"] = objectSchema(map[string]any{"sessionsRevoked": map[string]any{"type": "integer", "minimum": 0}}, "sessionsRevoked")

	cursor := map[string]any{"name": "cursor", "in": "query", "schema": map[string]any{"type": "string", "maxLength": 64}}
	limit := map[string]any{"name": "limit", "in": "query", "schema": map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "default": 50}}
	hitFilter := []any{
		map[string]any{"name": "ruleId", "in": "query", "schema": uuid},
		map[string]any{"name": "mode", "in": "query", "schema": mode},
		map[string]any{"name": "since", "in": "query", "schema": instant, "description": "RFC 3339; inclusive."},
		map[string]any{"name": "until", "in": "query", "schema": instant, "description": "RFC 3339; exclusive."},
	}
	routes := []struct {
		path, method, summary, body, result, status string
		params                                      []any
	}{
		{"/policy", "get", "Read the client control policy", "", "ClientPolicy", "200", nil},
		{"/policy", "put", "Replace the client control policy; takes effect on the next request; audited as client_control.policy_changed unless unchanged", "ClientPolicyInput", "ClientPolicy", "200", nil},
		{"/rules", "get", "List every client rule, enabled or not, in precedence order (priority descending, then ID)", "", "ClientRule[]", "200", nil},
		{"/rules", "post", "Create a client rule; the rule is compiled before it is stored (400 invalid_request when it cannot be enforced); at most 10000 rules and 1000 enabled regex rules (409 conflict); audited as client_control.rule_created", "ClientRuleInput", "ClientRule", "201", nil},
		{"/rules/{id}", "get", "Read a client rule with its hit count", "", "ClientRule", "200", nil},
		{"/rules/{id}", "put", "Replace a client rule's settings; hit counters are kept; audited as client_control.rule_updated unless unchanged", "ClientRuleInput", "ClientRule", "200", nil},
		{"/rules/{id}", "delete", "Delete a client rule; its hit records stay without the rule; audited as client_control.rule_deleted; empty body", "", "", "204", nil},
		{"/rules/{id}/enforce", "post", "Switch an observe or shadow rule to enforcing its intent; no-op for an enforcing rule; audited as client_control.rule_mode_changed; body {}", "", "ClientRule", "200", nil},
		{"/rules/{id}/observe", "post", "Switch an enforcing rule to observing its action; no-op for an observe or shadow rule; audited as client_control.rule_mode_changed; body {}", "", "ClientRule", "200", nil},
		{"/hits", "get", "Page aggregated hit records newest first; addresses masked to /24 or /48, user agents truncated, no paths", "", "ClientHitPage", "200", append(append([]any{}, hitFilter...), cursor, limit)},
		{"/hits/export", "get", "Export at most 10000 masked hit records newest first as a JSON attachment; more is refused with 409 stats_export_limit; audited as client_control.hits_exported", "", "ClientHitExport", "200", hitFilter},
		{"/stats", "get", "Hit statistics: totals, by mode and action, top user agents, top addresses and top rules; shadow hits are excluded", "", "ClientHitStats", "200", []any{
			map[string]any{"name": "hours", "in": "query", "schema": map[string]any{"type": "integer", "minimum": 1, "maximum": 720, "default": 24}},
			map[string]any{"name": "top", "in": "query", "schema": map[string]any{"type": "integer", "minimum": 1, "maximum": 50, "default": 10}},
		}},
		{"/clients", "get", "Page the clients seen on authenticated requests, most recently seen first", "", "KnownClientPage", "200", []any{cursor, limit}},
		{"/clients/{id}", "patch", "Rename a known client or change its trust; trust takes effect on the next request; audited as client_control.client_updated unless unchanged", "KnownClientUpdate", "KnownClient", "200", nil},
		{"/clients/{id}/block", "post", "Add an exact deny rule for the client's device ID, or its user agent when it reports none, at priority 100000; audited as client_control.client_blocked; body {}", "", "ClientRule", "201", nil},
		{"/clients/{id}/kick", "post", "Revoke every active session the client used; it may log in again unless blocked; audited as client_control.client_kicked; body {}", "", "ClientKickResult", "200", nil},
	}
	for _, route := range routes {
		path := "/api/v1/client-control" + route.path
		op := operation(route.summary, route.status, "400", "401", "403", "404", "409", "413", "415", "429", "503")
		op["security"] = []any{map[string]any{"bearer": []string{}}}
		op["x-jelee-role"] = "administrator"
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
				data = map[string]any{"type": "array", "maxItems": 10000, "items": schemaRef(strings.TrimSuffix(route.result, "[]"))}
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
