package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const sampleIgnore = `## Sample
## 1. Build
# Build output.
bin/
## 2. Tools
# Tools.
.tools
## 3. Tests
# Reports.
reports/
## 4. Runtime
# Logs.
*.log
## 5. Secrets
# Env.
.env
# Example stays.
!.env.example
## 6. Databases
# Databases.
*.db
## 7. IDE
# Editor.
.idea/
## 8. Temp
# Temp.
tmp/
`

func TestParseGitignoreAcceptsCommentedGroups(t *testing.T) {
	rules, problems := parseGitignore(sampleIgnore)
	if len(problems) != 0 {
		t.Fatalf("problems: %v", problems)
	}
	if len(rules) != 9 || rules[0].pattern != "bin/" || rules[5].pattern != "!.env.example" {
		t.Fatalf("rules: %+v", rules)
	}
}

func TestParseGitignoreReportsStructureProblems(t *testing.T) {
	cases := map[string]struct {
		text string
		want string
	}{
		"uncommented":   {strings.Replace(sampleIgnore, "# Logs.\n", "", 1), `pattern "*.log" needs its own comment`},
		"blank between": {strings.Replace(sampleIgnore, "# Logs.\n", "# Logs.\n\n", 1), `pattern "*.log" needs its own comment`},
		"outside group": {"# Lone.\nlone/\n" + sampleIgnore, `pattern "lone/" is outside the numbered groups`},
		"duplicate":     {sampleIgnore + "# Again.\nbin/\n", `pattern "bin/" repeats line`},
		"order":         {strings.Replace(sampleIgnore, "## 3. Tests", "## 4. Tests", 1), "group 4 follows group 2"},
		"missing group": {sampleIgnore[:strings.Index(sampleIgnore, "## 8.")], "expected the eight G01.4 groups, found 7"},
		"empty comment": {strings.Replace(sampleIgnore, "# Logs.\n", "#\n", 1), `pattern "*.log" needs its own comment`},
	}
	for name, tc := range cases {
		_, problems := parseGitignore(tc.text)
		if !strings.Contains(strings.Join(problems, "\n"), tc.want) {
			t.Errorf("%s: want %q in %v", name, tc.want, problems)
		}
	}
}

func TestUndocumented(t *testing.T) {
	rules, _ := parseGitignore(sampleIgnore)
	doc := "`bin/` `.tools` `reports/` `*.log` `.env` `!.env.example` `*.db` `.idea/`"
	if got := undocumented(rules, doc); len(got) != 1 || got[0] != "tmp/" {
		t.Fatalf("undocumented = %v", got)
	}
}

