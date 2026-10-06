package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspect(t *testing.T) {
	text := attr{text: "auto", eol: "lf", diff: "unspecified"}
	binary := attr{text: "unset", eol: "unspecified", diff: "unset"}
	cases := []struct {
		name string
		data string
		a    attr
		want string
	}{
		{"clean", "package a\n// 中文注释\n", text, ""},
		{"bom", "\xef\xbb\xbfpackage a\n", text, "UTF-8 byte order mark"},
		{"crlf", "a\nb\r\nc\n", text, "CR line ending at line 2"},
		{"lone cr", "a\rb", text, "CR line ending at line 1"},
		{"latin1", "ok\ncaf\xe9\n", text, "invalid UTF-8 at line 2"},
		{"binary ok", "\x89PNG\x00\x00", binary, ""},
		{"binary unmarked", "\x89PNG\x00\x00", text, "add a `binary` rule"},
		{"text marked binary", "plain\n", binary, "text content marked binary"},
		{"crlf attribute", "plain\n", attr{text: "set", eol: "crlf"}, "eol=crlf"},
	}
	for _, tc := range cases {
		got := strings.Join(inspect([]byte(tc.data), tc.a), "; ")
		if tc.want == "" && got != "" || !strings.Contains(got, tc.want) {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestIsBinaryLooksAtFirst8000Bytes(t *testing.T) {
	late := append(bytes.Repeat([]byte("a"), 8000), 0)
	if isBinary(late) || !isBinary([]byte{'a', 0}) {
		t.Fatal("binary detection must follow Git's first-8000-bytes rule")
	}
}

func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	git(t, root, "init", "-q")
	write(t, root, ".gitattributes", "* text=auto eol=lf\n*.png binary\n")
	write(t, root, "a.go", "package a\n")
	write(t, root, "img.png", "\x89PNG\x00")
	git(t, root, "add", "-A")
	return root
}

func git(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}

func write(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runIn(t *testing.T, root string, args ...string) (int, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := run(append([]string{"-root", root}, args...), &out, &errOut)
	return code, out.String() + errOut.String()
}

func TestRunTrackedFiles(t *testing.T) {
	root := gitRepo(t)
	if code, out := runIn(t, root); code != 0 || !strings.Contains(out, "files=3 binary=1 violations=0") {
		t.Fatalf("clean repository: exit %d\n%s", code, out)
	}
	// An unmarked binary and a CRLF file in the work tree both fail.
	write(t, root, "blob.bin", "\x00\x01")
	write(t, root, "a.go", "package a\r\n")
	git(t, root, "add", "blob.bin")
	code, out := runIn(t, root)
	if code != 1 || !strings.Contains(out, "blob.bin: binary content") || !strings.Contains(out, "a.go: CR line ending") {
		t.Fatalf("exit %d\n%s", code, out)
	}
}

func TestRunStagedReadsIndex(t *testing.T) {
	root := gitRepo(t)
	write(t, root, "b.txt", "\xef\xbb\xbfhello\n")
	git(t, root, "add", "b.txt")
	// Fixing only the work tree copy does not help: the index is committed.
	write(t, root, "b.txt", "hello\n")
	code, out := runIn(t, root, "-staged")
	if code != 1 || !strings.Contains(out, "b.txt: UTF-8 byte order mark") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	git(t, root, "add", "b.txt")
	if code, out := runIn(t, root, "-staged"); code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
}

func TestRunErrors(t *testing.T) {
	// A missing root, not merely a directory outside a repository: Windows CI
	// keeps TMP inside the checkout (scripts/run-go.ps1).
	if code, _ := runIn(t, filepath.Join(t.TempDir(), "missing")); code != 2 {
		t.Fatalf("missing root: exit %d", code)
	}
	if code, _ := runIn(t, ".", "-nope"); code != 2 {
		t.Fatalf("bad flag: exit %d", code)
	}
}

// TestRepositoryAttributes keeps the real .gitattributes in line with the
// binary types gitignore-check knows about.
func TestRepositoryAttributes(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", ".gitattributes"))
	if err != nil {
		t.Fatal(err)
	}
	for _, ext := range []string{"png", "jpg", "webp", "ico", "mp4", "mkv", "zip", "exe", "db"} {
		if !strings.Contains(string(data), "\n*."+ext+" binary\n") {
			t.Errorf(".gitattributes does not lock *.%s as binary", ext)
		}
	}
	if !strings.HasPrefix(strings.SplitN(string(data), "\n* ", 2)[1], "text=auto eol=lf") {
		t.Error(".gitattributes must set `* text=auto eol=lf`")
	}
}
