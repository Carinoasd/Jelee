package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// Unified list conventions (G08.2, G49.1). Every native list operation is
// described by one listContract: the shared parser reads cursor, offset,
// limit, sort, order and fields with the operation's whitelists, and the
// shared helpers page, sort and project the result. Unknown parameters,
// sort keys, orders and fields are 400 invalid_request like every other
// strict query. listSpecification documents the same contract in OpenAPI
// and TestOpenAPIListOperationsDeclareListContract requires that every list
// operation has one (or a reasoned exemption).

// listPaging is how an operation pages natively.
type listPaging string

const (
	// pagingKeyset lists page by an opaque cursor in storage order. offset
	// is accepted for compatibility and walked through cursor pages, so it is
	// bounded by listKeysetOffsetMax.
	pagingKeyset listPaging = "keyset"
	// pagingOffset lists page by offset in storage; cursor is an opaque
	// offset token.
	pagingOffset listPaging = "offset"
	// pagingMemory lists are small bounded sets read whole; sorting, paging
	// and projection happen in memory and limit is optional.
	pagingMemory listPaging = "memory"
)

// listKeysetOffsetMax bounds offset on keyset lists: each skipped page is
// one storage read of at most MaxLimit rows.
const listKeysetOffsetMax = 1000

// listOffsetCursorPrefix marks the opaque cursor of offset and memory lists.
const listOffsetCursorPrefix = "o."

// Standard list query parameters, in documentation order.
var listStandardParameters = []string{"cursor", "offset", "limit", "sort", "order", "fields"}

type listContract struct {
	// Path is the OpenAPI path of the GET operation.
	Path string
	// Items is the member of data holding the rows; empty when data itself
	// is the array.
	Items string
	// ID is the identifying member a projection always keeps; "-" when the
	// rows have none.
	ID string
	// Fields are the selectable row members: exactly the properties of the
	// row schema (checked against OpenAPI).
	Fields []string
	// Sorts is the sort whitelist. Keyset and offset lists sort natively by
	// Sorts[0] only; memory lists keep the storage order without sort.
	Sorts []string
	// Orders is the order whitelist; Orders[0] is the default.
	Orders []string
	// Filters are the operation's own query parameters (its filter
	// whitelist); their values are validated by the handler.
	Filters []string
	Paging  listPaging
	// DefaultLimit applies without limit; zero on memory lists returns
	// every row.
	DefaultLimit, MaxLimit int
	// OffsetMax bounds offset.
	OffsetMax int
}

func (c *listContract) id() string {
	switch c.ID {
	case "":
		return "id"
	case "-":
		return ""
	}
	return c.ID
}

// keysetSort describes a keyset or offset list's single native order.
func keysetSort(key, order string) ([]string, []string) { return []string{key}, []string{order} }

var (
	ascending  = []string{"asc", "desc"}
	itemSorts  = []string{"name", "premiereDate", "productionYear"}
	userFields = []string{"admin", "allowNative", "createdAt", "deletedAt", "disabled", "displayName", "hidden", "id", "locale", "name"}
	// sessionFields is shared by the administrator and per-user listings.
	sessionFields = []string{"client", "clientKind", "createdAt", "deviceId", "deviceName", "expiresAt", "id", "lastIp", "lastSeenAt", "revokedAt", "userId", "version"}
)

func keyset(path, items string, fields []string, sortKey, order string, defaultLimit, maxLimit int, filters ...string) *listContract {
	sorts, orders := keysetSort(sortKey, order)
	return &listContract{Path: path, Items: items, Fields: fields, Sorts: sorts, Orders: orders, Filters: filters, Paging: pagingKeyset, DefaultLimit: defaultLimit, MaxLimit: maxLimit, OffsetMax: listKeysetOffsetMax}
}

func memory(path, items, id string, fields, sorts []string) *listContract {
	return &listContract{Path: path, Items: items, ID: id, Fields: fields, Sorts: sorts, Orders: ascending, Paging: pagingMemory, MaxLimit: 100, OffsetMax: domain.BrowseOffsetMax}
}

