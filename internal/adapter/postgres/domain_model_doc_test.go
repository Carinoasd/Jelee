package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// G49.7 / G49.8: docs/domain-model.md carries a Mermaid ER diagram of the
// main tables. Every relationship, column and cardinality it draws must
// exist in the migrated schema, and every foreign key between two drawn
// tables must be drawn: a document that disagrees with the schema is a bug.

type erRelation struct {
	parent, child string
	columns       []string
	// optional is "|o" on the parent side: a nullable foreign key.
	optional bool
	// single is "o|" on the child side: at most one child per parent.
	single bool
}

func (r erRelation) key() string {
	return r.child + "(" + strings.Join(r.columns, ",") + ")->" + r.parent
}

type erDocument struct {
	entities  map[string][]string
	relations []erRelation
	kinds     []string
}

var (
	erEntityLine    = regexp.MustCompile(`^\s+([a-z_][a-z0-9_]*) \{$`)
	erAttributeLine = regexp.MustCompile(`^\s+[a-z][a-z0-9_]* ([a-z_][a-z0-9_]*)(?: (?:PK|FK))?$`)
	erRelationLine  = regexp.MustCompile(`^\s+([a-z_][a-z0-9_]*) (\|\||\|o)--(o\{|o\|) ([a-z_][a-z0-9_]*) : "([a-z0-9_,]+)"$`)
	erKindsLine     = regexp.MustCompile(`<!-- item-kinds: ([A-Za-z ]+) -->`)
)

func readDomainModelDocument(t *testing.T) erDocument {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "domain-model.md"))
	if err != nil {
		t.Fatalf("read docs/domain-model.md: %v", err)
	}
	doc, problems := parseDomainModelDocument(string(data))
	for _, p := range problems {
		t.Error(p)
	}
	if t.Failed() {
		t.FailNow()
	}
	return doc
}

func parseDomainModelDocument(text string) (erDocument, []string) {
	doc := erDocument{entities: map[string][]string{}}
	var problems []string
	if m := erKindsLine.FindStringSubmatch(text); m != nil {
		doc.kinds = strings.Fields(m[1])
	} else {
		problems = append(problems, "item-kinds marker missing")
	}
	_, body, found := strings.Cut(text, "```mermaid\nerDiagram\n")
	if !found {
		return doc, append(problems, "no ```mermaid erDiagram block")
	}
	body, _, found = strings.Cut(body, "\n```")
	if !found {
		return doc, append(problems, "erDiagram block not closed")
	}
	entity := ""
	seen := map[string]bool{}
	for _, line := range strings.Split(body, "\n") {
		switch {
		case strings.TrimSpace(line) == "":
		case entity != "" && strings.TrimSpace(line) == "}":
			entity = ""
		case entity != "":
			m := erAttributeLine.FindStringSubmatch(line)
			if m == nil {
				problems = append(problems, "unparsed attribute in "+entity+": "+strings.TrimSpace(line))
				continue
			}
			doc.entities[entity] = append(doc.entities[entity], m[1])
		case erEntityLine.MatchString(line):
			entity = erEntityLine.FindStringSubmatch(line)[1]
			if _, dup := doc.entities[entity]; dup {
				problems = append(problems, "entity declared twice: "+entity)
			}
			doc.entities[entity] = nil
		case erRelationLine.MatchString(line):
			m := erRelationLine.FindStringSubmatch(line)
			r := erRelation{parent: m[1], child: m[4], columns: strings.Split(m[5], ","), optional: m[2] == "|o", single: m[3] == "o|"}
			if seen[r.key()] {
				problems = append(problems, "relationship drawn twice: "+r.key())
			}
			seen[r.key()] = true
			doc.relations = append(doc.relations, r)
		default:
			problems = append(problems, "unparsed diagram line: "+strings.TrimSpace(line))
		}
	}
	for _, r := range doc.relations {
		for _, end := range []string{r.parent, r.child} {
			if _, ok := doc.entities[end]; !ok {
				problems = append(problems, r.key()+": "+end+" has no entity block")
			}
		}
		for _, c := range r.columns {
			if !slices.Contains(doc.entities[r.child], c) {
				problems = append(problems, r.key()+": column "+c+" is not listed in the "+r.child+" block")
			}
		}
	}
	return doc, problems
}

