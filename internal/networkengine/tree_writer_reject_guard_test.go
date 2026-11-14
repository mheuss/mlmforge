package networkengine

import (
	"errors"
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

// rejectionTypeName is the identifier that names the rejection event type.
const rejectionTypeName = "EventTypeEventRejected"

// rejectionPayloadName is the type name of a rejection's payload.
const rejectionPayloadName = "EventRejectedPayload"

// writerPackageDir is the module-relative directory of the package that owns
// the exempt Reject method.
const writerPackageDir = "internal/networkengine"

// isRejectionType reports whether e names the rejection event type.
func isRejectionType(e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.ParenExpr:
		return isRejectionType(e.X)
	case *ast.Ident:
		return e.Name == rejectionTypeName
	case *ast.SelectorExpr:
		return e.Sel.Name == rejectionTypeName
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

// collectionElem returns the element type of an array, slice or map type,
// without a pointer, or nil for any other expression.
func collectionElem(e ast.Expr) ast.Expr {
	var elem ast.Expr
	switch e := e.(type) {
	case *ast.ArrayType:
		elem = e.Elt
	case *ast.MapType:
		elem = e.Value
	default:
		return nil
	}
	if star, ok := elem.(*ast.StarExpr); ok {
		return star.X
	}
	return elem
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

// rejectionScan is what findRejectionProducers saw.
type rejectionScan struct {
	// producers holds "path:line: what" for each build outside the exempt
	// method.
	producers []string
	// exemptTypes and exemptPayloads count the builds inside the exempt
	// method.
	exemptTypes, exemptPayloads int
}

// isSkippedDir reports whether the walk leaves out the directory at path.
func isSkippedDir(path, name string) (bool, error) {
	if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "testdata" {
		return true, nil
	}
	_, err := os.Stat(filepath.Join(path, "go.mod"))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return false, err
}

// findRejectionProducers reports each place a non-test Go file in the module
// at root builds a tree.event_rejected event or its payload, and counts the
// builds inside the Reject method on *TreeWriter in the writer's package.
func findRejectionProducers(root string) (rejectionScan, error) {
	var scan rejectionScan
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if path == root {
				return nil
			}
			skip, err := isSkippedDir(path, name)
			if err != nil {
				return err
			}
			if skip {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") ||
			!strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		inWriterPackage := filepath.ToSlash(filepath.Dir(rel)) == writerPackageDir
		for _, decl := range file.Decls {
			fn, isFunc := decl.(*ast.FuncDecl)
			exempt := inWriterPackage && isFunc && isWriterReject(fn)
			sawType := func(pos token.Pos, what string) {
				if exempt {
					scan.exemptTypes++
					return
				}
				scan.producers = append(scan.producers, fmt.Sprintf("%s:%d: %s", rel, fset.Position(pos).Line, what))
			}
			sawPayload := func(pos token.Pos, what string) {
				if exempt {
					scan.exemptPayloads++
					return
				}
				scan.producers = append(scan.producers, fmt.Sprintf("%s:%d: %s", rel, fset.Position(pos).Line, what))
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.CallExpr:
					for _, arg := range n.Args {
						if isRejectionType(arg) {
							sawType(arg.Pos(), "rejection type passed to a call")
						}
					}
					if fun, ok := n.Fun.(*ast.Ident); ok && fun.Name == "new" && len(n.Args) == 1 && isRejectionPayload(n.Args[0]) {
						sawPayload(n.Pos(), "rejection payload allocated")
					}
				case *ast.AssignStmt:
					for _, rhs := range n.Rhs {
						if isRejectionType(rhs) {
							sawType(rhs.Pos(), "rejection type assigned")
						}
					}
				case *ast.KeyValueExpr:
					if key, ok := n.Key.(*ast.Ident); ok && key.Name == "Type" && isRejectionType(n.Value) {
						sawType(n.Value.Pos(), "rejection type set as an event's Type")
					}
				case *ast.CompositeLit:
					if isRejectionPayload(n.Type) && len(n.Elts) > 0 {
						sawPayload(n.Pos(), "rejection payload built")
					}
					if elem := collectionElem(n.Type); elem != nil && isRejectionPayload(elem) {
						for _, elt := range n.Elts {
							if kv, ok := elt.(*ast.KeyValueExpr); ok {
								elt = kv.Value
							}
							if lit, ok := elt.(*ast.CompositeLit); ok && lit.Type == nil && len(lit.Elts) > 0 {
								sawPayload(lit.Pos(), "rejection payload built")
							}
						}
					}
					for _, elt := range n.Elts {
						if _, keyed := elt.(*ast.KeyValueExpr); !keyed && isRejectionType(elt) {
							sawType(elt.Pos(), "rejection type in an unkeyed literal")
						}
					}
				}
				return true
			})
		}
		return nil
	})
	return scan, err
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
	var ev platform.NewEvent
	ev.Type = EventTypeEventRejected
	p := new(EventRejectedPayload)
	_ = platform.NewEvent{"id", EventTypeEventRejected, nil, nil}
	_, _ = ev, p
}

