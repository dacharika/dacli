package application

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestApplicationDependencies(t *testing.T) {
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			name, _ := strconv.Unquote(imp.Path.Value)
			if strings.Contains(name, "/internal/features/") || (strings.HasSuffix(name, "/internal/cli") || strings.Contains(name, "/internal/cli/")) || strings.Contains(name, "/internal/mcp") || strings.Contains(name, "/internal/runtime") || strings.Contains(name, "/internal/provider") || strings.Contains(name, "/internal/gitx") || strings.Contains(name, "/internal/procmon") || name == "os/exec" {
				t.Errorf("%s imports forbidden transport/process dependency %s", path, name)
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			if (ident.Name == "os" && (sel.Sel.Name == "Getwd" || sel.Sel.Name == "Getenv" || sel.Sel.Name == "LookupEnv")) || (ident.Name == "agentid" && sel.Sel.Name == "Resolve") || (ident.Name == "workspace" && sel.Sel.Name == "Find") {
				t.Errorf("%s reads ambient context through %s.%s", path, ident.Name, sel.Sel.Name)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