// The diagram must parse and stay meaningful without a database.
func TestDomainModelDocumentParses(t *testing.T) {
	doc := readDomainModelDocument(t)
	if len(doc.entities) < 20 || len(doc.relations) < 30 {
		t.Fatalf("diagram shrank: %d entities, %d relationships", len(doc.entities), len(doc.relations))
	}
	// G02.1 retained domains map onto these tables; dropping one from the
	// diagram drops it from the document.
	for _, table := range []string{"libraries", "items", "item_parent_links", "media_sources", "collections", "playlists", "user_item_data", "playback_sessions"} {
		if _, ok := doc.entities[table]; !ok {
			t.Errorf("retained domain table %s missing from the diagram", table)
		}
	}
	if !slices.Equal(doc.kinds, []string{"Movie", "Series", "Season", "Episode", "HomeVideo"}) {
		t.Errorf("item kinds = %v", doc.kinds)
	}
	// The parser must reject what it cannot read rather than skip it.
	_, problems := parseDomainModelDocument("<!-- item-kinds: Movie -->\n```mermaid\nerDiagram\n    a {\n        uuid id PK\n    }\n    a }o--|| b : \"id\"\n    a ||--o{ c : \"x\"\n```\n")
	if len(problems) != 3 {
		t.Fatalf("parser problems = %q, want an unparsed line, a missing entity and a missing column", problems)
	}
}

type schemaForeignKey struct {
	child, parent string
	columns       []string
}

