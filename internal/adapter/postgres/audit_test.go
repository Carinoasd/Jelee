package postgres

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// Every event literal passed to an audit writer must be whitelisted, and the
// raw table may only be written by appendAudit.
func TestAuditEventWhitelistCoversSourceAndSingleWriter(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	literal := regexp.MustCompile(`(?:auditAccount\([^,]+,[^,]+,[^,]+,\s*|Event:\s*|event\s*:?=\s*)"([a-z_.]+)"`)
	insert := regexp.MustCompile(`(?i)INSERT\s+INTO\s+audit_logs`)
	seen := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range literal.FindAllStringSubmatch(string(data), -1) {
			seen++
			if _, ok := auditCategory(m[1]); !ok {
				t.Errorf("%s: audit event %q is not whitelisted", name, m[1])
			}
		}
		if name != "audit.go" && len(insert.FindAllString(string(data), -1)) != 0 {
			t.Errorf("%s writes audit_logs directly instead of through appendAudit", name)
		}
	}
	if seen < 30 {
		t.Fatalf("audit event scan found only %d literals", seen)
	}
	format := regexp.MustCompile(`^[a-z][a-z_]*(\.[a-z][a-z_]*)+$`)
	for event, category := range auditEvents {
		if len(event) > 64 || !format.MatchString(event) || category != domain.AuditCategoryAudit && category != domain.AuditCategorySecurity {
			t.Errorf("whitelisted event %q violates the stored contract", event)
		}
	}
	if c, _ := auditCategory("login.failed"); c != domain.AuditCategorySecurity {
		t.Fatal("login failures must be security events")
	}
}

func TestAuditStateRedactsSecrets(t *testing.T) {
	type nested struct {
		Name        string `json:"name"`
		AccessToken string `json:"access_token"`
	}
	in := map[string]any{
		"passwordChanged": true,
		"newPassword":     "hunter2-secret",
		"PasswordHash":    "plain",
		"token":           "tok-123",
		"webhook-secret":  "whs-1",
		"apiKey":          "key-1",
		"Authorization":   "Bearer abc",
		"cookie":          "sid=1",
		"items":           []any{nested{Name: "Basic Instinct", AccessToken: "tok-456"}, "$argon2id$v=19$m=1$abc"},
		"count":           json.Number("12345678901234567890"),
		"idempotencyKey":  "keep-me",
	}
	out, err := auditState(in)
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	for _, secret := range []string{"hunter2", "plain", "tok-123", "whs-1", "key-1", "Bearer", "sid=1", "tok-456", "argon2"} {
		if strings.Contains(text, secret) {
			t.Fatalf("audit state leaked %q", secret)
		}
	}
	for _, kept := range []string{`"passwordChanged":true`, `"Basic Instinct"`, `12345678901234567890`, `"keep-me"`} {
		if !strings.Contains(text, kept) {
			t.Fatalf("audit state lost safe value %s", kept)
		}
	}
	if empty, err := auditState(nil); err != nil || string(empty) != "{}" {
		t.Fatal("nil state must stay an empty object")
	}
}

func TestAuditStateBoundsSize(t *testing.T) {
	small, err := auditState(map[string]string{"overview": strings.Repeat("a", auditStateLimit-64)})
	if err != nil || len(small) > auditStateLimit || strings.Contains(string(small), "truncated") {
		t.Fatal("state under the limit must be stored unchanged")
	}
	large, err := auditState(map[string]string{"overview": strings.Repeat("a", auditStateLimit)})
	if err != nil {
		t.Fatal(err)
	}
	var marker map[string]any
	if err = json.Unmarshal(large, &marker); err != nil || marker["truncated"] != true || len(marker["sha256"].(string)) != 64 || marker["bytes"].(float64) <= auditStateLimit {
		t.Fatalf("oversized state was not replaced by a bounded marker: %s", large)
	}
	if _, err = auditState(func() {}); err == nil {
		t.Fatal("unencodable state must fail")
	}
}

func TestAuditRequestIDAndCursorValidation(t *testing.T) {
	for id, want := range map[string]bool{"0123abcd": true, "a.b_c:d-e": true, "": false, strings.Repeat("a", 129): false, "bad id": false, "x\n": false, "é": false} {
		if validAuditRequestID(id) != want {
			t.Errorf("request id %q validity", id)
		}
	}
	for cursor, want := range map[string]bool{"": true, "1": true, "42": true, "0": false, "-1": false, "01": false, "+1": false, "x": false, "99999999999999999999": false} {
		if _, ok := parseAuditCursor(cursor); ok != want {
			t.Errorf("cursor %q validity", cursor)
		}
	}
	if validAuditFilter(domain.AuditFilter{Category: "debug"}) || validAuditFilter(domain.AuditFilter{Event: "user.unknown"}) || validAuditFilter(domain.AuditFilter{ActorID: "x"}) || !validAuditFilter(domain.AuditFilter{Category: "security", Event: "login.failed", Target: "audit_retention"}) {
		t.Fatal("audit filter validation")
	}
}
