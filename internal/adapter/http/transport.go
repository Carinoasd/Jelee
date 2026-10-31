package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"

	"github.com/go-chi/chi/v5"
)

// Transport of native JSON reads (G25.2). Compression is the boundary's
// job (compress.go); this file adds the two other parts:
//
//   - Validators: a successful GET answered as JSON carries a strong ETag
//     derived from the exact body sent, that is after every visibility rule,
//     mask and field selection. A matching If-None-Match (weak comparison,
//     so the W/ form a compressed response carries also matches) is 304
//     without a body. The hash covers what this caller would receive, so a
//     tag never matches another user's or another projection's
//     representation, and nothing is cached on the server.
//   - Cache policy: catalog reads (revalidatedRoutes) are
//     "private, no-cache" with Vary: Authorization, Cookie, so a browser may
//     keep a copy but revalidates it on every use and shared caches never
//     store it. Every other account, administrator and security read keeps
//     the boundary's "no-store"; it still answers If-None-Match for clients
//     that track validators themselves.
//
// Downloads (Content-Disposition), playback information and the
// compatibility layer are not touched.

// revalidatedRoutes are the GET route patterns whose JSON may be kept by the
// browser and revalidated: catalog reads of the content visible to the
// caller and the public site appearance. Per-user state (preferences,
// progress, resume, statistics), accounts, sessions, administrator views and
// token-bearing responses are deliberately absent and stay no-store.
var revalidatedRoutes = map[string]bool{
	"/api/v1/items":               true,
	"/api/v1/items/{id}":          true,
	"/api/v1/items/{id}/details":  true,
	"/api/v1/items/{id}/sources":  true,
	"/api/v1/items/{id}/versions": true,
	"/api/v1/libraries":           true,
	"/api/v1/collections":         true,
	"/api/v1/collections/{id}":    true,
	"/api/v1/playlists":           true,
	"/api/v1/playlists/{id}":      true,
	"/api/v1/site/appearance":     true,
}

// jsonETagHexLength is the number of hex digits of the SHA-256 body digest
// kept in the tag (128 bits).
const jsonETagHexLength = 32

// routePattern is the matched chi pattern of the request, or "".
func routePattern(r *http.Request) string {
	if rc := chi.RouteContext(r.Context()); rc != nil {
		return rc.RoutePattern()
	}
	return ""
}

// jsonETag is the strong validator of an encoded JSON body.
func jsonETag(body []byte) string {
	sum := sha256.Sum256(body)
	return `"` + hex.EncodeToString(sum[:])[:jsonETagHexLength] + `"`
}

// writeValidatedJSON answers a successful read with value as JSON, its
// validator and cache policy, or 304 when If-None-Match matches. Requests
// other than GET and HEAD, and attachments, are written as by writeJSON.
func writeValidatedJSON(w http.ResponseWriter, r *http.Request, value any) {
	if (r.Method != http.MethodGet && r.Method != http.MethodHead) || w.Header().Get("Content-Disposition") != "" {
		writeJSON(w, http.StatusOK, value)
		return
	}
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(value); err != nil {
		WriteError(w, r, err)
		return
	}
	h := w.Header()
	etag := jsonETag(body.Bytes())
	h.Set("ETag", etag)
	if revalidatedRoutes[routePattern(r)] {
		h.Set("Cache-Control", "private, no-cache")
		h.Add("Vary", "Authorization, Cookie")
	}
	if imageNotModified(r.Header.Values("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Content-Length", strconv.Itoa(body.Len()))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body.Bytes())
	}
}

// detailContracts are the single resource reads that accept fields (G25.2),
// keyed by OpenAPI path (the chi route pattern). Fields are exactly the
// members of the data schema (TestOpenAPIDetailFieldsMatchSchema); ID is
// the member always kept, "-" when the resource has none at the top level.
var detailContracts = func() map[string]*listContract {
	item := []string{"id", "kind", "libraryId", "parentId", "premiereDate", "productionYear", "title"}
	details := append([]string{"externalIds", "genres", "isFavorite", "nfo", "originalTitle", "overview", "sortTitle", "tagline"}, item...)
	contracts := []*listContract{
		{Path: "/api/v1/items/{id}", Fields: item},
		{Path: "/api/v1/items/{id}/details", Fields: details},
		{Path: "/api/v1/collections/{id}", ID: "-", Fields: []string{"collection", "items", "truncated"}},
		{Path: "/api/v1/playlists/{id}", ID: "-", Fields: []string{"entries", "playlist"}},
		{Path: "/api/v1/users/me", Fields: userFields},
		{Path: "/api/v1/users/{id}", Fields: userFields},
		{Path: "/api/v1/jobs/{id}", Fields: listContracts["/api/v1/jobs"].Fields},
		{Path: "/api/v1/webhooks/{id}", Fields: listContracts["/api/v1/webhooks"].Fields},
		{Path: "/api/v1/shares/{id}", Fields: listContracts["/api/v1/shares"].Fields},
	}
	out := make(map[string]*listContract, len(contracts))
	for _, c := range contracts {
		out[c.Path] = c
	}
	return out
}()

// detailQuery validates the query of a single resource read: no parameters,
// or only fields when the GET operation has a detail contract.
func detailQuery(r *http.Request) (listRequest, error) {
	c := detailContracts[routePattern(r)]
	if c == nil || r.Method != http.MethodGet {
		_, err := strictQuery(r)
		return listRequest{}, err
	}
	values, err := strictQuery(r, "fields")
	if err != nil {
		return listRequest{}, err
	}
	q := listRequest{contract: c}
	if raw, ok := values["fields"]; ok {
		if q.Fields, err = c.parseFields(raw); err != nil {
			return listRequest{}, err
		}
	}
	return q, nil
}

// projectOne keeps the selected top-level members of one resource. Like list
// projection it works on the encoded resource, after every mask, and only
// removes members.
func (q listRequest) projectOne(data any) (any, error) {
	if q.Fields == nil {
		return data, nil
	}
	var row listRow
	if err := remarshal(data, &row); err != nil {
		return nil, err
	}
	return q.projectRows([]listRow{row})[0], nil
}

// detailSpecification declares fields on every detail operation present in
// paths and records the selectable members in x-jelee-fields.
func detailSpecification(paths map[string]any) {
	for path, c := range detailContracts {
		item, ok := paths[path].(map[string]any)
		if !ok {
			continue
		}
		op, ok := item["get"].(map[string]any)
		if !ok {
			continue
		}
		description := "Comma separated top-level members of data to return; the others are omitted. Unknown or empty names are 400 invalid_request."
		if id := c.id(); id != "" {
			description += " " + id + " is always returned."
		}
		params, _ := op["parameters"].([]any)
		op["parameters"] = append(slices.Clone(params), map[string]any{"name": "fields", "in": "query", "description": description,
			"schema": map[string]any{"type": "string", "maxLength": 1024}})
		op["x-jelee-fields"] = c.Fields
	}
}
