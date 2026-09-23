package networkengine

import (
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/mlmforge/mlmforge/internal/platform"
)

// canonicalID parses one identifier the writer was given.
func canonicalID(field, value string) (uuid.UUID, error) {
	id, err := uuid.Parse(value)
	if err != nil {
		return uuid.UUID{}, fmt.Errorf("%s %q is not a UUID: %w", field, value, err)
	}
	return id, nil
}

// treeShape is a tree's structure: its type, and for a matrix its width and
// spillover.
type treeShape struct {
	treeType  string
	width     int
	spillover string
}

// config returns the shape in the loader's configuration form.
func (s treeShape) config() loadTreeConfig {
	if s.treeType != treeTypeMatrix {
		return loadTreeConfig{}
	}
	return loadTreeConfig{matrixParamsSet: true, matrixWidth: s.width, matrixSpillover: s.spillover}
}

// readTreeShape reads the shape recorded by a stream's version 1, and refuses
// one that is not a complete root_added.
func readTreeShape(stream string, first platform.Event) (treeShape, error) {
	if first.Type != EventTypeRootAdded {
		return treeShape{}, fmt.Errorf("stream %s holds %q at version 1, not %s", stream, first.Type, EventTypeRootAdded)
	}
	var p RootAddedPayload
	if err := json.Unmarshal(first.Payload, &p); err != nil {
		return treeShape{}, fmt.Errorf("unmarshal root_added at version 1 in stream %s: %w", stream, err)
	}
	if TreeStreamName(p.TreeID) != stream {
		return treeShape{}, fmt.Errorf("root_added at version 1 in stream %s names tree %q", stream, p.TreeID)
	}
	if p.TreeType == "" {
		return treeShape{}, fmt.Errorf("root_added at version 1 in stream %s has no tree_type", stream)
	}
	if !supportedTreeTypes[p.TreeType] {
		return treeShape{}, fmt.Errorf("root_added at version 1 in stream %s has unsupported tree_type %q", stream, p.TreeType)
	}
	if p.TreeType != treeTypeMatrix && (p.MatrixWidth != nil || p.MatrixSpillover != nil) {
		return treeShape{}, fmt.Errorf("%s root_added at version 1 in stream %s carries a matrix width or spillover, which only a matrix root carries",
			p.TreeType, stream)
	}
	shape := treeShape{treeType: p.TreeType}
	if p.TreeType == treeTypeMatrix {
		if p.MatrixWidth == nil {
			return treeShape{}, fmt.Errorf("matrix root_added at version 1 in stream %s has no matrix_width", stream)
		}
		if p.MatrixSpillover == nil {
			return treeShape{}, fmt.Errorf("matrix root_added at version 1 in stream %s has no matrix_spillover", stream)
		}
		shape.width, shape.spillover = *p.MatrixWidth, *p.MatrixSpillover
	}
	if err := validateTreeConfig(p.TreeID, shape.treeType, shape.config()); err != nil {
		return treeShape{}, fmt.Errorf("root_added at version 1 in stream %s: %w", stream, err)
	}
	return shape, nil
}

// shapeFromRequest builds the shape an AddRoot request names, for a stream
// that has no version 1 yet.
func shapeFromRequest(tree, treeType string, width *int, spillover *string) (treeShape, error) {
	if treeType != treeTypeMatrix && (width != nil || spillover != nil) {
		return treeShape{}, fmt.Errorf(
			"add root to tree %s: matrix width and spillover apply only to matrix trees, and the request names %q",
			tree, treeType)
	}
	shape := treeShape{treeType: treeType}
	if treeType == treeTypeMatrix {
		if width == nil || spillover == nil {
			return treeShape{}, fmt.Errorf("add root to tree %s: a matrix tree needs a width and a spillover", tree)
		}
		shape.width, shape.spillover = *width, *spillover
	}
	if err := validateTreeConfig(tree, shape.treeType, shape.config()); err != nil {
		return treeShape{}, err
	}
	return shape, nil
}
