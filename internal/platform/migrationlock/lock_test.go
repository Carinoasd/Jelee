package migrationlock

import (
	"strings"
	"testing"
	"testing/fstest"
)

func migrations(files map[string]string) fstest.MapFS {
	fsys := fstest.MapFS{}
	for name, body := range files {
		fsys[name] = &fstest.MapFile{Data: []byte(body)}
	}
	return fsys
}

func TestCheckLocksReleasedMigrations(t *testing.T) {
	released := map[string]string{"000001_a.up.sql": "CREATE TABLE a();", "000001_a.down.sql": "DROP TABLE a;", "000002_b.up.sql": "CREATE TABLE b();", "000002_b.down.sql": "DROP TABLE b;"}
	files, err := Files(migrations(released))
	if err != nil {
		t.Fatal(err)
	}
	lock, err := Parse(Format(files))
	if err != nil || len(lock) != 4 {
		t.Fatalf("round trip: %v %v", lock, err)
	}
	if appendable, violations := Check(lock, files); len(appendable) != 0 || len(violations) != 0 {
		t.Fatalf("unchanged: %v %v", appendable, violations)
	}
	for name, change := range map[string]func(map[string]string){
		"edited":  func(m map[string]string) { m["000001_a.up.sql"] = "CREATE TABLE a(x int);" },
		"removed": func(m map[string]string) { delete(m, "000002_b.up.sql"); delete(m, "000002_b.down.sql") },
		"renumbered": func(m map[string]string) {
			m["000003_b.up.sql"], m["000003_b.down.sql"] = m["000002_b.up.sql"], m["000002_b.down.sql"]
			delete(m, "000002_b.up.sql")
			delete(m, "000002_b.down.sql")
		},
		"inserted": func(m map[string]string) { m["000002_c.up.sql"], m["000002_c.down.sql"] = "x", "y" },
	} {
		current := map[string]string{}
		for k, v := range released {
			current[k] = v
		}
		change(current)
		files, err := Files(migrations(current))
		if err != nil {
			if name == "inserted" && strings.Contains(err.Error(), "also named") {
				continue
			}
			t.Fatalf("%s: %v", name, err)
		}
		if _, violations := Check(lock, files); len(violations) == 0 {
			t.Errorf("%s: a changed release must be a violation", name)
		}
	}
	added := map[string]string{"000003_c.up.sql": "CREATE TABLE c();", "000003_c.down.sql": "DROP TABLE c;"}
	for k, v := range released {
		added[k] = v
	}
	files, _ = Files(migrations(added))
	appendable, violations := Check(lock, files)
	if len(violations) != 0 || len(appendable) != 2 || appendable[0].Name != "000003_c.down.sql" {
		t.Fatalf("new migration: %v %v", appendable, violations)
	}
	below := []Entry{{Name: "000001_a.down.sql", Sum: lock[0].Sum}, {Name: "000005_z.down.sql", Sum: lock[0].Sum}}
	if _, violations := Check(below, files); !strings.Contains(strings.Join(violations, "\n"), "000003_c.up.sql: new migration must be numbered above the last locked migration 000005") {
		t.Fatalf("migrations below the highest locked number must be refused: %v", violations)
	}
}

func TestFilesAndParseRejectMalformedInput(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"bad name":     {"1_a.up.sql": ""},
		"missing down": {"000001_a.up.sql": ""},
		"two names":    {"000001_a.up.sql": "", "000001_a.down.sql": "", "000001_b.up.sql": "", "000001_b.down.sql": ""},
	} {
		if _, err := Files(migrations(files)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	sum := strings.Repeat("a", 64)
	for _, lock := range []string{
		"nonsense\n", sum + " 000001_a.up.sql\n", strings.Repeat("A", 64) + "  000001_a.up.sql\n", sum + "  a.sql\n",
		sum + "  000002_a.up.sql\n" + sum + "  000001_a.up.sql\n", sum + "  000001_a.up.sql\n" + sum + "  000001_a.up.sql\n",
	} {
		if _, err := Parse([]byte(lock)); err == nil {
			t.Errorf("lock %q accepted", lock)
		}
	}
	base := []Entry{{Name: "000001_a.up.sql", Sum: sum}, {Name: "000001_a.down.sql", Sum: sum}}
	if v := Retained(base, base); len(v) != 0 {
		t.Fatal(v)
	}
	if v := Retained(base, []Entry{{Name: "000001_a.up.sql", Sum: strings.Repeat("b", 64)}}); len(v) != 2 {
		t.Fatalf("a dropped and an edited base entry must both be reported: %v", v)
	}
}