func TestParseProbes(t *testing.T) {
	probes, err := parseProbes("# c\n\nignored bin/x\nkept  src/a.go\r\n")
	if err != nil || len(probes) != 2 || !probes[0].ignored || probes[1].ignored || probes[1].path != "src/a.go" || probes[1].line != 4 {
		t.Fatalf("probes %+v, %v", probes, err)
	}
	for _, bad := range []string{"maybe x\n", "ignored\n", "kept \n"} {
		if _, err := parseProbes(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestClassify(t *testing.T) {
	large := bytes.Repeat([]byte("a"), largeFileLimit+1)
	keyBody := "-----BEGIN " + "PRIVATE KEY-----\n" + strings.Repeat("QUJD", 16) + "\n-----END PRIVATE KEY-----\n"
	cases := []struct {
		path string
		exec bool
		data []byte
		want string
	}{
		{"internal/a.go", false, []byte("package a\n"), ""},
		{"web/e2e/a.png", false, []byte("\x89PNG\r\n\x1a\n\x00\x00"), "binary"},
		{"api/openapi.json", false, large, "large"},
		{"fixtures/clip.MP4", false, []byte("text"), "media"},
		{"x.tar", false, []byte("text"), "archive"},
		{"legacy/jellyfin.db", false, []byte("text"), "database"},
		{"certs/server.pem", false, []byte("text"), "key"},
		{"tool", false, []byte("\x7fELF\x02\x01\x01\x00"), "binary,executable"},
		{"tool.exe", false, []byte("MZ\x90\x00"), "binary,executable"},
		{"README.md", false, []byte("MZ is just text"), ""},
		{"scripts/run", true, []byte("#!/bin/sh\necho\n"), ""},
		{"scripts/run", true, []byte("echo\n"), "executable"},
		{".tools/go/bin/go", false, []byte("x"), "generated"},
		{"data/images/a", false, []byte("x"), "generated"},
		{"internal/data/a.go", false, []byte("x"), ""},
		{"secrets/k.txt", false, []byte(keyBody), "private-key"},
		{"redact_test.go", false, []byte(`"pem": "-----BEGIN ` + `PRIVATE KEY-----",`), ""},
	}
	for _, tc := range cases {
		if got := strings.Join(classify(tc.path, tc.exec, tc.data), ","); got != tc.want {
			t.Errorf("classify(%s) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

const sampleAllowlist = "# Title\n\n| 类型 | 判定 |\n| --- | --- |\n| `binary` | NUL |\n\n" + allowlistHeading + "\n\n" +
	"| 路径模式 | 类型 | 上限 | 数量 | 用途 | 来源 |\n| --- | --- | --- | --- | --- | --- |\n" +
	"| `shots/*.png` | binary | 1,000 | 2 个 | baselines | review |\n" +
	"| `api/spec.json` | large, binary | 4194304 | 1 个 | spec | generated |\n\n## Later\n\n| `ignored/*` | binary | 1 | x | y | z |\n"

func TestParseAllowlist(t *testing.T) {
	entries, problems := parseAllowlist(sampleAllowlist)
	if len(problems) != 0 || len(entries) != 2 {
		t.Fatalf("entries %+v problems %v", entries, problems)
	}
	if entries[0].maxBytes != 1000 || !entries[1].kinds["large"] || !entries[1].kinds["binary"] {
		t.Fatalf("entries %+v", entries)
	}
	bad := map[string]string{
		"| `a/*` | generated | 10 | 1 | p | s |": `kind "generated" cannot be allowlisted`,
		"| `a/*` | binary | ten | 1 | p | s |":   "is not a positive integer",
		"| `a/*` | binary | 0 | 1 | p | s |":     "is not a positive integer",
		"| `a/*` | binary | 10 |  | p | s |":     "must not be empty",
		"| `a/*` | binary | 10 |":                "want 6 columns",
		"| `a/[` | binary | 10 | 1 | p | s |":    "bad pattern",
	}
	for row, want := range bad {
		_, problems := parseAllowlist(allowlistHeading + "\n" + row + "\n")
		if !strings.Contains(strings.Join(problems, "\n"), want) {
			t.Errorf("%s: want %q in %v", row, want, problems)
		}
	}
	if _, problems := parseAllowlist("| `a` | binary | 1 | 1 | p | s |\n"); len(problems) != 1 || !strings.Contains(problems[0], "missing section") {
		t.Errorf("missing section: %v", problems)
	}
}

func TestMatchAllowlist(t *testing.T) {
	entries, _ := parseAllowlist(sampleAllowlist)
	cases := []struct {
		file, kind string
		size       int64
		want       int
	}{
		{"shots/a.png", "binary", 1000, 0},
		{"shots/a.png", "binary", 1001, -1},
		{"shots/a.png", "large", 10, -1},
		{"shots/sub/a.png", "binary", 10, -1},
		{"api/spec.json", "large", 2 << 20, 1},
		{"other.png", "binary", 1, -1},
	}
	for _, tc := range cases {
		if got := matchAllowlist(entries, tc.file, tc.kind, tc.size); got != tc.want {
			t.Errorf("match(%s,%s,%d) = %d, want %d", tc.file, tc.kind, tc.size, got, tc.want)
		}
	}
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

func TestEvaluateProbes(t *testing.T) {
	requireGit(t)
	rules, _ := parseGitignore(sampleIgnore)
	probes, err := parseProbes("ignored bin/x\nignored .tools/go\nignored reports/a\nignored a.log\nignored .env\nkept .env.example\n" +
		"ignored a.db\nignored .idea/x\nignored tmp/x\nkept src/a.go\n")
	if err != nil {
		t.Fatal(err)
	}
	results, err := evaluateProbes(sampleIgnore, probes)
	if err != nil {
		t.Fatal(err)
	}
	if problems := probeProblems(rules, probes, results); len(problems) != 0 {
		t.Fatalf("problems: %v", problems)
	}
	if results[5].ignored || results[5].pattern != "!.env.example" || results[9].ruleLine != 0 {
		t.Fatalf("results %+v", results)
	}
	// A wrong expectation and a rule that decides nothing are both reported.
	probes[9].ignored = true
	problems := probeProblems(rules, probes[1:], results[1:])
	joined := strings.Join(problems, "\n")
	if !strings.Contains(joined, "src/a.go should be ignored") || !strings.Contains(joined, `pattern "bin/" decides no probe`) {
		t.Fatalf("problems: %v", problems)
	}
}

// fixtureRepo builds a small repository whose gate passes, and returns its root.
func fixtureRepo(t *testing.T) string {
	t.Helper()
	requireGit(t)
	root := t.TempDir()
	write := func(name, content string, mode os.FileMode) {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	git := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q")
	write(".gitignore", sampleIgnore, 0o644)
	write("docs/toolchain.md", "`bin/` `.tools` `reports/` `*.log` `.env` `!.env.example` `*.db` `.idea/` `tmp/`\n", 0o644)
	write("docs/binary-allowlist.md", sampleAllowlist, 0o644)
	write("tools/gitignore-check/probes.txt", "ignored bin/x\nignored .tools/go\nignored reports/a\nignored a.log\nignored .env\nkept .env.example\n"+
		"ignored a.db\nignored .idea/x\nignored tmp/x\n", 0o644)
	write("shots/a.png", "\x89PNG\x00\x00", 0o644)
	write("api/spec.json", "{}", 0o644)
	write("src/a.go", "package a\n", 0o644)
	write("scripts/run.sh", "#!/bin/sh\n", 0o755)
	git("add", "-A")
	return root
}

func runIn(t *testing.T, root string, args ...string) (int, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := run(append([]string{"-root", root}, args...), &out, &errOut)
	return code, out.String() + errOut.String()
}

func TestRunPassesAndLists(t *testing.T) {
	root := fixtureRepo(t)
	// api/spec.json is tiny here, so its entry would be stale: track a large file.
	if err := os.WriteFile(filepath.Join(root, "api/spec.json"), bytes.Repeat([]byte("x"), largeFileLimit+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.log"), []byte("log"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("draft"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out := runIn(t, root, "-list")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, want := range []string{"ignored\ta.log", "not-ignored\tnotes.txt", "violations=0", "allowlisted=2"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestRunFailures(t *testing.T) {
	cases := map[string]struct {
		mutate func(t *testing.T, root string)
		want   string
	}{
		"binary not allowlisted": {func(t *testing.T, root string) {
			add(t, root, "assets/icon.ico", "\x00\x00\x01\x00", 0o644)
		}, "tracked file assets/icon.ico: binary"},
		"stale allowlist entry": {func(*testing.T, string) {}, "entry `api/spec.json` matches no tracked file"},
		"tracked against rules": {func(t *testing.T, root string) {
			add(t, root, "tmp/keep.txt", "x", 0o644, "-f")
		}, "tracked file tmp/keep.txt: ignored"},
		"generated directory": {func(t *testing.T, root string) {
			add(t, root, ".tools/go/VERSION", "go", 0o644, "-f")
		}, "tracked file .tools/go/VERSION: generated"},
		"untracked media": {func(t *testing.T, root string) {
			if err := os.WriteFile(filepath.Join(root, "clip.mkv"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, "untracked file clip.mkv: media"},
		"executable without shebang": {func(t *testing.T, root string) {
			add(t, root, "scripts/tool", "echo\n", 0o755)
		}, "tracked file scripts/tool: executable"},
		"undocumented pattern": {func(t *testing.T, root string) {
			if err := os.WriteFile(filepath.Join(root, "docs/toolchain.md"), []byte("`bin/`\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, `pattern "tmp/" is not listed in docs/toolchain.md`},
		"probe mismatch": {func(t *testing.T, root string) {
			appendFile(t, filepath.Join(root, "tools/gitignore-check/probes.txt"), "ignored src/a.go\n")
		}, "src/a.go should be ignored"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			root := fixtureRepo(t)
			if name != "stale allowlist entry" {
				// Keep the large-file entry in use so only the mutation fails.
				add(t, root, "api/spec.json", strings.Repeat("x", largeFileLimit+1), 0o644)
			}
			tc.mutate(t, root)
			code, out := runIn(t, root)
			if code != 1 || !strings.Contains(out, tc.want) {
				t.Fatalf("exit %d, want 1 with %q:\n%s", code, tc.want, out)
			}
		})
	}
}

func TestRunToolErrors(t *testing.T) {
	root := fixtureRepo(t)
	if err := os.Remove(filepath.Join(root, "tools/gitignore-check/probes.txt")); err != nil {
		t.Fatal(err)
	}
	if code, _ := runIn(t, root); code != 2 {
		t.Fatalf("missing probes: exit %d", code)
	}
	if code, _ := runIn(t, root, "-unknown"); code != 2 {
		t.Fatalf("bad flag: exit %d", code)
	}
}

func add(t *testing.T, root, name, content string, mode os.FileMode, flags ...string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	args := append([]string{"-C", root, "add"}, flags...)
	if mode&0o111 != 0 {
		args = append(args, "--chmod=+x")
	}
	if out, err := exec.Command("git", append(args, name)...).CombinedOutput(); err != nil {
		t.Fatalf("git add %s: %v %s", name, err, out)
	}
}

func appendFile(t *testing.T, name, text string) {
	t.Helper()
	f, err := os.OpenFile(name, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestRepositoryIgnoreRules runs the structure, documentation and probe
// checks on the real .gitignore so `go test` fails before CI does.
func TestRepositoryIgnoreRules(t *testing.T) {
	requireGit(t)
	read := func(name string) string {
		data, err := os.ReadFile(filepath.Join("..", "..", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	ignore := read(".gitignore")
	rules, problems := parseGitignore(ignore)
	problems = append(problems, undocumented(rules, read("docs/toolchain.md"))...)
	probes, err := parseProbes(read("tools/gitignore-check/probes.txt"))
	if err != nil {
		t.Fatal(err)
	}
	results, err := evaluateProbes(ignore, probes)
	if err != nil {
		t.Fatal(err)
	}
	problems = append(problems, probeProblems(rules, probes, results)...)
	if _, allow := parseAllowlist(read("docs/binary-allowlist.md")); len(allow) != 0 {
		problems = append(problems, allow...)
	}
	if len(problems) != 0 {
		t.Fatalf("repository ignore rules:\n%s", strings.Join(problems, "\n"))
	}
}
