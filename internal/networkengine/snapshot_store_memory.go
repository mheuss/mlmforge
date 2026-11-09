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
	attempts  map[string]SnapshotAttempt
}

func NewMemorySnapshotStore() *MemorySnapshotStore {
	return &MemorySnapshotStore{snapshots: map[string]Snapshot{}, attempts: map[string]SnapshotAttempt{}}
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

func (s *MemorySnapshotStore) Put(ctx context.Context, snap Snapshot) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if snap.Version < 1 {
		return false, fmt.Errorf("snapshot of tree %s has version %d, below 1", snap.TreeID, snap.Version)
	}
	if !supportedTreeTypes[snap.TreeType] {
		return false, fmt.Errorf("snapshot of tree %s has tree type %q, not a supported tree type", snap.TreeID, snap.TreeType)
	}
	if snap.Data == nil {
		return false, fmt.Errorf("snapshot of tree %s at version %d has nil data", snap.TreeID, snap.Version)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur, ok := s.snapshots[snap.TreeID]; ok && cur.Version >= snap.Version {
		return false, nil
	}
	snap.Data = bytes.Clone(snap.Data)
	s.snapshots[snap.TreeID] = snap
	return true, nil
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

func (s *MemorySnapshotStore) LastAttempt(ctx context.Context, treeID string) (SnapshotAttempt, bool, error) {
	if err := ctx.Err(); err != nil {
		return SnapshotAttempt{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.attempts[treeID]
	return a, ok, nil
}

func (s *MemorySnapshotStore) RecordAttempt(ctx context.Context, a SnapshotAttempt) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.Version < 1 {
		return fmt.Errorf("snapshot attempt of tree %s has version %d, below 1", a.TreeID, a.Version)
	}
	if a.Stored != (a.Observed == "") {
		return fmt.Errorf("snapshot attempt of tree %s at version %d has stored %t and observed %q",
			a.TreeID, a.Version, a.Stored, a.Observed)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur, ok := s.attempts[a.TreeID]; ok && a.Version < cur.Version {
		return nil
	}
	s.attempts[a.TreeID] = a
	return nil
}
