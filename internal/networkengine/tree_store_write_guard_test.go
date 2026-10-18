package networkengine

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// unversionedStoreWrites are the method names the guard reports.
var unversionedStoreWrites = map[string]bool{
	"insertNode": true,
	"deleteNode": true,
}

// unversionedStoreHelpers are the package function names the guard reports.
var unversionedStoreHelpers = map[string]bool{
	"insertNode": true,
}

// storeImplementations are the files the guard does not read.
var storeImplementations = map[string]bool{
	"tree_store_memory.go":   true,
	"tree_store_postgres.go": true,
}

// findUnversionedStoreWrites returns "file:line: Name" for each use of a write
// that skips the projected version, in a non-test Go file in dir, other than in
// the store implementations.
func findUnversionedStoreWrites(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var found []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || storeImplementations[name] {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		report := func(pos token.Pos, ident string) {
			found = append(found, fmt.Sprintf("%s:%d: %s", name, fset.Position(pos).Line, ident))
		}
		// A selector's name and a declaration's name are idents too. Marking
		// them first stops the ident case reporting them a second time.
		seen := map[*ast.Ident]bool{}
		ast.Inspect(file, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.FuncDecl:
				seen[n.Name] = true
			case *ast.SelectorExpr:
				seen[n.Sel] = true
				if unversionedStoreWrites[n.Sel.Name] {
					report(n.Pos(), n.Sel.Name)
				}
			case *ast.Ident:
				if !seen[n] && unversionedStoreHelpers[n.Name] {
					report(n.Pos(), n.Name)
				}
			}
			return true
		})
	}
	return found, nil
}

// moduleRoot walks up from the working directory to the directory holding
// go.mod.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, dir, parent, "no go.mod above the working directory")
		dir = parent
	}
}

func TestFindUnversionedStoreWrites_NamesEachUseOutsideTheStores(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644))
	}
	write("writer.go", "package networkengine\n\nfunc run(s *MemoryTreeStore) {\n\ts.insertNode(nil, row)\n\ts.ProjectInsert(nil, row, 1)\n\tf := s.deleteNode\n\tinsertNode(nil, tx, row)\n\tg := insertNode\n}\n")
	write("writer_test.go", "package networkengine\n\nfunc seed(s *MemoryTreeStore) { s.insertNode(nil, row) }\n")
	write("tree_store_memory.go", "package networkengine\n\nfunc f(s *MemoryTreeStore) { s.deleteNode(nil, \"\", \"\") }\n")
	write("other.go", "package networkengine\n\nfunc (h holder) insertNode() {}\n")

	found, err := findUnversionedStoreWrites(dir)

	require.NoError(t, err)
	assert.Equal(t, []string{
		"writer.go:4: insertNode",
		"writer.go:6: deleteNode",
		"writer.go:7: insertNode",
		"writer.go:8: insertNode",
	}, found)
}

func TestNoProductionCodeCallsAnUnversionedStoreWrite(t *testing.T) {
	found, err := findUnversionedStoreWrites(filepath.Join(moduleRoot(t), "internal", "networkengine"))

	require.NoError(t, err)
	assert.Empty(t, found, "uses of insertNode or deleteNode in non-test Go files outside the store implementations")
}