func Reject() { _, _ = newTreeEvent("tree.event_rejected", nil) }

func (o *Other) Reject() { _, _ = newTreeEvent((EventTypeEventRejected), nil) }

func (w TreeWriter) Reject() { _ = EventRejectedPayload{TreeID: "t"} }

func reads(ev platform.Event) bool {
	var empty EventRejectedPayload
	_ = EventRejectedPayload{}
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

func (w *TreeWriter) Reject() { _ = platform.NewEvent{Type: networkengine.EventTypeEventRejected} }

var payloads = []networkengine.EventRejectedPayload{{Reason: "r"}}
var byID = map[string]networkengine.EventRejectedPayload{"a": {Reason: "r"}}
var ptrs = []*networkengine.EventRejectedPayload{{Reason: "r"}}
var none = []networkengine.EventRejectedPayload{{}}
var events = []platform.NewEvent{{Type: networkengine.EventTypeEventRejected}}
var empty = []networkengine.EventRejectedPayload{}
`)
	producer := "package x\n\nfunc f() { g(EventTypeEventRejected) }\n"
	write("cmd/tool/main_test.go", producer)
	write("internal/x/testdata/fixture.go", producer)
	write(".hidden/y.go", producer)
	write("_skip/y.go", producer)
	write("internal/networkengine/_ignored.go", producer)
	write("internal/networkengine/.dotfile.go", producer)
	write("docs/copy/go.mod", "module copy\n")
	write("docs/copy/internal/networkengine/n.go", producer)

	scan, err := findRejectionProducers(root)

	require.NoError(t, err)
	assert.Equal(t, []string{
		"cmd/tool/main.go:4: rejection type set as an event's Type",
		"cmd/tool/main.go:5: rejection payload built",
		"cmd/tool/main.go:8: rejection type set as an event's Type",
		"cmd/tool/main.go:10: rejection payload built",
		"cmd/tool/main.go:11: rejection payload built",
		"cmd/tool/main.go:12: rejection payload built",
		"cmd/tool/main.go:14: rejection type set as an event's Type",
		"internal/networkengine/writer.go:6: rejection type passed to a call",
		"internal/networkengine/writer.go:7: rejection payload built",
		"internal/networkengine/writer.go:9: rejection type assigned",
		"internal/networkengine/writer.go:10: rejection payload allocated",
		"internal/networkengine/writer.go:11: rejection type in an unkeyed literal",
		"internal/networkengine/writer.go:15: rejection type passed to a call",
		"internal/networkengine/writer.go:17: rejection type passed to a call",
		"internal/networkengine/writer.go:19: rejection payload built",
	}, scan.producers)
	assert.Equal(t, 1, scan.exemptTypes)
	assert.Equal(t, 1, scan.exemptPayloads)
}

func TestNoProductionCodeBuildsARejectionOutsideTheWritersReject(t *testing.T) {
	scan, err := findRejectionProducers(moduleRoot(t))

	require.NoError(t, err)
	assert.Empty(t, scan.producers,
		"expected no non-test code to build tree.event_rejected outside (*TreeWriter).Reject; found:\n%s",
		strings.Join(scan.producers, "\n"))
	assert.Positive(t, scan.exemptTypes,
		"expected (*TreeWriter).Reject to build an event of the rejection type; the scan found %d such builds", scan.exemptTypes)
	assert.Positive(t, scan.exemptPayloads,
		"expected (*TreeWriter).Reject to build a rejection payload; the scan found %d", scan.exemptPayloads)
}
