package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Test vectors are assembled at run time so this file never contains a
// literal that the scanner itself would report.
func join(parts ...string) string { return strings.Join(parts, "") }

var (
	randomA  = "q7Vx2LmN9pR4sT8wY1zB"             // 20 mixed characters
	randomB  = "Zk3Hf8Qw2Lp9Xr5Tn1Vb6Md4Gs7Jc0Ya" // 32 mixed characters
	pemBody  = strings.Repeat("MIIEvQIBADANBgkqhkiG9w0BAQEFAASC", 2)
	pemBegin = join("-----BEGIN ", "PRIVATE KEY-----")
)

func detect(t *testing.T, text string) []finding {
	t.Helper()
	return scanFile("sample.txt", []byte(text))
}

func rules(found []finding) string {
	var ids []string
	for _, f := range found {
		ids = append(ids, f.rule)
	}
	return strings.Join(ids, ",")
}

func TestDetectorsFindSecrets(t *testing.T) {
	cases := map[string]string{
		"private-key":               join("x\n", pemBegin, "\n", pemBody, "\n-----END PRIVATE KEY-----\n"),
		"json-private-key":          join(`{"private_key": "`, pemBegin, `\n`, pemBody, `"}`),
		"aws-access-key-id":         join("id = AK", "IAQ3EGRGSTUVWXYZ23\n"),
		"aws-secret-access-key":     join("aws_secret_access_key = ", randomB, "Ab3dEf7h\n"),
		"github-token":              join("gh", "p_", randomB, "Ab3d\n"),
		"github-fine-grained-token": join("github", "_pat_", randomB, randomB, "\n"),
		"gitlab-token":              join("gl", "pat-", randomA, "\n"),
		"slack-token":               join("xo", "xb-", "1234567890-", randomA, "\n"),
		"slack-webhook":             join("https://hooks.", "slack.com/services/T0001/B0002/", randomA, "\n"),
		"google-api-key":            join("AI", "za", randomB, "AbC\n"),
		"stripe-live-key":           join("s", "k_live_", randomB, "\n"),
		"npm-token":                 join("np", "m_", randomB, "Ab3d\n"),
		"telegram-bot-token":        join("123456789:A", "A", randomB, "x\n"),
		"anthropic-api-key":         join("s", "k-ant-", randomB, "\n"),
		"openai-api-key":            join("s", "k-proj-", randomB, "\n"),
		"jwt":                       join("ey", "JhbGciOiJIUzI1NiJ9.", "ey", "JzdWIiOiJqZWxlZSJ9.", randomA, "\n"),
		"url-credentials":           join("postgres://jelee:", randomA, "@db.internal/jelee\n"),
		"secret-assignment":         join(`clientSecret := "`, randomA, `"`, "\n"),
		"secret-env":                join("export WEBHOOK_SECRET=", randomA, "\n"),
	}
	for want, text := range cases {
		found := detect(t, text)
		if !strings.Contains(","+rules(found)+",", ","+want+",") {
			t.Errorf("%s not detected; got %q", want, rules(found))
		}
		for _, f := range found {
			if strings.Contains(f.String(), f.secret) {
				t.Errorf("%s: output %q contains the value", want, f.String())
			}
		}
	}
}

func TestDetectorsIgnoreLookalikes(t *testing.T) {
	cases := map[string]string{
		"bare pem header":    join(`"pem": "`, pemBegin, `",`),
		"placeholder dsn":    "postgres://user:pass@localhost/db and https://${USER}:${PASS}@h/x and postgres://u:<password>@h/db",
		"placeholder value":  join(`password = "`, "change-me-please-123", `"`),
		"low entropy":        `token = "aaaaaaaaaaaaaaaaaaaa"`,
		"single class":       `secret = "abcdefghijklmnopqrstuv"`,
		"identifier path":    `tokenFile = "internal/platform/token/store.go"`,
		"file name key":      `"internal/adapter/http/password_rate_test.go": "8bb4fbebb3bc663225c67f56ace494994f8ba248"`,
		"env reference":      "JELEE_DB_PASSWORD=${JELEE_DB_PASSWORD_FROM_VAULT}",
		"short env value":    "API_TOKEN=abc123",
		"url without secret": "https://example.com/a?b=c",
	}
	for name, text := range cases {
		if found := detect(t, text); len(found) != 0 {
			t.Errorf("%s: unexpected %q", name, rules(found))
		}
	}
}

func TestBinaryAndOversizedFilesAreSkipped(t *testing.T) {
	secret := join("gh", "p_", randomB, "Ab3d")
	if found := scanFile("a.bin", []byte("\x00"+secret)); len(found) != 0 {
		t.Fatal("binary content must be left to gitignore-check")
	}
	big := append(bytes.Repeat([]byte("a"), maxFileBytes), []byte(secret)...)
	if found := scanFile("big.txt", big); len(found) != 0 {
		t.Fatal("oversized content must be left to gitignore-check")
	}
}

