package networkengine

import "context"

// Snapshot is one stored engine snapshot of a tree, taken at a stream version.
type Snapshot struct {
	TreeID   string
	Version  int64
	TreeType string
	Data     []byte
}

// SnapshotStore keeps the latest snapshot of each tree.
type SnapshotStore interface {
	// Get returns the tree's snapshot. found is false when none is stored.
	Get(ctx context.Context, treeID string) (snapshot Snapshot, found bool, err error)
	// Put stores s, unless the tree's stored snapshot is at s.Version or later.
	Put(ctx context.Context, s Snapshot) error
	// Delete removes the tree's snapshot when it is at version.
	Delete(ctx context.Context, treeID string, version int64) error
}
