package httpapi

import (
	"strings"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// siteSettingsSchemas documents the site-wide web client settings and the
// layout members shared with user preferences.
func siteSettingsSchemas(schemas map[string]any) {
	layoutArea := map[string]any{"type": "array", "maxItems": domain.LayoutAreaLimit, "items": schemaRef("LayoutEntry"), "description": "Blocks in display order; IDs unique. IDs belong to the web client, which drops unknown ones and appends missing ones."}
	tokenMap := map[string]any{"type": "object", "maxProperties": len(domain.ThemeTokenNames), "propertyNames": map[string]any{"enum": domain.ThemeTokenNames},
		"additionalProperties": map[string]any{"type": "string", "maxLength": 256, "description": "A CSS value without url(), blocks, statements, escapes or markup; quotes only in font-family."}}
	theme := map[string]any{"type": "string", "enum": []string{domain.ThemeSystem, domain.ThemeLight, domain.ThemeDark}}
	host := map[string]any{"type": "string", "maxLength": 253, "pattern": `^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`, "description": "A DNS host name, lower case after trimming; a numeric last label (an IP address) is refused."}
	hosts := map[string]any{"type": "array", "maxItems": domain.FontHostLimit, "uniqueItems": true, "items": host}
	instant := map[string]any{"type": "string", "format": "date-time"}
	revision := map[string]any{"type": "integer", "minimum": 0, "description": "Increases with every change. A replacement must send the revision it read; another value is 409 conflict."}
	nullableLayout := func(description string) map[string]any {
		return map[string]any{"oneOf": []any{schemaRef("PageLayout"), map[string]any{"type": "null"}}, "description": description}
	}
	pluginID := map[string]any{"type": "string", "pattern": `^[a-z][a-z0-9-]{0,31}(?:\.[a-z][a-z0-9-]{0,31}){1,3}$`}
	settings := map[string]any{"type": "object", "maxProperties": domain.SitePluginLimit, "propertyNames": pluginID,
		"additionalProperties": map[string]any{"type": "object", "additionalProperties": true, "maxProperties": domain.PluginSettingsMaxKey, "description": "One plugin's settings namespace: keys match ^[A-Za-z][A-Za-z0-9_.-]{0,63}$, values are JSON (null allowed) at most eight levels deep, at most 16 KiB compact. Readable by every signed-in user: never store secrets."},
		"description":          "Settings namespace per plugin ID (G32.4)."}
	plugins := map[string]any{"type": "array", "maxItems": domain.SitePluginLimit, "items": schemaRef("SitePluginState"), "description": "Plugins in the administrator's order; IDs unique. Plugins not listed keep their bundled default and follow the listed ones."}
	appearanceMembers := func() map[string]any {
		return map[string]any{
			"defaultTheme":       theme,
			"tokens":             objectSchema(map[string]any{"light": tokenMap, "dark": tokenMap}, "light", "dark"),
			"customCss":          map[string]any{"type": "string", "maxLength": domain.CustomCSSMaxLength, "description": "Administrator CSS as entered, at most 65536 UTF-16 code units. The server sanitizes it like the web client (G33.4): markup, backslash escapes, control characters, unterminated comments and unbalanced blocks refuse the whole text with 400 custom_css_rejected; @import, unknown at-rules, expression(), script URLs, bindings, image-set()/src()/element()/paint(), attr() URLs and every url() that is not a same-origin path or fragment are removed and listed in cssIssues. Only the sanitized text reaches other users."},
			"allowExternalFonts": map[string]any{"type": "boolean", "description": "Allow @font-face sources on fontHosts over https and add them to the web client's CSP font-src."},
			"fontHosts":          hosts,
			"defaultLayout":      nullableLayout("Layout for users who never saved one; null keeps the web client's standard layout."),
		}
	}
	appearanceRequired := []string{"defaultTheme", "tokens", "customCss", "allowExternalFonts", "fontHosts", "defaultLayout"}
	pluginMembers := func() map[string]any { return map[string]any{"plugins": plugins, "settings": settings} }

	schemas["LayoutEntry"] = objectSchema(map[string]any{"id": map[string]any{"type": "string", "pattern": `^[A-Za-z][A-Za-z0-9_.-]{0,63}$`}, "visible": map[string]any{"type": "boolean"}}, "id", "visible")
	schemas["PageLayout"] = objectSchema(map[string]any{"home": layoutArea, "detail": layoutArea}, "home", "detail")
	schemas["LayoutPreset"] = objectSchema(map[string]any{
		"id":     map[string]any{"type": "string", "pattern": `^custom-[0-9]{1,6}$`},
		"name":   map[string]any{"type": "string", "minLength": 1, "maxLength": domain.LayoutPresetNameMax, "description": "At most 40 characters, no surrounding spaces or control characters."},
		"layout": schemaRef("PageLayout"),
	}, "id", "name", "layout")
	schemas["UserLayout"] = objectSchema(map[string]any{
		"current": schemaRef("PageLayout"),
		"presets": map[string]any{"type": "array", "maxItems": domain.LayoutPresetLimit, "items": schemaRef("LayoutPreset"), "description": "Saved presets; IDs unique."},
	}, "current", "presets")
	schemas["UserLayout"].(map[string]any)["description"] = "A user's page layout (G33.5): at most 16 KiB encoded."

	schemas["SiteAppearanceInput"] = objectSchema(func() map[string]any {
		m := appearanceMembers()
		m["revision"] = revision
		return m
	}(), append(appearanceRequired, "revision")...)
	schemas["SiteAppearanceDocument"] = objectSchema(appearanceMembers(), appearanceRequired...)
	schemas["CSSIssue"] = objectSchema(map[string]any{
		"code": map[string]any{"type": "string", "enum": []string{domain.CSSIssueTooLong, domain.CSSIssueMarkup, domain.CSSIssueEscape, domain.CSSIssueControlChar, domain.CSSIssueUnterminatedComment, domain.CSSIssueUnbalanced,
			domain.CSSIssueTooDeep, domain.CSSIssueImportBlocked, domain.CSSIssueAtRuleBlocked, domain.CSSIssueInvalidSelector, domain.CSSIssueInvalidDeclaration, domain.CSSIssueNestingUnsupported,
			domain.CSSIssueExpression, domain.CSSIssueScriptURL, domain.CSSIssueBinding, domain.CSSIssueBlockedFunction, domain.CSSIssueExternalURL, domain.CSSIssueExternalFont}},
		"excerpt": map[string]any{"type": "string", "maxLength": 80},
	}, "code", "excerpt")
	schemas["SiteAppearanceConfig"] = objectSchema(func() map[string]any {
		m := appearanceMembers()
		m["revision"], m["updatedAt"] = revision, instant
		m["cssIssues"] = map[string]any{"type": "array", "items": schemaRef("CSSIssue"), "description": "What the sanitizer removed from customCss under the current settings."}
		return m
	}(), append(appearanceRequired, "revision", "updatedAt", "cssIssues")...)
	schemas["SiteAppearance"] = objectSchema(map[string]any{
		"defaultTheme":  theme,
		"tokens":        objectSchema(map[string]any{"light": tokenMap, "dark": tokenMap}, "light", "dark"),
		"css":           map[string]any{"type": "string", "description": "Sanitized administrator CSS, ready for a constructed stylesheet; the raw text is administrator-only."},
		"fontHosts":     map[string]any{"type": "array", "maxItems": domain.FontHostLimit, "items": host, "description": "Hosts external fonts may load from; empty while external fonts are off."},
		"defaultLayout": nullableLayout("Layout for users who never saved one; null keeps the standard layout."),
	}, "defaultTheme", "tokens", "css", "fontHosts", "defaultLayout")
	schemas["SitePluginState"] = objectSchema(map[string]any{"id": pluginID, "enabled": map[string]any{"type": "boolean"}}, "id", "enabled")
	schemas["SitePlugins"] = objectSchema(map[string]any{"plugins": plugins, "settings": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "object", "additionalProperties": true}, "description": "Settings namespaces of plugins that are not disabled."}}, "plugins", "settings")
	schemas["SitePluginsInput"] = objectSchema(func() map[string]any {
		m := pluginMembers()
		m["revision"] = revision
		return m
	}(), "plugins", "settings", "revision")
	schemas["SitePluginsDocument"] = objectSchema(pluginMembers(), "plugins", "settings")
	schemas["SitePluginsConfig"] = objectSchema(func() map[string]any {
		m := pluginMembers()
		m["revision"], m["updatedAt"] = revision, instant
		return m
	}(), "plugins", "settings", "revision", "updatedAt")
	schemas["SiteSettingsDocument"] = objectSchema(map[string]any{
		"format":     map[string]any{"type": "string", "const": domain.SiteSettingsFormat},
		"version":    map[string]any{"type": "integer", "const": domain.SiteSettingsFormatVersion},
		"exportedAt": map[string]any{"type": "string", "format": "date-time", "description": "Informational; ignored on import."},
		"appearance": schemaRef("SiteAppearanceDocument"),
		"plugins":    schemaRef("SitePluginsDocument"),
	}, "format", "version", "appearance", "plugins")
	schemas["SiteSettingsImport"] = objectSchema(map[string]any{"appearance": schemaRef("SiteAppearanceConfig"), "plugins": schemaRef("SitePluginsConfig")}, "appearance", "plugins")
}

