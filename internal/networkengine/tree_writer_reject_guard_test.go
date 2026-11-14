package networkengine

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rejectionTypeNames are the identifiers that name the rejection event type.
var rejectionTypeNames = map[string]bool{"EventTypeEventRejected": true}

// rejectionPayloadName is the type name of a rejection's payload.
const rejectionPayloadName = "EventRejectedPayload"

// isRejectionType reports whether e names the rejection event type, by
// constant or by its literal value.
func isRejectionType(e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.Ident:
		return rejectionTypeNames[e.Name]
	case *ast.SelectorExpr:
		return rejectionTypeNames[e.Sel.Name]
	case *ast.BasicLit:
		v, err := strconv.Unquote(e.Value)
		return e.Kind == token.STRING && err == nil && v == EventTypeEventRejected
	}
	return false
}

// isRejectionPayload reports whether e is the rejection payload type.
func isRejectionPayload(e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.Ident:
		return e.Name == rejectionPayloadName
	case *ast.SelectorExpr:
		return e.Sel.Name == rejectionPayloadName
	}
	return false
}

// isWriterReject reports whether d is the Reject method on *TreeWriter.
func isWriterReject(d *ast.FuncDecl) bool {
	if d.Name.Name != "Reject" || d.Recv == nil || len(d.Recv.List) != 1 {
		return false
	}
	star, ok := d.Recv.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	ident, ok := star.X.(*ast.Ident)
	return ok && ident.Name == "TreeWriter"
}

// findRejectionProducers returns "path:line: what" for each place a non-test
// Go file under root builds a tree.event_rejected event or its payload,
// other than inside the Reject method on *TreeWriter. It skips directories
// whose names start with a dot or an underscore, and testdata.
func findRejectionProducers(root string) ([]string, error) {
	var found []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if path != root && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		report := func(pos token.Pos, what string) {
			found = append(found, fmt.Sprintf("%s:%d: %s", filepath.ToSlash(rel), fset.Position(pos).Line, what))
		}
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && isWriterReject(fn) {
				continue
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.CallExpr:
					for _, arg := range n.Args {
						if isRejectionType(arg) {
							report(arg.Pos(), "rejection type passed to a call")
						}
					}
				case *ast.KeyValueExpr:
					if key, ok := n.Key.(*ast.Ident); ok && key.Name == "Type" && isRejectionType(n.Value) {
						report(n.Value.Pos(), "rejection type set as an event's Type")
					}
				case *ast.CompositeLit:
					if isRejectionPayload(n.Type) && len(n.Elts) > 0 {
						report(n.Pos(), "rejection payload built")
					}
				}
				return true
			})
		}
		return nil
	})
	return found, err
}

func TestFindRejectionProducers_NamesEachBuildOutsideTheWritersReject(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		path := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	}
	write("internal/networkengine/writer.go", `package networkengine

func (w *TreeWriter) Reject() { _, _ = newTreeEvent(EventTypeEventRejected, EventRejectedPayload{TreeID: "t"}) }

func (w *TreeWriter) other() {
	_, _ = newTreeEvent(EventTypeEventRejected, nil)
	_ = EventRejectedPayload{Reason: "r"}
}

func Reject() { _, _ = newTreeEvent("tree.event_rejected", nil) }

func reads(ev platform.Event) bool {
	var empty EventRejectedPayload
	_ = map[string]bool{EventTypeEventRejected: true}
	switch ev.Type {
	case EventTypeEventRejected:
	}
	_ = empty
	return ev.Type == EventTypeEventRejected || ev.Type != "tree.event_rejected"
}
`)
	write("cmd/tool/main.go", `package main

func main() {
	_ = platform.NewEvent{Type: networkengine.EventTypeEventRejected}
	_ = networkengine.EventRejectedPayload{TreeID: "t"}
}
`)
	write("cmd/tool/main_test.go", "package main\n\nfunc seed() { _ = platform.NewEvent{Type: networkengine.EventTypeEventRejected} }\n")
	write("internal/x/testdata/fixture.go", "package x\n\nfunc f() { g(EventTypeEventRejected) }\n")
	write(".hidden/y.go", "package y\n\nfunc f() { g(EventTypeEventRejected) }\n")

	found, err := findRejectionProducers(root)

	require.NoError(t, err)
	assert.Equal(t, []string{
		"cmd/tool/main.go:4: rejection type set as an event's Type",
		"cmd/tool/main.go:5: rejection payload built",
		"internal/networkengine/writer.go:6: rejection type passed to a call",
		"internal/networkengine/writer.go:7: rejection payload built",
		"internal/networkengine/writer.go:10: rejection type passed to a call",
	}, found)
}

func TestNoProductionCodeBuildsARejectionOutsideTheWritersReject(t *testing.T) {
	found, err := findRejectionProducers(moduleRoot(t))

	require.NoError(t, err)
	assert.Empty(t, found, "code that builds a tree.event_rejected event outside (*TreeWriter).Reject, in non-test Go files")
}
