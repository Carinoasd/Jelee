package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

const fixtureTable = `package httpapi

var errorCodeStatuses = map[string][]int{
	"conflict":         {409},
	"feature_removed":  {501},
	"job_busy":         {409},
	"not_found":        {404},
	"never_mentioned":  {503},
}
`

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		file := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func baseTree(extra map[string]string) map[string]string {
	files := map[string]string{
		defaultErrorTable: fixtureTable,
		"README.md":       "# Project\n\nSee [guide](docs/guide.md) and [setup](docs/guide.md#安装与升级).\n",
		"docs/guide.md": strings.Join([]string{
			"# Guide",
			"",
			"## 安装与升级",
			"## Repeated",
			"## Repeated",
			"Setext Title",
			"------------",
			"## `code` *and* [link](other.md) (G37.3)",
			`<a id="custom-anchor"></a>`,
			"",
			"[self](#安装与升级) [dup](#repeated-1) [setext](#setext-title) [mixed](#code-and-link-g373) [custom](#custom-anchor)",
			"[dir](../deploy) [file](../Makefile) [line](../Makefile#L3) [ext](https://example.com/a?b=c#d) [mail](mailto:a@example.com)",
			"[spaced](<other.md>) [escaped](other%2Emd) [titled](other.md \"Title\") [root](/docs/other.md)",
			"[ref]: other.md#second",
			"",
			"`[not a link](missing.md)`",
			"```",
			"[not a link either](missing.md)",
			"```",
			"",
			"Returns `job_busy`/409, `conflict`（409） or HTTP 501，`feature_removed`.",
			"Job failure code `observer_unavailable` has no HTTP status and is not audited.",
		}, "\n"),
		"docs/other.md": "# Other\n\n## Second\n",
		"deploy/x.conf": "x\n",
		"Makefile":      "all:\n",
	}
	for name, content := range extra {
		files[name] = content
	}
	return files
}

