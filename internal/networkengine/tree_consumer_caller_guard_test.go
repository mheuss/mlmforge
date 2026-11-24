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

// handleEventWriterReferences is how many references to HandleEvent
// handleEventWriterFile may hold. Pinned, so that adding one fails this test
// and has to change this line.
const handleEventWriterReferences = 2

// consumerFile is the module-relative path of the one non-test file allowed to
// refer to consumerHandlers.
const consumerFile = writerPackageDir + "/tree_consumer.go"

// consumerHandlers names the methods only consumerFile may refer to.
var consumerHandlers = map[string]bool{
	"handleRootAdded":     true,
	"handleNodePlaced":    true,
	"handleNodeRemoved":   true,
	"handleEventRejected": true,
}

// handleEventScan is what findHandleEventReferences saw.
type handleEventScan struct {
	// others holds "path:line: name" for each reference outside the file
	// allowed to make it.
	others []string
	// inWriter counts the references to HandleEvent in handleEventWriterFile.
	inWriter int
}

// findHandleEventReferences reports each selector naming HandleEvent outside
// handleEventWriterFile, or naming a consumer handler outside consumerFile, in
// a non-test Go file of the module at root. A call and a method value count
// alike.
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
			if !ok {
				return true
			}
			name := sel.Sel.Name
			switch {
			case name == "HandleEvent" && rel == handleEventWriterFile:
				scan.inWriter++
			case name == "HandleEvent", consumerHandlers[name] && rel != consumerFile:
				scan.others = append(scan.others, fmt.Sprintf("%s:%d: %s", rel, fset.Position(sel.Pos()).Line, name))
			}
			return true
		})
		return nil
	})
	return scan, err
}

// handleEventGuardFailures returns one message for each rule scan breaks.
func handleEventGuardFailures(scan handleEventScan) []string {
	var failures []string
	if len(scan.others) > 0 {
		failures = append(failures, fmt.Sprintf(
			"expected HandleEvent only in %s and the consumer handlers only in %s; found:\n%s",
			handleEventWriterFile, consumerFile, strings.Join(scan.others, "\n")))
	}
	if scan.inWriter != handleEventWriterReferences {
		failures = append(failures, fmt.Sprintf("expected %d HandleEvent references in %s; found %d",
			handleEventWriterReferences, handleEventWriterFile, scan.inWriter))
	}
	return failures
}

func TestFindHandleEventReferences_NamesEachReferenceOutsideItsFile(t *testing.T) {
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

func (c *TreeEventConsumer) dispatch() { _ = c.handleNodePlaced() }
`)
	write("internal/networkengine/tree_replay.go", `package networkengine

func replayAll(c *TreeEventConsumer) {
	_ = c.handleRootAdded()
	_ = c.handleNodePlaced()
	_ = c.handleNodeRemoved()
	_ = c.handleEventRejected()
	_ = c.handleOther()
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
	write("internal/networkengine/_ignored.go", caller)
	write("internal/networkengine/.dotfile.go", caller)
	write("docs/copy/go.mod", "module copy\n")
	write("docs/copy/internal/networkengine/n.go", caller)

	scan, err := findHandleEventReferences(root)

	require.NoError(t, err)
	assert.Equal(t, []string{
		"cmd/tool/main.go:3: HandleEvent",
		"internal/networkengine/tree_consumer.go:7: HandleEvent",
		"internal/networkengine/tree_consumer.go:8: HandleEvent",
		"internal/networkengine/tree_replay.go:4: handleRootAdded",
		"internal/networkengine/tree_replay.go:5: handleNodePlaced",
		"internal/networkengine/tree_replay.go:6: handleNodeRemoved",
		"internal/networkengine/tree_replay.go:7: handleEventRejected",
	}, scan.others)
	assert.Equal(t, 1, scan.inWriter)
}

func TestHandleEventGuardFailures_EachRuleFailsOnItsOwn(t *testing.T) {
	t.Run("a handler referenced outside the consumer", func(t *testing.T) {
		scan := handleEventScan{
			others:   []string{"internal/networkengine/tree_replay.go:4: handleNodePlaced"},
			inWriter: handleEventWriterReferences,
		}

		assert.Equal(t, []string{
			"expected HandleEvent only in " + handleEventWriterFile + " and the consumer handlers only in " +
				consumerFile + "; found:\ninternal/networkengine/tree_replay.go:4: handleNodePlaced",
		}, handleEventGuardFailures(scan))
	})
	t.Run("one reference too many in the writer", func(t *testing.T) {
		scan := handleEventScan{inWriter: handleEventWriterReferences + 1}

		assert.Equal(t, []string{
			fmt.Sprintf("expected 2 HandleEvent references in %s; found 3", handleEventWriterFile),
		}, handleEventGuardFailures(scan))
	})
	t.Run("both rules hold", func(t *testing.T) {
		assert.Empty(t, handleEventGuardFailures(handleEventScan{inWriter: handleEventWriterReferences}))
	})
}

func TestNoProductionCodeCallsHandleEventOutsideTheWriter(t *testing.T) {
	scan, err := findHandleEventReferences(moduleRoot(t))

	require.NoError(t, err)
	assert.Empty(t, handleEventGuardFailures(scan))
}
