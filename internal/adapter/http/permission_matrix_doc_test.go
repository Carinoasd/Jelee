package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/adapter/compat"
	"github.com/MoYuanCN/Jelee/internal/adapter/postgres"
)

// G49.7 / G49.8: docs/permission-matrix.md states, per capability, which
// roles may call its routes. Every registered route must belong to exactly
// one capability row, and each row's role and session columns must equal
// what the implementation does: the unauthenticated column is probed against
// the real router, the administrator role comes from the OpenAPI role marker
// and the leak route table, the share guest column from guestRoutes, and the
// session column from the OpenAPI session marker and the compatibility
// layer's native-only rule. A document that disagrees is a bug.

const (
	permAllowed = "Y"
	permDenied  = "N"
	// permLoopback marks a route answered only to loopback callers without
	// forwarding headers; every remote caller gets 404.
	permLoopback = "L"
)

type permissionFacts struct {
	anonymous, viewer, guest, admin string
	// login is true when an unauthenticated remote request gets 401.
	login  bool
	native bool
}

type permissionRow struct {
	id, anonymous, viewer, guest, admin, session string
}

type permissionPattern struct {
	row     string
	methods []string
	path    string
	prefix  bool
}

var (
	permissionRowLine     = regexp.MustCompile(`^\| (P\d{2}) \|`)
	permissionPatternLine = regexp.MustCompile(`^(P\d{2}) (\*|[A-Z]+(?:,[A-Z]+)*) (/\S*)$`)
)

func readPermissionMatrix(t *testing.T) (map[string]permissionRow, []permissionPattern) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "permission-matrix.md"))
	if err != nil {
		t.Fatalf("read docs/permission-matrix.md: %v", err)
	}
	rows := map[string]permissionRow{}
	var patterns []permissionPattern
	text := string(data)
	for _, line := range strings.Split(text, "\n") {
		if !permissionRowLine.MatchString(line) {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		if len(cells) != 9 {
			t.Fatalf("matrix row needs 9 cells: %s", line)
		}
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		row := permissionRow{id: cells[0], anonymous: cells[2], viewer: cells[3], guest: cells[4], admin: cells[5], session: cells[6]}
		if _, dup := rows[row.id]; dup {
			t.Fatalf("matrix row %s declared twice", row.id)
		}
		rows[row.id] = row
	}
	_, block, found := strings.Cut(text, "```text permission-routes\n")
	if !found {
		t.Fatal("permission-routes block missing")
	}
	block, _, _ = strings.Cut(block, "```")
	for _, line := range strings.Split(strings.TrimSpace(block), "\n") {
		m := permissionPatternLine.FindStringSubmatch(line)
		if m == nil {
			t.Fatalf("unparsed route line: %q", line)
		}
		p := permissionPattern{row: m[1], path: m[3]}
		if m[2] != "*" {
			p.methods = strings.Split(m[2], ",")
		}
		if strings.HasSuffix(p.path, "**") {
			p.prefix, p.path = true, strings.TrimSuffix(p.path, "**")
		}
		if _, ok := rows[p.row]; !ok {
			t.Fatalf("route line names unknown row: %q", line)
		}
		patterns = append(patterns, p)
	}
	return rows, patterns
}