// listContracts holds every native list operation, keyed by OpenAPI path.
var listContracts = func() map[string]*listContract {
	contracts := []*listContract{
		{Path: "/api/v1/items", Fields: []string{"id", "kind", "libraryId", "parentId", "premiereDate", "productionYear", "title"}, Sorts: itemSorts, Orders: ascending,
			Filters: []string{"libraryId", "parentId", "type", "q"}, Paging: pagingKeyset, DefaultLimit: 50, MaxLimit: itemsPageMax, OffsetMax: domain.BrowseOffsetMax},
		keyset("/api/v1/users", "users", userFields, "id", "asc", 50, 100, "includeDeleted"),
		keyset("/api/v1/sessions", "sessions", sessionFields, "id", "asc", 50, 100),
		keyset("/api/v1/jobs", "jobs", []string{"attempts", "bytes", "cancelRequested", "createdAt", "directories", "errorCode", "files", "finishedAt", "id", "kind", "libraryId", "missing", "priority", "reviewRequired", "skipped", "startedAt", "state"}, "id", "asc", 50, 100, "state"),
		keyset("/api/v1/jobs/{id}/entries", "entries", []string{"id", "kind", "modifiedUnixNano", "path", "rootId", "size"}, "id", "asc", 50, 100),
		keyset("/api/v1/libraries", "libraries", []string{"id", "name", "roots"}, "id", "asc", 50, 100),
		keyset("/api/v1/libraries/{id}/catalog-sync/pending", "entries", []string{"absolute", "confidence", "episode", "episodeEnd", "id", "kind", "path", "reason", "rootId", "season", "special", "title", "year"}, "id", "asc", 50, 100),
		keyset("/api/v1/libraries/{id}/nfo/current-validations", "items", []string{"entries", "errorCount", "expiresAt", "failureCode", "id", "issueCount", "issuesTruncated", "observedAt", "path", "rootId", "status", "warningCount"}, "id", "asc", domain.NFOObservationPageDefault, domain.NFOObservationPageMax),
		keyset("/api/v1/webhooks/{id}/deliveries", "deliveries", []string{"attempts", "createdAt", "eventId", "eventType", "id", "lastAttemptAt", "lastOutcome", "lastStatus", "nextAttemptAt", "occurredAt", "replays", "state", "webhookId"}, "createdAt", "desc", 50, 100, "state"),
		keyset("/api/v1/client-control/hits", "hits", []string{"action", "appName", "bucket", "hits", "id", "mode", "network", "ruleId", "surface", "userAgent", "userId"}, "id", "desc", 50, 100, "ruleId", "mode", "since", "until"),
		keyset("/api/v1/client-control/clients", "clients", []string{"activeSessions", "alias", "appName", "appVersion", "blockRuleId", "blocked", "clientKind", "deviceId", "deviceName", "firstSeenAt", "id", "lastIp", "lastSeenAt", "lastUserId", "trusted", "userAgent"}, "lastSeenAt", "desc", 50, 100),
		keyset("/api/v1/shares/{id}/access", "records", []string{"actorId", "clientKind", "deviceName", "event", "id", "ip", "occurredAt", "reason", "route", "sessionId"}, "occurredAt", "desc", 50, 100),
		keyset("/api/v1/collections", "collections", []string{"coverItemId", "createdAt", "id", "itemCount", "name", "nfoName", "overview", "updatedAt"}, "name", "asc", collectionPageDefault, domain.CollectionPageMax),
		keyset("/api/v1/playlists", "playlists", []string{"coverItemId", "createdAt", "id", "itemCount", "name", "owned", "ownerId", "ownerName", "public", "updatedAt"}, "name", "asc", collectionPageDefault, domain.CollectionPageMax),
		{Path: "/api/v1/jobs/{id}/ignore", Items: "entries", ID: "-", Fields: []string{"family", "kind", "matchedPath", "outcome", "path", "reason", "rootId", "ruleDirectory", "ruleLine", "source"},
			Sorts: []string{"path"}, Orders: []string{"asc"}, Paging: pagingKeyset, DefaultLimit: 50, MaxLimit: 100, OffsetMax: listKeysetOffsetMax},
		{Path: "/api/v1/users/me/resume", Items: "items", Fields: []string{"id", "kind", "libraryId", "parentId", "title", "userData"}, Sorts: []string{"lastPlayedAt"}, Orders: []string{"desc"},
			Paging: pagingOffset, DefaultLimit: 50, MaxLimit: domain.BrowseLimitMax, OffsetMax: domain.BrowseOffsetMax},
		memory("/api/v1/users/{id}/sessions", "", "", sessionFields, []string{"createdAt", "lastSeenAt", "expiresAt"}),
		memory("/api/v1/users/{id}/app-passwords", "", "", []string{"createdAt", "id", "lastUsedAt", "name"}, []string{"createdAt", "lastUsedAt", "name"}),
		memory("/api/v1/users/{id}/libraries", "", "libraryId", []string{"libraryId", "name"}, []string{"name", "libraryId"}),
		memory("/api/v1/shares", "", "", []string{"activeSessions", "allowPlayback", "createdAt", "createdBy", "expiresAt", "id", "itemId", "itemKind", "itemTitle", "lastUsedAt", "libraryId", "libraryName", "maxStreams", "note", "readOnly", "revokedAt", "state"}, []string{"createdAt", "expiresAt", "lastUsedAt", "state"}),
		memory("/api/v1/access/network-rules", "", "", []string{"cidrs", "clientKinds", "createdAt", "enabled", "id", "includeAdmins", "libraryId", "libraryName", "network", "note", "updatedAt"}, []string{"createdAt", "updatedAt", "libraryName"}),
		memory("/api/v1/client-control/rules", "", "", []string{"action", "caseFold", "createdAt", "dimension", "enabled", "header", "hitCount", "id", "intent", "lastHitAt", "libraries", "match", "note", "pattern", "priority", "rateLimit", "scopeKind", "scopeValues", "updatedAt", "window"}, []string{"priority", "createdAt", "updatedAt", "hitCount", "lastHitAt"}),
		memory("/api/v1/playback/sessions", "", "", []string{"clientName", "delivery", "deviceId", "id", "itemId", "itemTitle", "lastReportAt", "paused", "positionTicks", "runtimeTicks", "sourceId", "startedAt", "userId", "userName"}, []string{"startedAt", "lastReportAt", "userName", "itemTitle"}),
		memory("/api/v1/webhooks", "webhooks", "", []string{"createdAt", "dead", "enabled", "events", "headerNames", "id", "name", "pending", "previousSecretUntil", "retry", "timeoutSeconds", "updatedAt", "url"}, []string{"name", "createdAt", "updatedAt"}),
	}
	out := make(map[string]*listContract, len(contracts))
	for _, c := range contracts {
		out[c.Path] = c
	}
	return out
}()

