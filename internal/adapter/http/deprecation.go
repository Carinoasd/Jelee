package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// G49.2 API versioning and deprecation.
//
// Every native route lives under /api/v1. A breaking change ships as a new
// route under /api/v2 while the v1 route keeps working for at least
// deprecationMinimumTransition; the v1 route is listed in apiDeprecations
// for that period. A listed route answers with
//
//	Deprecation: @<unix seconds>                       (RFC 9745)
//	Sunset: <HTTP-date>                                (RFC 8594)
//	Link: <successor>; rel="successor-version"
//	Link: </api-docs#deprecations>; rel="deprecation"  (RFC 9745)
//
// and its OpenAPI operation is marked deprecated with the same dates. The
// table must match docs/api-deprecations.md, which
// TestDeprecatedRoutesAreDocumented enforces.

// Deprecation marks one registered route as deprecated.
type Deprecation struct {
	// Route is "METHOD /pattern" exactly as the router registers it, for
	// example "GET /api/v1/items/{id}".
	Route string
	// Since is the deprecation date, sent in the Deprecation header.
	Since time.Time
	// Sunset is the earliest removal date, sent in the Sunset header. It is
	// at least deprecationMinimumTransition after Since.
	Sunset time.Time
	// Successor is the replacement path, sent as rel="successor-version";
	// empty when the route is removed without a replacement.
	Successor string
	// Notice explains the change and the migration in English. It is shown
	// in the OpenAPI description and on /api-docs.
	Notice string
}

// deprecationMinimumTransition is the shortest time between deprecating a
// route and removing it (the v2 transition period of docs/api-deprecations.md).
const deprecationMinimumTransition = 180 * 24 * time.Hour

// deprecationDocsLink is the rel="deprecation" target: the deprecation
// section of the browsable API reference served by this instance.
const deprecationDocsLink = "/api-docs#deprecations"

// apiDeprecations lists the deprecated routes of this build. It is empty:
// no route is deprecated yet. Keep it sorted by Route and in sync with
// docs/api-deprecations.md.
var apiDeprecations = []Deprecation{}

// withDeprecations replaces the deprecation table of one server. Tests use
// it to exercise the mechanism on a real route; production servers always
// use apiDeprecations.
func withDeprecations(list []Deprecation) Option {
	return func(s *Server) { s.deprecations = list }
}

// validateDeprecations rejects a table that could not be served correctly.
func validateDeprecations(list []Deprecation) error {
	seen := map[string]bool{}
	for _, d := range list {
		method, pattern, ok := strings.Cut(d.Route, " ")
		if !ok || method == "" || method != strings.ToUpper(method) || !strings.HasPrefix(pattern, "/") {
			return fmt.Errorf("deprecation %q: route must be \"METHOD /pattern\"", d.Route)
		}
		if seen[d.Route] {
			return fmt.Errorf("deprecation %q: listed twice", d.Route)
		}
		seen[d.Route] = true
		if d.Since.IsZero() || d.Sunset.IsZero() {
			return fmt.Errorf("deprecation %q: since and sunset dates are required", d.Route)
		}
		if d.Sunset.Sub(d.Since) < deprecationMinimumTransition {
			return fmt.Errorf("deprecation %q: sunset must be at least %d days after the deprecation", d.Route, int(deprecationMinimumTransition/(24*time.Hour)))
		}
		if d.Successor != "" && (!strings.HasPrefix(d.Successor, "/") || strings.ContainsAny(d.Successor, "<>\" \r\n")) {
			return fmt.Errorf("deprecation %q: successor must be an absolute path", d.Route)
		}
		if strings.TrimSpace(d.Notice) == "" {
			return fmt.Errorf("deprecation %q: a notice is required", d.Route)
		}
	}
	return nil
}

// deprecationIndex keys a validated table by route.
func deprecationIndex(list []Deprecation) map[string]Deprecation {
	if len(list) == 0 {
		return nil
	}
	index := make(map[string]Deprecation, len(list))
	for _, d := range list {
		index[d.Route] = d
	}
	return index
}

// deprecationHeaders writes the deprecation headers when the request reaches
// a deprecated route. It costs nothing while the table is empty.
func (s *Server) deprecationHeaders(w http.ResponseWriter, r *http.Request) {
	if len(s.deprecationIndex) == 0 || s.router == nil {
		return
	}
	path := r.URL.RawPath
	if path == "" {
		path = r.URL.Path
	}
	// Find matches exactly like routing does, so only the method and
	// pattern a handler is registered for carry the headers.
	pattern := s.router.Find(chi.NewRouteContext(), r.Method, path)
	d, ok := s.deprecationIndex[r.Method+" "+pattern]
	if pattern == "" || !ok {
		return
	}
	header := w.Header()
	header.Set("Deprecation", "@"+strconv.FormatInt(d.Since.Unix(), 10))
	header.Set("Sunset", d.Sunset.UTC().Format(http.TimeFormat))
	if d.Successor != "" {
		header.Add("Link", "<"+d.Successor+`>; rel="successor-version"`)
	}
	header.Add("Link", "<"+deprecationDocsLink+`>; rel="deprecation"`)
}

// deprecationSpecification marks the deprecated operations of paths. A
// route that is not part of this rollout is skipped.
func deprecationSpecification(paths map[string]any, list []Deprecation) {
	for _, d := range list {
		method, pattern, _ := strings.Cut(d.Route, " ")
		item, ok := paths[pattern].(map[string]any)
		if !ok {
			continue
		}
		op, ok := item[strings.ToLower(method)].(map[string]any)
		if !ok {
			continue
		}
		op["deprecated"] = true
		notice := "Deprecated since " + d.Since.UTC().Format(time.DateOnly) + "; removal no earlier than " + d.Sunset.UTC().Format(time.DateOnly) + "."
		if d.Successor != "" {
			notice += " Use " + d.Successor + " instead."
		}
		notice += " " + d.Notice + " See docs/api-deprecations.md."
		if description, _ := op["description"].(string); description != "" {
			notice += "\n\n" + description
		}
		op["description"] = notice
		extension := map[string]any{"since": d.Since.UTC().Format(time.RFC3339), "sunset": d.Sunset.UTC().Format(time.RFC3339)}
		if d.Successor != "" {
			extension["successor"] = d.Successor
		}
		op["x-jelee-deprecation"] = extension
		for _, raw := range op["responses"].(map[string]any) {
			response := raw.(map[string]any)
			headers, _ := response["headers"].(map[string]any)
			if headers == nil {
				headers = map[string]any{}
			}
			for name, header := range deprecationResponseHeaders() {
				headers[name] = header
			}
			response["headers"] = headers
		}
	}
}

func deprecationResponseHeaders() map[string]any {
	text := map[string]any{"type": "string"}
	return map[string]any{
		"Deprecation": map[string]any{"description": "RFC 9745 deprecation date as a structured field date (@ followed by Unix seconds).", "schema": text},
		"Sunset":      map[string]any{"description": "RFC 8594 HTTP-date after which the operation may be removed.", "schema": text},
		"Link":        map[string]any{"description": "rel=\"successor-version\" names the replacement when there is one; rel=\"deprecation\" links to the deprecation notes on /api-docs.", "schema": text},
	}
}

// sortedDeprecations returns a copy of list ordered by route.
func sortedDeprecations(list []Deprecation) []Deprecation {
	sorted := append([]Deprecation(nil), list...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Route < sorted[j].Route })
	return sorted
}

var errDeprecationTable = errors.New("invalid API deprecation table")