func TestEntropy(t *testing.T) {
	if entropy("") != 0 || entropy("aaaa") != 0 || entropy("abcd") != 2 {
		t.Fatalf("entropy: %v %v %v", entropy(""), entropy("aaaa"), entropy("abcd"))
	}
}

func TestLineNumbersAndFingerprints(t *testing.T) {
	text := join("one\ntwo\nkey: gh", "p_", randomB, "Ab3d\n")
	found := detect(t, text)
	if len(found) != 1 || found[0].line != 3 || found[0].path != "sample.txt" {
		t.Fatalf("found %+v", found)
	}
	again := scanFile("other/place.go", []byte("\n\n\n\n"+text))
	if again[0].fingerprint() != found[0].fingerprint() {
		t.Fatal("fingerprints must not depend on path or line")
	}
	if len(found[0].fingerprint()) != 16 {
		t.Fatalf("fingerprint %q", found[0].fingerprint())
	}
}

func TestParseAllowlist(t *testing.T) {
	entries, err := parseAllowlist("# c\n\n0123456789abcdef url-credentials a/*.go fake dsn\n* jwt **/testdata/** fixtures\n")
	if err != nil || len(entries) != 2 {
		t.Fatalf("%+v %v", entries, err)
	}
	bad := map[string]string{
		"0123 jwt a reason":                 "is not 16 hex digits",
		"0123456789abcdef nope a reason":    "unknown rule",
		"* * a reason":                      "would hide everything",
		"0123456789abcdef jwt a":            "want",
		"0123456789ABCDEF jwt a/b.go fixed": "is not 16 hex digits",
	}
	for line, want := range bad {
		if _, err := parseAllowlist(line); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: error %v, want %q", line, err, want)
		}
	}
}

func TestGlob(t *testing.T) {
	cases := []struct {
		glob, path string
		want       bool
	}{
		{"a/*.go", "a/b.go", true},
		{"a/*.go", "a/b/c.go", false},
		{"**/testdata/**", "x/y/testdata/z/f.txt", true},
		{"**/testdata/**", "testdata/f.txt", true},
		{"a/?.go", "a/b.go", true},
		{"a.go", "a_go", false},
	}
	for _, tc := range cases {
		if got := globRegexp(tc.glob).MatchString(tc.path); got != tc.want {
			t.Errorf("%s ~ %s = %v", tc.glob, tc.path, got)
		}
	}
}

func TestFilter(t *testing.T) {
	f := finding{path: "a/b_test.go", line: 1, rule: "jwt", secret: "v"}
	entries, err := parseAllowlist(f.fingerprint() + " jwt a/*_test.go reviewed\n* url-credentials a/*.go reviewed\n")
	if err != nil {
		t.Fatal(err)
	}
	used := make([]bool, len(entries))
	other := finding{path: "a/b_test.go", rule: "jwt", secret: "different"}
	moved := finding{path: "b/b_test.go", rule: "jwt", secret: "v"}
	reported, allowed := filter([]finding{f, other, moved}, entries, used)
	if allowed != 1 || len(reported) != 2 || !used[0] || used[1] {
		t.Fatalf("reported %v allowed %d used %v", reported, allowed, used)
	}
}

func TestParsePatch(t *testing.T) {
	secret := join("gh", "p_", randomB, "Ab3d")
	patch := join("commit 0123456789012345678901234567890123456789\n",
		"diff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n@@ -10,0 +11,2 @@\n+first\n+key := \"", secret, "\"\n",
		"diff --git a/gone.go b/gone.go\n--- a/gone.go\n+++ /dev/null\n@@ -1 +0,0 @@\n-old ", secret, "\n",
		"commit 1111111111111111111111111111111111111111\n",
		"diff --git \"a/sp ace.txt\" \"b/sp ace.txt\"\n+++ \"b/sp ace.txt\"\n@@ -0,0 +1 @@\n+", secret, "\n")
	found, commits, err := parsePatch(strings.NewReader(patch))
	if err != nil || commits != 2 || len(found) != 2 {
		t.Fatalf("found %+v commits %d err %v", found, commits, err)
	}
	if found[0].path != "x.go" || found[0].line != 12 || found[0].commit[:4] != "0123" {
		t.Fatalf("first %+v", found[0])
	}
	if found[1].path != "sp ace.txt" || found[1].line != 1 || !strings.HasPrefix(found[1].String(), "commit 111111111111 sp ace.txt:1:") {
		t.Fatalf("second %+v %s", found[1], found[1])
	}
}

func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	git(t, root, "init", "-q")
	write(t, root, "tools/secretscan/allowlist.txt", "# none\n")
	write(t, root, "tools/secretscan/history-allowlist.txt", "# none\n")
	write(t, root, "a.go", "package a\n")
	git(t, root, "add", "-A")
	git(t, root, "commit", "-q", "-m", "chore: init")
	return root
}

func git(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}

