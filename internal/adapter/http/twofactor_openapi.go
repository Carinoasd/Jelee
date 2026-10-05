package httpapi

import (
	"strconv"
	"strings"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// twoFactorSchemas documents the optional second factor and application
// passwords (G07.8).
func twoFactorSchemas(schemas map[string]any) {
	instant := map[string]any{"type": "string", "format": "date-time"}
	uuid := map[string]any{"type": "string", "format": "uuid"}
	totpCode := map[string]any{"type": "string", "maxLength": 16, "writeOnly": true, "description": "Six digits from the authenticator app; spaces are ignored."}
	recovery := map[string]any{"type": "string", "maxLength": 64, "writeOnly": true, "description": "One unused recovery code, as shown (xxxx-xxxx-xxxx-xxxx); case, hyphens and spaces are ignored. Each works once."}
	challenge := map[string]any{"type": "string", "minLength": 43, "maxLength": 43, "description": "Bearer secret of the second login step, valid for " + strconv.Itoa(int(domain.ChallengeTTL.Minutes())) + " minutes and for at most " + strconv.Itoa(domain.ChallengeAttempts) + " wrong codes; spent by the first correct one."}
	schemas["SecondFactorChallenge"] = objectSchema(map[string]any{
		"secondFactorRequired": map[string]any{"type": "boolean", "const": true},
		"challenge":            challenge,
		"expiresAt":            instant,
	}, "secondFactorRequired", "challenge", "expiresAt")
	schemas["SecondFactorLogin"] = objectSchema(map[string]any{"challenge": challenge, "code": totpCode, "recoveryCode": recovery}, "challenge")
	schemas["TwoFactorStatus"] = objectSchema(map[string]any{
		"available":              map[string]any{"type": "boolean", "description": "False when the server has no master key (JELEE_WEBHOOK_MASTER_KEY): enrollment answers 409 two_factor_unavailable and authenticator codes cannot be checked."},
		"enabled":                map[string]any{"type": "boolean"},
		"enabledAt":              instant,
		"pending":                map[string]any{"type": "boolean", "description": "An enrollment waits for its confirming code."},
		"recoveryCodesRemaining": map[string]any{"type": "integer", "minimum": 0, "maximum": domain.RecoveryCodeCount},
	}, "available", "enabled", "pending", "recoveryCodesRemaining")
	schemas["TwoFactorEnrollment"] = objectSchema(map[string]any{
		"secret": map[string]any{"type": "string", "description": "The 160-bit secret in unpadded base32, for manual entry. Returned only here."},
		"uri":    map[string]any{"type": "string", "description": "otpauth://totp URI (SHA1, 6 digits, 30 seconds, issuer Jelee) for a QR code. Returned only here."},
	}, "secret", "uri")
	schemas["TwoFactorCode"] = objectSchema(map[string]any{"code": totpCode}, "code")
	schemas["RecoveryCodes"] = objectSchema(map[string]any{"recoveryCodes": map[string]any{"type": "array", "minItems": domain.RecoveryCodeCount, "maxItems": domain.RecoveryCodeCount,
		"items": map[string]any{"type": "string"}, "description": "Shown once; the server keeps only digests. Each works once instead of an authenticator code."}}, "recoveryCodes")
	schemas["TwoFactorDisable"] = objectSchema(map[string]any{"password": map[string]any{"type": "string", "maxLength": 1024, "writeOnly": true}, "code": totpCode, "recoveryCode": recovery}, "password")
	schemas["AppPassword"] = objectSchema(map[string]any{"id": uuid, "name": stringSchema(domain.AppPasswordNameMax), "createdAt": instant,
		"lastUsedAt": map[string]any{"type": "string", "format": "date-time", "description": "Last login that used the password."}}, "id", "name", "createdAt")
	schemas["AppPasswordInput"] = objectSchema(map[string]any{"name": map[string]any{"type": "string", "minLength": 1, "maxLength": domain.AppPasswordNameMax, "description": "Label of the device or client; no surrounding spaces or control characters."}}, "name")
	schemas["NewAppPassword"] = objectSchema(map[string]any{"appPassword": schemaRef("AppPassword"),
		"password": map[string]any{"type": "string", "description": "160 random bits as eight groups of four characters. Shown once; use it instead of the account password in native and compatibility logins."}}, "appPassword", "password")
}

func twoFactorSpecification(paths map[string]any) {
	type route struct {
		path, method, summary, body, result, status string
		admin, public                               bool
	}
	webOnly := " Requires a web session (403 forbidden for native sessions, including those an application password issued)."
	routes := []route{
		{"/auth/login/second-factor", "post", "Complete a web login whose password verified for an account with a second factor (G07.8): exactly one of code (authenticator, ±1 time step, never the same step twice) or recoveryCode. Wrong codes count toward the account's login failure lock and the challenge's attempt limit, and draw from a separate per-address and per-challenge budget (429 auth_rate_limited). Issues the web session and cookie like /auth/login. 400 invalid_two_factor_code for a wrong or reused code; 401 login_challenge_invalid when the challenge is unknown, spent, expired or exhausted, or the password or factor changed since", "SecondFactorLogin", "SessionGrant", "200", false, true},
		{"/users/{id}/two-factor", "get", "Read the second factor state of self or, as administrator, of any user", "", "TwoFactorStatus", "200", false, false},
		{"/users/{id}/two-factor", "delete", "Reset a user's second factor and recovery codes for a user who lost them; application passwords stay; audited as user.two_factor_reset unless nothing was enrolled; empty body." + webOnly, "", "", "204", true, false},
		{"/users/me/two-factor/enroll", "post", "Start enrollment: a new 160-bit secret replaces any pending one and is returned once. 409 conflict when already enabled; 409 two_factor_unavailable without a master key." + webOnly, "Empty", "TwoFactorEnrollment", "200", false, false},
		{"/users/me/two-factor/confirm", "post", "Enable the pending factor with a code from the authenticator; returns ten recovery codes once and revokes every other session of the account; audited as user.two_factor_enabled. 400 invalid_two_factor_code for a wrong code; 409 conflict without a pending enrollment." + webOnly, "TwoFactorCode", "RecoveryCodes", "200", false, false},
		{"/users/me/two-factor/recovery-codes", "post", "Replace all recovery codes after a current authenticator code verifies; returns the new codes once; audited." + webOnly, "TwoFactorCode", "RecoveryCodes", "200", false, false},
		{"/users/me/two-factor/disable", "post", "Disable the second factor: the account password (400 invalid_password) and exactly one of a current code or an unused recovery code (400 invalid_two_factor_code); application passwords stay; audited as user.two_factor_disabled." + webOnly, "TwoFactorDisable", "", "204", false, false},
		{"/users/{id}/app-passwords", "get", "List application passwords of self or, as administrator, of any user; never the passwords", "", "AppPassword[]", "200", false, false},
		{"/users/me/app-passwords", "post", "Create an application password for a device or client that cannot ask for a second factor (native login, compatibility layer). The password is returned once; at most " + strconv.Itoa(domain.AppPasswordLimit) + " per user (409 conflict); audited." + webOnly, "AppPasswordInput", "NewAppPassword", "201", false, false},
		{"/users/{id}/app-passwords/{appPasswordId}", "delete", "Revoke an application password and every session it issued; self (web session only) or administrator; audited as user.app_password_revoked; empty body", "", "", "204", false, false},
	}
	for _, route := range routes {
		path := "/api/v1" + route.path
		op := operation(route.summary, route.status, "400", "401", "403", "404", "409", "413", "415", "429", "503")
		if !route.public {
			op["security"] = []any{map[string]any{"bearer": []string{}}}
		}
		if route.admin {
			op["x-jelee-role"] = "administrator"
		}
		params := []any{}
		if strings.Contains(path, "{id}") {
			params = append(params, idParameter())
		}
		if strings.Contains(path, "{appPasswordId}") {
			p := idParameter()
			p["name"] = "appPasswordId"
			params = append(params, p)
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
				data = map[string]any{"type": "array", "maxItems": domain.AppPasswordLimit, "items": schemaRef(strings.TrimSuffix(route.result, "[]"))}
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
	// A web login of an account with a second factor answers the challenge
	// instead of a session.
	if item, ok := paths["/api/v1/auth/login"].(map[string]any); ok {
		if op, ok := item["post"].(map[string]any); ok {
			op["summary"] = "Password login; issues a web session and sets the HttpOnly __Host-jelee_session cookie. For an account with a second factor (G07.8) the verified password yields a SecondFactorChallenge instead (no session, no cookie); complete it with POST /api/v1/auth/login/second-factor"
			op["responses"].(map[string]any)["200"] = map[string]any{"description": "Successful response; Cache-Control: no-store", "content": map[string]any{"application/json": map[string]any{"schema": objectSchema(map[string]any{
				"data": map[string]any{"oneOf": []any{schemaRef("SessionGrant"), schemaRef("SecondFactorChallenge")}},
			}, "data")}}}
		}
	}
	if item, ok := paths["/api/v1/auth/login/native"].(map[string]any); ok {
		if op, ok := item["post"].(map[string]any); ok {
			op["summary"] = op["summary"].(string) + ". An account with a second factor (G07.8) signs in here only with an application password; its account password is refused with 403 app_password_required after it verified"
		}
	}
}
