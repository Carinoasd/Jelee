package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
)

// devRecord logs one developer mode SQL and one body record through a
// production router and returns the decoded records.
func devRecords(t *testing.T, sqlOn, bodyOn *atomic.Bool, connect bool, emit func(*slog.Logger)) []map[string]any {
	t.Helper()
	var buf bytes.Buffer
	r, err := Open(Options{}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if connect {
		r.SetDeveloperLogging(sqlOn.Load, bodyOn.Load)
	}
	emit(r.Logger())
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("line %q: %v", line, err)
		}
		out = append(out, rec)
	}
	return out
}

const devSQLSample = "SELECT id FROM accounts WHERE name = $1; SET x.dsn = 'postgres://jelee:pg-pass-777@db/jelee'; ALTER ROLE r PASSWORD 'role-pass-888'; -- Authorization: Bearer abc.def.ghi password=pw-999 jdm_0123abcd"

func emitDev(l *slog.Logger) {
	l.Info("developer mode SQL log", "component", "db", "code", "devmode_sql_log", "statement", DeveloperSQL(devSQLSample),
		"durationMicros", int64(42), "rows", int64(3), "failed", false)
	l.Info("developer mode body log", "component", "http", "code", "devmode_body_log", "method", "POST", "route", "/api/v1/auth/login", "status", 200,
		"requestBody", DeveloperBody(`{"name":"alice","password":"hunter2-pw","code":"123456","recoveryCode":"rc-1111","nested":{"apiKey":"k-222","title":"Movie"}}`),
		"responseBody", DeveloperBody(`{"data":{"accessToken":"at-333","uri":"otpauth://totp/Jelee:alice?secret=TOTPSECRET444","note":"see postgres://u:conn-555@h/db"},"error":{"code":"not_found"}}`))
}

var devSecrets = []string{"pg-pass-777", "role-pass-888", "abc.def.ghi", "pw-999", "jdm_0123abcd", "hunter2-pw", "123456", "rc-1111", "k-222", "at-333", "TOTPSECRET444", "conn-555"}

func TestDeveloperFieldsVisibleWhileSwitchedOn(t *testing.T) {
	var sqlOn, bodyOn atomic.Bool
	sqlOn.Store(true)
	bodyOn.Store(true)
	recs := devRecords(t, &sqlOn, &bodyOn, true, emitDev)
	if len(recs) != 2 {
		t.Fatalf("records: %v", recs)
	}
	sql, body := recs[0], recs[1]
	if sql["code"] != "devmode_sql_log" || sql["durationMicros"] != float64(42) || sql["rows"] != float64(3) || sql["failed"] != false {
		t.Fatalf("sql fields: %v", sql)
	}
	statement, _ := sql["statement"].(string)
	if !strings.Contains(statement, "SELECT id FROM accounts WHERE name = $1") || !strings.Contains(statement, "ALTER ROLE r PASSWORD") {
		t.Fatalf("statement not readable: %q", statement)
	}
	if body["code"] != "devmode_body_log" || body["route"] != "/api/v1/auth/login" {
		t.Fatalf("body fields: %v", body)
	}
	request, _ := body["requestBody"].(string)
	response, _ := body["responseBody"].(string)
	if !strings.Contains(request, `"name":"alice"`) || !strings.Contains(request, `"title":"Movie"`) || !strings.Contains(response, `"code":"not_found"`) {
		t.Fatalf("bodies not readable: %s / %s", request, response)
	}
	all, _ := json.Marshal(recs)
	for _, secret := range devSecrets {
		if bytes.Contains(all, []byte(secret)) {
			t.Errorf("secret %q logged: %s", secret, all)
		}
	}
}

