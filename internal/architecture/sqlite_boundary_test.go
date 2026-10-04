package architecture

import (
	"sort"
	"strings"
	"testing"
)

// sqliteOwners may import an SQLite driver: the legacy import reader
// (G04.6) and its test fixture generator.
var sqliteOwners = map[string]bool{"internal/adapter/legacydb": true, "internal/adapter/legacydb/legacydbtest": true}

func sqliteImport(path string) bool {
	return strings.Contains(strings.ToLower(path), "sqlite")
}

// TestSQLiteStaysOutOfTheServer enforces G04.8 statically: only the legacy
// reader links an SQLite driver, and neither the server nor the schema
// migrator reaches it through any import chain.
func TestSQLiteStaysOutOfTheServer(t *testing.T) {
	packages := loadModulePackages(t, repositoryRoot(t))
	owners := 0
	for importPath, pkg := range packages {
		for imported := range pkg.imports {
			if !sqliteImport(imported) || packages[imported] != nil {
				continue
			}
			if !sqliteOwners[pkg.dir] {
				t.Errorf("%s imports %s; only the legacy import reader may", importPath, imported)
			}
			owners++
		}
	}
	if owners == 0 {
		t.Fatal("no package imports the SQLite driver; the scanner is broken or the reader moved")
	}
	for _, binary := range []string{"cmd/jelee", "cmd/jelee-migrate"} {
		start := modulePath + "/" + binary
		if packages[start] == nil {
			t.Fatalf("%s not found", binary)
		}
		via := map[string]string{start: ""}
		queue := []string{start}
		for len(queue) > 0 {
			current := queue[0]
			queue = queue[1:]
			pkg := packages[current]
			if sqliteOwners[pkg.dir] {
				chain := current
				for parent := via[current]; parent != ""; parent = via[parent] {
					chain = parent + " -> " + chain
				}
				t.Errorf("%s reaches the SQLite reader: %s", binary, chain)
				continue
			}
			imports := make([]string, 0, len(pkg.imports))
			for imported := range pkg.imports {
				imports = append(imports, imported)
			}
			sort.Strings(imports)
			for _, imported := range imports {
				if sqliteImport(imported) && packages[imported] == nil {
					t.Errorf("%s imports %s", current, imported)
				}
				if _, seen := via[imported]; seen || packages[imported] == nil {
					continue
				}
				via[imported] = current
				queue = append(queue, imported)
			}
		}
	}
	// The CLI does reach it; this keeps the walk honest.
	if !reaches(packages, modulePath+"/cmd/jelee-cli", modulePath+"/internal/adapter/legacydb") {
		t.Fatal("jelee-cli no longer reaches the legacy reader; update this test")
	}
}

func reaches(packages map[string]*goPackage, from, to string) bool {
	seen := map[string]bool{from: true}
	queue := []string{from}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if current == to {
			return true
		}
		for imported := range packages[current].imports {
			if !seen[imported] && packages[imported] != nil {
				seen[imported] = true
				queue = append(queue, imported)
			}
		}
	}
	return false
}
