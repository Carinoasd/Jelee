package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// G49.3: /api-docs renders every operation, every error code and the
// deprecation list from the same document /api/v1/openapi.json serves.
func TestAPIDocsRendersTheServedDocument(t *testing.T) {
	cfg := ReferenceConfig()
	handler := contractRouter(t, cfg)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "http://localhost/api-docs", nil))
	if w.Code != 200 || w.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("/api-docs: %d %s", w.Code, w.Header().Get("Content-Type"))
	}
	page := w.Body.String()
	policy := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(policy, "style-src "+apiDocsStyleHash+";") || strings.Contains(policy, "script-src") || strings.Contains(policy, "unsafe-inline") || !strings.Contains(policy, "default-src 'none'") {
		t.Fatalf("CSP %q", policy)
	}
	// No script and nothing fetched from elsewhere except the TMDB logo the
	// attribution requires.
	if strings.Contains(strings.ToLower(page), "<script") || strings.Contains(page, `rel="stylesheet"`) {
		t.Fatal("page loads script or an external stylesheet")
	}
	for _, src := range regexp.MustCompile(`src="([^"]*)"`).FindAllStringSubmatch(page, -1) {
		if !strings.HasPrefix(src[1], "https://www.themoviedb.org/") {
			t.Errorf("external resource %s", src[1])
		}
	}
	if !strings.Contains(page, "<style>"+apiDocsStyle+"</style>") {
		t.Fatal("stylesheet differs from the hashed one")
	}

	spec := Specification(cfg)
	operations := 0
	for path, raw := range spec["paths"].(map[string]any) {
		for method := range raw.(map[string]any) {
			operations++
			anchor := `id="op-` + method + "-" + apiDocsAnchor(path) + `"`
			if !strings.Contains(page, anchor) {
				t.Errorf("%s %s missing (%s)", method, path, anchor)
			}
		}
	}
	if got := strings.Count(page, `<article class="op"`); got != operations {
		t.Errorf("page has %d operations, document %d", got, operations)
	}
	for code := range errorCodeStatuses {
		if !strings.Contains(page, `<tr id="error-`+code+`">`) {
			t.Errorf("error code %s missing", code)
		}
	}
	for name := range spec["components"].(map[string]any)["schemas"].(map[string]any) {
		if !strings.Contains(page, `id="schema-`+name+`"`) {
			t.Errorf("schema %s missing", name)
		}
	}
	// Every in-page link resolves.
	ids := map[string]bool{}
	for _, match := range regexp.MustCompile(` id="([^"]+)"`).FindAllStringSubmatch(page, -1) {
		if ids[match[1]] {
			t.Errorf("duplicate id %s", match[1])
		}
		ids[match[1]] = true
	}
	for _, match := range regexp.MustCompile(`href="#([^"]+)"`).FindAllStringSubmatch(page, -1) {
		if !ids[match[1]] {
			t.Errorf("dangling link #%s", match[1])
		}
	}
	for _, want := range []string{"No operation is deprecated.", `<h2>Error codes</h2>`, "Example request", "Example response", "This product uses the TMDB API but is not endorsed or certified by TMDB."} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	// Rendering is cached: the second answer is identical.
	again := httptest.NewRecorder()
	handler.ServeHTTP(again, httptest.NewRequest("GET", "http://localhost/api-docs", nil))
	if again.Body.String() != page {
		t.Fatal("second rendering differs")
	}

	// Without TMDB there are no credits and no image source at all.
	plain := ReferenceConfig()
	plain.TMDBAPIKey = ""
	w = httptest.NewRecorder()
	contractRouter(t, plain).ServeHTTP(w, httptest.NewRequest("GET", "http://localhost/api-docs", nil))
	if w.Code != 200 || strings.Contains(w.Body.String(), "<img") || strings.Contains(w.Header().Get("Content-Security-Policy"), "img-src") {
		t.Fatal("credits without TMDB")
	}
}

// Before setup the page is public exactly like the document it renders.
func TestAPIDocsBeforeSetupMatchesOpenAPIAccess(t *testing.T) {
	handler := setupRouter(t, incompleteSetupWizard(), setupTestToken)
	for _, path := range []string{"/api-docs", "/api/v1/openapi.json"} {
		w := setupRequest(handler, "GET", path, "", "")
		if w.Code != 200 {
			t.Errorf("%s before setup: %d %s", path, w.Code, w.Body.String())
		}
	}
	// The wizard page itself is not documented beyond the public document.
	w := setupRequest(handler, "GET", "/api/v1/items", "", "")
	if w.Code != 503 {
		t.Fatalf("catalog before setup: %d", w.Code)
	}
}