func runCheck(t *testing.T, root string) report {
	t.Helper()
	codes, err := loadErrorCodes(filepath.Join(root, filepath.FromSlash(defaultErrorTable)))
	if err != nil {
		t.Fatal(err)
	}
	out, err := check(root, codes)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func kinds(findings []finding) []string {
	var out []string
	for _, f := range findings {
		out = append(out, f.kind+" "+f.target)
	}
	sort.Strings(out)
	return out
}

func TestCleanTreePasses(t *testing.T) {
	root := writeTree(t, baseTree(nil))
	got := runCheck(t, root)
	if len(got.findings) != 0 {
		t.Fatalf("unexpected findings: %v", got.findings)
	}
	if !reflect.DeepEqual(got.undocumented, []string{"never_mentioned", "not_found"}) {
		t.Fatalf("undocumented = %v", got.undocumented)
	}
}

func TestReportsBrokenLinksAnchorsAndExternalFormat(t *testing.T) {
	root := writeTree(t, baseTree(map[string]string{
		"docs/sub/bad.md": strings.Join([]string{
			"# Bad",
			"[missing](missing.md) [anchor](../other.md#nope) [self](#absent)",
			"[escape](../../../outside.md) [win](C:\\docs\\x.md) [ftp](ftp://example.com/x)",
			"[nohost](https://) [proto](//example.com/x) [mail](mailto:nobody)",
			"<https://example.com/ok> [ref]: gone.md",
			"[ok](../guide.md#repeated) [case](../other.md#Second)",
		}, "\n"),
	}))
	got := kinds(runCheck(t, root).findings)
	want := []string{
		"bad-external-link //example.com/x",
		"bad-external-link C:\\docs\\x.md",
		"bad-external-link ftp://example.com/x",
		"bad-external-link https://",
		"bad-external-link mailto:nobody",
		"broken-anchor #absent",
		"broken-anchor ../other.md#nope",
		"broken-link ../../../outside.md",
		"broken-link missing.md",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("findings:\n got %q\nwant %q", got, want)
	}
}

func TestReferenceDefinitionsAreChecked(t *testing.T) {
	root := writeTree(t, baseTree(map[string]string{"docs/ref.md": "# Ref\n\n[x][y]\n\n[y]: gone.md\n"}))
	got := kinds(runCheck(t, root).findings)
	if !reflect.DeepEqual(got, []string{"broken-link gone.md"}) {
		t.Fatalf("findings = %q", got)
	}
}

func TestErrorCodeClaims(t *testing.T) {
	root := writeTree(t, baseTree(map[string]string{
		"docs/codes.md": strings.Join([]string{
			"# Codes",
			"`not_found`/404 is fine; `job_busy`（503） has the wrong status.",
			"HTTP 403，`client_blocked` is not implemented.",
			"```json",
			`{"error":{"code":"made_up","message":"x"}}`,
			"```",
		}, "\n"),
		// Requirement quotes describe planned behaviour and are skipped.
		"docs/requirements-source.md": "# Req\n\n`client_blocked`，HTTP 403\n",
	}))
	got := runCheck(t, root)
	if want := []string{"error-code-status job_busy/503", "unknown-error-code client_blocked", "unknown-error-code made_up"}; !reflect.DeepEqual(kinds(got.findings), want) {
		t.Fatalf("findings:\n got %q\nwant %q", kinds(got.findings), want)
	}
	if !reflect.DeepEqual(got.undocumented, []string{"never_mentioned"}) {
		t.Fatalf("undocumented = %v", got.undocumented)
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"8. 忽略格式來源補充（2026-10-01）":           "8-忽略格式來源補充2026-10-01",
		"Hosting the Web Client Separately": "hosting-the-web-client-separately",
		"NFO 旁车文件":                          "nfo-旁车文件",
		"G37.3 反向代理：Nginx／Caddy":            "g373-反向代理nginxcaddy",
		"snake_case & `code`":               "snake_case--code",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBaselineToleratesKnownAndRejectsNewAndStale(t *testing.T) {
	root := writeTree(t, baseTree(map[string]string{"docs/bad.md": "# Bad\n[a](gone.md)\n"}))
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-root", root}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "broken-link") {
		t.Fatalf("without baseline: exit %d, stderr %s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"-root", root, "-update-baseline"}, &stdout, &stderr); code != 0 {
		t.Fatalf("update: exit %d, %s", code, stderr.String())
	}
	stdout.Reset()
	if code := run([]string{"-root", root}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "1 tolerated") {
		t.Fatalf("with baseline: exit %d, stdout %s stderr %s", code, stdout.String(), stderr.String())
	}
	// A second identical breakage is new even though its key is known.
	if err := os.WriteFile(filepath.Join(root, "docs/bad.md"), []byte("# Bad\n[a](gone.md)\n[b](gone.md)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stderr.Reset()
	if code := run([]string{"-root", root}, &stdout, &stderr); code != 1 {
		t.Fatalf("duplicate breakage accepted: %s", stderr.String())
	}
	// Fixing the document makes the baseline entry stale, which also fails.
	if err := os.WriteFile(filepath.Join(root, "docs/bad.md"), []byte("# Bad\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stderr.Reset()
	if code := run([]string{"-root", root}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "stale baseline entry") {
		t.Fatalf("stale baseline accepted: exit %d, %s", code, stderr.String())
	}
}

func TestLoadErrorCodesRejectsNonLiteralTables(t *testing.T) {
	root := writeTree(t, map[string]string{defaultErrorTable: "package httpapi\n\nvar errorCodeStatuses = build()\n"})
	if _, err := loadErrorCodes(filepath.Join(root, filepath.FromSlash(defaultErrorTable))); err == nil {
		t.Fatal("non-literal table accepted")
	}
}

// TestRepositoryDocuments runs the gate on the real tree, so `go test` alone
// also rejects new broken links or unknown error codes.
func TestRepositoryDocuments(t *testing.T) {
	root := filepath.Join("..", "..")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-root", root}, &stdout, &stderr); code != 0 {
		t.Fatalf("doccheck failed on the repository:\n%s", stderr.String())
	}
}
