package platform

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ErrEmptyAppend is returned when Append is called with an empty events slice.
var ErrEmptyAppend = errors.New("eventstore: cannot append zero events")

// ErrEmptyStreamName is returned when Append is called with an empty stream name.
var ErrEmptyStreamName = errors.New("eventstore: stream name must not be empty")

// ValidateNewEvent checks that required fields on a NewEvent are populated.
// Returns an error describing the first missing field.
func ValidateNewEvent(e NewEvent, index int) error {
	if e.ID == "" {
		return fmt.Errorf("eventstore: event at index %d has empty ID", index)
	}
	if e.Type == "" {
		return fmt.Errorf("eventstore: event at index %d has empty Type", index)
	}
	if e.Payload == nil {
		return fmt.Errorf("eventstore: event at index %d has nil Payload", index)
	}
	return nil
}

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
	if stream == "" {
		return ErrEmptyStreamName
	}
	if len(events) == 0 {
		return ErrEmptyAppend
	}
	for i, e := range events {
		if err := ValidateNewEvent(e, i); err != nil {
			return err
		}
	}

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
// Pass limit=0 to read all matching events.
func (m *MemoryEventStore) ReadStream(_ context.Context, stream string, fromVersion int64, limit int64) ([]Event, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	events := m.streams[stream]
	if fromVersion < 1 {
		fromVersion = 1
	}
	startIdx := int(fromVersion - 1)
	if startIdx >= len(events) {
		return nil, nil
	}
	result := make([]Event, len(events)-startIdx)
	copy(result, events[startIdx:])
	if limit > 0 && int64(len(result)) > limit {
		result = result[:limit]
	}
	return result, nil
}

// ReadCategory returns events across all streams matching a category prefix.
// Category is the part before the first hyphen, matching PostgreSQL's
// split_part(stream, '-', 1). Pass limit=0 to read all matching events.
func (m *MemoryEventStore) ReadCategory(_ context.Context, category string, afterPosition int64, limit int64) ([]Event, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []Event
	for _, e := range m.global {
		if e.GlobalPosition > afterPosition && categoryOf(e.Stream) == category {
			result = append(result, e)
		}
	}
	if limit > 0 && int64(len(result)) > limit {
		result = result[:limit]
	}
	return result, nil
}

// categoryOf extracts the category from a stream name. The category is the
// part before the first hyphen, matching PostgreSQL's split_part(stream, '-', 1).
func categoryOf(stream string) string {
	cat, _, _ := strings.Cut(stream, "-")
	return cat
}
