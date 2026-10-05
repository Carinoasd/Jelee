package httpapi

import "github.com/MoYuanCN/Jelee/internal/platform/devmode"

// devSpecification documents the developer mode routes (G45). They exist
// only on an instance that meets its own developer mode thresholds, so the
// committed reference document (a production rollout) never contains them;
// a capable instance serves them in its own /api/v1/openapi.json. See
// docs/developer-mode.md.
func devSpecification(paths, schemas map[string]any) {
	var toggles []string
	for _, t := range devmode.Toggles() {
		toggles = append(toggles, string(t))
	}
	instant := map[string]any{"type": "string", "format": "date-time"}
	schemas["DevModeToggle"] = objectSchema(map[string]any{
		"name":      map[string]any{"type": "string", "enum": toggles},
		"kind":      map[string]any{"type": "string", "enum": []string{"restriction", "debug"}},
		"dangerous": map[string]any{"type": "boolean", "description": "Switching it on needs iUnderstand (G45.6)."},
		"available": map[string]any{"type": "boolean", "description": "Wired into this build; unavailable toggles answer 409 devmode_toggle_unavailable."},
		"enabled":   map[string]any{"type": "boolean"},
	}, "name", "kind", "dangerous", "available", "enabled")
	schemas["DevModeState"] = objectSchema(map[string]any{
		"active":     map[string]any{"type": "boolean"},
		"enabledAt":  instant,
		"expiresAt":  instant,
		"source":     map[string]any{"type": "string", "maxLength": 64},
		"ttlSeconds": map[string]any{"type": "integer", "minimum": 1, "maximum": int64(devmode.MaxTTL.Seconds())},
		"toggles":    map[string]any{"type": "array", "items": schemaRef("DevModeToggle")},
	}, "active", "ttlSeconds", "toggles")
	data := func(schema map[string]any) map[string]any {
		return map[string]any{"application/json": map[string]any{"schema": objectSchema(map[string]any{"data": schema}, "data")}}
	}
	devOnly := "Developer mode only: registered when JELEE_DEV_MODE=true and dev.enabled are set outside JELEE_ENV=production; otherwise 404. "
	admin := func(op map[string]any) map[string]any {
		op["security"] = []any{map[string]any{"bearer": []string{}}, map[string]any{"webSession": []string{}}}
		op["x-jelee-role"] = "administrator"
		op["x-jelee-dev-only"] = true
		return op
	}

	token := operation("Issue a one-time developer mode enable token", "201", "400", "404", "503")
	token["description"] = devOnly + "Loopback only: the transport peer must be a loopback address and the request must carry no forwarding header, otherwise 404. No authentication; body {}. The token is valid for five minutes and only `jelee-cli devmode enable --token <token>` redeems it, once; HTTP can never enable developer mode. Audited as devmode.token_issued."
	token["x-jelee-dev-only"] = true
	token["responses"].(map[string]any)["201"].(map[string]any)["content"] = data(objectSchema(map[string]any{
		"token":     map[string]any{"type": "string", "pattern": "^jdm_[0-9a-f]{64}$"},
		"expiresAt": instant,
		"enable":    map[string]any{"type": "string"},
	}, "token", "expiresAt", "enable"))
	paths[DevAPIPrefix+"/token"] = map[string]any{"post": token}

	state := admin(operation("Read the developer mode session and every toggle", "200", "400", "401", "403"))
	state["description"] = devOnly + "Administrators only."
	state["responses"].(map[string]any)["200"].(map[string]any)["content"] = data(schemaRef("DevModeState"))
	paths[DevAPIPrefix] = map[string]any{"get": state}

	toggle := admin(operation("Switch one developer mode toggle", "200", "400", "401", "403", "404", "409", "413", "415"))
	toggle["description"] = devOnly + "Administrators only. Needs an active session (409 devmode_inactive). Switching a dangerous toggle on needs iUnderstand: true (400 confirmation_required); switching any toggle off never does. Toggles not wired into this build answer 409 devmode_toggle_unavailable, unknown names 404. Audited as devmode.toggle_changed; refusals as devmode.denied."
	toggle["parameters"] = []any{map[string]any{"name": "toggle", "in": "path", "required": true, "schema": map[string]any{"type": "string", "enum": toggles}}}
	toggle["requestBody"] = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": objectSchema(map[string]any{
		"enabled":     map[string]any{"type": "boolean"},
		"iUnderstand": map[string]any{"type": "boolean", "description": "Explicit confirmation for dangerous toggles (G45.6)."},
	}, "enabled")}}}
	toggle["responses"].(map[string]any)["200"].(map[string]any)["content"] = data(schemaRef("DevModeState"))
	paths[DevAPIPrefix+"/toggles/{toggle}"] = map[string]any{"put": toggle}

	disable := admin(operation("Disable developer mode now", "204", "400", "401", "403", "409"))
	disable["description"] = devOnly + "Administrators only. Ends the session on every instance and restores every production restriction; 409 devmode_inactive without a session. Body {}. Audited as devmode.disabled."
	paths[DevAPIPrefix+"/disable"] = map[string]any{"post": disable}

	pprofDescription := devOnly + "net/http/pprof (G45.5). Answers 404 unless a session is active and debug_pprof is on; then loopback callers without forwarding headers, or administrators, only (anyone else 404)."
	profile := operation("Read a runtime profile", "200", "401", "404")
	profile["description"] = pprofDescription + " The trailing segment names the profile (heap, goroutine, profile, trace, cmdline, symbol, ...); empty lists them."
	profile["x-jelee-dev-only"] = true
	paths[devPprofPrefix+"*"] = map[string]any{"get": profile}
	symbol := operation("Resolve program counters to symbols", "200", "401", "404")
	symbol["description"] = pprofDescription
	symbol["x-jelee-dev-only"] = true
	paths[devPprofPrefix+"symbol"] = map[string]any{"post": symbol}
}
