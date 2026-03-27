package networkengine

import (
	"context"
	"time"
)

// TreeNodeRow represents a row in the tree_nodes adjacency table.
type TreeNodeRow struct {
	ID         string
	TreeID     string
	UserID     string
	ParentID   *string
	SponsorID  *string
	Position   *int
	Depth      int
	EnrolledAt time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
	RemovedAt  *time.Time
}

// TreeStore is the repository interface for tree node persistence.
// The adjacency table is a read model projected from events. The Rust
// engine is the runtime authority for topology queries. This interface
// serves reporting, admin tools, and startup bulk-load.
type TreeStore interface {
	// InsertNode adds a node to the adjacency table.
	InsertNode(ctx context.Context, node TreeNodeRow) error

	// DeleteNode soft-deletes a node by setting removed_at.
	DeleteNode(ctx context.Context, treeID, userID string) error

	// GetNode returns a single active node by tree and user ID.
	GetNode(ctx context.Context, treeID, userID string) (*TreeNodeRow, error)

	// GetChildren returns active children of a parent node.
	GetChildren(ctx context.Context, treeID, parentUserID string) ([]TreeNodeRow, error)

	// GetByTree returns all active nodes in a tree.
	GetByTree(ctx context.Context, treeID string) ([]TreeNodeRow, error)

	// GetByTreeDepthOrdered returns all active nodes in a tree ordered
	// by depth ascending. Used for startup bulk-load (parents before children).
	GetByTreeDepthOrdered(ctx context.Context, treeID string) ([]TreeNodeRow, error)

	// BulkInsert adds multiple nodes in a single transaction.
	BulkInsert(ctx context.Context, nodes []TreeNodeRow) error
}
