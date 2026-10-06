package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"html/template"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/config"
)

// G49.3 browsable API reference. /api-docs renders the same OpenAPI document
// that /api/v1/openapi.json serves, server side with html/template: no
// script, no external stylesheet and no new dependency. It is public like
// the document itself and is reachable before initial setup.

// apiDocsStyle is the only stylesheet of the page; the CSP allows exactly
// this text by its hash.
const apiDocsStyle = `:root{color-scheme:light dark;--fg:#1d1d1f;--bg:#fff;--muted:#5f6368;--line:#d9dce1;--code:#f4f5f7;--get:#1a7f37;--post:#0969da;--put:#9a6700;--patch:#8250df;--delete:#cf222e;--head:#57606a}
@media (prefers-color-scheme:dark){:root{--fg:#e6e6e6;--bg:#16181c;--muted:#a0a4ab;--line:#30343b;--code:#22252b}}
body{margin:0;background:var(--bg);color:var(--fg);font:15px/1.5 system-ui,-apple-system,"Segoe UI",sans-serif}
main{max-width:1100px;margin:0 auto;padding:16px}
a{color:inherit}
h1{margin:.5em 0 .2em}h2{border-bottom:1px solid var(--line);padding-bottom:.2em;margin-top:2em}
nav ul{columns:3 14em;padding-left:1.2em}
.op{border:1px solid var(--line);border-radius:6px;margin:1em 0;padding:.6em .9em}
.op h3{margin:.2em 0;font-size:1em;font-family:ui-monospace,monospace;overflow-wrap:anywhere}
.m{display:inline-block;min-width:4.2em;font-weight:700}
.m-get{color:var(--get)}.m-post{color:var(--post)}.m-put{color:var(--put)}.m-patch{color:var(--patch)}.m-delete{color:var(--delete)}.m-head{color:var(--head)}
.dep{color:var(--delete);font-weight:700}
.meta{color:var(--muted);font-size:.9em}
table{border-collapse:collapse;width:100%;font-size:.92em}
th,td{border:1px solid var(--line);padding:.25em .45em;text-align:left;vertical-align:top}
td code,li code{overflow-wrap:anywhere}
pre{background:var(--code);padding:.6em;overflow:auto;font-size:.85em;max-height:28em}
.desc{white-space:pre-line}`

var apiDocsStyleHash = func() string {
	sum := sha256.Sum256([]byte(apiDocsStyle))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}()