// listExemptions are GET operations that look like lists but do not take
// the list parameters, with the reason (G08.2 exemption table, mirrored in
// docs/api-reference.md).
var listExemptions = map[string]string{
	"/api/v1/access/parental-ratings":                                       "fixed vocabulary of rating codes and levels compiled into the server, not a collection of stored resources",
	"/api/v1/client-control/hits/export":                                    "export download: the whole filtered set (bounded by the hit retention) in one attachment, with its own count",
	"/api/v1/libraries/{id}/nfo/current-validations/{observationId}/issues": "issues of one observation: a retained prefix of at most 64 diagnostics without identity, paged by offset only",
	"/api/v1/site/plugins":                                                  "plugin switches of the site settings document (a fixed, small map rendered as a list), not a collection",
	"/api/v1/site/plugins/config":                                           "administrator view of the same plugin switches; one settings document",
	"/api/v1/collections/{id}":                                              "one collection with its member items: a resource view whose embedded list is bounded by the collection size",
	"/api/v1/items/{id}/sources":                                            "media sources (versions) of one item, part of the item resource and bounded by its versions",
	"/api/v1/items/{id}/playback":                                           "playback information of one item: its sources with tracks, part of the item resource",
	"/api/v1/watch-stats/export":                                            "NDJSON or CSV export download; limit only lowers the export row cap (stats.exportMaxRows)",
}

// listRequest is a parsed list query.
type listRequest struct {
	contract *listContract
	Cursor   string
	Offset   int
	// OffsetSet reports an explicit offset (or an offset cursor).
	OffsetSet bool
	Limit     int
	// LimitSet is false when a memory list returns every row.
	LimitSet   bool
	Sort       string
	Descending bool
	// Fields is the projection; nil keeps every member.
	Fields  map[string]bool
	Filters map[string]string
}

// listQuery parses r against the registered contract of path. A path
// without a contract is a programming error and answered 500.
func listQuery(r *http.Request, path string) (listRequest, error) {
	c, ok := listContracts[path]
	if !ok {
		return listRequest{}, errListContractMissing
	}
	return c.parse(r)
}

type listContractError struct{}

func (listContractError) Error() string { return "list operation has no contract" }

var errListContractMissing error = listContractError{}

