package platform

import (
	"context"
	"strings"
	"sync"
	"time"
)

// Compile-time check: MemoryEventStore implements EventStore.
var _ EventStore = (*MemoryEventStore)(nil)

// MemoryEventStore is an in-memory EventStore for testing. Not persistent.
type MemoryEventStore struct {
	mu      sync.RWMutex
	streams map[string][]Event
	global  []Event
}

// NewMemoryEventStore creates an empty in-memory event store.
func NewMemoryEventStore() *MemoryEventStore {
	return &MemoryEventStore{
		streams: make(map[string][]Event),
	}
}

// Append writes events to a stream atomically with optimistic concurrency.
func (m *MemoryEventStore) Append(_ context.Context, stream string, expectedVersion int64, events []NewEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	current := int64(len(m.streams[stream]))

	if expectedVersion >= 0 && expectedVersion != current {
		return &ConcurrencyError{
			Stream:          stream,
			ExpectedVersion: expectedVersion,
			ActualVersion:   current,
		}
	}

	now := time.Now()
	for i, ne := range events {
		version := current + int64(i) + 1
		evt := Event{
			ID:             ne.ID,
			Stream:         stream,
			Type:           ne.Type,
			Version:        version,
			GlobalPosition: int64(len(m.global)) + 1,
			Payload:        ne.Payload,
			Metadata:       ne.Metadata,
			Timestamp:      now,
		}
		m.streams[stream] = append(m.streams[stream], evt)
		m.global = append(m.global, evt)
	}

	return nil
}

// ReadStream returns events from a single stream starting at fromVersion.
func (m *MemoryEventStore) ReadStream(_ context.Context, stream string, fromVersion int64) ([]Event, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	events := m.streams[stream]
	var result []Event
	for _, e := range events {
		if e.Version >= fromVersion {
			result = append(result, e)
		}
	}
	return result, nil
}

// ReadCategory returns events across all streams matching a category prefix.
// Category is the part before the first hyphen, matching PostgreSQL's
// split_part(stream, '-', 1).
func (m *MemoryEventStore) ReadCategory(_ context.Context, category string, afterPosition int64) ([]Event, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []Event
	for _, e := range m.global {
		if e.GlobalPosition > afterPosition && categoryOf(e.Stream) == category {
			result = append(result, e)
		}
	}
	return result, nil
}

// categoryOf extracts the category from a stream name. The category is the
// part before the first hyphen, matching PostgreSQL's split_part(stream, '-', 1).
func categoryOf(stream string) string {
	cat, _, _ := strings.Cut(stream, "-")
	return cat
}
