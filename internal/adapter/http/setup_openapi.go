package httpapi

import "github.com/MoYuanCN/Jelee/internal/domain"

// setupSpecification documents the G18 wizard. It is mounted with the
// account rollout because the wizard creates the first administrator.
func setupSpecification(paths, schemas map[string]any) {
	steps := []string{}
	for step := domain.SetupStepLanguage; step <= domain.SetupStepComplete; step++ {
		steps = append(steps, step.String())
	}
	locale := map[string]any{"type": "string", "enum": []string{"zh-CN", "zh-TW", "ja-JP", "en-US"}}
	stringList := func(max int) map[string]any {
		return map[string]any{"type": "array", "maxItems": max, "items": map[string]any{"type": "string"}}
	}
	library := objectSchema(map[string]any{"name": stringSchema(128), "path": map[string]any{"type": "string", "maxLength": 4096, "description": "Absolute, clean server path; must exist and be readable by the service account."}}, "name", "path")
	tmdb := objectSchema(map[string]any{"enabled": map[string]any{"type": "boolean"}, "language": locale}, "enabled")
	policy := objectSchema(map[string]any{
		"nfoRead":        map[string]any{"type": "string", "enum": []string{domain.NFOModeOff, domain.NFOModeReadOnly}},
		"nfoWrite":       map[string]any{"type": "string", "enum": []string{domain.SetupNFOWriteOff, domain.SetupNFOWriteBack}, "description": "write-back requires nfoRead read-only."},
		"imageFetch":     map[string]any{"type": "boolean", "description": "Requires TMDB."},
		"imageWriteBack": map[string]any{"type": "boolean"},
	}, "nfoRead", "nfoWrite", "imageFetch", "imageWriteBack")
	network := objectSchema(map[string]any{
		"mode":                map[string]any{"type": "string", "enum": []string{domain.SetupNetworkLocal, domain.SetupNetworkLAN, domain.SetupNetworkReverseProxy}},
		"listen":              map[string]any{"type": "string", "description": "IP:port. local needs a loopback address, lan a non-loopback one. A port held by another program is refused; this server's own configured address is always accepted."},
		"allowedHosts":        stringList(32),
		"trustedProxies":      map[string]any{"type": "array", "maxItems": 64, "items": map[string]any{"type": "string", "description": "CIDR prefix."}, "description": "Required for reverse-proxy."},
		"privacyAcknowledged": map[string]any{"type": "boolean", "description": "Required for lan and reverse-proxy: clients learn the server address (docs/network-privacy.md)."},
	}, "mode", "listen", "allowedHosts", "privacyAcknowledged")
	schemas["SetupState"] = objectSchema(map[string]any{
		"version":        map[string]any{"type": "integer", "format": "int64", "minimum": 0},
		"current":        map[string]any{"type": "string", "enum": steps},
		"completedAt":    map[string]any{"type": "string", "format": "date-time"},
		"locale":         locale,
		"admin":          objectSchema(map[string]any{"userId": map[string]any{"type": "string", "format": "uuid"}, "name": stringSchema(128), "displayName": stringSchema(128)}),
		"database":       objectSchema(map[string]any{"schemaVersion": map[string]any{"type": "integer", "minimum": 0}}),
		"media":          map[string]any{"type": "array", "maxItems": 64, "items": library},
		"tmdb":           tmdb,
		"toolchain":      objectSchema(map[string]any{"available": stringList(16), "missing": stringList(16), "acceptDegraded": map[string]any{"type": "boolean"}}, "acceptDegraded"),
		"metadataPolicy": objectSchema(map[string]any{"nfoRead": map[string]any{"type": "string"}, "nfoWrite": map[string]any{"type": "string"}, "imageFetch": map[string]any{"type": "boolean"}, "imageWriteBack": map[string]any{"type": "boolean"}}, "imageFetch", "imageWriteBack"),
		"network":        objectSchema(map[string]any{"mode": map[string]any{"type": "string"}, "listen": map[string]any{"type": "string"}, "allowedHosts": stringList(32), "trustedProxies": stringList(64), "privacyAcknowledged": map[string]any{"type": "boolean"}}, "privacyAcknowledged"),
	}, "version", "current", "admin", "database", "tmdb", "toolchain", "metadataPolicy", "network")
	data := func(schema map[string]any) map[string]any {
		return map[string]any{"application/json": map[string]any{"schema": objectSchema(map[string]any{"data": schema}, "data")}}
	}
	token := map[string]any{"name": SetupTokenHeader, "in": "header", "required": true, "schema": map[string]any{"type": "string"},
		"description": "One-time setup token printed by the server at startup (or written to setupTokenFile). Without the exact value every wizard call is 401 setup_token_invalid."}
	common := "Available only while initial setup is incomplete; afterwards every wizard path answers 410 setup_completed. "
	wizard := func(summary, description string, statuses ...string) map[string]any {
		op := operation(summary, append([]string{"200", "400", "401", "408", "410", "503"}, statuses...)...)
		op["description"] = common + description
		op["parameters"] = []any{token}
		op["x-jelee-setup"] = "token"
		op["responses"].(map[string]any)["200"].(map[string]any)["content"] = data(schemaRef("SetupState"))
		op["responses"].(map[string]any)["400"] = map[string]any{"description": "setup_validation_failed with details.step and details.issues, or invalid_request."}
		return op
	}

	status := operation("Ask whether initial setup is required", "200", "400", "410")
	status["description"] = "Public. 200 while setup is incomplete, 410 setup_completed afterwards. The frontend uses it to decide whether to show the wizard."
	status["responses"].(map[string]any)["200"].(map[string]any)["content"] = data(objectSchema(map[string]any{
		"setupRequired": map[string]any{"type": "boolean", "const": true}, "tokenRequired": map[string]any{"type": "boolean"},
	}, "setupRequired", "tokenRequired"))
	paths[domain.SetupAPIPrefix+"/status"] = map[string]any{"get": status}

	read := wizard("Read setup progress", "Returns the stored wizard state; it never contains a password, hash or credential.")
	paths[domain.SetupAPIPrefix] = map[string]any{"get": read}

	submit := wizard("Submit the current setup step", "The step must be the current one (409 setup_step_order otherwise). Every step is validated live and the state is stored with a version check, so an interrupted wizard resumes where it stopped. Bodies by step: language {locale}; admin {name, displayName, password} (password 12..1024 UTF-8 bytes, at least 4 distinct characters, not containing the name; the administrator is created once, re-entering after back only rechecks the name); database: no body; media {libraries:[{name, path}]}; tmdb {enabled, language} (the key itself only comes from TMDB_API_KEY/TMDB_API_KEY_FILE); toolchain {acceptDegraded}; metadata-policy {nfoRead, nfoWrite, imageFetch, imageWriteBack}; network {mode, listen, allowedHosts, trustedProxies, privacyAcknowledged}. Records setup.step_saved, and setup.admin_created for the administrator.", "404", "409", "413", "415")
	submit["parameters"] = []any{token, map[string]any{"name": "step", "in": "path", "required": true, "schema": map[string]any{"type": "string", "enum": steps[:len(steps)-1]}}}
	submit["requestBody"] = map[string]any{"required": false, "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"oneOf": []any{
		objectSchema(map[string]any{"locale": locale}, "locale"),
		objectSchema(map[string]any{"name": stringSchema(128), "displayName": stringSchema(128), "password": map[string]any{"type": "string", "minLength": 12, "maxLength": 1024, "writeOnly": true}}, "name", "password"),
		objectSchema(map[string]any{"libraries": map[string]any{"type": "array", "maxItems": 64, "items": library}}),
		tmdb,
		objectSchema(map[string]any{"acceptDegraded": map[string]any{"type": "boolean"}}),
		policy,
		network,
	}}}}}
	submit["responses"].(map[string]any)["409"] = map[string]any{"description": "setup_step_order, or conflict when another writer changed the state first."}
	paths[domain.SetupAPIPrefix+"/steps/{step}"] = map[string]any{"post": submit}

	back := wizard("Return to the previous setup step", "Keeps the data already entered. No body.", "409")
	paths[domain.SetupAPIPrefix+"/back"] = map[string]any{"post": back}

	complete := wizard("Complete initial setup", "Rechecks the database and media directories, then in one transaction creates the libraries with the chosen NFO and metadata language policy and marks setup complete; any failure rolls everything back and the wizard stays at the complete step. Records library.registered and setup.completed. No body.", "409")
	paths[domain.SetupAPIPrefix+"/complete"] = map[string]any{"post": complete}
}