func (c *listContract) parse(r *http.Request) (listRequest, error) {
	q := listRequest{contract: c, Filters: map[string]string{}, Limit: c.DefaultLimit}
	values, err := parseQuery(r.URL.RawQuery)
	if err != nil {
		return q, domain.ErrInvalid
	}
	for key, vs := range values {
		if len(vs) != 1 || !slices.Contains(listStandardParameters, key) && !slices.Contains(c.Filters, key) {
			return q, domain.ErrInvalid
		}
		if slices.Contains(c.Filters, key) {
			q.Filters[key] = vs[0]
		}
	}
	if err := q.parsePaging(values); err != nil {
		return q, err
	}
	if err := q.parseOrder(values); err != nil {
		return q, err
	}
	if raw, ok := values["fields"]; ok {
		if q.Fields, err = c.parseFields(raw[0]); err != nil {
			return q, err
		}
	}
	return q, nil
}

func (q *listRequest) parsePaging(values map[string][]string) error {
	c := q.contract
	if raw, ok := values["limit"]; ok {
		n, err := strconv.Atoi(raw[0])
		if err != nil || n < 1 || n > c.MaxLimit {
			return domain.ErrInvalid
		}
		q.Limit, q.LimitSet = n, true
	} else if c.DefaultLimit > 0 {
		q.LimitSet = true
	}
	cursor, hasCursor := values["cursor"]
	offset, hasOffset := values["offset"]
	if hasCursor && hasOffset {
		return domain.ErrInvalid
	}
	if hasOffset {
		n, err := strconv.Atoi(offset[0])
		if err != nil || n < 0 || n > c.OffsetMax {
			return domain.ErrInvalid
		}
		q.Offset, q.OffsetSet = n, true
	}
	if hasCursor {
		if c.Paging == pagingKeyset {
			q.Cursor = cursor[0]
			return nil
		}
		n, ok := parseOffsetCursor(cursor[0])
		if !ok || n > c.OffsetMax {
			return domain.ErrInvalid
		}
		q.Offset, q.OffsetSet = n, true
	}
	return nil
}

func (q *listRequest) parseOrder(values map[string][]string) error {
	c := q.contract
	order, hasOrder := values["order"]
	if hasOrder && !slices.Contains(c.Orders, order[0]) {
		return domain.ErrInvalid
	}
	raw, hasSort := values["sort"]
	if hasSort {
		if !slices.Contains(c.Sorts, raw[0]) {
			return domain.ErrInvalid
		}
		q.Sort = raw[0]
	} else if c.Paging != pagingMemory {
		q.Sort = c.Sorts[0]
	} else if hasOrder {
		// A memory list keeps its storage order without sort; an order
		// alone would have nothing to apply to.
		return domain.ErrInvalid
	}
	direction := c.Orders[0]
	if hasOrder {
		direction = order[0]
	}
	q.Descending = direction == "desc"
	return nil
}

// parseFields reads a comma separated projection. Unknown or empty names are
// invalid; the identifying member is always kept.
func (c *listContract) parseFields(raw string) (map[string]bool, error) {
	fields := map[string]bool{}
	for name := range strings.SplitSeq(raw, ",") {
		if !slices.Contains(c.Fields, name) {
			return nil, domain.ErrInvalid
		}
		fields[name] = true
	}
	if id := c.id(); id != "" {
		fields[id] = true
	}
	return fields, nil
}

func offsetCursor(n int) string { return listOffsetCursorPrefix + strconv.Itoa(n) }

func parseOffsetCursor(raw string) (int, bool) {
	digits, ok := strings.CutPrefix(raw, listOffsetCursorPrefix)
	if !ok || digits == "" || len(digits) > 7 || digits[0] == '0' && len(digits) > 1 {
		return 0, false
	}
	n, err := strconv.Atoi(digits)
	return n, err == nil && n >= 0
}

// keysetPage reads one page of a keyset list. fetch returns the rows after
// cursor and the cursor that continues them (empty after the last page).
// An offset walks forward through pages of at most MaxLimit rows first.
func keysetPage[T any](q listRequest, fetch func(cursor string, limit int) ([]T, string, error)) ([]T, string, error) {
	cursor := q.Cursor
	for remaining := q.Offset; remaining > 0; {
		n := min(remaining, q.contract.MaxLimit)
		rows, next, err := fetch(cursor, n)
		if err != nil {
			return nil, "", err
		}
		if len(rows) < n || next == "" {
			return []T{}, "", nil
		}
		cursor, remaining = next, remaining-n
	}
	return fetch(cursor, q.Limit)
}

// nextKey is the keyset continuation of a page: the key of its last row when
// the page is full.
func nextKey[T any](rows []T, limit int, key func(T) string) string {
	if len(rows) == 0 || len(rows) < limit {
		return ""
	}
	return key(rows[len(rows)-1])
}

// listRow is one row of a memory list or a projection, by JSON member.
type listRow = map[string]json.RawMessage

