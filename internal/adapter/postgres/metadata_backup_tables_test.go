package postgres

import (
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

var (
	migrationCreateTable = regexp.MustCompile(`(?i)\bCREATE\s+(?:UNLOGGED\s+)?TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([a-z_][a-z0-9_]*)`)
	migrationRenameTable = regexp.MustCompile(`(?i)\bALTER\s+TABLE\s+(?:IF\s+EXISTS\s+)?([a-z_][a-z0-9_]*)\s+RENAME\s+TO\s+([a-z_][a-z0-9_]*)`)
	migrationDropTable   = regexp.MustCompile(`(?i)\bDROP\s+TABLE\s+(?:IF\s+EXISTS\s+)?([a-z_][a-z0-9_, ]*)`)
)

// migrationTables replays the up migrations: tables created, renamed and
// dropped, in file order. golang-migrate adds schema_migrations.
func migrationTables(t *testing.T) map[string]bool {
	t.Helper()
	names, err := fs.Glob(migrationFiles, "migrations/*.up.sql")
	if err != nil || len(names) == 0 {
		t.Fatalf("migrations: %v", err)
	}
	sort.Strings(names)
	tables := map[string]bool{"schema_migrations": true}
	for _, name := range names {
		data, err := fs.ReadFile(migrationFiles, name)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			line, _, _ = strings.Cut(line, "--")
			if strings.Contains(strings.ToUpper(line), "TEMP TABLE") {
				continue
			}
			for _, m := range migrationCreateTable.FindAllStringSubmatch(line, -1) {
				tables[strings.ToLower(m[1])] = true
			}
			for _, m := range migrationRenameTable.FindAllStringSubmatch(line, -1) {
				delete(tables, strings.ToLower(m[1]))
				tables[strings.ToLower(m[2])] = true
			}
			for _, m := range migrationDropTable.FindAllStringSubmatch(line, -1) {
				for _, table := range strings.Split(m[1], ",") {
					delete(tables, strings.ToLower(strings.TrimSpace(table)))
				}
			}
		}
	}
	return tables
}

// checkMetadataBackupClassification fails for a table that is neither
// exported by a record kind nor listed as excluded with a reason, for a
// table in both lists and for list entries naming no table.
func metadataBackupClassificationProblems(tables map[string]bool) []string {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	exported := map[string]string{}
	for kind, owned := range metadataBackupTables {
		for _, table := range owned {
			if other, ok := exported[table]; ok {
				add("table %s is exported by both %s and %s", table, kind, other)
			}
			exported[table] = kind
		}
	}
	for table := range tables {
		kind, isExported := exported[table]
		reason, isExcluded := metadataBackupExcluded[table]
		switch {
		case isExported && isExcluded:
			add("table %s is exported by %s and also excluded", table, kind)
		case !isExported && !isExcluded:
			add("table %s is not classified: export it with a record kind (metadataBackupTables) or list it in metadataBackupExcluded with a reason, and update docs/backup-restore.md", table)
		case isExcluded && strings.TrimSpace(reason) == "":
			add("table %s is excluded without a reason", table)
		}
	}
	for table := range exported {
		if !tables[table] {
			add("metadataBackupTables names %s, which no migration creates", table)
		}
	}
	for table := range metadataBackupExcluded {
		if !tables[table] {
			add("metadataBackupExcluded names %s, which no migration creates", table)
		}
	}
	sort.Strings(problems)
	return problems
}

func checkMetadataBackupClassification(t *testing.T, tables map[string]bool) {
	t.Helper()
	for _, problem := range metadataBackupClassificationProblems(tables) {
		t.Error(problem)
	}
}

// TestMetadataBackupClassifiesEveryTable is the source guard: every table
// the migrations create is either carried by a record kind or excluded with
// a reason, so a new table cannot silently miss the backup.
func TestMetadataBackupClassifiesEveryTable(t *testing.T) {
	tables := migrationTables(t)
	if len(tables) < 100 || !tables["library_inventory_baseline_data"] || tables["library_inventory_baseline"] {
		t.Fatalf("migration replay looks wrong: %d tables", len(tables))
	}
	checkMetadataBackupClassification(t, tables)
}

// TestMetadataBackupKindsAreWired keeps the kind list, the table map, the
// export queries and the import steps in step.
func TestMetadataBackupKindsAreWired(t *testing.T) {
	queries := metadataExportQueries(false)
	if len(queries) != len(domain.MetadataBackupKinds) || len(metadataApply) != len(domain.MetadataBackupKinds) || len(metadataBackupTables) != len(domain.MetadataBackupKinds) {
		t.Fatalf("kinds %d, export queries %d, import steps %d, table map %d", len(domain.MetadataBackupKinds), len(queries), len(metadataApply), len(metadataBackupTables))
	}
	for i, kind := range domain.MetadataBackupKinds {
		if queries[i].kind != kind || metadataApply[i].kind != kind {
			t.Errorf("position %d: kind %s, export %s, import %s", i, kind, queries[i].kind, metadataApply[i].kind)
		}
		owned := metadataBackupTables[kind]
		if len(owned) == 0 {
			t.Errorf("kind %s names no table", kind)
		}
		for _, table := range owned {
			if !strings.Contains(queries[i].sql, " FROM "+table) {
				t.Errorf("export of %s does not read %s", kind, table)
			}
		}
	}
	// The reverse check: a table that cannot be classified fails.
	probe := map[string]bool{"libraries": true, "brand_new_table": true}
	problems := metadataBackupClassificationProblems(probe)
	if len(problems) == 0 || !strings.Contains(strings.Join(problems, "\n"), "brand_new_table is not classified") {
		t.Fatalf("an unclassified table passed the guard: %v", problems)
	}
}
