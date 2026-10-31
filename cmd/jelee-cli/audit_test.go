package main

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeArchive(t *testing.T, dir, name string, lines ...string) {
	t.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	for _, l := range lines {
		_, _ = w.Write([]byte(l + "\n"))
	}
	_ = w.Close()
	if err := os.WriteFile(filepath.Join(dir, name), b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAuditArchiveCLI(t *testing.T) {
	dir := t.TempDir()
	rec := func(id string) string {
		return `{"id":` + id + `,"category":"audit","event":"user.created","before":null,"after":null,"occurredAt":"2026-01-01T01:00:00Z"}`
	}
	writeArchive(t, dir, "audit-audit-20260101-1-2.ndjson.gz", rec("1"), rec("2"))
	run := func(env map[string]string, args ...string) (int, string, string) {
		var out, errs bytes.Buffer
		code := runAuditCLIWith(args, &out, &errs, func(k string) (string, bool) { v, ok := env[k]; return v, ok })
		return code, out.String(), errs.String()
	}
	if code, out, _ := run(nil, "archive", "list", "--dir", dir); code != 0 || !strings.Contains(out, "audit-audit-20260101-1-2.ndjson.gz") {
		t.Fatalf("list: %d %s", code, out)
	}
	env := map[string]string{"JELEE_AUDIT_ARCHIVE_DIR": dir}
	if code, out, _ := run(env, "archive", "verify"); code != 0 || !strings.Contains(out, "1 files, 2 rows, 0 damaged") {
		t.Fatalf("verify: %d %s", code, out)
	}
	if code, out, _ := run(env, "archive", "verify", "--json"); code != 0 || !strings.Contains(out, `"rows": 2`) {
		t.Fatalf("verify json: %d %s", code, out)
	}
	if code, out, _ := run(env, "archive", "list", "--json"); code != 0 || !strings.Contains(out, `"firstId": 1`) {
		t.Fatalf("list json: %d %s", code, out)
	}
	// A damaged file makes verify exit 3 and say which.
	writeArchive(t, dir, "audit-audit-20260102-1-3.ndjson.gz", rec("1"))
	if code, out, _ := run(env, "archive", "verify"); code != auditArchiveDamagedExit || !strings.Contains(out, "DAMAGED") || !strings.Contains(out, "1 damaged") {
		t.Fatalf("damaged verify: %d %s", code, out)
	}
	empty := t.TempDir()
	if code, out, _ := run(nil, "archive", "list", "--dir", empty, "--json"); code != 0 || strings.TrimSpace(out) != "[]" {
		t.Fatalf("empty json: %d %q", code, out)
	}
	for _, args := range [][]string{nil, {"archive"}, {"archive", "export"}, {"export", "list"}, {"archive", "list", "extra"}, {"archive", "list", "--bogus"}, {"archive", "list"}} {
		if code, _, errs := run(nil, args...); code != 2 || errs == "" {
			t.Errorf("%v: code %d", args, code)
		}
	}
	if code, _, _ := run(nil, "archive", "list", "--dir", filepath.Join(dir, "missing")); code != 1 {
		t.Fatalf("missing dir code %d", code)
	}
}
