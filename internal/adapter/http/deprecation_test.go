package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// testDeprecation deprecates a real route of every rollout so the mechanism
// is exercised while apiDeprecations is empty.
func testDeprecation() Deprecation {
	return Deprecation{
		Route:     "GET /api/v1/system",
		Since:     time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
		Sunset:    time.Date(2027, 4, 1, 0, 0, 0, 0, time.UTC),
		Successor: "/api/v2/system",
		Notice:    "Test deprecation of the service information route.",
	}
}

func TestDeprecatedRouteSendsHeadersAndIsMarkedInOpenAPI(t *testing.T) {
	d := testDeprecation()
	handler := contractRouter(t, ReferenceConfig(), withDeprecations([]Deprecation{d}))
	serve := func(method, path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(method, "http://localhost"+path, nil))
		return w
	}
	w := serve("GET", "/api/v1/system")
	if w.Code != 200 {
		t.Fatalf("deprecated route answered %d", w.Code)
	}
	// RFC 9745: a structured field date; RFC 8594: an HTTP-date.
	if got := w.Header().Get("Deprecation"); got != "@"+strconv.FormatInt(d.Since.Unix(), 10) || got != "@1790812800" {
		t.Errorf("Deprecation = %q", got)
	}
	if got := w.Header().Get("Sunset"); got != "Thu, 01 Apr 2027 00:00:00 GMT" {
		t.Errorf("Sunset = %q", got)
	}
	if links := w.Header().Values("Link"); !slices.Equal(links, []string{`</api/v2/system>; rel="successor-version"`, `</api-docs#deprecations>; rel="deprecation"`}) {
		t.Errorf("Link = %q", links)
	}
	// Other routes and other methods of the same path carry nothing.
	for _, other := range []*httptest.ResponseRecorder{serve("GET", "/healthz"), serve("POST", "/api/v1/system"), serve("GET", "/api/v1/system/x")} {
		if other.Header().Get("Deprecation") != "" || other.Header().Get("Sunset") != "" || len(other.Header().Values("Link")) != 0 {
			t.Errorf("non-deprecated response carries deprecation headers: %v", other.Header())
		}
	}

	// The served document marks the same operation.
	var spec map[string]any
	if err := json.Unmarshal(serve("GET", "/api/v1/openapi.json").Body.Bytes(), &spec); err != nil {
		t.Fatal(err)
	}
	op := spec["paths"].(map[string]any)["/api/v1/system"].(map[string]any)["get"].(map[string]any)
	if op["deprecated"] != true || !strings.HasPrefix(op["description"].(string), "Deprecated since 2026-10-01; removal no earlier than 2027-04-01. Use /api/v2/system instead.") {
		t.Fatalf("served operation not deprecated: %v", op)
	}
	extension := op["x-jelee-deprecation"].(map[string]any)
	if extension["since"] != "2026-10-01T00:00:00Z" || extension["sunset"] != "2027-04-01T00:00:00Z" || extension["successor"] != "/api/v2/system" {
		t.Fatalf("x-jelee-deprecation = %v", extension)
	}
	headers := op["responses"].(map[string]any)["200"].(map[string]any)["headers"].(map[string]any)
	for _, name := range []string{"Deprecation", "Sunset", "Link"} {
		if _, ok := headers[name]; !ok {
			t.Errorf("response header %s undocumented", name)
		}
	}
	if problems := deprecationProblems([]Deprecation{d}, spec, "| `GET /api/v1/system` | 2026-10-01 | 2027-04-01 | `/api/v2/system` | test |\n"); len(problems) != 0 {
		t.Fatalf("guard rejects a consistent table: %v", problems)
	}

	// The browsable page lists it.
	page := serve("GET", "/api-docs").Body.String()
	if !strings.Contains(page, `<span class="dep">deprecated</span>`) || !strings.Contains(page, "<td><code>GET /api/v1/system</code></td><td>2026-10-01</td><td>2027-04-01</td>") {
		t.Fatal("/api-docs does not show the deprecation")
	}

	// Without the table nothing changes.
	plain := contractRouter(t, ReferenceConfig())
	w = httptest.NewRecorder()
	plain.ServeHTTP(w, httptest.NewRequest("GET", "http://localhost/api/v1/system", nil))
	if w.Header().Get("Deprecation") != "" {
		t.Fatal("production table deprecates /api/v1/system")
	}
}

