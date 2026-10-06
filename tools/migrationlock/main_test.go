package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunAppendsOnlyAndRefusesChanges(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	exec := func(args ...string) (int, string) {
		var out bytes.Buffer
		code := run(append([]string{"-dir", dir}, args...), &out, &out)
		return code, out.String()
	}
	write("000001_a.up.sql", "CREATE TABLE a();")
	write("000001_a.down.sql", "DROP TABLE a;")
	if code, out := exec(); code != 1 || !strings.Contains(out, "000001_a.up.sql") {
		t.Fatalf("unlocked migrations must fail: %d %s", code, out)
	}
	if code, out := exec("-update"); code != 0 {
		t.Fatalf("update: %d %s", code, out)
	}
	if code, out := exec(); code != 0 {
		t.Fatalf("locked: %d %s", code, out)
	}
	write("000002_b.up.sql", "CREATE TABLE b();")
	write("000002_b.down.sql", "DROP TABLE b;")
	if code, _ := exec(); code != 1 {
		t.Fatal("a new migration must be locked")
	}
	if code, out := exec("-update"); code != 0 || !strings.Contains(out, "2 new") {
		t.Fatalf("append: %d %s", code, out)
	}
	write("000001_a.up.sql", "CREATE TABLE a(x int);")
	for _, args := range [][]string{nil, {"-update"}} {
		if code, out := exec(args...); code != 1 || !strings.Contains(out, "changed") {
			t.Fatalf("%v: an edited release must fail even with -update: %d %s", args, code, out)
		}
	}
	if code, _ := exec("-base", "-x"); code != 2 {
		t.Fatal("a base starting with - is refused")
	}
	if code, _ := exec("extra"); code != 2 {
		t.Fatal("arguments are refused")
	}
}

// The repository's own lock is current.
func TestRepositoryLockIsCurrent(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"-dir", filepath.Join("..", "..", defaultDir)}, &out, &out); code != 0 {
		t.Fatalf("%d %s", code, out.String())
	}
}
