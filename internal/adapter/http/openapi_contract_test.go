package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/go-chi/chi/v5"
)

// undocumentedRoutes lists router entries deliberately absent from the
// OpenAPI document, keyed by "METHOD /path", with the reason. Keep it empty
// unless a route is not part of the public API contract.
var undocumentedRoutes = map[string]string{
	"GET /compat/System/Info/Public": compatExemption,
	"GET /compat/System/Info":        compatExemption,
	"GET /compat/System/Ping":        compatExemption,
	"POST /compat/System/Ping":       compatExemption,
}

// compatExemption: the /compat layer reproduces a third-party wire protocol
// (PascalCase DTOs, upstream status codes and bodies, case-insensitive paths)
// that this API's conventions and error envelope do not describe. Its
// contract lives in docs/compat-matrix.md and the golden files under
// internal/adapter/compat/testdata/golden.
const compatExemption = "third-party client compatibility protocol, not the Jelee API; contract in docs/compat-matrix.md and compat golden files"

// contractRouter builds the real router with every service present so that
// only the rollout flags in cfg decide which routes are registered.
func contractRouter(t *testing.T, cfg config.Config) http.Handler {
	t.Helper()
	jobs, err := app.NewJobs(httpJobRepo{}, config.DefaultJobsConfig().Policy())
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := app.NewMetadata(&httpMovieProvider{})
	if err != nil {
		t.Fatal(err)
	}
	images, err := app.NewImages(httpImageRepository(func(context.Context, domain.Actor, string) (domain.LocalImageSource, error) {
		return domain.LocalImageSource{}, domain.ErrImageUnavailable
	}), httpImageRenderer(func(context.Context, domain.LocalImageSource, domain.ImageRequest) (app.ImageResult, error) {
		return app.ImageResult{}, domain.ErrImageUnavailable
	}))
	if err != nil {
		t.Fatal(err)
	}
	handler, err := newServer(cfg, &fakeBackend{}, app.NewCatalog(&fakeRepository{}), &fakeResolver{}, slog.New(slog.NewTextHandler(io.Discard, nil)), metricsAccounts(t), jobs, metadata, http.NotFoundHandler(), images, nil)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func registeredRoutes(t *testing.T, handler http.Handler) map[string]bool {
	t.Helper()
	routes := map[string]bool{}
	if err := chi.Walk(handler.(chi.Routes), func(method, path string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		routes[method+" "+path] = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return routes
}

func documentedRoutes(spec map[string]any) map[string]bool {
	routes := map[string]bool{}
	for path, item := range spec["paths"].(map[string]any) {
		for method := range item.(map[string]any) {
			routes[strings.ToUpper(method)+" "+path] = true
		}
	}
	return routes
}

func sortedDifference(left, right map[string]bool) []string {
	var result []string
	for key := range left {
		if !right[key] {
			result = append(result, key)
		}
	}
	sort.Strings(result)
	return result
}

// Every valid rollout must document exactly the routes it registers.
func TestOpenAPIDocumentsEveryRegisteredRouteForEveryRollout(t *testing.T) {
	flags := []string{"catalog", "direct", "accounts", "metrics", "images", "jobs", "tmdb"}
	valid := 0
	for mask := range 1 << len(flags) {
		cfg := ReferenceConfig()
		enabled := func(i int) bool { return mask&(1<<i) != 0 }
		cfg.EnableCatalog, cfg.EnableDirect, cfg.EnableAccounts, cfg.EnableMetrics, cfg.EnableImages, cfg.EnableJobs = enabled(0), enabled(1), enabled(2), enabled(3), enabled(4), enabled(5)
		cfg.EnableProbe, cfg.EnableFamilyIgnore = cfg.EnableJobs, cfg.EnableJobs
		if !enabled(6) {
			cfg.TMDBAPIKey = ""
		}
		if cfg.Validate() != nil {
			continue
		}
		valid++
		var names []string
		for i, name := range flags {
			if enabled(i) {
				names = append(names, name)
			}
		}
		t.Run("rollout="+strings.Join(names, "+"), func(t *testing.T) {
			registered := registeredRoutes(t, contractRouter(t, cfg))
			for route, reason := range undocumentedRoutes {
				if reason == "" {
					t.Fatalf("exemption %s needs a reason", route)
				}
				delete(registered, route)
			}
			documented := documentedRoutes(Specification(cfg))
			if missing := sortedDifference(registered, documented); len(missing) != 0 {
				t.Errorf("registered routes missing from OpenAPI: %v", missing)
			}
			if phantom := sortedDifference(documented, registered); len(phantom) != 0 {
				t.Errorf("OpenAPI paths without a registered route: %v", phantom)
			}
		})
	}
	if valid < 10 {
		t.Fatalf("only %d rollout combinations validated", valid)
	}
	for route := range undocumentedRoutes {
		if !registeredRoutes(t, contractRouter(t, ReferenceConfig()))[route] {
			t.Errorf("stale exemption %s", route)
		}
	}
}

// The served document must equal the committed api/openapi.json.
func TestServedOpenAPIMatchesCommittedSpecification(t *testing.T) {
	cfg := ReferenceConfig()
	w := httptest.NewRecorder()
	contractRouter(t, cfg).ServeHTTP(w, httptest.NewRequest("GET", "http://localhost/api/v1/openapi.json", nil))
	if w.Code != 200 {
		t.Fatalf("openapi endpoint returned %d", w.Code)
	}
	var served, committed any
	if err := json.Unmarshal(w.Body.Bytes(), &served); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "api", "openapi.json"))
	if err != nil {
		t.Fatalf("read committed specification: %v", err)
	}
	if err := json.Unmarshal(data, &committed); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(served, committed) {
		t.Fatal("api/openapi.json differs from /api/v1/openapi.json; run `go run ./tools/openapi` from the repository root")
	}
}

func TestOpenAPIDescribesErrorEnvelopeAndCodes(t *testing.T) {
	spec := Specification(ReferenceConfig())
	schemas := spec["components"].(map[string]any)["schemas"].(map[string]any)
	envelope := schemas["Error"].(map[string]any)
	inner := envelope["properties"].(map[string]any)["error"].(map[string]any)
	if !slices.Equal(inner["required"].([]string), []string{"code", "message", "details", "traceId"}) || !reflect.DeepEqual(envelope["required"], []string{"error"}) {
		t.Fatal("error envelope does not match writeProblem")
	}
	// The documented envelope must describe what writeProblem actually writes.
	w := httptest.NewRecorder()
	writeProblem(w, httptest.NewRequest("GET", "http://localhost/", nil), 400, "invalid_request", "Request is invalid.")
	var body map[string]map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || len(body) != 1 {
		t.Fatal("unexpected problem body")
	}
	keys := make([]string, 0, len(body["error"]))
	for key := range body["error"] {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	documented := slices.Clone(inner["required"].([]string))
	sort.Strings(documented)
	if !slices.Equal(keys, documented) {
		t.Fatalf("problem body keys %v differ from documented %v", keys, documented)
	}

	used, err := publicErrorCodes(".")
	if err != nil {
		t.Fatal(err)
	}
	codes := schemas["ErrorCode"].(map[string]any)
	enum := codes["enum"].([]string)
	if !slices.IsSorted(enum) || len(slices.Compact(slices.Clone(enum))) != len(enum) {
		t.Fatal("error codes are not sorted and unique")
	}
	documentedCodes := map[string]bool{}
	for _, code := range enum {
		documentedCodes[code] = true
	}
	if missing, unused := sortedDifference(used, documentedCodes), sortedDifference(documentedCodes, used); len(missing) != 0 || len(unused) != 0 {
		t.Fatalf("OpenAPI error codes: missing=%v unused=%v", missing, unused)
	}
	pairs, err := publicErrorStatuses(".")
	if err != nil {
		t.Fatal(err)
	}
	table := map[string]map[int]bool{}
	for code, statuses := range errorCodeStatuses {
		table[code] = map[int]bool{}
		for _, status := range statuses {
			table[code][status] = true
		}
	}
	if !reflect.DeepEqual(pairs, table) {
		t.Fatalf("error status table differs from sources:\nsource=%v\ntable=%v", pairs, table)
	}

	for path, item := range spec["paths"].(map[string]any) {
		for method, raw := range item.(map[string]any) {
			responses := raw.(map[string]any)["responses"].(map[string]any)
			if _, ok := responses["default"]; !ok {
				t.Errorf("%s %s lacks a default error response", method, path)
			}
			for status, response := range responses {
				if status != "default" && status[0] != '4' && status[0] != '5' {
					continue
				}
				content, _ := response.(map[string]any)["content"].(map[string]any)
				media, _ := content["application/json"].(map[string]any)
				if media == nil || !reflect.DeepEqual(media["schema"], schemaRef("Error")) {
					t.Errorf("%s %s response %s does not reference the Error schema", method, path, status)
				}
			}
		}
	}
}

// publicErrorStatuses pairs each literal error code with its HTTP status from
// WriteError's mapping assignments and direct writeProblem calls.
func publicErrorStatuses(folder string) (map[string]map[int]bool, error) {
	paths, err := filepath.Glob(filepath.Join(folder, "*.go"))
	if err != nil {
		return nil, err
	}
	named := map[string]int{"StatusBadRequest": 400, "StatusServiceUnavailable": 503, "StatusTooManyRequests": 429, "StatusInternalServerError": 500}
	status := func(expr ast.Expr) (int, error) {
		switch value := expr.(type) {
		case *ast.BasicLit:
			if value.Kind == token.INT {
				return strconv.Atoi(value.Value)
			}
		case *ast.SelectorExpr:
			if n, ok := named[value.Sel.Name]; ok {
				return n, nil
			}
		}
		return 0, fmt.Errorf("unsupported status expression %T; extend publicErrorStatuses", expr)
	}
	result := map[string]map[int]bool{}
	add := func(code string, n int) {
		if result[code] == nil {
			result[code] = map[int]bool{}
		}
		result[code][n] = true
	}
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return nil, err
		}
		var walkErr error
		ast.Inspect(file, func(node ast.Node) bool {
			if walkErr != nil {
				return false
			}
			switch node := node.(type) {
			case *ast.AssignStmt:
				if len(node.Lhs) != 3 || len(node.Rhs) != 3 {
					break
				}
				if name, ok := node.Lhs[1].(*ast.Ident); !ok || name.Name != "code" {
					break
				}
				code, ok := stringLiteral(node.Rhs[1])
				if !ok {
					break
				}
				n, err := status(node.Rhs[0])
				if err != nil {
					walkErr = fmt.Errorf("%s: %w", path, err)
					break
				}
				add(code, n)
			case *ast.CallExpr:
				if name, ok := node.Fun.(*ast.Ident); !ok || name.Name != "writeProblem" || len(node.Args) != 5 {
					break
				}
				code, ok := stringLiteral(node.Args[3])
				if !ok {
					break
				}
				n, err := status(node.Args[2])
				if err != nil {
					walkErr = fmt.Errorf("%s: %w", path, err)
					break
				}
				add(code, n)
			}
			return true
		})
		if walkErr != nil {
			return nil, walkErr
		}
	}
	return result, nil
}

// The reference configuration renders the specification on every developer
// and CI platform, so its paths must be absolute on the running OS.
func TestReferenceConfigValidatesOnThisPlatform(t *testing.T) {
	if err := ReferenceConfig().Images.Validate(); err != nil {
		t.Fatal(err)
	}
}