func TestDomainModelDocumentMatchesSchema(t *testing.T) {
	doc := readDomainModelDocument(t)
	dsn := os.Getenv("JELEE_TEST_DATABASE_URL")
	if dsn == "" {
		if strings.EqualFold(os.Getenv("JELEE_REQUIRE_INTEGRATION"), "true") {
			t.Fatal("required domain model integration database is unavailable")
		}
		t.Skip("domain model schema check NOT RUN: JELEE_TEST_DATABASE_URL unset")
	}
	u, err := url.Parse(dsn)
	if err != nil || u == nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Path != "/jelee_test" || u.Hostname() == "" {
		t.Fatal("domain model integration requires dedicated jelee_test database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal("cannot connect to configured test database; connection details are omitted")
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	nonce := make([]byte, 10)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	schema := "jelee_erdoc_" + hex.EncodeToString(nonce)
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal("cannot create isolated test schema")
	}
	t.Cleanup(func() {
		c, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, err := admin.Exec(c, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Errorf("could not remove isolated schema %s", schema)
		}
	})
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	if version, dirty, err := Migrate(ctx, u.String(), "up"); err != nil || dirty || version != SchemaVersion {
		t.Fatalf("migrate: %d %t %v", version, dirty, err)
	}

	// Columns and their nullability.
	nullable := map[string]map[string]bool{}
	rows, err := admin.Query(ctx, `SELECT table_name, column_name, is_nullable = 'YES' FROM information_schema.columns WHERE table_schema = $1`, schema)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var table, column string
		var null bool
		if err := rows.Scan(&table, &column, &null); err != nil {
			t.Fatal(err)
		}
		if nullable[table] == nil {
			nullable[table] = map[string]bool{}
		}
		nullable[table][column] = null
	}
	if rows.Err() != nil {
		t.Fatal(rows.Err())
	}

	// Foreign keys with their columns in constraint order.
	var keys []schemaForeignKey
	rows, err = admin.Query(ctx, `SELECT cl.relname, ref.relname, array_agg(a.attname::text ORDER BY k.ord)
FROM pg_constraint con
JOIN pg_class cl ON cl.oid = con.conrelid
JOIN pg_class ref ON ref.oid = con.confrelid
JOIN pg_namespace n ON n.oid = cl.relnamespace
CROSS JOIN LATERAL unnest(con.conkey) WITH ORDINALITY AS k(attnum, ord)
JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.attnum
WHERE n.nspname = $1 AND con.contype = 'f'
GROUP BY con.oid, cl.relname, ref.relname`, schema)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var k schemaForeignKey
		if err := rows.Scan(&k.child, &k.parent, &k.columns); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, k)
	}
	if rows.Err() != nil {
		t.Fatal(rows.Err())
	}

	// Unique, unconditional, plain-column indexes (primary keys included).
	unique := map[string][][]string{}
	rows, err = admin.Query(ctx, `SELECT cl.relname, array(SELECT a.attname::text FROM unnest(i.indkey) AS k(attnum) JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k.attnum)
FROM pg_index i
JOIN pg_class cl ON cl.oid = i.indrelid
JOIN pg_namespace n ON n.oid = cl.relnamespace
WHERE n.nspname = $1 AND i.indisunique AND i.indpred IS NULL AND i.indexprs IS NULL`, schema)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var table string
		var columns []string
		if err := rows.Scan(&table, &columns); err != nil {
			t.Fatal(err)
		}
		unique[table] = append(unique[table], columns)
	}
	if rows.Err() != nil {
		t.Fatal(rows.Err())
	}

	var problems []string
	// Every drawn table and column exists.
	for entity, columns := range doc.entities {
		if nullable[entity] == nil {
			problems = append(problems, "table "+entity+" does not exist")
			continue
		}
		for _, c := range columns {
			if _, ok := nullable[entity][c]; !ok {
				problems = append(problems, "column "+entity+"."+c+" does not exist")
			}
		}
	}
	// Every drawn relationship is a foreign key with the drawn cardinality.
	actual := map[string]schemaForeignKey{}
	for _, k := range keys {
		actual[erRelation{parent: k.parent, child: k.child, columns: k.columns}.key()] = k
	}
	drawn := map[string]bool{}
	for _, r := range doc.relations {
		drawn[r.key()] = true
		if _, ok := actual[r.key()]; !ok {
			problems = append(problems, "relationship "+r.key()+" is not a foreign key")
			continue
		}
		optional := false
		for _, c := range r.columns {
			optional = optional || nullable[r.child][c]
		}
		if optional != r.optional {
			problems = append(problems, r.key()+": parent side must be "+map[bool]string{true: "|o (nullable)", false: "|| (not null)"}[optional])
		}
		single := false
		for _, index := range unique[r.child] {
			covered := true
			for _, c := range index {
				covered = covered && slices.Contains(r.columns, c)
			}
			single = single || covered
		}
		if single != r.single {
			problems = append(problems, r.key()+": child side must be "+map[bool]string{true: "o| (unique)", false: "o{ (many)"}[single])
		}
	}
	// Every foreign key between two drawn tables is drawn.
	for key, k := range actual {
		_, parent := doc.entities[k.parent]
		_, child := doc.entities[k.child]
		if parent && child && !drawn[key] {
			problems = append(problems, "foreign key "+key+" between drawn tables is missing from the diagram")
		}
	}
	// The documented item kinds are exactly the ones items accepts.
	var definitions []string
	rows, err = admin.Query(ctx, `SELECT pg_get_constraintdef(con.oid) FROM pg_constraint con JOIN pg_class cl ON cl.oid = con.conrelid JOIN pg_namespace n ON n.oid = cl.relnamespace
WHERE n.nspname = $1 AND cl.relname = 'items' AND con.contype = 'c'`, schema)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(d, "kind") {
			definitions = append(definitions, d)
		}
	}
	if rows.Err() != nil {
		t.Fatal(rows.Err())
	}
	if len(definitions) != 1 {
		problems = append(problems, "items has no single kind check")
	} else {
		var kinds []string
		for _, m := range regexp.MustCompile(`'([A-Za-z]+)'`).FindAllStringSubmatch(definitions[0], -1) {
			kinds = append(kinds, m[1])
		}
		documented := slices.Clone(doc.kinds)
		sort.Strings(kinds)
		sort.Strings(documented)
		if !slices.Equal(kinds, documented) {
			problems = append(problems, "items kinds "+strings.Join(kinds, ",")+" differ from the documented "+strings.Join(documented, ","))
		}
	}
	sort.Strings(problems)
	for _, p := range problems {
		t.Error(p)
	}
}
