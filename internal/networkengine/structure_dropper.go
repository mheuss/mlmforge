package networkengine

import "context"

// StructureDropper removes a named structure from the engine.
type StructureDropper interface {
	DropStructure(ctx context.Context, structure string) (bool, error)
}

var _ StructureDropper = (*EngineClient)(nil)
