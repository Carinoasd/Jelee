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
// management statements it may contain instead, or to visibilityWholeFile
// for the file that administers the table; every other occurrence in a
// non-test source of this package fails.
var visibilityGuardedTables = map[string]map[string][]string{
	"library_acl": {
		// Account administration writes and lists the grants themselves.
		"account_acl.go": {
			`DELETE FROM library_acl WHERE user_id=`,
			`INSERT INTO library_acl(user_id,library_id)`,
			`SELECT l.id::text,l.name FROM library_acl a JOIN libraries l ON l.id=a.library_id WHERE a.user_id=`,
		},
	},
	"user_item_access_rules": {"content_access.go": {visibilityWholeFile}},
	"user_blocked_tags":      {"content_access.go": {visibilityWholeFile}},
	"access_policy":          {"content_access.go": {visibilityWholeFile}},
	"parental_ratings":       {"content_access.go": {visibilityWholeFile}},
	"parental_rating_max":    {"content_access.go": {visibilityWholeFile}},
	// Share links and network rules (G48.5, G48.6) are administered, and
	// guest sessions issued, by their own files only.
	"share_links":           {"shares.go": {visibilityWholeFile}},
	"library_network_rules": {"network_rules.go": {visibilityWholeFile}},
	// Collections and playlists (G02.1) bind their members to the reader
	// through itemVisibleSQL; only their own files read them, and account
	// deletion drops the deleted user's playlists (G07.7).
	"collections":      {"collections.go": {visibilityWholeFile}},
	"collection_items": {"collections.go": {visibilityWholeFile}},
	"playlists": {
		"playlists.go": {visibilityWholeFile},
		"accounts.go":  {`DELETE FROM playlists WHERE owner_id=`},
	},
	"playlist_items": {"playlists.go": {visibilityWholeFile}},
}

// Metadata backup copies the grant and rule rows verbatim as data (G36.4),
// the legacy import writes the grants and restrictions it migrates (G04.6),
// and a version merge keeps and transfers an absorbed item's rule rows the
// same way (G20.3, hide wins), and the personal data export and permanent
// deletion copy out or remove the user's own rows (G07.7; which items an
// export names still goes through the unified predicates); none decides
// visibility, so these files may name every guarded table.
var visibilityBackupFiles = []string{"metadata_backup.go", "metadata_import.go", "legacy_import.go", "legacy_import_phases.go", "item_versions_rows.go", "userdata.go"}

func init() {
	for _, files := range visibilityGuardedTables {
		for _, name := range visibilityBackupFiles {
			files[name] = []string{visibilityWholeFile}
		}
	}
}

// visibilityWholeFile allows every occurrence in the administering file.
const visibilityWholeFile = "*"

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
			rest := text
			for _, statement := range allowed[name] {
				if statement == visibilityWholeFile {
					rest = ""
				}
				rest = strings.ReplaceAll(rest, statement, "")
			}
			if n := strings.Count(rest, table); n > 0 {
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