func siteSettingsSpecification(paths map[string]any) {
	type route struct {
		path, method, summary, body, result string
		admin                               bool
	}
	routes := []route{
		{"/site/appearance", "get", "Read the effective site appearance (G33.2–G33.5): default theme, token overrides, sanitized custom CSS, effective font hosts and default layout; any signed-in user", "", "SiteAppearance", false},
		{"/site/appearance/config", "get", "Read the stored site appearance with the raw CSS, the sanitizer's findings and the revision", "", "SiteAppearanceConfig", true},
		{"/site/appearance", "put", "Replace the site appearance; every field is required and revision must be current (409 conflict otherwise); the CSS is sanitized on the server (400 custom_css_rejected for structural problems); the frontend CSP adds font-src for fontHosts while allowExternalFonts is on; audited as site.appearance_changed unless unchanged", "SiteAppearanceInput", "SiteAppearanceConfig", true},
		{"/site/appearance/reset", "post", "Restore the default site appearance; audited as site.appearance_changed unless unchanged", "Empty", "SiteAppearanceConfig", true},
		{"/site/plugins", "get", "Read the effective plugin configuration (G32.4): plugin order and enablement, and settings of plugins that are not disabled; any signed-in user", "", "SitePlugins", false},
		{"/site/plugins/config", "get", "Read the stored plugin configuration with its revision", "", "SitePluginsConfig", true},
		{"/site/plugins", "put", "Replace the plugin configuration; every field is required and revision must be current (409 conflict otherwise); audited as site.plugins_changed unless unchanged", "SitePluginsInput", "SitePluginsConfig", true},
		{"/site/plugins/reset", "post", "Restore every plugin's bundled default and remove all plugin settings; audited as site.plugins_changed unless unchanged", "Empty", "SitePluginsConfig", true},
		{"/site/export", "get", "Export the site appearance and plugin configuration as one importable document (G33.3)", "", "SiteSettingsDocument", true},
		{"/site/import", "post", "Replace the site appearance and plugin configuration from an exported document in one transaction, regardless of revisions; validated and sanitized like the replacements; audited per document", "SiteSettingsDocument", "SiteSettingsImport", true},
	}
	for _, route := range routes {
		op := operation(route.summary, "200", "400", "401", "403", "409", "413", "415", "429", "503")
		op["security"] = []any{map[string]any{"bearer": []string{}}}
		if route.admin {
			op["x-jelee-role"] = "administrator"
		}
		if route.body != "" {
			limit := "Maximum 2 MiB"
			if route.body == "Empty" {
				limit = "Maximum 64 KiB"
			}
			op["requestBody"] = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": schemaRef(route.body)}},
				"description": limit + "; exactly one object; unknown or duplicate keys rejected; null only where the schema allows it."}
		}
		op["responses"].(map[string]any)["200"] = map[string]any{"description": "Successful response; Cache-Control: no-store", "content": map[string]any{"application/json": map[string]any{"schema": objectSchema(map[string]any{"data": schemaRef(route.result)}, "data")}}}
		path := "/api/v1" + route.path
		item, ok := paths[path].(map[string]any)
		if !ok {
			item = map[string]any{}
			paths[path] = item
		}
		if strings.HasSuffix(route.path, "/config") || route.path == "/site/export" {
			op["description"] = "Administrator only; other users get 403 forbidden."
		}
		item[route.method] = op
	}
}