func TestOpenAPIErrorExamplesMatchTheEnvelope(t *testing.T) {
	spec := Specification(ReferenceConfig())
	examples := spec["components"].(map[string]any)["examples"].(map[string]any)
	if len(examples) != len(errorCodeStatuses) {
		t.Fatalf("%d examples for %d codes", len(examples), len(errorCodeStatuses))
	}
	for code, statuses := range errorCodeStatuses {
		example, ok := examples[errorExampleName(code)].(map[string]any)
		if !ok {
			t.Errorf("no example for %s", code)
			continue
		}
		// The example is what writeProblem writes for the code in en-US.
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "http://localhost/", nil)
		r.Header.Set("Accept-Language", "en-US")
		writeProblem(w, r, statuses[0], code, "fallback")
		var written, documented map[string]map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &written); err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(example["value"])
		if err := json.Unmarshal(data, &documented); err != nil {
			t.Fatal(err)
		}
		written["error"]["traceId"], written["error"]["details"] = exampleTraceID, documented["error"]["details"]
		if !reflect.DeepEqual(written, documented) {
			t.Errorf("%s example %v differs from the written envelope %v", code, documented, written)
		}
		if !strings.Contains(example["summary"].(string), strconv.Itoa(statuses[0])) {
			t.Errorf("%s summary %q lacks its status", code, example["summary"])
		}
	}
	// Every error response names an example whose code is sent with that
	// status (internal_error for default).
	for path, item := range spec["paths"].(map[string]any) {
		for method, raw := range item.(map[string]any) {
			for status, response := range raw.(map[string]any)["responses"].(map[string]any) {
				if status != "default" && status[0] != '4' && status[0] != '5' {
					continue
				}
				media := response.(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)
				refs, _ := media["examples"].(map[string]any)
				if len(refs) != 1 {
					t.Errorf("%s %s %s: %d error examples", method, path, status, len(refs))
					continue
				}
				for code, ref := range refs {
					if ref.(map[string]any)["$ref"] != "#/components/examples/"+errorExampleName(code) {
						t.Errorf("%s %s %s: bad reference %v", method, path, status, ref)
					}
					n, _ := strconv.Atoi(status)
					if (status == "default" && code != "internal_error") || (status != "default" && !containsStatus(errorCodeStatuses[code], n)) {
						t.Errorf("%s %s %s: example %s is not sent with this status", method, path, status, code)
					}
				}
			}
		}
	}
	if errorExampleCode("404") != "not_found" || errorExampleCode("429") != "auth_rate_limited" || errorExampleCode("418") != "" || errorExampleCode("x") != "" {
		t.Fatal("example code selection changed")
	}
}

func containsStatus(statuses []int, status int) bool {
	for _, s := range statuses {
		if s == status {
			return true
		}
	}
	return false
}

func TestSampleValueTerminatesAndFollowsSchemas(t *testing.T) {
	schemas := map[string]any{
		"Node":  map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string", "format": "uuid"}, "next": schemaRef("Node")}},
		"Login": map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}, "kind": map[string]any{"enum": []string{"a", "b"}}, "n": map[string]any{"type": "integer", "minimum": 1}, "at": map[string]any{"type": []any{"string", "null"}, "format": "date-time"}}},
	}
	got := sampleValue(schemaRef("Node"), schemas, map[string]bool{}, 0)
	want := map[string]any{"id": sampleUUID, "next": map[string]any{}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("recursive sample %v", got)
	}
	got = sampleValue(map[string]any{"oneOf": []any{schemaRef("Login"), schemaRef("Node")}}, schemas, map[string]bool{}, 0)
	want = map[string]any{"name": "string", "kind": "a", "n": 1, "at": sampleDateTime}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("oneOf sample %v", got)
	}
	if schemaSummary(map[string]any{"type": "array", "items": map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "default": 50}}) != "array of integer; min 1; max 100; default 50" {
		t.Fatal("schema summary changed")
	}
	// Every success response of the reference document yields a sample.
	spec := Specification(ReferenceConfig())
	all := spec["components"].(map[string]any)["schemas"].(map[string]any)
	for path, item := range spec["paths"].(map[string]any) {
		for method, raw := range item.(map[string]any) {
			for status, response := range raw.(map[string]any)["responses"].(map[string]any) {
				content, _ := response.(map[string]any)["content"].(map[string]any)
				media, _ := content["application/json"].(map[string]any)
				if status[0] != '2' || media == nil {
					continue
				}
				if sampleValue(media["schema"].(map[string]any), all, map[string]bool{}, 0) == nil {
					t.Errorf("%s %s %s: no sample", method, path, status)
				}
			}
		}
	}
}