func write(t *testing.T, root, name, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runIn(t *testing.T, root string, args ...string) (int, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := run(append([]string{"-root", root}, args...), &out, &errOut)
	return code, out.String() + errOut.String()
}

func TestRunModes(t *testing.T) {
	root := gitRepo(t)
	secret := join("gh", "p_", randomB, "Ab3d")
	if code, out := runIn(t, root); code != 0 {
		t.Fatalf("clean: %d %s", code, out)
	}
	// Staged: the index copy is scanned.
	write(t, root, "b.go", "package a\nvar k = \""+secret+"\"\n")
	git(t, root, "add", "b.go")
	code, out := runIn(t, root, "-staged")
	if code != 1 || !strings.Contains(out, "b.go:2: github-token") || strings.Contains(out, secret) {
		t.Fatalf("staged: %d %s", code, out)
	}
	git(t, root, "commit", "-q", "-m", "test: add b")
	// Removing the value from the tree does not remove it from history.
	write(t, root, "b.go", "package a\n")
	git(t, root, "commit", "-q", "-am", "test: drop b")
	if code, out := runIn(t, root); code != 0 {
		t.Fatalf("tracked after removal: %d %s", code, out)
	}
	code, out = runIn(t, root, "-range", "HEAD~2..HEAD")
	if code != 1 || !strings.Contains(out, "commits=2 findings=1") {
		t.Fatalf("range: %d %s", code, out)
	}
	if code, out = runIn(t, root, "-range", "HEAD~1..HEAD"); code != 0 {
		t.Fatalf("range without the commit: %d %s", code, out)
	}
	if code, out = runIn(t, root, "-history"); code != 1 || !strings.Contains(out, "github-token") {
		t.Fatalf("history: %d %s", code, out)
	}
	// The history allowlist accepts it in -history mode only.
	fp := finding{rule: "github-token", secret: secret}.fingerprint()
	write(t, root, "tools/secretscan/history-allowlist.txt", fp+" github-token b.go removed fake\n")
	if code, out = runIn(t, root, "-history"); code != 0 {
		t.Fatalf("history allowlisted: %d %s", code, out)
	}
	if code, out = runIn(t, root, "-range", "HEAD~2..HEAD"); code != 1 {
		t.Fatalf("range must not read the history allowlist: %d %s", code, out)
	}
	// A history entry that matches nothing is stale in -history mode.
	write(t, root, "tools/secretscan/history-allowlist.txt", fp+" github-token other.go wrong path\n")
	if code, out = runIn(t, root, "-history"); code != 1 || !strings.Contains(out, "history-allowlist.txt line 1") {
		t.Fatalf("history stale: %d %s", code, out)
	}
	// Allowlisting the fingerprint in the main list accepts it in every mode.
	write(t, root, "tools/secretscan/history-allowlist.txt", "# none\n")
	write(t, root, "tools/secretscan/allowlist.txt", fp+" github-token b.go reviewed fake\n")
	if code, out = runIn(t, root, "-range", "HEAD~2..HEAD"); code != 0 {
		t.Fatalf("range allowlisted: %d %s", code, out)
	}
	// The full tracked scan reports the main entry that no longer matches.
	if code, out = runIn(t, root); code != 1 || !strings.Contains(out, "allowlist.txt line 1") {
		t.Fatalf("stale: %d %s", code, out)
	}
}

func TestRunErrors(t *testing.T) {
	root := gitRepo(t)
	for _, args := range [][]string{{"-staged", "-history"}, {"-nope"}, {"-allowlist", "missing.txt"}, {"-range", "nope..HEAD"}} {
		if code, out := runIn(t, root, args...); code != 2 {
			t.Errorf("%v: exit %d %s", args, code, out)
		}
	}
	write(t, root, "tools/secretscan/history-allowlist.txt", "bad\n")
	if code, _ := runIn(t, root, "-history"); code != 2 {
		t.Errorf("bad history allowlist: exit %d", code)
	}
	if code, _ := runIn(t, root, "-history", "-history-allowlist", "missing.txt"); code != 2 {
		t.Errorf("missing history allowlist: exit %d", code)
	}
	write(t, root, "tools/secretscan/allowlist.txt", "bad\n")
	if code, _ := runIn(t, root); code != 2 {
		t.Errorf("bad allowlist: exit %d", code)
	}
}

// TestRepositoryAllowlistsParse keeps the committed allowlists valid.
func TestRepositoryAllowlistsParse(t *testing.T) {
	for _, name := range []string{"allowlist.txt", "history-allowlist.txt"} {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := parseAllowlist(string(data)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestIdentifierValuesAreNotSecrets(t *testing.T) {
	for _, v := range []string{"WindowsServiceIntro2", "ButtonTermsOfService", "Access-Control-Allow-Credentials"} {
		if likelySecret("token", v) {
			t.Errorf("%s accepted as a secret", v)
		}
	}
	if !likelySecret("token", randomA) || likelySecret("internal/a_test.go", randomA) {
		t.Error("name handling")
	}
	if likelySecret("password", `\u5bc6\u7801\u91cd\u7f6e1`) || likelySecret("password", "密码重置密码重置密码重置密码重置1a") {
		t.Error("translated text accepted as a secret")
	}
}
