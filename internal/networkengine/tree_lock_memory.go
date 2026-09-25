package networkengine

import (
	"context"
	"fmt"
	"sync"

	"github.com/google/uuid"
)

// MemoryTreeLocker grants exclusive use of one tree within one process.
type MemoryTreeLocker struct {
	mu    sync.Mutex
	slots map[uuid.UUID]chan struct{}
}

// NewMemoryTreeLocker creates a locker with no tree held.
func NewMemoryTreeLocker() *MemoryTreeLocker {
	return &MemoryTreeLocker{slots: make(map[uuid.UUID]chan struct{})}
}

// Lock waits until the tree is free or ctx ends.
func (l *MemoryTreeLocker) Lock(ctx context.Context, treeID uuid.UUID) (func() error, error) {
	// A select with a free slot and an ended context picks either case at
	// random, so without this check an ended context could take the lock.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	l.mu.Lock()
	slot, ok := l.slots[treeID]
	if !ok {
		slot = make(chan struct{}, 1)
		l.slots[treeID] = slot
	}
	l.mu.Unlock()

	select {
	case slot <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	var once sync.Once
	return func() error {
		released := false
		once.Do(func() {
			<-slot
			released = true
		})
		if !released {
			return fmt.Errorf("unlock of tree %s was called after the lock was released", treeID)
		}
		return nil
	}, nil
}
