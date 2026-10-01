package httpapi

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/platform/i18n"
)

// The adapter owns this cross-layer contract. Audit production files only so
// tests cannot make an otherwise unused translation appear to have a caller.
func TestPublicHTTPErrorCodesHaveTranslations(t *testing.T) {
	used, err := publicErrorCodes(".")
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := englishCatalogKeys("../../platform/i18n/messages.go")
	if err != nil {
		t.Fatal(err)
	}
	missing, unused := keyDifferences(used, catalog)
	if len(missing) != 0 || len(unused) != 0 {
		t.Fatalf("HTTP translation keys: missing=%v unused=%v", missing, unused)
	}
	for code := range used {
		for _, locale := range []string{"zh-CN", "zh-TW", "ja-JP", "en-US"} {
			if i18n.Message(code, locale, "MISSING") == "MISSING" {
				t.Errorf("untranslated public HTTP code %s/%s", locale, code)
			}
		}
	}
}

func stringLiteral(expr ast.Expr) (string, bool) {
	literal, ok := expr.(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(literal.Value)
	return value, err == nil
}

func publicErrorCodes(folder string) (map[string]bool, error) {
	paths, err := filepath.Glob(filepath.Join(folder, "*.go"))
	if err != nil {
		return nil, err
	}
	used := map[string]bool{}
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return nil, err
		}
		var extractionErr error
		ast.Inspect(file, func(node ast.Node) bool {
			if extractionErr != nil {
				return false
			}
			switch node := node.(type) {
			case *ast.AssignStmt:
				for index, left := range node.Lhs {
					name, ok := left.(*ast.Ident)
					if !ok || name.Name != "code" {
						continue
					}
					if len(node.Lhs) != len(node.Rhs) {
						extractionErr = fmt.Errorf("%s: unsupported multi-result code assignment", path)
						break
					}
					if value, ok := stringLiteral(node.Rhs[index]); ok {
						used[value] = true
					} else {
						extractionErr = fmt.Errorf("%s: nonliteral code assignment; review translation audit", path)
					}
				}
			case *ast.ValueSpec:
				for index, name := range node.Names {
					if name.Name != "code" {
						continue
					}
					if len(node.Names) != len(node.Values) {
						extractionErr = fmt.Errorf("%s: unsupported code declaration", path)
						break
					}
					if value, ok := stringLiteral(node.Values[index]); ok {
						used[value] = true
					} else {
						extractionErr = fmt.Errorf("%s: nonliteral code declaration; review translation audit", path)
					}
				}
			case *ast.CallExpr:
				name, ok := node.Fun.(*ast.Ident)
				if !ok || name.Name != "writeProblem" {
					break
				}
				if len(node.Args) != 5 {
					extractionErr = fmt.Errorf("%s: changed writeProblem signature; review translation audit", path)
					break
				}
				if value, ok := stringLiteral(node.Args[3]); ok {
					used[value] = true
				} else if name, ok := node.Args[3].(*ast.Ident); !ok || name.Name != "code" || name.Obj == nil {
					extractionErr = fmt.Errorf("%s: unsupported dynamic error code; review translation audit", path)
				} else if _, parameter := name.Obj.Decl.(*ast.Field); parameter {
					extractionErr = fmt.Errorf("%s: error code parameter is not statically auditable", path)
				}
			}
			return true
		})
		if extractionErr != nil {
			return nil, extractionErr
		}
	}
	if len(used) == 0 {
		return nil, fmt.Errorf("no public HTTP error codes found in %s", folder)
	}
	return used, nil
}

func englishCatalogKeys(path string) (map[string]bool, error) {
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		return nil, err
	}
	keys := map[string]bool{}
	ast.Inspect(file, func(node ast.Node) bool {
		spec, ok := node.(*ast.ValueSpec)
		if !ok || len(spec.Names) != 1 || spec.Names[0].Name != "messages" || len(spec.Values) != 1 {
			return true
		}
		outer, ok := spec.Values[0].(*ast.CompositeLit)
		if !ok {
			return false
		}
		for _, element := range outer.Elts {
			pair, ok := element.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			locale, ok := stringLiteral(pair.Key)
			if !ok || locale != "en-US" {
				continue
			}
			inner, ok := pair.Value.(*ast.CompositeLit)
			if !ok {
				continue
			}
			for _, element := range inner.Elts {
				pair, ok := element.(*ast.KeyValueExpr)
				if ok {
					if key, ok := stringLiteral(pair.Key); ok {
						keys[key] = true
					}
				}
			}
		}
		return false
	})
	if len(keys) == 0 {
		return nil, fmt.Errorf("no English translation catalog keys found in %s", path)
	}
	return keys, nil
}

func keyDifferences(used, catalog map[string]bool) (missing, unused []string) {
	for key := range used {
		if !catalog[key] {
			missing = append(missing, key)
		}
	}
	for key := range catalog {
		if !used[key] {
			unused = append(unused, key)
		}
	}
	slices.Sort(missing)
	slices.Sort(unused)
	return missing, unused
}

func TestPublicErrorCodeAuditExtraction(t *testing.T) {
	cases := []struct {
		name, source string
		want         []string
		fail         bool
	}{
		{"new_file_selector_status", `package example; func f() { writeProblem(w, r, http.StatusBadRequest, "new_error", "fallback") }`, []string{"new_error"}, false},
		{"assignment_and_raw_literal", "package example; func f() { status, code, message := 400, `assigned_error`, `fallback`; writeProblem(w, r, status, code, message) }", []string{"assigned_error"}, false},
		{"dynamic_assignment_rejected", `package example; func f() { code := dynamic(); writeProblem(w, r, 400, code, "fallback") }`, nil, true},
		{"dynamic_declaration_rejected", `package example; var code = dynamic(); func f() { writeProblem(w, r, 400, code, "fallback") }`, nil, true},
		{"code_parameter_rejected", `package example; func f(code string) { writeProblem(w, r, 400, code, "fallback") }`, nil, true},
		{"dynamic_code_rejected", `package example; func f() { writeProblem(w, r, 400, dynamic(), "fallback") }`, nil, true},
		{"changed_signature_rejected", `package example; func f() { writeProblem(w, r, 400, "code") }`, nil, true},
		{"syntax_error_rejected", `package example; func f( {`, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			folder := t.TempDir()
			if err := os.WriteFile(filepath.Join(folder, "future_handler.go"), []byte(tc.source), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(folder, "caller_test.go"), []byte(`package example; func f() { writeProblem(w, r, 400, "test_only", "fallback") }`), 0600); err != nil {
				t.Fatal(err)
			}
			got, err := publicErrorCodes(folder)
			if tc.fail {
				if err == nil {
					t.Fatal("invalid source unexpectedly passed")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			missing, extra := keyDifferences(got, func() map[string]bool {
				result := map[string]bool{}
				for _, key := range tc.want {
					result[key] = true
				}
				return result
			}())
			if len(missing) != 0 || len(extra) != 0 {
				t.Fatalf("extracted codes differ: %v %v", missing, extra)
			}
		})
	}
}

func TestHTTPTranslationAuditRejectsMissingAndUnusedKeys(t *testing.T) {
	missing, unused := keyDifferences(map[string]bool{"used": true, "untranslated": true}, map[string]bool{"used": true, "unused": true})
	if !slices.Equal(missing, []string{"untranslated"}) || !slices.Equal(unused, []string{"unused"}) {
		t.Fatalf("missing=%v unused=%v", missing, unused)
	}
}
