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
	// stored reports whether it did.
	Put(ctx context.Context, s Snapshot) (stored bool, err error)
	// Delete removes the tree's snapshot when it is at version.
	Delete(ctx context.Context, treeID string, version int64) error
	// LastAttempt returns the tree's recorded attempt. found is false when none is recorded.
	LastAttempt(ctx context.Context, treeID string) (attempt SnapshotAttempt, found bool, err error)
	// RecordAttempt records a, unless the tree's recorded attempt is at a higher
	// version. A failed attempt replaces a stored one at any version.
	RecordAttempt(ctx context.Context, a SnapshotAttempt) error
}

// SnapshotAttempt is the recorded outcome of a tree's snapshot attempt.
type SnapshotAttempt struct {
	TreeID  string
	Version int64
	Stored  bool
	// Observed is empty exactly when Stored is true.
	Observed string
}
