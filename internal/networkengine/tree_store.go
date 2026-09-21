package networkengine

import (
	"context"
	"errors"
	"fmt"
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
	// Written by the soft delete. Null while the row is active.
	RemovedByEventID *string
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

	// DeleteNodeAndResponsor soft-deletes a node and repoints the recruits
	// the engine moved, in one transaction.
	//
	// Both writes or neither. A soft delete that lands without the sponsor
	// updates leaves the store naming a user the active-row query will not
	// return, and the tree stops reloading.
	DeleteNodeAndResponsor(ctx context.Context, treeID, userID, removalEventID string, moved []Responsored) error

	// GetNode returns a single active node by tree and user ID.
	GetNode(ctx context.Context, treeID, userID string) (*TreeNodeRow, error)

	// GetNodeIncludingRemoved returns a node by tree and user ID whether or
	// not it is soft-deleted. An active row wins over any tombstone, and the
	// newest tombstone wins over older ones.
	GetNodeIncludingRemoved(ctx context.Context, treeID, userID string) (*TreeNodeRow, error)

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
	// What the sort buys is preflight errors that are stable for rows differing
	// on enrollment time, which is a different thing from replay order.
	// validateNodes runs before orderForReplay and returns on the first fault it
	// meets. On data with several faults the message therefore depends on row
	// order: which pair "more than one depth-0 root (%s and %s)" names, and
	// which of several dangling nodes gets reported. A stable input makes a
	// failing startup load fail the same way every time, which is what an
	// operator needs to act on.
	//
	// Rows tying on depth and enrollment time have no defined order. The message
	// names whichever the sort happened to put first.
	//
	// idx_tree_nodes_depth(tree_id, depth) backs the leading key, so the cost
	// is a sort within each depth group.
	GetByTreeDepthOrdered(ctx context.Context, treeID string) ([]TreeNodeRow, error)

	// BulkInsert adds multiple nodes in a single transaction.
	BulkInsert(ctx context.Context, nodes []TreeNodeRow) error
}

// The conditions an insert can be refused for, and one the consumer raises
// after reading the row an insert reported. Sentinels rather than structs
// because the caller branches on which one and needs nothing else from them.
var (
	// ErrNodeAlreadyProjected reports an insert that matched an existing row
	// on the event id. Whether that row is the one this event wrote, and
	// whether it is still active, are not known here.
	ErrNodeAlreadyProjected = errors.New("insert affected no rows; a row with this event id exists")

	// ErrActiveUserConflict reports a different event already placing this
	// user in this tree.
	ErrActiveUserConflict = errors.New("an active row already places this user in this tree")

	// ErrSlotConflict reports a different event already holding this parent
	// and position.
	ErrSlotConflict = errors.New("an active row already holds this parent and position")

	// ErrRootConflict reports an active depth-0 row already rooting this tree.
	ErrRootConflict = errors.New("an active row already roots this tree")

	// ErrReplayedPlacement reports an insert matching a row that has since
	// been soft-deleted. The placement is not current and must not be resumed.
	ErrReplayedPlacement = errors.New("a row with this event id exists and is soft-deleted")
)

// RemovalNotProjectedError reports a removal the engine applied whose store
// write did not land.
type RemovalNotProjectedError struct {
	TreeID  string
	UserID  string
	EventID string
}

func (e *RemovalNotProjectedError) Error() string {
	return fmt.Sprintf(
		"engine reports %s absent from tree %s, and an active row remains; event %s",
		e.UserID, e.TreeID, e.EventID)
}
