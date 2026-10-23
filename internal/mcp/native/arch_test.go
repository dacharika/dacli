package native

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const internalImportRoot = "github.com/mlnomadpy/dacli/internal/"

// Native MCP is a host-neutral governance boundary. Keep its direct production
// imports away from the removed coding CLI lifecycle and its process adapters;
// shared store and eventlog packages remain valid data dependencies. This checks
// direct imports only and does not prove that the transitive graph lacks
// historical process records.
func TestNativeMCPImportsExcludeLegacyExecutionDependencies(t *testing.T) {
	for _, allowed := range []string{
		internalImportRoot + "store",
		internalImportRoot + "eventlog",
	} {
		if forbiddenNativeImport(allowed) {
			t.Errorf("shared data dependency %s was prohibited", allowed)
		}
	}
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		for _, spec := range file.Imports {
			name, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Errorf("%s has invalid import path %s: %v", path, spec.Path.Value, err)
				continue
			}
			if forbiddenNativeImport(name) {
				t.Errorf("%s imports %s; native MCP must not depend on the removed coding CLI execution boundary", path, name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func forbiddenNativeImport(name string) bool {
	if name == "os/exec" {
		return true
	}
	for _, prefix := range []string{
		internalImportRoot + "cli",
		internalImportRoot + "runtime",
		internalImportRoot + "provider",
		internalImportRoot + "procmon",
		internalImportRoot + "gitx",
		internalImportRoot + "mcp",
		internalImportRoot + "features",
	} {
		if name == prefix || strings.HasPrefix(name, prefix+"/") {
			return true
		}
	}
	return false
}
