package networkengine

import (
	"bytes"
	"context"
	"fmt"
)

// Compile-time check.
var _ SnapshotStore = (*MemorySnapshotStore)(nil)

// MemorySnapshotStore is an in-memory SnapshotStore for testing.
type MemorySnapshotStore struct {
	snapshots map[string]Snapshot
}

func NewMemorySnapshotStore() *MemorySnapshotStore {
	return &MemorySnapshotStore{snapshots: map[string]Snapshot{}}
}

func (s *MemorySnapshotStore) Get(ctx context.Context, treeID string) (Snapshot, bool, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, false, err
	}
	snap, ok := s.snapshots[treeID]
	if !ok {
		return Snapshot{}, false, nil
	}
	snap.Data = bytes.Clone(snap.Data)
	return snap, true, nil
}

func (s *MemorySnapshotStore) Put(ctx context.Context, snap Snapshot) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if snap.Version < 1 {
		return fmt.Errorf("snapshot of tree %s has version %d, below 1", snap.TreeID, snap.Version)
	}
	if !supportedTreeTypes[snap.TreeType] {
		return fmt.Errorf("snapshot of tree %s has tree type %q, which tree_snapshots does not accept", snap.TreeID, snap.TreeType)
	}
	if cur, ok := s.snapshots[snap.TreeID]; ok && cur.Version >= snap.Version {
		return nil
	}
	snap.Data = bytes.Clone(snap.Data)
	s.snapshots[snap.TreeID] = snap
	return nil
}

func (s *MemorySnapshotStore) Delete(ctx context.Context, treeID string, version int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if cur, ok := s.snapshots[treeID]; ok && cur.Version == version {
		delete(s.snapshots, treeID)
	}
	return nil
}
