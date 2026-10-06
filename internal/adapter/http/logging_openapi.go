package httpapi

import (
	"strings"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/logging"
)

// loggingSpecification documents the logging administration (G46.2, G46.9,
// G46.10). See docs/logging.md.
func loggingSpecification(paths, schemas map[string]any) {
	var scopes, mandatory []string
	for _, info := range logging.ComponentTable() {
		if info.Mandatory {
			mandatory = append(mandatory, info.Name)
			continue
		}
		scopes = append(scopes, info.Name)
	}
	levels := []string{"debug", "info", "warn", "error"}
	level := map[string]any{"type": "string", "enum": levels}
	schemas["LogLevelOverride"] = objectSchema(map[string]any{
		"level":     level,
		"expiresAt": map[string]any{"type": "string", "format": "date-time", "description": "Absent: the override stays until it is reset."},
	}, "level")
	schemas["LogScopeLevel"] = objectSchema(map[string]any{
		"name":       map[string]any{"type": "string", "description": "A scope, or global for the global level."},
		"aliases":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Component names filtered under this scope."},
		"mandatory":  map[string]any{"type": "boolean", "description": "Audit and security scopes: never above INFO and never adjustable (G46.10)."},
		"configured": map[string]any{"type": []string{"string", "null"}, "enum": []any{"debug", "info", "warn", "error", nil}, "description": "Level from the process configuration; null when the scope follows the global level."},
		"effective":  level,
		"override":   map[string]any{"oneOf": []any{schemaRef("LogLevelOverride"), map[string]any{"type": "null"}}},
	}, "name", "aliases", "mandatory", "configured", "effective", "override")
	schemas["LogLevels"] = objectSchema(map[string]any{
		"production":         map[string]any{"type": "boolean", "description": "JELEE_ENV=production: a DEBUG override must expire."},
		"debugMaxTtlSeconds": map[string]any{"type": "integer", "minimum": 1},
		"global":             schemaRef("LogScopeLevel"),
		"components":         map[string]any{"type": "array", "items": schemaRef("LogScopeLevel")},
	}, "production", "debugMaxTtlSeconds", "global", "components")
	schemas["LogLevelChange"] = objectSchema(map[string]any{
		"component":  map[string]any{"type": "string", "description": "global, a scope (" + strings.Join(scopes, ", ") + ") or an alias of one. " + strings.Join(mandatory, " and ") + " answer 409 log_component_mandatory."},
		"level":      map[string]any{"type": "string", "enum": append(append([]string{}, levels...), "reset"), "description": "reset removes the override; the scope returns to its configured level."},
		"ttlSeconds": map[string]any{"type": "integer", "minimum": 0, "maximum": int64(logOverrideMaxTTL / time.Second), "description": "0: no expiry. In production a DEBUG override always expires: 0 means one hour, at most four hours."},
	}, "component", "level")
	schemas["LogRetention"] = objectSchema(map[string]any{
		"logDays":       map[string]any{"type": "integer", "minimum": 0, "maximum": domain.LogRetentionMaxDays, "description": "Rotated log files older than this are removed; 0 keeps them regardless of age."},
		"logMaxTotalMB": map[string]any{"type": "integer", "minimum": 0, "maximum": domain.LogMaxTotalMBMax, "description": "Oldest rotated log files are removed while the log files exceed this many MiB; 0 disables the cap."},
		"auditDays":     map[string]any{"type": "integer", "minimum": domain.AuditRetentionMinDays, "maximum": domain.AuditRetentionMaxDays},
		"securityDays":  map[string]any{"type": "integer", "minimum": domain.AuditRetentionMinDays, "maximum": domain.AuditRetentionMaxDays},
		"fileLogging":   map[string]any{"type": "boolean", "readOnly": true, "description": "This instance writes a log file; without one only the audit retention has an effect here."},
	}, "logDays", "logMaxTotalMB", "auditDays", "securityDays")
	data := func(schema map[string]any) map[string]any {
		return map[string]any{"application/json": map[string]any{"schema": objectSchema(map[string]any{"data": schema}, "data")}}
	}
	admin := func(op map[string]any, result string) map[string]any {
		op["security"] = []any{map[string]any{"bearer": []string{}}}
		op["responses"].(map[string]any)["200"].(map[string]any)["content"] = data(schemaRef(result))
		return op
	}
	body := func(op map[string]any, schema string) {
		op["requestBody"] = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": schemaRef(schema)}}, "description": "Maximum 64 KiB; one strict JSON object."}
	}
	getLevels := admin(adminOperation("Read the runtime log levels", "200", "400", "401", "403", "503"), "LogLevels")
	getLevels["description"] = "Global level and every scope with its configured, overridden and effective level on the instance that answers. Overrides are shared by every instance through storage and applied within 15 seconds."
	putLevels := admin(adminOperation("Set or reset a runtime log level", "200", "400", "401", "403", "408", "409", "413", "415", "503"), "LogLevels")
	putLevels["description"] = "Takes effect at once on this instance and within 15 seconds on the others. Audited as logging.level_changed. The audit and security scopes cannot be lowered or switched off: 409 log_component_mandatory, audited as the security event logging.level_change_refused and logged in the security log."
	body(putLevels, "LogLevelChange")
	paths["/api/v1/admin/logging/levels"] = map[string]any{"get": getLevels, "put": putLevels}
	getRetention := admin(adminOperation("Read the log and audit retention", "200", "400", "401", "403", "408", "503"), "LogRetention")
	getRetention["description"] = "Retention of rotated log files (age and total size, applied by every instance to its own files) and of the audit trail's audit and security categories."
	putRetention := admin(adminOperation("Change the log and audit retention", "200", "400", "401", "403", "408", "413", "415", "503"), "LogRetention")
	putRetention["description"] = "Every member is required (fileLogging is ignored). Audited as logging.retention_changed and audit.retention_changed for the parts that changed."
	body(putRetention, "LogRetention")
	paths["/api/v1/admin/logging/retention"] = map[string]any{"get": getRetention, "put": putRetention}
}
