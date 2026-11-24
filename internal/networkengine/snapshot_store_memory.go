package networkengine

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/mlmforge/mlmforge/internal/platform"
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
	key, err := snapshotTreeKey("get", treeID)
	if err != nil {
		return Snapshot{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	snap, ok := s.snapshots[key]
	if !ok {
		return Snapshot{}, false, nil
	}
	snap.TreeID = treeID
	snap.Data = bytes.Clone(snap.Data)
	return snap, true, nil
}

func (s *MemorySnapshotStore) Put(ctx context.Context, snap Snapshot) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	key, err := snapshotTreeKey("put", snap.TreeID)
	if err != nil {
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
	if cur, ok := s.snapshots[key]; ok && cur.Version >= snap.Version {
		return false, nil
	}
	snap.Data = bytes.Clone(snap.Data)
	s.snapshots[key] = snap
	return true, nil
}

func (s *MemorySnapshotStore) Delete(ctx context.Context, treeID string, version int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := snapshotTreeKey("delete", treeID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur, ok := s.snapshots[key]; ok && cur.Version == version {
		delete(s.snapshots, key)
	}
	return nil
}

func (s *MemorySnapshotStore) LastAttempt(ctx context.Context, treeID string) (SnapshotAttempt, bool, error) {
	if err := ctx.Err(); err != nil {
		return SnapshotAttempt{}, false, err
	}
	key, err := snapshotTreeKey("last attempt", treeID)
	if err != nil {
		return SnapshotAttempt{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.attempts[key]
	if !ok {
		return SnapshotAttempt{}, false, nil
	}
	a.TreeID = treeID
	return a, true, nil
}

func (s *MemorySnapshotStore) RecordAttempt(ctx context.Context, a SnapshotAttempt) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := snapshotTreeKey("record attempt", a.TreeID)
	if err != nil {
		return err
	}
	if a.Version < 1 {
		return fmt.Errorf("snapshot attempt of tree %s has version %d, below 1", a.TreeID, a.Version)
	}
	if a.Stored != (a.Observed == "") {
		return fmt.Errorf("snapshot attempt of tree %s at version %d has stored %t and observed %q",
			a.TreeID, a.Version, a.Stored, a.Observed)
	}
	if !utf8.ValidString(a.Observed) || strings.ContainsRune(a.Observed, 0) {
		return fmt.Errorf("snapshot attempt of tree %s at version %d has observed %q, which is not valid UTF-8 or holds a NUL byte",
			a.TreeID, a.Version, a.Observed)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur, ok := s.attempts[key]; ok && a.Version < cur.Version && (!cur.Stored || a.Stored) {
		return nil
	}
	s.attempts[key] = a
	return nil
}

// snapshotTreeKey returns treeID in canonical form, or an error naming op when it is not a UUID.
func snapshotTreeKey(op, treeID string) (string, error) {
	key, ok := platform.CanonicalUUID(treeID)
	if !ok {
		return "", fmt.Errorf("snapshot %s: tree id %q is not a UUID", op, treeID)
	}
	return key, nil
}