func TestDeveloperFieldsRedactedWhileSwitchedOff(t *testing.T) {
	RegisterRoutePatterns("/api/v1/auth/login")
	var sqlOn, bodyOn atomic.Bool
	for _, connect := range []bool{false, true} {
		recs := devRecords(t, &sqlOn, &bodyOn, connect, emitDev)
		all, _ := json.Marshal(recs)
		for i, keys := range [][]string{{"code", "statement", "durationMicros", "rows", "failed"}, {"code", "requestBody", "responseBody"}} {
			for _, key := range keys {
				if recs[i][key] != redacted {
					t.Errorf("connected=%v: %s = %v while off", connect, key, recs[i][key])
				}
			}
		}
		// The route pattern is an access log field (G46.3) and stays
		// readable; the bodies do not.
		if recs[1]["route"] != "/api/v1/auth/login" {
			t.Errorf("connected=%v: route = %v", connect, recs[1]["route"])
		}
		for _, needle := range append(devSecrets, "accounts", "alice") {
			if bytes.Contains(all, []byte(needle)) {
				t.Errorf("connected=%v: %q logged while off", connect, needle)
			}
		}
	}
	// Each switch opens only its own fields.
	sqlOn.Store(true)
	recs := devRecords(t, &sqlOn, &bodyOn, true, emitDev)
	if recs[0]["statement"] == redacted || recs[1]["requestBody"] != redacted || recs[1]["code"] != redacted {
		t.Fatalf("sql switch opened body fields: %v", recs)
	}
}

func TestDeveloperFieldsNeedTheMarker(t *testing.T) {
	var on atomic.Bool
	on.Store(true)
	recs := devRecords(t, &on, &on, true, func(l *slog.Logger) {
		// Plain strings under developer keys, and a body marker under the
		// statement key, are refused even while the switches are on.
		l.Info("other", "statement", "SELECT secret_plain", "requestBody", "plain-body", "responseBody", DeveloperSQL("SELECT 1"),
			"rows", "3", "failed", "no", "route", "/a b?token=x", "other", DeveloperBody("{}"))
	})
	for _, key := range []string{"statement", "requestBody", "responseBody", "rows", "failed", "route", "other"} {
		if recs[0][key] != redacted {
			t.Errorf("%s = %v", key, recs[0][key])
		}
	}
	// A plain handler never renders the marked text.
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("x", "statement", DeveloperSQL("SELECT hidden"))
	if strings.Contains(buf.String(), "hidden") || !strings.Contains(buf.String(), redacted) {
		t.Fatalf("plain handler: %s", buf.String())
	}
	// Offline bundles have no switch: developer fields stay redacted.
	off := NewRedactor(IPRedact, PathRedact, nil)
	if got := off.Attr(slog.Any("statement", DeveloperSQL("SELECT 1"))); got.Value.String() != redacted {
		t.Fatalf("offline: %v", got)
	}
}

func TestMaskSecrets(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`{"code":"123456"}`, `{"code":"[redacted]"}`},
		{`{"code":123456}`, `{"code":"[redacted]"}`},
		{`{"code":"bad_request"}`, `{"code":"bad_request"}`},
		{`{"totpCode":"1","otp":"2","recovery_codes":["a"]}`, `{"otp":"[redacted]","recovery_codes":"[redacted]","totpCode":"[redacted]"}`},
		{`{"hash":"$argon2id$v=19$x"}`, `{"hash":"[redacted]"}`},
		{`{"h":"Bearer abc"}`, `{"h":"[redacted]"}`},
		{`{"Set-Cookie":"s","Authorization":"a","connection_string":"c"}`, `{"Authorization":"[redacted]","Set-Cookie":"[redacted]","connection_string":"[redacted]"}`},
		{`{"n":12345678901234567890}`, `{"n":12345678901234567890}`},
	} {
		if got := maskBody(tc.in); got != tc.want {
			t.Errorf("%s: %s", tc.in, got)
		}
	}
	for _, tc := range []struct{ in, want string }{
		{"postgres://u:p@h/db?sslmode=disable", "postgres://[redacted]@h/db?sslmode=disable"},
		{"host=h password=secret user=u", "host=h password=[redacted] user=u"},
		{`api_key: "abc"`, `api_key: [redacted]`},
		{"WHERE token_hash = $1", "WHERE token_hash = $1"},
		{"Authorization: Basic dXNlcg==", "Authorization: [redacted] [redacted]"},
		{"SELECT 'plain' FROM t", "SELECT 'plain' FROM t"},
	} {
		if got := MaskSecretText(tc.in); got != tc.want {
			t.Errorf("%q: %q", tc.in, got)
		}
	}
}
