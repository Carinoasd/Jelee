package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckMessageAccepts(t *testing.T) {
	for _, m := range []string{
		"feat(access): 關鍵字封鎖與限制時段（G48.4）",
		"fix: handle empty playlists",
		"docs(traceability): G04.2 更新",
		"refactor(webhook,stats)!: rename delivery fields",
		"perf(nfo/reader): reuse buffers",
		"ci(release.yml): add tag workflow",
		"test(scan-memory): cover cancellation\n\nBody text.\n\nCo-Authored-By: Someone <x@example.invalid>",
		"chore!: drop legacy flag\n\nBREAKING CHANGE: the flag is gone",
		"build(deps): bump",
		"Merge branch 'feat/access2' into feat/repohygiene",
		"Merge pull request #12 from Carinoasd/feat/x",
		`Revert "feat(access): 關鍵字封鎖"`,
		`Reapply "fix: x"`,
	} {
		if problems := checkMessage(m, false); len(problems) != 0 {
			t.Errorf("%q rejected: %v", m, problems)
		}
	}
}

func TestCheckMessageRejects(t *testing.T) {
	cases := map[string]string{
		"功能：原子保存忽略掃描批次":              "must start with <type>",
		"wip(nfo): halfway":          `type "wip" is not one of`,
		"style: whitespace":          `type "style" is not one of`,
		"Feat(api): capitalised":     `type "Feat" is not one of`,
		"feat(API): upper scope":     "scope (API) must be lower-case",
		"feat(a b): spaced scope":    "scope (a b) must be lower-case",
		"feat(): empty scope":        "scope () must be lower-case",
		"feat(api):missing space":    "followed by one space",
		"feat(api):  ":               "followed by one space",
		"feat(api)： 全形冒號":            "must start with <type>",
		"feat: ok\nbody without gap": "blank line between the subject and the body",
		"":                           "empty subject line",
		"fixup! feat: x":             "must start with <type>",
		"Merged stuff":               "must start with <type>",
	}
	for m, want := range cases {
		problems := strings.Join(checkMessage(m, false), "; ")
		if !strings.Contains(problems, want) {
			t.Errorf("%q: got %q, want %q", m, problems, want)
		}
	}
}

func TestAutosquashOnlyInHook(t *testing.T) {
	for _, m := range []string{"fixup! feat: x", "squash! fix(a): y", "amend! docs: z"} {
		if len(checkMessage(m, true)) != 0 {
			t.Errorf("%q rejected in the hook", m)
		}
		if len(checkMessage(m, false)) == 0 {
			t.Errorf("%q accepted in a range", m)
		}
	}
}

func TestStripComments(t *testing.T) {
	in := "\n# Please enter the commit message\nfeat: x\n\nbody\n# comment\n# ------------------------ >8 ------------------------\ndiff --git a b\n"
	if got := stripComments(in); got != "feat: x\n\nbody" {
		t.Fatalf("got %q", got)
	}
}

func TestMeasureSkipsGeneratedFiles(t *testing.T) {
	numstat := "10\t2\tinternal/a.go\n-\t-\tweb/e2e/__screenshots__/desktop/a.png\n500\t500\tapi/openapi.json\n-\t-\tassets/logo.png\n3\t0\tdocs/evidence/run.json\n"
	if s := measure(numstat); s.files != 2 || s.lines != 12 {
		t.Fatalf("measure = %+v", s)
	}
}

func TestRunMessageModes(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "COMMIT_EDITMSG")
	cases := []struct {
		content string
		code    int
	}{
		{"feat(api): add\n# comment\n", 0},
		{"# only comments\n\n", 0},
		{"功能：新增\n", 1},
		{"fixup! feat(api): add\n", 0},
	}
	for _, tc := range cases {
		if err := os.WriteFile(file, []byte(tc.content), 0o600); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if code := run([]string{"-message-file", file}, &out, &out); code != tc.code {
			t.Errorf("%q: exit %d want %d\n%s", tc.content, code, tc.code, out.String())
		}
	}
	var out bytes.Buffer
	if code := run([]string{"-message", "docs: ok"}, &out, &out); code != 0 {
		t.Fatalf("message: %d %s", code, out.String())
	}
	for _, args := range [][]string{{}, {"-message", "a", "-range", "b"}, {"-message-file", filepath.Join(dir, "missing")}, {"-message", "a", "-size-mode", "loud"}, {"-x"}} {
		if code := run(args, &out, &out); code != 2 {
			t.Errorf("%v: exit %d", args, code)
		}
	}
}

func gitIn(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func commitFiles(t *testing.T, root, message string, files int) {
	t.Helper()
	prefix := strings.Fields(message)[0]
	prefix = strings.NewReplacer("(", "-", ")", "-", ":", "", "!", "").Replace(prefix)
	for i := 0; i < files; i++ {
		name := filepath.Join(root, fmt.Sprintf("%s%03d.txt", prefix, i))
		if err := os.WriteFile(name, []byte(message+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	gitIn(t, root, "add", "-A")
	gitIn(t, root, "commit", "-q", "-m", message)
}

func TestRunRange(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	gitIn(t, root, "init", "-q", "-b", "main")
	commitFiles(t, root, "初始提交，不符合規範", 1) // history before the range is not checked
	base := gitIn(t, root, "rev-parse", "HEAD")
	commitFiles(t, root, "feat(core): small", 2)
	commitFiles(t, root, "feat(core): large without trailer", 5)
	commitFiles(t, root, "feat(core): large with trailer\n\nLarge-Change: one migration touches every file", 6)
	gitIn(t, root, "checkout", "-q", "-b", "side", base)
	commitFiles(t, root, "fix(side): parallel", 1)
	gitIn(t, root, "checkout", "-q", "main")
	gitIn(t, root, "merge", "-q", "--no-ff", "--no-edit", "side")

	run1 := func(args ...string) (int, string) {
		var out bytes.Buffer
		code := run(append([]string{"-root", root, "-max-files", "4"}, args...), &out, &out)
		return code, out.String()
	}
	code, out := run1("-range", base+"..HEAD")
	if code != 0 || !strings.Contains(out, "commits=5 problems=0 size-warnings=1") || !strings.Contains(out, "large commit: 5 files") {
		t.Fatalf("warn mode: %d\n%s", code, out)
	}
	t.Setenv("GITHUB_ACTIONS", "true")
	code, out = run1("-range", base+"..HEAD", "-size-mode", "fail")
	if code != 1 || !strings.Contains(out, "::error title=Large commit") {
		t.Fatalf("fail mode: %d\n%s", code, out)
	}
	commitFiles(t, root, "update: not conventional", 1)
	code, out = run1("-range", base+"..HEAD")
	if code != 1 || !strings.Contains(out, `type "update" is not one of`) || !strings.Contains(out, "::error title=Commit message") {
		t.Fatalf("bad message: %d\n%s", code, out)
	}
	if code, _ = run1("-range", "nope..HEAD"); code != 2 {
		t.Fatalf("bad range: %d", code)
	}
}
