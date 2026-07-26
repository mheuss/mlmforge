package networkengine

import "context"

// TreeMutator is the command interface for tree lifecycle and mutation
// operations. Defined by the needs of TreeEventConsumer and TreeLoader.
type TreeMutator interface {
	CreateTree(ctx context.Context, structure, treeType string) error
	// CreateMatrixTree creates a matrix tree, which needs width and spillover
	// that CreateTree does not carry. The loader routes matrix trees here.
	CreateMatrixTree(ctx context.Context, structure string, width int, spillover string) error
	AddRoot(ctx context.Context, structure, userID string, enrolledAt int64) error
	AddNode(ctx context.Context, structure, userID, parentID, sponsorID string, enrolledAt int64, opts ...AddNodeOption) error
	// AddNodeAt places a node at an explicit parent and position. Matrix trees
	// need it on reload. Their AddNode re-derives placement by spillover and
	// ignores the stored parent and position.
	AddNodeAt(ctx context.Context, structure, userID, parentID, sponsorID string, position int, enrolledAt int64) error
	RemoveNode(ctx context.Context, structure, userID string) error
}

// Compile-time check: EngineClient must satisfy TreeMutator.
var _ TreeMutator = (*EngineClient)(nil)
