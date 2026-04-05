package networkengine

import "context"

// TreeMutator is the command interface for tree lifecycle and mutation
// operations. Defined by the needs of TreeEventConsumer and TreeLoader.
type TreeMutator interface {
	CreateTree(ctx context.Context, structure, treeType string) error
	AddRoot(ctx context.Context, structure, userID string, enrolledAt int64) error
	AddNode(ctx context.Context, structure, userID, parentID, sponsorID string, enrolledAt int64, opts ...AddNodeOption) error
	RemoveNode(ctx context.Context, structure, userID string) error
}

// Compile-time check: EngineClient must satisfy TreeMutator.
var _ TreeMutator = (*EngineClient)(nil)
