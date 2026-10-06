package httpapi

import (
	"strconv"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// userDataSchemas documents the data rights of G07.7.
func userDataSchemas(schemas map[string]any) {
	totpCode := map[string]any{"type": "string", "maxLength": 16, "writeOnly": true, "description": "Six digits from the authenticator app; required instead of recoveryCode when the account has a second factor."}
	recovery := map[string]any{"type": "string", "maxLength": 64, "writeOnly": true, "description": "One unused recovery code; required instead of code when the account has a second factor."}
	schemas["AccountPurge"] = objectSchema(map[string]any{"password": map[string]any{"type": "string", "maxLength": 1024, "writeOnly": true}, "code": totpCode, "recoveryCode": recovery}, "password")
	schemas["UserDataRecord"] = objectSchema(map[string]any{
		"type": map[string]any{"type": "string", "enum": []string{"export", "account", "preferences", "trackPreference", "blockedTag", "libraryAccess", "itemAccessRule", "itemData",
			"playbackSession", "playbackSample", "watchStatsDay", "watchStatsItem", "session", "appPassword", "shareLink", "clientControlHit", "auditEvent", "end"}},
		"data": map[string]any{"type": "object", "description": "The export record: format " + domain.UserDataExportFormat + ", version, userId, userName and generatedAt (the snapshot time). A data record: the row with camelCase fields; null fields are left out. The end record: records, the number of data records."},
	}, "type", "data")
}

func userDataSpecification(paths map[string]any) {
	bearer := []any{map[string]any{"bearer": []string{}}}
	export := operation("Export the personal data of self or, as administrator, of any user including a soft-deleted one (G07.7)", "200", "400", "401", "403", "404", "409", "503")
	export["security"] = bearer
	export["parameters"] = []any{idParameter()}
	export["description"] = "Streams NDJSON from one database snapshot, one UserDataRecord per line: first the export record, then the account, preferences, track preferences, blocked tags, library access, item access rules, playback progress and played state (itemData), playback history (playbackSession, playbackSample), watch statistics, sessions and devices, application passwords, shares the user created, client control hits and audit events naming the user (event, time and role only; the address only when the user acted), last the end record. A stream without the end record was cut short. Items are named by ID only, and records about an item or library the exported user cannot see now (G48, including the request restriction) are left out. Secrets are never exported: password and application password digests, authenticator secrets, recovery codes, session, share and challenge tokens, webhook secrets. Rows are streamed, not buffered. The audit event user.data_exported (security category) is recorded before the first byte. At most " + strconv.Itoa(userDataExportLimit) + " exports run at once on an instance and one per caller (409 conflict with Retry-After); the stream is bounded by " + domain.UserDataExportTimeout.String() + "."
	responses := export["responses"].(map[string]any)
	responses["200"] = map[string]any{
		"description": "The records, streamed as an attachment.",
		"headers": map[string]any{
			"X-Jelee-Export-Complete": map[string]any{"schema": map[string]any{"type": "string", "enum": []string{"true", "false"}}, "description": "Trailer: false when the stream ended early."},
		},
		"content": map[string]any{"application/x-ndjson": map[string]any{"schema": schemaRef("UserDataRecord")}},
	}
	paths["/api/v1/users/{id}/data-export"] = map[string]any{"get": export}

	irreversible := " Irreversible: the account, its sessions and devices, application passwords, second factor, preferences, access grants, playback progress and history, watch statistics, client control hits, shares it created (with their guest accounts) and webhook events about it are deleted in one transaction, and nothing can restore them. Scan schedules it owned move to an administrator; jobs and rules it created stay without attribution. Audit events are kept for their retention but de-identified: the user's addresses are removed and states about the user and its sessions, application passwords and shares are replaced by {\"redacted\":\"user_purged\"}; the event user.purged is recorded. The last active administrator cannot be deleted (409 last_admin)."
	self := operation("Permanently delete the own account after re-authentication (G07.7)", "204", "400", "401", "403", "409", "413", "415", "429", "503")
	self["security"] = bearer
	self["description"] = "The account password (400 invalid_password) and, when the account has a second factor, exactly one of a current authenticator code or an unused recovery code (400 invalid_two_factor_code). Password and code attempts draw from the second factor budget (429 auth_rate_limited). Requires a web session (403 forbidden for native sessions). Clears the session cookie." + irreversible
	self["requestBody"] = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": schemaRef("AccountPurge")}}, "description": "Maximum 64 KiB; exactly one object; unknown or duplicate keys rejected."}
	paths["/api/v1/users/me/purge"] = map[string]any{"post": self}

	admin := operation("Permanently delete another user, active or soft-deleted (G07.7)", "204", "400", "401", "403", "404", "409", "413", "415", "503")
	admin["security"] = bearer
	admin["x-jelee-role"] = "administrator"
	admin["parameters"] = []any{idParameter()}
	admin["description"] = "Empty JSON object body. Requires an administrator web session; the own account is refused with 403 (use /api/v1/users/me/purge)." + irreversible
	admin["requestBody"] = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": schemaRef("Empty")}}, "description": "Exactly {}."}
	paths["/api/v1/users/{id}/purge"] = map[string]any{"post": admin}
}
