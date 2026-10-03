package networkengine

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// TreeNodeRow represents a row in the tree_nodes adjacency table.
//
// CreatedAt and UpdatedAt are owned by the store.
type TreeNodeRow struct {
	ID               string
	TreeID           string
	UserID           string
	ParentID         *string
	SponsorID        *string
	Position         *int
	Depth            int
	EnrolledAt       time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
	RemovedAt        *time.Time
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

	// DeleteNodeAndResponsor soft-deletes a node, stamps the removing event
	// onto the tombstoned row, and repoints the recruits the engine moved, in
	// one transaction.
	//
	// Both writes or neither. A soft delete that lands without the sponsor
	// updates leaves the store naming a user the active-row query will not
	// return, and the tree stops reloading.
	DeleteNodeAndResponsor(ctx context.Context, treeID, userID, removalEventID string, moved []Responsored) error

	// GetNode returns a single active node by tree and user ID.
	GetNode(ctx context.Context, treeID, userID string) (*TreeNodeRow, error)

	// GetNodeIncludingRemoved returns a node by tree and user ID whether or
	// not it is soft-deleted. An active row wins over any tombstone. Among
	// tombstones the latest removed_at wins, and on equal removed_at the
	// highest id wins. The id order is defined only for canonical hyphenated
	// UUIDs.
	GetNodeIncludingRemoved(ctx context.Context, treeID, userID string) (*TreeNodeRow, error)

	// GetNodeByRemovalEvent returns the row that the given removal event
	// tombstoned, or nil when that event has stamped no row in this tree.
	// When the event stamped more than one row, the latest removed_at wins,
	// and on equal removed_at the highest id wins. The id order is defined only
	// for canonical hyphenated UUIDs.
	GetNodeByRemovalEvent(ctx context.Context, treeID, removalEventID string) (*TreeNodeRow, error)

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

	// BulkInsert adds multiple nodes in a single transaction, and records no
	// projected version.
	BulkInsert(ctx context.Context, nodes []TreeNodeRow) error

	// ProjectedVersion returns the stream version the tree's rows reflect.
	// found is false when the tree has no projection row.
	ProjectedVersion(ctx context.Context, treeID string) (version int64, found bool, err error)

	// ProjectInsert inserts the row that the event at eventVersion projects,
	// and records eventVersion as the tree's projected version.
	ProjectInsert(ctx context.Context, node TreeNodeRow, eventVersion int64) error

	// ProjectRemoval soft-deletes the user's row and repoints the moved
	// recruits for the removal event at eventVersion, and records eventVersion
	// as the tree's projected version.
	ProjectRemoval(ctx context.Context, treeID, userID, removalEventID string, eventVersion int64, moved []Responsored) error

	// UndoRootProjection soft-deletes the root row that the event at
	// eventVersion inserted, and returns the tree's projected version to
	// eventVersion - 1.
	UndoRootProjection(ctx context.Context, treeID, userID string, eventVersion int64) error

	// ProjectRejection soft-deletes the active rows the rejected event
	// inserted, and records eventVersion as the tree's projected version.
	ProjectRejection(ctx context.Context, treeID, rejectedEventID string, eventVersion int64) error
}

// ProjectionRefusedError reports a projection whose event version was below
// the tree's projected version.
type ProjectionRefusedError struct {
	TreeID           string
	EventVersion     int64
	ProjectedVersion int64
}

func (e *ProjectionRefusedError) Error() string {
	return fmt.Sprintf("tree %s has projected version %d; the event at version %d was not projected",
		e.TreeID, e.ProjectedVersion, e.EventVersion)
}

// checkEventVersion refuses an event version below 1.
func checkEventVersion(treeID string, eventVersion int64) error {
	if eventVersion < 1 {
		return fmt.Errorf("tree %s was given event version %d, below 1; nothing was written", treeID, eventVersion)
	}
	return nil
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

	// ErrUnprojectableEvent reports an event refused for what it carries,
	// before any engine call.
	ErrUnprojectableEvent = errors.New("the event cannot be projected as written")
)

// unprojectableEventError marks err as ErrUnprojectableEvent and keeps err's
// text.
type unprojectableEventError struct{ err error }

func (e *unprojectableEventError) Error() string        { return e.err.Error() }
func (e *unprojectableEventError) Unwrap() error        { return e.err }
func (e *unprojectableEventError) Is(target error) bool { return target == ErrUnprojectableEvent }

// unprojectable marks err as ErrUnprojectableEvent.
func unprojectable(err error) error { return &unprojectableEventError{err: err} }

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