var apiDocsTemplate = template.Must(template.New("api-docs").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Jelee API</title>
<style>` + apiDocsStyle + `</style>
</head>
<body>
<main>
<h1>Jelee API</h1>
<p>Version {{.Version}}. {{.Description}}</p>
<p>Machine-readable document: <a href="/api/v1/openapi.json">OpenAPI 3.1 specification</a>. Every error uses the envelope <code>{"error":{"code","message","details","traceId"}}</code>; see <a href="#errors">error codes</a>.</p>
<nav aria-label="Contents"><h2>Contents</h2><ul>
{{range .Groups}}<li><a href="#{{.Anchor}}">{{.Name}}</a> ({{len .Operations}})</li>
{{end}}<li><a href="#errors">Error codes</a></li>
<li><a href="#deprecations">Deprecations</a></li>
<li><a href="#schemas">Schemas</a></li>
</ul></nav>
{{range .Groups}}<section id="{{.Anchor}}"><h2>{{.Name}}</h2>
{{range .Operations}}<article class="op" id="{{.Anchor}}">
<h3><span class="m m-{{.MethodClass}}">{{.Method}}</span> {{.Path}}{{if .Deprecated}} <span class="dep">deprecated</span>{{end}}</h3>
<p>{{.Summary}}</p>
<p class="meta">{{.Access}}</p>
{{if .Description}}<p class="desc">{{.Description}}</p>{{end}}
{{if .Parameters}}<table><thead><tr><th>Parameter</th><th>In</th><th>Schema</th><th>Description</th></tr></thead><tbody>
{{range .Parameters}}<tr><td><code>{{.Name}}</code>{{if .Required}} (required){{end}}</td><td>{{.In}}</td><td>{{.Schema}}</td><td>{{.Description}}</td></tr>
{{end}}</tbody></table>{{end}}
{{with .Request}}<h4>Request body</h4><p class="meta">{{.MediaType}}{{if .Schema}} · <a href="#schema-{{.Schema}}">{{.Schema}}</a>{{end}}{{if .Description}} · {{.Description}}{{end}}</p>
{{if .Example}}<details><summary>Example request</summary><pre>{{.Example}}</pre></details>{{end}}{{end}}
<h4>Responses</h4><table><thead><tr><th>Status</th><th>Description</th></tr></thead><tbody>
{{range .Responses}}<tr><td>{{.Status}}</td><td>{{.Description}}{{if .Schema}} · <a href="#schema-{{.Schema}}">{{.Schema}}</a>{{end}}{{if .MediaType}} <span class="meta">({{.MediaType}})</span>{{end}}{{if .CodeStatus}} · <a href="#status-{{.CodeStatus}}">error codes</a>{{end}}{{if .Example}}<details><summary>Example response</summary><pre>{{.Example}}</pre></details>{{end}}</td></tr>
{{end}}</tbody></table>
</article>
{{end}}</section>
{{end}}<section id="errors"><h2>Error codes</h2>
<p>Each code is sent only with the listed HTTP status. Messages are localized from Accept-Language; the examples show en-US.</p>
<table><thead><tr><th>Status</th><th>Codes</th></tr></thead><tbody>
{{range .Statuses}}<tr id="status-{{.Status}}"><td>{{.Status}}</td><td>{{range $i, $c := .Codes}}{{if $i}}, {{end}}<a href="#error-{{$c}}"><code>{{$c}}</code></a>{{end}}</td></tr>
{{end}}</tbody></table>
<table><thead><tr><th>Code</th><th>Status</th><th>Example</th></tr></thead><tbody>
{{range .Errors}}<tr id="error-{{.Code}}"><td><code>{{.Code}}</code></td><td>{{.Statuses}}</td><td><pre>{{.Example}}</pre></td></tr>
{{end}}</tbody></table></section>
<section id="deprecations"><h2>Deprecations</h2>
<p>Deprecated operations answer with the Deprecation (RFC 9745) and Sunset (RFC 8594) headers and a Link to their successor. Breaking changes ship under /api/v2 while the /api/v1 route stays available for at least {{.TransitionDays}} days.</p>
{{if .Deprecations}}<table><thead><tr><th>Operation</th><th>Deprecated</th><th>Sunset</th><th>Successor</th><th>Notice</th></tr></thead><tbody>
{{range .Deprecations}}<tr><td><code>{{.Route}}</code></td><td>{{.Since}}</td><td>{{.Sunset}}</td><td>{{if .Successor}}<code>{{.Successor}}</code>{{else}}none{{end}}</td><td>{{.Notice}}</td></tr>
{{end}}</tbody></table>{{else}}<p>No operation is deprecated.</p>{{end}}</section>
<section id="schemas"><h2>Schemas</h2>
{{range .Schemas}}<details id="schema-{{.Name}}"><summary><code>{{.Name}}</code></summary><pre>{{.JSON}}</pre></details>
{{end}}</section>
{{if .TMDB}}<section aria-label="Credits"><h2>Credits</h2><a href="https://www.themoviedb.org"><img width="64" alt="TMDB" src="https://www.themoviedb.org/assets/v4/logos/v2/blue_short-8e7b30f73a4020692ccca9c88bafe5dcb6f8a62a4c6bc55cd9ba82bb2cd95f6c.svg"></a><p>This product uses the TMDB API but is not endorsed or certified by TMDB.</p></section>{{end}}
</main>
</body>
</html>
`))

type apiDocsView struct {
	Version        string
	Description    string
	Groups         []apiDocsGroup
	Errors         []apiDocsError
	Statuses       []apiDocsStatus
	Deprecations   []apiDocsDeprecation
	TransitionDays int
	Schemas        []apiDocsSchema
	TMDB           bool
}

type apiDocsGroup struct {
	Name, Anchor string
	Operations   []apiDocsOperation
}

type apiDocsOperation struct {
	Anchor, Method, MethodClass, Path string
	Summary, Description, Access      string
	Deprecated                        bool
	Parameters                        []apiDocsParameter
	Request                           *apiDocsBody
	Responses                         []apiDocsResponse
}

type apiDocsParameter struct {
	Name, In, Schema, Description string
	Required                      bool
}

type apiDocsBody struct {
	MediaType, Schema, Description, Example string
}

type apiDocsResponse struct {
	Status, Description, MediaType, Schema, Example string
	// CodeStatus links an error response to the codes of its status;
	// "default" links to internal_error's status.
	CodeStatus string
}

type apiDocsStatus struct {
	Status string
	Codes  []string
}

type apiDocsError struct{ Code, Statuses, Example string }

type apiDocsDeprecation struct{ Route, Since, Sunset, Successor, Notice string }

type apiDocsSchema struct{ Name, JSON string }

// apiDocsHandler renders the page once per server: it depends only on the
// configuration and the deprecation table, never on the caller.
func apiDocsHandler(cfg config.Config, deprecations []Deprecation) http.HandlerFunc {
	render := sync.OnceValues(func() ([]byte, error) { return renderAPIDocs(cfg, deprecations) })
	policy := "default-src 'none'; style-src " + apiDocsStyleHash + "; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"
	if cfg.TMDBAPIKey != "" {
		policy = "default-src 'none'; style-src " + apiDocsStyleHash + "; img-src https://www.themoviedb.org; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"
	}
	return func(w http.ResponseWriter, r *http.Request) {
		page, err := render()
		if err != nil {
			writeProblem(w, r, 500, "internal_error", "Request could not be completed.")
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", policy)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(page)
	}
}

// apiDocsSpecification documents the page itself.
func apiDocsSpecification(paths map[string]any) {
	op := paths["/api-docs"].(map[string]any)["get"].(map[string]any)
	op["summary"] = "Browse the API reference"
	op["description"] = "Public HTML rendering of this document: every operation with its parameters, request and response schemas, generated examples, the error code table with one example envelope per code, and the deprecation list (G49.3, G49.2). Available before initial setup like the document itself."
	op["responses"].(map[string]any)["200"].(map[string]any)["content"] = map[string]any{"text/html": map[string]any{"schema": map[string]any{"type": "string"}}}
}

func renderAPIDocs(cfg config.Config, deprecations []Deprecation) ([]byte, error) {
	spec := specification(cfg, deprecations)
	components := spec["components"].(map[string]any)
	schemas := components["schemas"].(map[string]any)
	info := spec["info"].(map[string]any)
	view := apiDocsView{Version: info["version"].(string), Description: info["description"].(string), TMDB: cfg.TMDBAPIKey != "",
		TransitionDays: int(deprecationMinimumTransition / (24 * time.Hour))}
	view.Groups = apiDocsGroups(spec["paths"].(map[string]any), schemas)
	statuses := map[string][]int{}
	for code, list := range errorCodeStatuses {
		statuses[code] = list
	}
	codes := make([]string, 0, len(statuses))
	for code := range statuses {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	for _, code := range codes {
		values := make([]string, 0, len(statuses[code]))
		for _, status := range statuses[code] {
			values = append(values, strconv.Itoa(status))
		}
		view.Errors = append(view.Errors, apiDocsError{Code: code, Statuses: strings.Join(values, ", "), Example: indentJSON(errorExampleEnvelope(code))})
	}
	byStatus := map[int]bool{}
	for _, list := range errorCodeStatuses {
		for _, status := range list {
			byStatus[status] = true
		}
	}
	numbers := make([]int, 0, len(byStatus))
	for status := range byStatus {
		numbers = append(numbers, status)
	}
	sort.Ints(numbers)
	for _, status := range numbers {
		view.Statuses = append(view.Statuses, apiDocsStatus{Status: strconv.Itoa(status), Codes: errorCodesFor(status)})
	}
	for _, d := range sortedDeprecations(deprecations) {
		view.Deprecations = append(view.Deprecations, apiDocsDeprecation{Route: d.Route, Since: d.Since.UTC().Format(time.DateOnly), Sunset: d.Sunset.UTC().Format(time.DateOnly), Successor: d.Successor, Notice: d.Notice})
	}
	names := make([]string, 0, len(schemas))
	for name := range schemas {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		view.Schemas = append(view.Schemas, apiDocsSchema{Name: name, JSON: indentJSON(schemas[name])})
	}
	var out bytes.Buffer
	if err := apiDocsTemplate.Execute(&out, view); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

var apiDocsMethodOrder = map[string]int{"get": 0, "head": 1, "post": 2, "put": 3, "patch": 4, "delete": 5}

func apiDocsGroups(paths map[string]any, schemas map[string]any) []apiDocsGroup {
	byGroup := map[string][]apiDocsOperation{}
	routes := make([]string, 0, len(paths))
	for path := range paths {
		routes = append(routes, path)
	}
	sort.Strings(routes)
	for _, path := range routes {
		item := paths[path].(map[string]any)
		methods := make([]string, 0, len(item))
		for method := range item {
			methods = append(methods, method)
		}
		sort.Slice(methods, func(i, j int) bool { return apiDocsMethodOrder[methods[i]] < apiDocsMethodOrder[methods[j]] })
		for _, method := range methods {
			op := item[method].(map[string]any)
			group := apiDocsGroupName(path)
			byGroup[group] = append(byGroup[group], apiDocsOperationView(method, path, op, schemas))
		}
	}
	names := make([]string, 0, len(byGroup))
	for name := range byGroup {
		names = append(names, name)
	}
	sort.Strings(names)
	groups := make([]apiDocsGroup, 0, len(names))
	for _, name := range names {
		groups = append(groups, apiDocsGroup{Name: name, Anchor: "group-" + apiDocsAnchor(name), Operations: byGroup[name]})
	}
	return groups
}

// apiDocsGroupName groups /api/v1/<name>/... by <name>; other roots by
// their first segment.
func apiDocsGroupName(path string) string {
	rest := strings.TrimPrefix(path, "/api/v1/")
	if rest == path {
		rest = strings.TrimPrefix(path, "/")
		if rest == "healthz" || rest == "readyz" || rest == "api-docs" || rest == "metrics" {
			return "service"
		}
	}
	name, _, _ := strings.Cut(rest, "/")
	if name == "openapi.json" || name == "system" {
		return "service"
	}
	return name
}

func apiDocsAnchor(value string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(value) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

func apiDocsOperationView(method, path string, op map[string]any, schemas map[string]any) apiDocsOperation {
	view := apiDocsOperation{Anchor: "op-" + method + "-" + apiDocsAnchor(path), Method: strings.ToUpper(method), MethodClass: method, Path: path}
	view.Summary, _ = op["summary"].(string)
	view.Description, _ = op["description"].(string)
	view.Deprecated, _ = op["deprecated"].(bool)
	view.Access = "Public"
	if _, secured := op["security"]; secured {
		view.Access = "Requires a bearer token or the session cookie"
	}
	if role, _ := op["x-jelee-role"].(string); role != "" {
		view.Access += "; role: " + role
	}
	if session, _ := op["x-jelee-session"].(string); session != "" {
		view.Access += "; session: " + session
	}
	parameters, _ := op["parameters"].([]any)
	for _, raw := range parameters {
		p, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		parameter := apiDocsParameter{}
		parameter.Name, _ = p["name"].(string)
		parameter.In, _ = p["in"].(string)
		parameter.Description, _ = p["description"].(string)
		parameter.Required, _ = p["required"].(bool)
		schema, _ := p["schema"].(map[string]any)
		parameter.Schema = schemaSummary(schema)
		view.Parameters = append(view.Parameters, parameter)
	}
	if body, ok := op["requestBody"].(map[string]any); ok {
		request := apiDocsBodyView(body["content"], schemas)
		request.Description, _ = body["description"].(string)
		view.Request = &request
	}
	responses := op["responses"].(map[string]any)
	statuses := make([]string, 0, len(responses))
	for status := range responses {
		statuses = append(statuses, status)
	}
	// Numeric statuses sort as text; "default" sorts after them.
	sort.Strings(statuses)
	for _, status := range statuses {
		raw := responses[status].(map[string]any)
		response := apiDocsResponse{Status: status}
		response.Description, _ = raw["description"].(string)
		body := apiDocsBodyView(raw["content"], schemas)
		response.MediaType, response.Schema = body.MediaType, body.Schema
		if n, err := strconv.Atoi(status); err == nil && n >= 400 {
			if len(errorCodesFor(n)) > 0 {
				response.CodeStatus = status
			}
		} else if status == "default" {
			response.CodeStatus = "500"
		} else {
			response.Example = body.Example
		}
		view.Responses = append(view.Responses, response)
	}
	return view
}

func errorCodesFor(status int) []string {
	var codes []string
	for code, statuses := range errorCodeStatuses {
		for _, s := range statuses {
			if s == status {
				codes = append(codes, code)
			}
		}
	}
	sort.Strings(codes)
	return codes
}

// apiDocsBodyView describes the first media type of a content object and a
// generated example of its schema.
func apiDocsBodyView(raw any, schemas map[string]any) apiDocsBody {
	content, _ := raw.(map[string]any)
	if len(content) == 0 {
		return apiDocsBody{}
	}
	types := make([]string, 0, len(content))
	for mediaType := range content {
		types = append(types, mediaType)
	}
	sort.Strings(types)
	media, _ := content[types[0]].(map[string]any)
	body := apiDocsBody{MediaType: strings.Join(types, ", ")}
	schema, _ := media["schema"].(map[string]any)
	if ref, _ := schema["$ref"].(string); ref != "" {
		body.Schema = strings.TrimPrefix(ref, "#/components/schemas/")
	}
	if types[0] == "application/json" && schema != nil {
		body.Example = indentJSON(sampleValue(schema, schemas, map[string]bool{}, 0))
	}
	return body
}

// schemaSummary is a one-line description of a parameter schema.
func schemaSummary(schema map[string]any) string {
	if schema == nil {
		return ""
	}
	kind, _ := schema["type"].(string)
	if kind == "array" {
		items, _ := schema["items"].(map[string]any)
		return "array of " + schemaSummary(items)
	}
	parts := []string{kind}
	if format, _ := schema["format"].(string); format != "" {
		parts[0] += " (" + format + ")"
	}
	if values, ok := schema["enum"].([]string); ok {
		parts = append(parts, "one of "+strings.Join(values, ", "))
	} else if values, ok := schema["enum"].([]any); ok {
		texts := make([]string, 0, len(values))
		for _, value := range values {
			texts = append(texts, jsonText(value))
		}
		parts = append(parts, "one of "+strings.Join(texts, ", "))
	}
	if minimum, ok := schema["minimum"]; ok {
		parts = append(parts, "min "+jsonText(minimum))
	}
	if maximum, ok := schema["maximum"]; ok {
		parts = append(parts, "max "+jsonText(maximum))
	}
	if value, ok := schema["default"]; ok {
		parts = append(parts, "default "+jsonText(value))
	}
	return strings.Join(parts, "; ")
}

// sampleExampleUUID and friends are the fixed placeholder values of
// generated examples.
const (
	sampleUUID     = "3fa85f64-5717-4562-b3fc-2c963f66afa6"
	sampleDateTime = "2026-01-02T03:04:05Z"
	sampleDate     = "2026-01-02"
	sampleMaxDepth = 8
)

// sampleValue builds an example instance of schema. Recursive references
// and deep nesting end in an empty object so every schema terminates.
func sampleValue(schema map[string]any, schemas map[string]any, active map[string]bool, depth int) any {
	if schema == nil || depth > sampleMaxDepth {
		return nil
	}
	if ref, _ := schema["$ref"].(string); ref != "" {
		name := strings.TrimPrefix(ref, "#/components/schemas/")
		target, _ := schemas[name].(map[string]any)
		if active[name] || target == nil {
			return map[string]any{}
		}
		active[name] = true
		defer delete(active, name)
		return sampleValue(target, schemas, active, depth+1)
	}
	for _, key := range []string{"const", "example", "default"} {
		if value, ok := schema[key]; ok {
			return value
		}
	}
	if values, ok := schema["examples"].([]any); ok && len(values) > 0 {
		return values[0]
	}
	if values, ok := schema["enum"].([]string); ok && len(values) > 0 {
		return values[0]
	}
	if values, ok := schema["enum"].([]any); ok && len(values) > 0 {
		return values[0]
	}
	for _, key := range []string{"oneOf", "anyOf", "allOf"} {
		if options := schemaList(schema[key]); len(options) > 0 {
			if key != "allOf" {
				return sampleValue(options[0], schemas, active, depth+1)
			}
			merged := map[string]any{}
			for _, option := range options {
				if object, ok := sampleValue(option, schemas, active, depth+1).(map[string]any); ok {
					for name, value := range object {
						merged[name] = value
					}
				}
			}
			return merged
		}
	}
	kind := schema["type"]
	if kinds, ok := kind.([]string); ok && len(kinds) > 0 {
		kind = kinds[0]
	} else if kinds, ok := kind.([]any); ok && len(kinds) > 0 {
		kind = kinds[0]
	}
	switch kind {
	case "object":
		object := map[string]any{}
		properties, _ := schema["properties"].(map[string]any)
		for name, raw := range properties {
			if property, ok := raw.(map[string]any); ok {
				object[name] = sampleValue(property, schemas, active, depth+1)
			}
		}
		return object
	case "array":
		items, _ := schema["items"].(map[string]any)
		if items == nil {
			return []any{}
		}
		return []any{sampleValue(items, schemas, active, depth+1)}
	case "integer", "number":
		if minimum, ok := schema["minimum"]; ok {
			return minimum
		}
		return 0
	case "boolean":
		return false
	case "null":
		return nil
	case "string":
		switch schema["format"] {
		case "uuid":
			return sampleUUID
		case "date-time":
			return sampleDateTime
		case "date":
			return sampleDate
		case "uri":
			return "https://example.invalid/"
		}
		return "string"
	}
	if _, ok := schema["properties"]; ok {
		return sampleValue(map[string]any{"type": "object", "properties": schema["properties"]}, schemas, active, depth)
	}
	return nil
}

func schemaList(raw any) []map[string]any {
	var out []map[string]any
	switch list := raw.(type) {
	case []any:
		for _, item := range list {
			if schema, ok := item.(map[string]any); ok {
				out = append(out, schema)
			}
		}
	case []map[string]any:
		out = list
	}
	return out
}

func indentJSON(value any) string {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return ""
	}
	return strings.TrimSuffix(out.String(), "\n")
}

func jsonText(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(data)
}
