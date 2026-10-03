package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const modulePath = "github.com/MoYuanCN/Jelee"

// deliveryRoots are the request-facing playback packages that must never be
// able to start a process. Sub-packages are included.
var deliveryRoots = []string{"internal/adapter/media", "internal/adapter/http", "internal/adapter/compat"}

// processImports give a package the ability to start another program.
var processImports = map[string]bool{"os/exec": true, "plugin": true, "C": true}

// processCalls are process-spawning functions outside os/exec, keyed by the
// import path of their package.
var processCalls = map[string]map[string]bool{
	"os":                    {"StartProcess": true},
	"syscall":               {"Exec": true, "ForkExec": true, "StartProcess": true, "CreateProcess": true},
	"golang.org/x/sys/unix": {"Exec": true},
	"golang.org/x/sys/windows": {
		"CreateProcess": true, "CreateProcessAsUser": true, "ShellExecute": true,
	},
}

type goPackage struct {
	dir        string
	imports    map[string]bool
	violations []string
	literals   []string
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate architecture test")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

// loadModulePackages parses every non-test Go file under internal/ and cmd/
// regardless of build tags, so platform-specific files are checked too.
func loadModulePackages(t *testing.T, root string) map[string]*goPackage {
	t.Helper()
	packages := map[string]*goPackage{}
	for _, top := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(file string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if name := entry.Name(); name == "testdata" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(file, ".go") || strings.HasSuffix(file, "_test.go") {
				return nil
			}
			rel, err := filepath.Rel(root, filepath.Dir(file))
			if err != nil {
				return err
			}
			importPath := path.Join(modulePath, filepath.ToSlash(rel))
			pkg := packages[importPath]
			if pkg == nil {
				pkg = &goPackage{dir: filepath.ToSlash(rel), imports: map[string]bool{}}
				packages[importPath] = pkg
			}
			return inspectFile(pkg, file)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return packages
}

func inspectFile(pkg *goPackage, file string) error {
	node, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.SkipObjectResolution)
	if err != nil {
		return err
	}
	names := map[string]string{}
	for _, imp := range node.Imports {
		name, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			return err
		}
		pkg.imports[name] = true
		if processImports[name] {
			pkg.violations = append(pkg.violations, file+" imports "+name)
		}
		local := path.Base(name)
		if imp.Name != nil {
			local = imp.Name.Name
		}
		names[local] = name
	}
	ast.Inspect(node, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.SelectorExpr:
			if ident, ok := v.X.(*ast.Ident); ok && processCalls[names[ident.Name]][v.Sel.Name] {
				pkg.violations = append(pkg.violations, file+" references "+names[ident.Name]+"."+v.Sel.Name)
			}
		case *ast.BasicLit:
			if v.Kind == token.STRING {
				if text, err := strconv.Unquote(v.Value); err == nil {
					pkg.literals = append(pkg.literals, file+"\x00"+text)
				}
			}
		}
		return true
	})
	return nil
}

func isDeliveryPackage(dir string) bool {
	for _, root := range deliveryRoots {
		if dir == root || strings.HasPrefix(dir, root+"/") {
			return true
		}
	}
	return false
}

// TestDeliveryPackagesCannotRunEncoders enforces G10.11 statically: the
// playback/compat/HTTP adapters, and every module package they reach through
// imports, must not import os/exec (or cgo/plugin) nor call another
// process-spawning API, and the delivery packages themselves must not carry
// an encoder executable name in any string constant or literal.
func TestDeliveryPackagesCannotRunEncoders(t *testing.T) {
	root := repositoryRoot(t)
	packages := loadModulePackages(t, root)
	var queue []string
	for importPath, pkg := range packages {
		if isDeliveryPackage(pkg.dir) {
			queue = append(queue, importPath)
		}
	}
	if len(queue) < 3 {
		t.Fatalf("expected the delivery packages to exist, found %d", len(queue))
	}
	sort.Strings(queue)
	via := map[string]string{}
	for _, importPath := range queue {
		via[importPath] = ""
	}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		pkg := packages[current]
		chain := current
		for parent := via[current]; parent != ""; parent = via[parent] {
			chain = parent + " -> " + chain
		}
		for _, violation := range pkg.violations {
			t.Errorf("%s: %s", chain, violation)
		}
		if isDeliveryPackage(pkg.dir) {
			for _, literal := range pkg.literals {
				file, text, _ := strings.Cut(literal, "\x00")
				if strings.Contains(strings.ToLower(text), "ffmpeg") {
					t.Errorf("%s: string literal references the encoder: %q", file, text)
				}
			}
		}
		for imported := range pkg.imports {
			if _, seen := via[imported]; seen || packages[imported] == nil {
				continue
			}
			via[imported] = current
			queue = append(queue, imported)
		}
	}
}

// TestNoExecScannerDetectsViolations guards the scanner against silently
// passing because of a parsing or matching regression.
func TestNoExecScannerDetectsViolations(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "bad.go")
	source := "package bad\n\nimport (\n\trun \"os/exec\"\n\tsys \"syscall\"\n\t\"os\"\n)\n\nconst tool = \"/usr/bin/FFmpeg\"\n\n" +
		"func start() { _ = run.Command(tool); _ = sys.Exec; _, _ = os.StartProcess(tool, nil, nil) }\n"
	if err := os.WriteFile(file, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	pkg := &goPackage{imports: map[string]bool{}}
	if err := inspectFile(pkg, file); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(pkg.violations, "\n")
	for _, want := range []string{"imports os/exec", "references syscall.Exec", "references os.StartProcess"} {
		if !strings.Contains(joined, want) {
			t.Errorf("scanner missed %q in %q", want, joined)
		}
	}
	found := false
	for _, literal := range pkg.literals {
		if strings.Contains(strings.ToLower(literal), "ffmpeg") {
			found = true
		}
	}
	if !found {
		t.Error("scanner missed the encoder string constant")
	}
}
