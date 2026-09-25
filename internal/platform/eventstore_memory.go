package platform

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// ErrEmptyAppend is returned when Append is called with an empty events slice.
var ErrEmptyAppend = errors.New("eventstore: cannot append zero events")

// ErrInvalidStreamName is returned when a stream name does not match the
// required {category}-{id} format. Use errors.Is to check for this error.
var ErrInvalidStreamName = errors.New("eventstore: invalid stream name")

// ValidateStreamName checks that a stream name follows the {category}-{id}
// convention documented in ADR-016. The category is the part before the first
// hyphen. Both category and id must be non-empty.
func ValidateStreamName(stream string) error {
	if stream == "" {
		return fmt.Errorf("%w: must not be empty", ErrInvalidStreamName)
	}
	cat, id, ok := strings.Cut(stream, "-")
	if !ok {
		return fmt.Errorf("%w: %q missing category-id separator '-'", ErrInvalidStreamName, stream)
	}
	if cat == "" {
		return fmt.Errorf("%w: %q has empty category", ErrInvalidStreamName, stream)
	}
	if id == "" {
		return fmt.Errorf("%w: %q has empty id", ErrInvalidStreamName, stream)
	}
	return nil
}

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
	ids     map[string]struct{}
}

// NewMemoryEventStore creates an empty in-memory event store.
func NewMemoryEventStore() *MemoryEventStore {
	return &MemoryEventStore{
		streams: make(map[string][]Event),
		ids:     make(map[string]struct{}),
	}
}

// Append writes events to a stream atomically with optimistic concurrency.
func (m *MemoryEventStore) Append(_ context.Context, stream string, expectedVersion int64, events []NewEvent) error {
	if err := ValidateStreamName(stream); err != nil {
		return err
	}
	if len(events) == 0 {
		return ErrEmptyAppend
	}
	ids := make([]string, len(events))
	for i, e := range events {
		if err := ValidateNewEvent(e, i); err != nil {
			return err
		}
		id, ok := canonicalEventID(e.ID)
		if !ok {
			return fmt.Errorf("eventstore: event at index %d has ID %q, which does not parse as a UUID", i, e.ID)
		}
		ids[i] = id
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

	batch := make(map[string]int, len(events))
	for i, ne := range events {
		if _, ok := m.ids[ids[i]]; ok {
			return fmt.Errorf("eventstore: event at index %d has ID %q, which the store already holds", i, ne.ID)
		}
		if first, ok := batch[ids[i]]; ok {
			return fmt.Errorf("eventstore: event at index %d has ID %q, which the event at index %d also has", i, ne.ID, first)
		}
		batch[ids[i]] = i
	}

	now := time.Now()
	for i, ne := range events {
		version := current + int64(i) + 1
		evt := Event{
			ID:             ids[i],
			Stream:         stream,
			Type:           ne.Type,
			Version:        version,
			GlobalPosition: int64(len(m.global)) + 1,
			Payload:        bytes.Clone(ne.Payload),
			Metadata:       bytes.Clone(ne.Metadata),
			Timestamp:      now,
		}
		m.streams[stream] = append(m.streams[stream], evt)
		m.global = append(m.global, evt)
		m.ids[ids[i]] = struct{}{}
	}

	return nil
}

// ReadStream returns events from a single stream starting at fromVersion.
// Pass limit=0 to read all matching events.
func (m *MemoryEventStore) ReadStream(_ context.Context, stream string, fromVersion int64, limit int64) ([]Event, error) {
	if err := ValidateStreamName(stream); err != nil {
		return nil, err
	}

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
	cloneEventBytes(result)
	return result, nil
}

// ReadLastEvent returns the stream's last event, or nil for an empty stream.
func (m *MemoryEventStore) ReadLastEvent(_ context.Context, stream string) (*Event, error) {
	if err := ValidateStreamName(stream); err != nil {
		return nil, err
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	events := m.streams[stream]
	if len(events) == 0 {
		return nil, nil
	}
	last := events[len(events)-1]
	last.Payload = bytes.Clone(last.Payload)
	last.Metadata = bytes.Clone(last.Metadata)
	return &last, nil
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
	cloneEventBytes(result)
	return result, nil
}

// cloneEventBytes gives each event its own copy of Payload and Metadata.
func cloneEventBytes(events []Event) {
	for i := range events {
		events[i].Payload = bytes.Clone(events[i].Payload)
		events[i].Metadata = bytes.Clone(events[i].Metadata)
	}
}

// categoryOf extracts the category from a stream name. The category is the
// part before the first hyphen, matching PostgreSQL's split_part(stream, '-', 1).
func categoryOf(stream string) string {
	cat, _, _ := strings.Cut(stream, "-")
	return cat
}

// canonicalEventID returns the lowercase hyphenated form of an event ID, or
// false when the ID is not a UUID.
func canonicalEventID(id string) (string, bool) {
	rest, braced := strings.CutPrefix(id, "{")
	var u uuid.UUID
	for i := range u {
		if len(rest) < 2 {
			return "", false
		}
		if _, err := hex.Decode(u[i:i+1], []byte(rest[:2])); err != nil {
			return "", false
		}
		rest = rest[2:]
		if i%2 == 1 && i < len(u)-1 {
			rest, _ = strings.CutPrefix(rest, "-")
		}
	}
	if braced {
		var closed bool
		if rest, closed = strings.CutPrefix(rest, "}"); !closed {
			return "", false
		}
	}
	return u.String(), rest == ""
}
