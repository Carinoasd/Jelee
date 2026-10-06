package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// referenceDocument is the reference OpenAPI document as plain JSON values.
func referenceDocument(t *testing.T) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(Specification(ReferenceConfig()))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(encoded, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func resolveSchema(doc map[string]any, schema any) map[string]any {
	for range 8 {
		m, _ := schema.(map[string]any)
		ref, ok := m["$ref"].(string)
		if !ok {
			return m
		}
		schema = doc["components"].(map[string]any)["schemas"].(map[string]any)[strings.TrimPrefix(ref, "#/components/schemas/")]
	}
	return nil
}

func properties(schema map[string]any) map[string]any {
	p, _ := schema["properties"].(map[string]any)
	return p
}

// looksLikeList is the classification the guard applies to every GET
// operation: it takes a paging parameter, its data is an array, it reports
// pagination, or its data is not a resource itself (no id) and holds exactly
// one array of identified objects.
func looksLikeList(doc map[string]any, op map[string]any) bool {
	for _, raw := range asList(op["parameters"]) {
		p := raw.(map[string]any)
		if p["in"] == "query" && slices.Contains([]string{"cursor", "offset", "limit"}, p["name"].(string)) {
			return true
		}
	}
	ok200, _ := op["responses"].(map[string]any)["200"].(map[string]any)
	content, _ := ok200["content"].(map[string]any)
	media, _ := content["application/json"].(map[string]any)
	root := resolveSchema(doc, media["schema"])
	if root == nil {
		return false
	}
	if _, ok := properties(root)["pagination"]; ok {
		return true
	}
	data := resolveSchema(doc, properties(root)["data"])
	if data == nil {
		return false
	}
	if data["type"] == "array" {
		return true
	}
	if _, ok := properties(data)["pagination"]; ok {
		return true
	}
	if _, ok := properties(data)["id"]; ok {
		return false
	}
	identified := 0
	for _, value := range properties(data) {
		array := resolveSchema(doc, value)
		if array["type"] != "array" {
			continue
		}
		if _, ok := properties(resolveSchema(doc, array["items"]))["id"]; ok {
			identified++
		}
	}
	return identified == 1
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

// rowSchema is the row schema of a contract's list.
func rowSchema(doc map[string]any, op map[string]any, c *listContract) map[string]any {
	media := op["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)
	data := resolveSchema(doc, properties(resolveSchema(doc, media["schema"]))["data"])
	if c.Items != "" {
		data = resolveSchema(doc, properties(data)[c.Items])
	}
	return resolveSchema(doc, data["items"])
}

// TestOpenAPIListOperationsDeclareListContract is the G08.2/G49.1 gate:
// every list operation of the reference document has a contract (or a
// reasoned exemption) and declares cursor, offset, limit, sort, order and
// fields; its other query parameters are exactly the filter whitelist; the
// selectable fields are exactly the row schema's members.
func TestOpenAPIListOperationsDeclareListContract(t *testing.T) {
	doc := referenceDocument(t)
	paths := doc["paths"].(map[string]any)
	lists := 0
	for path, raw := range paths {
		op, ok := raw.(map[string]any)["get"].(map[string]any)
		if !ok || !strings.HasPrefix(path, "/api/v1/") {
			continue
		}
		c, contracted := listContracts[path]
		reason, exempt := listExemptions[path]
		if !looksLikeList(doc, op) {
			if exempt {
				t.Errorf("%s: exemption for an operation that is not a list", path)
			}
			continue
		}
		lists++
		switch {
		case contracted && exempt:
			t.Errorf("%s: both a list contract and an exemption", path)
			continue
		case exempt:
			if strings.TrimSpace(reason) == "" {
				t.Errorf("%s: exemption needs a reason", path)
			}
			continue
		case !contracted:
			t.Errorf("%s: list operation without a list contract (add it to listContracts or, with a reason, to listExemptions)", path)
			continue
		}
		query := map[string]map[string]any{}
		for _, raw := range asList(op["parameters"]) {
			p := raw.(map[string]any)
			if p["in"] == "query" {
				query[p["name"].(string)] = p
			}
		}
		for _, name := range listStandardParameters {
			if _, ok := query[name]; !ok {
				t.Errorf("%s: list parameter %s is not declared", path, name)
			}
		}
		var others []string
		for name := range query {
			if !slices.Contains(listStandardParameters, name) {
				others = append(others, name)
			}
		}
		sort.Strings(others)
		filters := slices.Sorted(slices.Values(c.Filters))
		if !slices.Equal(others, filters) {
			t.Errorf("%s: filter parameters %v, contract whitelist %v", path, others, filters)
		}
		if limit, ok := query["limit"]; ok {
			if maximum, _ := limit["schema"].(map[string]any)["maximum"].(float64); int(maximum) != c.MaxLimit {
				t.Errorf("%s: limit maximum %v, contract %d", path, maximum, c.MaxLimit)
			}
		}
		for _, name := range []string{"sort", "order"} {
			p, ok := query[name]
			if !ok {
				continue
			}
			want := c.Sorts
			if name == "order" {
				want = c.Orders
			}
			if enum := asList(p["schema"].(map[string]any)["enum"]); enum != nil {
				var got []string
				for _, v := range enum {
					got = append(got, v.(string))
				}
				if !slices.Equal(got, want) {
					t.Errorf("%s: %s enum %v, contract %v", path, name, got, want)
				}
			}
		}
		if _, ok := op["x-jelee-list"]; !ok {
			t.Errorf("%s: x-jelee-list is missing", path)
		}
		row := rowSchema(doc, op, c)
		var members []string
		for name := range properties(row) {
			members = append(members, name)
		}
		sort.Strings(members)
		if !slices.Equal(members, slices.Sorted(slices.Values(c.Fields))) {
			t.Errorf("%s: selectable fields %v, row schema members %v", path, c.Fields, members)
		}
	}
	for path := range listContracts {
		if op, ok := paths[path].(map[string]any)["get"].(map[string]any); !ok || !looksLikeList(doc, op) {
			t.Errorf("stale list contract %s", path)
		}
	}
	for path := range listExemptions {
		if _, ok := paths[path]; !ok {
			t.Errorf("stale list exemption %s", path)
		}
	}
	if lists < len(listContracts) {
		t.Fatalf("classified %d list operations, fewer than the %d contracts", lists, len(listContracts))
	}
}

func TestListContractsAreWellFormed(t *testing.T) {
	for path, c := range listContracts {
		if c.Path != path || len(c.Sorts) == 0 || len(c.Orders) == 0 || c.MaxLimit < 1 || c.DefaultLimit > c.MaxLimit || c.OffsetMax < 1 {
			t.Errorf("%s: malformed contract %+v", path, c)
		}
		if c.Paging != pagingMemory && (len(c.Sorts) != 1 || len(c.Orders) != 1 || c.DefaultLimit < 1) && path != "/api/v1/items" {
			t.Errorf("%s: a storage paged list sorts by its storage order only and has a default limit", path)
		}
		if id := c.id(); id != "" && !slices.Contains(c.Fields, id) {
			t.Errorf("%s: identifying member %s is not selectable", path, id)
		}
		for _, f := range c.Filters {
			if slices.Contains(listStandardParameters, f) {
				t.Errorf("%s: filter %s shadows a list parameter", path, f)
			}
		}
		for _, s := range c.Sorts {
			if !slices.Contains(c.Fields, s) && c.Paging == pagingMemory {
				t.Errorf("%s: memory sort key %s is not a row member", path, s)
			}
		}
	}
}

var testContract = &listContract{Path: "/test", Fields: []string{"id", "name", "secret", "size"}, Sorts: []string{"name", "size"}, Orders: ascending,
	Filters: []string{"state"}, Paging: pagingMemory, MaxLimit: 10, OffsetMax: 1000}

func parseTestQuery(t *testing.T, c *listContract, query string) (listRequest, error) {
	t.Helper()
	return c.parse(httptest.NewRequest("GET", "http://localhost/x?"+query, nil))
}

func TestListQueryRejectsEverythingOutsideTheWhitelists(t *testing.T) {
	keysetContract := &listContract{Path: "/k", Fields: []string{"id", "name"}, Sorts: []string{"id"}, Orders: []string{"asc"}, Paging: pagingKeyset, DefaultLimit: 5, MaxLimit: 10, OffsetMax: 30}
	for _, tc := range []struct {
		c     *listContract
		query string
	}{
		{testContract, "unknown=1"}, {testContract, "%zz=1"}, {testContract, "state=a&state=b"}, {testContract, "sort=secretive"},
		{testContract, "sort=name&order=up"}, {testContract, "order=desc"}, {testContract, "fields=name,password"}, {testContract, "fields="},
		{testContract, "fields=name,"}, {testContract, "cursor=o.1&offset=1"}, {testContract, "limit=0"}, {testContract, "limit=11"},
		{testContract, "limit=x"}, {testContract, "offset=-1"}, {testContract, "offset=1001"}, {testContract, "cursor=1"},
		{testContract, "cursor=o.01"}, {testContract, "cursor=o."}, {testContract, "cursor=o.9999999"}, {testContract, "limit="},
		{keysetContract, "sort=name"}, {keysetContract, "order=desc"}, {keysetContract, "offset=31"}, {keysetContract, "state=x"},
	} {
		if _, err := parseTestQuery(t, tc.c, tc.query); err != domain.ErrInvalid {
			t.Errorf("%s ?%s: got %v, want ErrInvalid", tc.c.Path, tc.query, err)
		}
	}
	q, err := parseTestQuery(t, testContract, "state=on&sort=size&order=desc&fields=name&cursor=o.4&limit=3")
	if err != nil || q.Filters["state"] != "on" || q.Sort != "size" || !q.Descending || q.Offset != 4 || !q.OffsetSet || q.Limit != 3 || !q.LimitSet ||
		len(q.Fields) != 2 || !q.Fields["id"] || !q.Fields["name"] {
		t.Fatalf("parsed %+v, %v", q, err)
	}
	q, err = parseTestQuery(t, keysetContract, "")
	if err != nil || q.Sort != "id" || q.Descending || q.Limit != 5 || q.Fields != nil {
		t.Fatalf("keyset defaults %+v, %v", q, err)
	}
	if q, err = parseTestQuery(t, testContract, ""); err != nil || q.LimitSet || q.Sort != "" {
		t.Fatalf("memory defaults %+v, %v", q, err)
	}
	if _, err := listQuery(httptest.NewRequest("GET", "http://localhost/x", nil), "/api/v1/not-a-list"); err == nil {
		t.Fatal("a path without a contract must fail")
	}
}

type testRow struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Size   *int   `json:"size,omitempty"`
	Secret string `json:"secret,omitempty"`
}

func TestMemoryPageSortsPagesAndProjects(t *testing.T) {
	n := func(v int) *int { return &v }
	rows := []testRow{{ID: "a", Name: "pear", Size: n(3)}, {ID: "b", Name: "apple", Size: n(10)}, {ID: "c", Name: "fig"}, {ID: "d", Name: "apple", Size: n(2), Secret: "s"}}
	page := func(query string) ([]listRow, map[string]any) {
		t.Helper()
		q, err := parseTestQuery(t, testContract, query)
		if err != nil {
			t.Fatal(err)
		}
		out, pagination, err := memoryPage(q, rows)
		if err != nil {
			t.Fatal(err)
		}
		return out, pagination
	}
	ids := func(rows []listRow) string {
		var out []string
		for _, r := range rows {
			var id string
			_ = json.Unmarshal(r["id"], &id)
			out = append(out, id)
		}
		return strings.Join(out, ",")
	}
	if got, p := page(""); ids(got) != "a,b,c,d" || p["total"] != 4 || p["nextCursor"] != "" || p["limit"] != nil {
		t.Fatalf("storage order %s %v", ids(got), p)
	}
	// Ties keep the storage order; absent values sort first.
	if got, _ := page("sort=name"); ids(got) != "b,d,c,a" {
		t.Fatalf("sort by name %s", ids(got))
	}
	if got, _ := page("sort=size&order=desc"); ids(got) != "b,a,d,c" {
		t.Fatalf("sort by size desc %s", ids(got))
	}
	got, p := page("sort=size&limit=3")
	if ids(got) != "c,d,a" || p["nextCursor"] != "o.3" || p["limit"] != 3 || p["offset"] != 0 {
		t.Fatalf("first page %s %v", ids(got), p)
	}
	if got, p = page("sort=size&limit=3&cursor=o.3"); ids(got) != "b" || p["nextCursor"] != "" || p["offset"] != 3 {
		t.Fatalf("second page %s %v", ids(got), p)
	}
	if got, p = page("offset=9"); len(got) != 0 || p["total"] != 4 {
		t.Fatalf("past the end %v %v", got, p)
	}
	got, _ = page("fields=name")
	for _, r := range got {
		if len(r) != 2 || r["id"] == nil || r["name"] == nil {
			t.Fatalf("projection kept %v", r)
		}
	}
	if got, _ := memoryPageOf(t, nil); got == nil || len(got) != 0 {
		t.Fatal("a nil list is an empty page")
	}
}

func memoryPageOf(t *testing.T, rows any) ([]listRow, map[string]any) {
	t.Helper()
	q, _ := parseTestQuery(t, testContract, "")
	out, p, err := memoryPage(q, rows)
	if err != nil {
		t.Fatal(err)
	}
	return out, p
}

// Field selection works on the encoded, already masked response: it can
// only remove members, never add one a mask removed.
func TestListProjectionOnlyRemovesMembers(t *testing.T) {
	q, err := parseTestQuery(t, testContract, "fields=secret,size")
	if err != nil {
		t.Fatal(err)
	}
	q.contract = &listContract{Path: "/x", Items: "rows", Fields: testContract.Fields, Sorts: testContract.Sorts, Orders: ascending, Paging: pagingKeyset, MaxLimit: 10}
	// The masked row has no secret: the selection does not bring it back.
	data, err := q.project(map[string]any{"rows": []testRow{{ID: "a", Name: "n"}, {ID: "b", Name: "m", Secret: "visible"}}, "pagination": map[string]any{"nextCursor": "z"}})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(data)
	if string(encoded) != `{"pagination":{"nextCursor":"z"},"rows":[{"id":"a"},{"id":"b","secret":"visible"}]}` {
		t.Fatalf("projection %s", encoded)
	}
	q.contract.Items = ""
	data, err = q.project([]testRow{{ID: "a", Name: "n"}})
	if encoded, _ = json.Marshal(data); err != nil || string(encoded) != `[{"id":"a"}]` {
		t.Fatalf("array projection %s %v", encoded, err)
	}
	q.Fields = nil
	if same, _ := q.project("untouched"); same != "untouched" {
		t.Fatal("no selection must not re-encode")
	}
}

func TestKeysetPageWalksOffsetThroughCursorPages(t *testing.T) {
	all := make([]int, 250)
	for i := range all {
		all[i] = i
	}
	calls := 0
	fetch := func(cursor string, limit int) ([]int, string, error) {
		calls++
		start := 0
		if cursor != "" {
			last, err := strconv.Atoi(cursor)
			if err != nil {
				t.Fatal(err)
			}
			start = last + 1
		}
		if limit > 100 {
			t.Fatalf("page of %d rows exceeds MaxLimit", limit)
		}
		end := min(start+limit, len(all))
		rows := all[start:end]
		return rows, nextKey(rows, limit, func(v int) string { return fmt.Sprint(v) }), nil
	}
	c := &listContract{Path: "/k", Sorts: []string{"id"}, Orders: []string{"asc"}, Paging: pagingKeyset, DefaultLimit: 50, MaxLimit: 100, OffsetMax: 1000}
	q, _ := c.parse(httptest.NewRequest("GET", "http://localhost/x?offset=230&limit=50", nil))
	rows, next, err := keysetPage(q, fetch)
	if err != nil || len(rows) != 20 || rows[0] != 230 || next != "" || calls != 4 {
		t.Fatalf("offset 230: rows=%v next=%q calls=%d err=%v", rows, next, calls, err)
	}
	calls = 0
	q, _ = c.parse(httptest.NewRequest("GET", "http://localhost/x?offset=300", nil))
	if rows, next, err = keysetPage(q, fetch); err != nil || len(rows) != 0 || next != "" || calls != 3 {
		t.Fatalf("offset past the end: rows=%v next=%q calls=%d", rows, next, calls)
	}
	q, _ = c.parse(httptest.NewRequest("GET", "http://localhost/x?cursor=9&limit=2", nil))
	if rows, next, _ = keysetPage(q, fetch); len(rows) != 2 || rows[0] != 10 || next != "11" {
		t.Fatalf("cursor page rows=%v next=%q", rows, next)
	}
	failing := func(string, int) ([]int, string, error) { return nil, "", domain.ErrDatabase }
	q, _ = c.parse(httptest.NewRequest("GET", "http://localhost/x?offset=5", nil))
	if _, _, err := keysetPage(q, failing); err != domain.ErrDatabase {
		t.Fatalf("skip errors must surface, got %v", err)
	}
}

// sessionListRepository serves one user's sessions for the memory list.
type sessionListRepository struct {
	httpAccountRepository
	sessions []domain.Session
}

func (r sessionListRepository) ListSessions(context.Context, domain.Actor, string) ([]domain.Session, error) {
	return r.sessions, nil
}

func TestListParametersOnAccountRoutes(t *testing.T) {
	var cursors []string
	repo := httpAccountRepository{list: func(_ context.Context, _ domain.Actor, cursor string, limit int, _ bool) ([]domain.User, error) {
		cursors = append(cursors, fmt.Sprintf("%s/%d", cursor, limit))
		if cursor == "" {
			return []domain.User{{ID: itemID, Name: "skipped"}}, nil
		}
		return []domain.User{{ID: userID, Name: "alice", DisplayName: "Alice", Locale: "en-US"}}, nil
	}}
	f := newAccountHTTPFixture(t, repo, nil)
	w := f.serve(accountRequest("GET", "/api/v1/users?offset=1&limit=1&fields=name", "", "a"))
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"data":{"pagination":{"limit":1,"nextCursor":"`+userID+`"},"users":[{"id":"`+userID+`","name":"alice"}]}}` {
		t.Fatalf("users with offset and fields: %d %s", w.Code, w.Body.String())
	}
	if strings.Join(cursors, " ") != "/1 "+itemID+"/1" {
		t.Fatalf("offset walk cursors %v", cursors)
	}
	for _, query := range []string{"sort=name", "sort=id&order=desc", "fields=passwordHash", "fields=", "offset=1001", "offset=1&cursor=" + userID} {
		assertProblem(t, f.serve(accountRequest("GET", "/api/v1/users?"+query, "", "a")), 400, "invalid_request")
	}
	if w := f.serve(accountRequest("GET", "/api/v1/users?sort=id&order=asc", "", "a")); w.Code != 200 {
		t.Fatalf("the storage order is accepted explicitly: %d", w.Code)
	}

	seen := func(minutes int) *time.Time {
		at := time.Date(2026, 1, 1, 0, minutes, 0, 0, time.UTC)
		return &at
	}
	sessions := []domain.Session{
		{ID: "a0000000-0000-4000-8000-000000000001", UserID: userID, DeviceName: "old", LastSeenAt: seen(1), LastIP: "198.51.100.1"},
		{ID: "a0000000-0000-4000-8000-000000000002", UserID: userID, DeviceName: "new", LastSeenAt: seen(9)},
		{ID: "a0000000-0000-4000-8000-000000000003", UserID: userID, DeviceName: "never"},
	}
	f2 := newAccountHTTPFixtureWith(t, sessionListRepository{sessions: sessions}, nil)
	type page struct {
		Data       []map[string]any `json:"data"`
		Pagination map[string]any   `json:"pagination"`
	}
	read := func(query string) page {
		t.Helper()
		w := f2.serve(accountRequest("GET", "/api/v1/users/"+userID+"/sessions?"+query, "", "u"))
		var p page
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &p) != nil {
			t.Fatalf("?%s: %d %s", query, w.Code, w.Body.String())
		}
		return p
	}
	p := read("sort=lastSeenAt&order=desc&limit=1")
	if len(p.Data) != 1 || p.Data[0]["deviceName"] != "new" || p.Pagination["nextCursor"] != "o.1" || p.Pagination["total"] != float64(3) {
		t.Fatalf("first page %+v", p)
	}
	p = read("sort=lastSeenAt&order=desc&limit=5&cursor=o.1&fields=deviceName,lastIp")
	if len(p.Data) != 2 || p.Data[0]["deviceName"] != "old" || p.Data[1]["deviceName"] != "never" || p.Pagination["nextCursor"] != "" {
		t.Fatalf("second page %+v", p)
	}
	// lastIp was absent on the session without one; selecting it adds nothing.
	if _, ok := p.Data[1]["lastIp"]; ok || len(p.Data[1]) != 2 || p.Data[0]["lastIp"] != "198.51.100.1" {
		t.Fatalf("projection %+v", p.Data)
	}
	if p = read(""); len(p.Data) != 3 || p.Pagination["limit"] != nil {
		t.Fatalf("without limit every row is returned: %+v", p)
	}
	for _, query := range []string{"sort=deviceName", "order=desc", "fields=token", "limit=101", "cursor=" + userID} {
		assertProblem(t, f2.serve(accountRequest("GET", "/api/v1/users/"+userID+"/sessions?"+query, "", "u")), 400, "invalid_request")
	}
}
