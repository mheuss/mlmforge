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

// handleEventWriterFile is the module-relative path of the one non-test file
// allowed to refer to HandleEvent.
const handleEventWriterFile = writerPackageDir + "/tree_writer.go"

// handleEventScan is what findHandleEventReferences saw.
type handleEventScan struct {
	// others holds "path:line" for each reference outside the writer's file.
	others []string
	// inWriter counts the references inside it.
	inWriter int
}

// findHandleEventReferences reports each selector naming HandleEvent in a
// non-test Go file of the module at root, a call or a method value alike.
func findHandleEventReferences(root string) (handleEventScan, error) {
	var scan handleEventScan
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
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "HandleEvent" {
				return true
			}
			if rel == handleEventWriterFile {
				scan.inWriter++
				return true
			}
			scan.others = append(scan.others, fmt.Sprintf("%s:%d", rel, fset.Position(sel.Pos()).Line))
			return true
		})
		return nil
	})
	return scan, err
}

func TestFindHandleEventReferences_NamesEachReferenceOutsideTheWriter(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		path := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	}
	write(handleEventWriterFile, `package networkengine

func (w *TreeWriter) project() { _ = w.consumer.HandleEvent(nil, treeShape{}, platform.Event{}) }
`)
	write("internal/networkengine/tree_consumer.go", `package networkengine

// HandleEvent is declared here, and a comment naming it is not a reference.
func (c *TreeEventConsumer) HandleEvent() error { return nil }

func replay(c *TreeEventConsumer) {
	_ = c.HandleEvent()
	f := c.HandleEvent
	_ = f
}
`)
	write("cmd/tool/main.go", `package main

func main() { _ = consumer.HandleEvent(ctx, shape, ev) }
`)
	caller := "package x\n\nfunc f() { _ = c.HandleEvent() }\n"
	write("cmd/tool/main_test.go", caller)
	write("internal/x/testdata/fixture.go", caller)
	write(".hidden/y.go", caller)
	write("_skip/y.go", caller)
	write("docs/copy/go.mod", "module copy\n")
	write("docs/copy/internal/networkengine/n.go", caller)

	scan, err := findHandleEventReferences(root)

	require.NoError(t, err)
	assert.Equal(t, []string{
		"cmd/tool/main.go:3",
		"internal/networkengine/tree_consumer.go:7",
		"internal/networkengine/tree_consumer.go:8",
	}, scan.others)
	assert.Equal(t, 1, scan.inWriter)
}

func TestNoProductionCodeCallsHandleEventOutsideTheWriter(t *testing.T) {
	scan, err := findHandleEventReferences(moduleRoot(t))

	require.NoError(t, err)
	assert.Empty(t, scan.others,
		"expected no non-test file other than %s to refer to HandleEvent; found:\n%s",
		handleEventWriterFile, strings.Join(scan.others, "\n"))
	assert.Positive(t, scan.inWriter,
		"expected %s to refer to HandleEvent; the scan found %d references", handleEventWriterFile, scan.inWriter)
}
