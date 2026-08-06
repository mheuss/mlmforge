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

	// GetByTreeDepthOrdered returns all active nodes in a tree ordered by
	// depth ascending, then enrolled_at. Used for startup bulk-load by
	// TreeLoader.LoadTree, its only caller.
	//
	// Replay does not depend on this order. orderForReplay sorts by
	// (Depth, EnrolledAt, UserID), a total order over a set whose duplicate
	// user IDs validateNodes already rejected. So both the replay sequence and
	// its "%d of %d" progress counter are fixed by the node set alone.
	// TestOrderForReplay_IsIndependentOfInputOrder pins that. GetByTree returns
	// the same set unordered and would replay identically.
	//
	// What the sort buys is deterministic preflight errors, which is a
	// different thing from replay order. validateNodes runs before
	// orderForReplay and returns on the first fault it meets. On data with
	// several faults the message therefore depends on row order: which pair
	// "more than one depth-0 root (%s and %s)" names, and which of several
	// dangling nodes gets reported. A stable input makes a failing startup load
	// fail the same way every time, which is what an operator needs to act on.
	//
	// idx_tree_nodes_depth(tree_id, depth) backs the leading key, so the cost
	// is a sort within each depth group.
	GetByTreeDepthOrdered(ctx context.Context, treeID string) ([]TreeNodeRow, error)

	// BulkInsert adds multiple nodes in a single transaction.
	BulkInsert(ctx context.Context, nodes []TreeNodeRow) error
}
