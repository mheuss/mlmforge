package networkengine

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// unversionedStoreWrites are the method names the guard reports.
var unversionedStoreWrites = map[string]bool{
	"InsertNode":             true,
	"DeleteNode":             true,
	"DeleteNodeAndResponsor": true,
	"BulkInsert":             true,
}

// unversionedStoreHelpers are the package function names the guard reports.
var unversionedStoreHelpers = map[string]bool{
	"insertNode":             true,
	"deleteNodeAndResponsor": true,
}

// storeImplementations are the files the guard does not read.
var storeImplementations = map[string]bool{
	"internal/networkengine/tree_store_memory.go":   true,
	"internal/networkengine/tree_store_postgres.go": true,
}

// findUnversionedStoreWrites returns "path:line: Name" for each call to an
// unversioned write in a non-test Go file under root, other than in the store
// implementations.
func findUnversionedStoreWrites(root string) ([]string, error) {
	var found []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".worktrees", ".sop-tmp", "target", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if storeImplementations[rel] {
			return nil
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		report := func(pos token.Pos, name string) {
			found = append(found, fmt.Sprintf("%s:%d: %s", rel, fset.Position(pos).Line, name))
		}
		// Any selector, not only a call's, so a method value such as
		// f := s.InsertNode is reported too.
		ast.Inspect(file, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.SelectorExpr:
				if unversionedStoreWrites[n.Sel.Name] {
					report(n.Pos(), n.Sel.Name)
				}
			case *ast.CallExpr:
				if id, ok := n.Fun.(*ast.Ident); ok && unversionedStoreHelpers[id.Name] {
					report(n.Pos(), id.Name)
				}
			}
			return true
		})
		return nil
	})
	return found, err
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

func TestFindUnversionedStoreWrites_NamesEachCallOutsideTheStores(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		path := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	}
	write("cmd/tool/main.go", "package main\n\nfunc run(s store) {\n\ts.InsertNode(nil, row)\n\ts.ProjectInsert(nil, row, 1)\n\tf := s.DeleteNode\n\tinsertNode(nil, tx, row)\n}\n")
	write("cmd/tool/main_test.go", "package main\n\nfunc seed(s store) { s.BulkInsert(nil, nil) }\n")
	write("internal/networkengine/tree_store_memory.go", "package networkengine\n\nfunc f(s *S) { s.DeleteNode(nil, \"\", \"\") }\n")

	found, err := findUnversionedStoreWrites(root)

	require.NoError(t, err)
	assert.Equal(t, []string{
		"cmd/tool/main.go:4: InsertNode",
		"cmd/tool/main.go:6: DeleteNode",
		"cmd/tool/main.go:7: insertNode",
	}, found)
}

func TestNoProductionCodeCallsAnUnversionedStoreWrite(t *testing.T) {
	found, err := findUnversionedStoreWrites(moduleRoot(t))

	require.NoError(t, err)
	assert.Empty(t, found, "calls to InsertNode, DeleteNode, DeleteNodeAndResponsor or BulkInsert in non-test Go files outside the store implementations")
}