func TestInvalidDeprecationTablesAreRejected(t *testing.T) {
	valid := testDeprecation()
	cases := map[string]func(*Deprecation){
		"route without method": func(d *Deprecation) { d.Route = "/api/v1/system" },
		"lower-case method":    func(d *Deprecation) { d.Route = "get /api/v1/system" },
		"no since":             func(d *Deprecation) { d.Since = time.Time{} },
		"no sunset":            func(d *Deprecation) { d.Sunset = time.Time{} },
		"short transition":     func(d *Deprecation) { d.Sunset = d.Since.Add(deprecationMinimumTransition - time.Second) },
		"relative successor":   func(d *Deprecation) { d.Successor = "api/v2/system" },
		"header injection":     func(d *Deprecation) { d.Successor = "/api/v2/x>; rel=\"x\"" },
		"no notice":            func(d *Deprecation) { d.Notice = " " },
	}
	for name, change := range cases {
		d := valid
		change(&d)
		if err := validateDeprecations([]Deprecation{d}); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if err := validateDeprecations([]Deprecation{valid, valid}); err == nil {
		t.Error("duplicate route accepted")
	}
	removal := valid
	removal.Successor = ""
	if err := validateDeprecations([]Deprecation{removal}); err != nil {
		t.Errorf("removal without successor rejected: %v", err)
	}
	broken := valid
	broken.Notice = ""
	_, err := contractServer(t, ReferenceConfig(), &fakeBackend{}, slog.New(slog.NewTextHandler(io.Discard, nil)), withDeprecations([]Deprecation{broken}))
	if !errors.Is(err, errDeprecationTable) {
		t.Fatalf("server accepted an invalid table: %v", err)
	}
}

// deprecationRow is one row of the table in docs/api-deprecations.md:
// | `METHOD /path` | since | sunset | successor | notice |
var deprecationRow = regexp.MustCompile("(?m)^\\| `([A-Z]+ /[^`]*)` \\| (\\d{4}-\\d{2}-\\d{2}) \\| (\\d{4}-\\d{2}-\\d{2}) \\|")

// noDeprecationsMarker must appear in docs/api-deprecations.md while the
// table is empty.
const noDeprecationsMarker = "目前无弃用项目"

// deprecationProblems compares the deprecation table with an OpenAPI
// document and the deprecation notes.
func deprecationProblems(list []Deprecation, spec map[string]any, doc string) []string {
	var problems []string
	rows := map[string][2]string{}
	for _, match := range deprecationRow.FindAllStringSubmatch(doc, -1) {
		rows[match[1]] = [2]string{match[2], match[3]}
	}
	listed := map[string]bool{}
	for _, d := range list {
		listed[d.Route] = true
		row, ok := rows[d.Route]
		if !ok {
			problems = append(problems, d.Route+": missing from docs/api-deprecations.md")
		} else if row != [2]string{d.Since.UTC().Format(time.DateOnly), d.Sunset.UTC().Format(time.DateOnly)} {
			problems = append(problems, fmt.Sprintf("%s: documented dates %v differ from the table", d.Route, row))
		}
		method, pattern, _ := strings.Cut(d.Route, " ")
		item, _ := spec["paths"].(map[string]any)[pattern].(map[string]any)
		op, _ := item[strings.ToLower(method)].(map[string]any)
		if op == nil || op["deprecated"] != true {
			problems = append(problems, d.Route+": not marked deprecated in OpenAPI")
		}
	}
	for route := range rows {
		if !listed[route] {
			problems = append(problems, route+": documented as deprecated but not in apiDeprecations")
		}
	}
	for path, raw := range spec["paths"].(map[string]any) {
		for method, op := range raw.(map[string]any) {
			if op.(map[string]any)["deprecated"] == true && !listed[strings.ToUpper(method)+" "+path] {
				problems = append(problems, strings.ToUpper(method)+" "+path+": deprecated in OpenAPI but not in apiDeprecations")
			}
		}
	}
	if len(list) == 0 && !strings.Contains(doc, noDeprecationsMarker) {
		problems = append(problems, "docs/api-deprecations.md must state "+noDeprecationsMarker+" while nothing is deprecated")
	}
	if len(list) > 0 && strings.Contains(doc, noDeprecationsMarker) {
		problems = append(problems, "docs/api-deprecations.md still states "+noDeprecationsMarker)
	}
	return problems
}

// TestDeprecatedRoutesAreDocumented is the G49.2 gate: every route of
// apiDeprecations is registered, deprecated in the committed OpenAPI
// document and listed with the same dates in docs/api-deprecations.md, and
// nothing else is.
func TestDeprecatedRoutesAreDocumented(t *testing.T) {
	if err := validateDeprecations(apiDeprecations); err != nil {
		t.Fatal(err)
	}
	if !slices.IsSortedFunc(apiDeprecations, func(a, b Deprecation) int { return strings.Compare(a.Route, b.Route) }) {
		t.Error("apiDeprecations is not sorted by route")
	}
	doc, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "api-deprecations.md"))
	if err != nil {
		t.Fatal(err)
	}
	committed, err := os.ReadFile(filepath.Join("..", "..", "..", "api", "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	var spec map[string]any
	if err := json.Unmarshal(committed, &spec); err != nil {
		t.Fatal(err)
	}
	for _, problem := range deprecationProblems(apiDeprecations, spec, string(doc)) {
		t.Error(problem)
	}
	registered := registeredRoutes(t, contractRouter(t, ReferenceConfig()))
	for _, d := range apiDeprecations {
		if !registered[d.Route] {
			t.Errorf("%s: deprecated but not registered by the reference rollout", d.Route)
		}
	}

	// Reverse checks: the gate catches each kind of drift.
	extra := testDeprecation()
	if problems := deprecationProblems(append(slices.Clone(apiDeprecations), extra), spec, string(doc)); len(problems) < 2 {
		t.Errorf("undocumented, unmarked deprecation passed the gate: %v", problems)
	}
	row := "\n| `GET /api/v1/items` | 2026-10-01 | 2027-04-01 | none | stale |\n"
	if problems := deprecationProblems(apiDeprecations, spec, string(doc)+row); len(problems) == 0 {
		t.Error("documented route outside the table passed the gate")
	}
	marked := specification(ReferenceConfig(), []Deprecation{extra})
	if problems := deprecationProblems(apiDeprecations, marked, string(doc)); len(problems) == 0 {
		t.Error("OpenAPI deprecation outside the table passed the gate")
	}
	wrongDates := "| `GET /api/v1/system` | 2026-10-02 | 2027-04-01 | `/api/v2/system` | x |\n"
	if problems := deprecationProblems([]Deprecation{extra}, marked, wrongDates); len(problems) != 1 {
		t.Errorf("date drift: %v", problems)
	}
}
