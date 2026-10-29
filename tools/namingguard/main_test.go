package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheck(t *testing.T) {
	cases := []struct {
		name     string
		paths    []string
		messages []string
		fail     bool
	}{
		{"unrelated", []string{"internal/app/x.go", "docs/a.md"}, nil, false},
		{"parser test only", []string{"internal/domain/medianame/medianame_test.go"}, nil, false},
		{"parser with table", []string{"internal/domain/medianame/medianame.go", caseTable}, nil, false},
		{"parser without table", []string{"internal/domain/medianame/resolve.go"}, nil, true},
		{"parser with other test", []string{"internal/domain/medianame/scan.go", "internal/domain/medianame/medianame_test.go"}, nil, true},
		{"trailer", []string{"internal/domain/medianame/scan.go"}, []string{"refactor(medianame): rename\n\nNaming-Table: unchanged only a rename\n"}, false},
		{"empty trailer", []string{"internal/domain/medianame/scan.go"}, []string{"refactor: x\n\nNaming-Table: unchanged\n"}, true},
		{"nested package", []string{"internal/domain/medianame/sub/x.go"}, nil, true},
		{"similar prefix", []string{"internal/domain/medianamex/x.go"}, nil, false},
	}
	for _, c := range cases {
		if got := check(c.paths, c.messages) != ""; got != c.fail {
			t.Errorf("%s: fail=%t, want %t", c.name, got, c.fail)
		}
	}
}

func TestRunOnRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	gitIn := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	write := func(path, content string) {
		t.Helper()
		full := filepath.Join(dir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	gitIn("init", "-q")
	write("README", "x")
	gitIn("add", ".")
	gitIn("commit", "-q", "-m", "chore: base")
	write("internal/domain/medianame/resolve.go", "package medianame\n")
	gitIn("add", ".")
	gitIn("commit", "-q", "-m", "feat(medianame): parse more")

	var out, errOut bytes.Buffer
	if code := run([]string{"-C", dir, "-range", "HEAD~1..HEAD"}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), caseTable) {
		t.Fatalf("parser change without cases: code %d, %q", code, errOut.String())
	}
	write(caseTable, "package medianame\n")
	gitIn("add", ".")
	gitIn("commit", "-q", "-m", "test(medianame): add cases")
	out.Reset()
	errOut.Reset()
	if code := run([]string{"-C", dir, "-range", "HEAD~2..HEAD"}, &out, &errOut); code != 0 {
		t.Fatalf("parser change with cases: code %d, %q", code, errOut.String())
	}
	if code := run([]string{"-C", dir}, &out, &errOut); code != 2 {
		t.Fatalf("missing -range: code %d", code)
	}
}