// matchPermissionRoute picks the line for a route: an exact path wins over
// a prefix, a longer prefix over a shorter one. More than one best match is
// ambiguous and reported.
func matchPermissionRoute(patterns []permissionPattern, route string) (int, bool) {
	method, path, _ := strings.Cut(route, " ")
	best, score, ambiguous := -1, -1, false
	for i, p := range patterns {
		if p.methods != nil && !containsString(p.methods, method) {
			continue
		}
		s := -1
		switch {
		case !p.prefix && p.path == path:
			s = 1 << 20
		case p.prefix && strings.HasPrefix(path, p.path):
			s = len(p.path)
		}
		if s < 0 {
			continue
		}
		if s == score {
			ambiguous = true
		}
		if s > score {
			best, score, ambiguous = i, s, false
		}
	}
	return best, ambiguous
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// permissionFactsFor derives each role's access to every registered route.
func permissionFactsFor(t *testing.T) map[string]permissionFacts {
	t.Helper()
	cfg := leakConfig(t, "postgres://localhost/jelee", 0)
	handler := leakHandler(t, &postgres.Store{}, cfg)
	routes := leakWalk(t, handler)
	spec := Specification(cfg)["paths"].(map[string]any)
	table := leakRouteTable()
	facts := map[string]permissionFacts{}
	for route := range routes {
		method, pattern, _ := strings.Cut(route, " ")
		var op map[string]any
		if item, ok := spec[pattern].(map[string]any); ok {
			op, _ = item[strings.ToLower(method)].(map[string]any)
		}
		// The route is requested without credentials: the authentication
		// middleware answers 401 before any lookup, so no database is needed.
		status := func(remote string) int {
			path := leakPathParam.ReplaceAllString(pattern, "1")
			path = strings.ReplaceAll(path, "*", "heap")
			req := httptest.NewRequest(method, "http://localhost"+path, nil)
			req.RemoteAddr = remote
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			return w.Code
		}
		remote := status("192.0.2.1:1234")
		f := permissionFacts{}
		switch {
		case remote == http.StatusUnauthorized:
			f.login = true
		case remote != http.StatusNotFound:
			f.anonymous = permAllowed
		default:
			if local := status("127.0.0.1:1234"); local != http.StatusNotFound && local != http.StatusUnauthorized {
				f.anonymous = permLoopback
			} else {
				// Unavailable to everyone without a developer mode session.
				f.anonymous = permDenied
			}
		}
		if !f.login {
			f.viewer, f.guest, f.admin = f.anonymous, f.anonymous, f.anonymous
			facts[route] = f
			continue
		}
		f.anonymous = permDenied
		role, _ := op["x-jelee-role"].(string)
		adminOnly := role == "administrator" || table[route].mode == leakAdmin
		guestOnly := role == "guest"
		f.viewer, f.guest, f.admin = permAllowed, permDenied, permAllowed
		if adminOnly || guestOnly {
			f.viewer = permDenied
		}
		if guestOnly {
			f.admin = permDenied
		}
		if _, ok := guestRoutes[route]; ok {
			f.guest = permAllowed
		}
		session, _ := op["x-jelee-session"].(string)
		f.native = session == "native" || compat.HasPrefix(pattern)
		facts[route] = f
	}
	return facts
}

func aggregatePermission(values []string) string {
	all := func(v string) bool {
		for _, x := range values {
			if x != v {
				return false
			}
		}
		return true
	}
	switch {
	case all(permAllowed):
		return "✓"
	case all(permDenied):
		return "✗"
	case all(permLoopback):
		return "仅本机"
	}
	return "部分"
}

// leadingSymbol is the cell's claim: its first word, before any note.
func leadingSymbol(cell string) string {
	for _, s := range []string{"✓", "✗", "部分原生", "部分", "仅本机", "任意", "原生", "—"} {
		if strings.HasPrefix(cell, s) {
			return s
		}
	}
	return cell
}

func TestPermissionMatrixDocumentMatchesRoutes(t *testing.T) {
	rows, patterns := readPermissionMatrix(t)
	facts := permissionFactsFor(t)
	members := map[string][]string{}
	used := make([]bool, len(patterns))
	var problems []string
	for route := range facts {
		i, ambiguous := matchPermissionRoute(patterns, route)
		switch {
		case i < 0:
			problems = append(problems, route+": not assigned to any capability row")
			continue
		case ambiguous:
			problems = append(problems, route+": matches more than one route line equally")
		}
		used[i] = true
		members[patterns[i].row] = append(members[patterns[i].row], route)
	}
	for i, p := range patterns {
		if !used[i] {
			problems = append(problems, p.row+" "+p.path+": route line matches no registered route")
		}
	}
	for id, row := range rows {
		routes := members[id]
		if len(routes) == 0 {
			problems = append(problems, id+": row has no routes")
			continue
		}
		var anonymous, viewer, guest, admin []string
		login, native := 0, 0
		for _, route := range routes {
			f := facts[route]
			anonymous, viewer, guest, admin = append(anonymous, f.anonymous), append(viewer, f.viewer), append(guest, f.guest), append(admin, f.admin)
			if f.login {
				login++
				if f.native {
					native++
				}
			}
		}
		session := "—"
		switch {
		case login == 0:
		case native == 0:
			session = "任意"
		case native == login:
			session = "原生"
		default:
			session = "部分原生"
		}
		for _, c := range []struct{ name, cell, want string }{
			{"unauthenticated", row.anonymous, aggregatePermission(anonymous)},
			{"user", row.viewer, aggregatePermission(viewer)},
			{"share guest", row.guest, aggregatePermission(guest)},
			{"administrator", row.admin, aggregatePermission(admin)},
			{"session", row.session, session},
		} {
			if got := leadingSymbol(c.cell); got != c.want {
				sort.Strings(routes)
				problems = append(problems, id+" "+c.name+" column says "+got+", implementation is "+c.want+" over "+strings.Join(routes, "; "))
			}
		}
	}
	sort.Strings(problems)
	for _, p := range problems {
		t.Error(p)
	}
}

// The two sources of the administrator role must agree: a route the leak
// traversal treats as administrator-only is marked so in OpenAPI too.
func TestPermissionMatrixAdministratorSourcesAgree(t *testing.T) {
	cfg := leakConfig(t, "postgres://localhost/jelee", 0)
	spec := Specification(cfg)["paths"].(map[string]any)
	for route, entry := range leakRouteTable() {
		if entry.mode != leakAdmin {
			continue
		}
		method, pattern, _ := strings.Cut(route, " ")
		item, ok := spec[pattern].(map[string]any)
		if !ok {
			continue
		}
		op, _ := item[strings.ToLower(method)].(map[string]any)
		if role, _ := op["x-jelee-role"].(string); role != "administrator" {
			t.Errorf("%s: administrator-only in the leak table but OpenAPI x-jelee-role is %q", route, role)
		}
	}
}

// The matcher must refuse what the document cannot assign unambiguously.
func TestPermissionMatrixMatcher(t *testing.T) {
	patterns := []permissionPattern{
		{row: "P01", path: "/api/v1/users/", prefix: true},
		{row: "P02", path: "/api/v1/users/me", prefix: false},
		{row: "P03", methods: []string{"GET"}, path: "/api/v1/users/me/", prefix: true},
		{row: "P04", methods: []string{"PUT"}, path: "/api/v1/users/me/", prefix: true},
		{row: "P05", path: "/api/v1/x", prefix: true},
		{row: "P06", path: "/api/v1/x", prefix: true},
	}
	for route, want := range map[string]string{
		"GET /api/v1/users/{id}":        "P01",
		"GET /api/v1/users/me":          "P02",
		"GET /api/v1/users/me/resume":   "P03",
		"PUT /api/v1/users/me/password": "P04",
		"POST /api/v1/users/me/purge":   "P01",
	} {
		i, ambiguous := matchPermissionRoute(patterns, route)
		if i < 0 || ambiguous || patterns[i].row != want {
			t.Errorf("%s matched %d (ambiguous %t), want %s", route, i, ambiguous, want)
		}
	}
	if i, _ := matchPermissionRoute(patterns, "GET /healthz"); i >= 0 {
		t.Error("unmatched route was assigned")
	}
	if _, ambiguous := matchPermissionRoute(patterns, "GET /api/v1/xy"); !ambiguous {
		t.Error("equal prefixes were not reported")
	}
	if aggregatePermission([]string{permAllowed, permDenied}) != "部分" || aggregatePermission([]string{permLoopback}) != "仅本机" || leadingSymbol("部分原生（流）") != "部分原生" {
		t.Error("aggregation changed")
	}
}

// TestPermissionMatrixDump prints the derived facts when
// JELEE_PERMISSION_MATRIX_DUMP is set, to help edit the document.
func TestPermissionMatrixDump(t *testing.T) {
	if os.Getenv("JELEE_PERMISSION_MATRIX_DUMP") == "" {
		t.Skip("set JELEE_PERMISSION_MATRIX_DUMP to print the derived route facts")
	}
	facts := permissionFactsFor(t)
	var lines []string
	for route, f := range facts {
		lines = append(lines, route+" anon="+f.anonymous+" user="+f.viewer+" guest="+f.guest+" admin="+f.admin+" native="+map[bool]string{true: "Y", false: "N"}[f.native])
	}
	sort.Strings(lines)
	t.Log("\n" + strings.Join(lines, "\n"))
}