// memoryPage sorts, pages and projects a whole list read into memory. It
// returns the page and its pagination object.
func memoryPage(q listRequest, rows any) ([]listRow, map[string]any, error) {
	var all []listRow
	if err := remarshal(rows, &all); err != nil {
		return nil, nil, err
	}
	if all == nil {
		all = []listRow{}
	}
	if q.Sort != "" {
		keys := make([]listSortKey, len(all))
		for i, row := range all {
			keys[i] = newListSortKey(row[q.Sort])
		}
		index := make([]int, len(all))
		for i := range index {
			index[i] = i
		}
		sort.SliceStable(index, func(a, b int) bool {
			cmp := keys[index[a]].compare(keys[index[b]])
			if q.Descending {
				cmp = -cmp
			}
			return cmp < 0
		})
		sorted := make([]listRow, len(all))
		for i, at := range index {
			sorted[i] = all[at]
		}
		all = sorted
	}
	total := len(all)
	start := min(q.Offset, total)
	end := total
	if q.LimitSet {
		end = min(start+q.Limit, total)
	}
	page := all[start:end]
	next := ""
	if end < total {
		next = offsetCursor(end)
	}
	pagination := map[string]any{"nextCursor": next, "offset": start, "total": total}
	if q.LimitSet {
		pagination["limit"] = q.Limit
	}
	return q.projectRows(page), pagination, nil
}

// listSortKey orders JSON scalars: absent and null first, then false, true,
// numbers and strings (RFC 3339 timestamps compare as strings).
type listSortKey struct {
	rank   int
	number float64
	text   string
}

func newListSortKey(raw json.RawMessage) listSortKey {
	var v any
	if len(raw) == 0 || json.Unmarshal(raw, &v) != nil {
		return listSortKey{}
	}
	switch v := v.(type) {
	case bool:
		if v {
			return listSortKey{rank: 2}
		}
		return listSortKey{rank: 1}
	case float64:
		return listSortKey{rank: 3, number: v}
	case string:
		return listSortKey{rank: 4, text: v}
	}
	return listSortKey{}
}

func (k listSortKey) compare(o listSortKey) int {
	switch {
	case k.rank != o.rank:
		return k.rank - o.rank
	case k.number < o.number:
		return -1
	case k.number > o.number:
		return 1
	}
	return strings.Compare(k.text, o.text)
}

// project applies the field selection to the rows inside data (the value
// written under "data"). Projection works on the encoded response, after
// every visibility rule and mask, and only removes members: a field the
// caller may not see is absent before and stays absent.
func (q listRequest) project(data any) (any, error) {
	if q.Fields == nil {
		return data, nil
	}
	if q.contract.Items == "" {
		var rows []listRow
		if err := remarshal(data, &rows); err != nil {
			return nil, err
		}
		return q.projectRows(rows), nil
	}
	var envelope map[string]json.RawMessage
	if err := remarshal(data, &envelope); err != nil {
		return nil, err
	}
	var rows []listRow
	if raw := envelope[q.contract.Items]; len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		if err := json.Unmarshal(raw, &rows); err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(q.projectRows(rows))
		if err != nil {
			return nil, err
		}
		envelope[q.contract.Items] = encoded
	}
	return envelope, nil
}

func (q listRequest) projectRows(rows []listRow) []listRow {
	if q.Fields == nil {
		return rows
	}
	out := make([]listRow, len(rows))
	for i, row := range rows {
		kept := make(listRow, len(q.Fields))
		for name, value := range row {
			if q.Fields[name] {
				kept[name] = value
			}
		}
		out[i] = kept
	}
	return out
}

func remarshal(value any, target any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, target)
}

// listResponse is a data-array list with a top-level pagination object;
// accountEndpoint writes it as {"data": …, "pagination": …}.
type listResponse struct {
	data       any
	pagination map[string]any
}

// memoryList is the common body of a memory list whose rows are data.
func memoryList(r *http.Request, path string, read func() (any, error)) (any, int, error) {
	q, err := listQuery(r, path)
	if err != nil {
		return nil, 0, err
	}
	rows, err := read()
	if err != nil {
		return nil, 0, err
	}
	page, pagination, err := memoryPage(q, rows)
	if err != nil {
		return nil, 0, err
	}
	return listResponse{data: page, pagination: pagination}, http.StatusOK, nil
}

// listData projects a list operation's data and answers 200.
func listData(q listRequest, data any) (any, int, error) {
	projected, err := q.project(data)
	if err != nil {
		return nil, 0, err
	}
	return projected, http.StatusOK, nil
}
