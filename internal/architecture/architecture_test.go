package architecture

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestDomainAndApplicationDependencyDirection(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate architecture test")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	for _, layer := range []string{"domain", "app"} {
		err := filepath.WalkDir(filepath.Join(root, "internal", layer), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			node, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			for _, imp := range node.Imports {
				name, err := strconv.Unquote(imp.Path.Value)
				if err != nil {
					return err
				}
				if strings.Contains(name, "/internal/") {
					if layer != "app" || !strings.HasPrefix(name, "github.com/MoYuanCN/Jelee/internal/domain") {
						t.Errorf("%s imports forbidden dependency %s", layer, name)
					}
				} else if strings.Contains(strings.Split(name, "/")[0], ".") || name == "os" || name == "net" || strings.HasPrefix(name, "net/") || strings.HasPrefix(name, "database/") {
					t.Errorf("%s must not own I/O dependency %s", layer, name)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// TestExamplesUseOnlyTheStandardLibrary keeps examples/ (G49.5) a template a
// client author can copy: its Go code may import the standard library and
// other example packages, never Jelee internals or third-party modules.
func TestExamplesUseOnlyTheStandardLibrary(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate architecture test")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	files := 0
	err := filepath.WalkDir(filepath.Join(root, "examples"), func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		files++
		node, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range node.Imports {
			name, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return err
			}
			if strings.HasPrefix(name, "github.com/MoYuanCN/Jelee/examples/") {
				continue
			}
			if strings.Contains(strings.Split(name, "/")[0], ".") {
				t.Errorf("%s imports %s; examples use the standard library only", path, name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files == 0 {
		t.Fatal("no Go example found under examples/")
	}
}
