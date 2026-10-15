package networkengine

import (
	"bytes"
	"context"
	"fmt"
	"sync"
)

// Compile-time check.
var _ SnapshotStore = (*MemorySnapshotStore)(nil)

// MemorySnapshotStore is an in-memory SnapshotStore for testing.
type MemorySnapshotStore struct {
	mu        sync.Mutex
	snapshots map[string]Snapshot
}

func NewMemorySnapshotStore() *MemorySnapshotStore {
	return &MemorySnapshotStore{snapshots: map[string]Snapshot{}}
}

func (s *MemorySnapshotStore) Get(ctx context.Context, treeID string) (Snapshot, bool, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
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
		return fmt.Errorf("snapshot of tree %s has tree type %q, not a supported tree type", snap.TreeID, snap.TreeType)
	}
	if snap.Data == nil {
		return fmt.Errorf("snapshot of tree %s at version %d has nil data", snap.TreeID, snap.Version)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur, ok := s.snapshots[treeID]; ok && cur.Version == version {
		delete(s.snapshots, treeID)
	}
	return nil
}
