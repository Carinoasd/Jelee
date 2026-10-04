package postgres

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// visibilityGuardedTables are the authorization tables only the unified
// filter may read (G48.2). Each maps a non-test file to the exact
// management statements it may contain instead; every other occurrence in
// a non-test source of this package fails.
var visibilityGuardedTables = map[string]map[string][]string{
	"library_acl": {
		// Account administration writes and lists the grants themselves.
		"account_acl.go": {
			`DELETE FROM library_acl WHERE user_id=`,
			`INSERT INTO library_acl(user_id,library_id)`,
			`SELECT l.id::text,l.name FROM library_acl a JOIN libraries l ON l.id=a.library_id WHERE a.user_id=`,
		},
	},
}

// visibilitySourceFile is the single source of the authorization predicates.
const visibilitySourceFile = "visibility.go"

func TestVisibilityPredicateHasOneSource(t *testing.T) {
	problems := visibilityGuardProblems(t, ".")
	for _, p := range problems {
		t.Error(p)
	}
}

// visibilityGuardProblems scans the non-test Go sources of dir.
func visibilityGuardProblems(t *testing.T, dir string) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	var problems []string
	sources := 0
	for _, path := range paths {
		name := filepath.Base(path)
		if strings.HasSuffix(name, "_test.go") || name == visibilitySourceFile {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		sources++
		text := string(data)
		for table, allowed := range visibilityGuardedTables {
			for _, statement := range allowed[name] {
				text = strings.ReplaceAll(text, statement, "")
			}
			if n := strings.Count(text, table); n > 0 {
				problems = append(problems, name+": "+table+" appears "+strconv.Itoa(n)+" time(s) outside "+visibilitySourceFile+"; use the unified visibility predicates instead of reading the grant table")
			}
		}
	}
	if sources < 20 {
		t.Fatalf("visibility guard scanned only %d sources", sources)
	}
	sort.Strings(problems)
	return problems
}

// The guard itself is checked against a planted copy of the predicate, so a
// broken scan cannot pass silently.
func TestVisibilityGuardDetectsCopiedPredicate(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 20; i++ {
		if err := os.WriteFile(filepath.Join(dir, "filler"+strconv.Itoa(i+1)+".go"), []byte("package postgres\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	copied := "package postgres\n\nconst q = `SELECT 1 FROM items i WHERE EXISTS(SELECT 1 FROM library_acl a WHERE a.library_id=i.library_id)`\n"
	if err := os.WriteFile(filepath.Join(dir, "copied.go"), []byte(copied), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "account_acl.go"), []byte("package postgres\n\nconst d = `DELETE FROM library_acl WHERE user_id=$1`\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	problems := visibilityGuardProblems(t, dir)
	if len(problems) != 1 || !strings.HasPrefix(problems[0], "copied.go: library_acl") {
		t.Fatalf("guard problems = %q, want only the planted copy", problems)
	}
}
