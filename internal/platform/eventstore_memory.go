package platform

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unicode/utf8"
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
	if problem := textProblem(stream); problem != "" {
		return fmt.Errorf("%w: %q %s", ErrInvalidStreamName, stream, problem)
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

// validateCategory checks that a category is storable text.
func validateCategory(category string) error {
	if problem := textProblem(category); problem != "" {
		return fmt.Errorf("eventstore: category %q %s", category, problem)
	}
	return nil
}

// ValidateNewEvent checks that a NewEvent's fields are present and storable.
// Returns an error describing the first field that fails.
func ValidateNewEvent(e NewEvent, index int) error {
	if e.ID == "" {
		return fmt.Errorf("eventstore: event at index %d has empty ID", index)
	}
	if e.Type == "" {
		return fmt.Errorf("eventstore: event at index %d has empty Type", index)
	}
	if problem := textProblem(e.Type); problem != "" {
		return fmt.Errorf("eventstore: event at index %d has Type %q, which %s", index, e.Type, problem)
	}
	if e.Payload == nil {
		return fmt.Errorf("eventstore: event at index %d has nil Payload", index)
	}
	if problem := jsonbProblem(e.Payload); problem != "" {
		return fmt.Errorf("eventstore: event at index %d has Payload that %s", index, problem)
	}
	if e.Metadata != nil {
		if problem := jsonbProblem(e.Metadata); problem != "" {
			return fmt.Errorf("eventstore: event at index %d has Metadata that %s", index, problem)
		}
	}
	return nil
}

// textProblem describes why s is not storable text, or returns "".
func textProblem(s string) string {
	if !utf8.ValidString(s) {
		return "is not valid UTF-8"
	}
	if strings.ContainsRune(s, 0) {
		return "holds a NUL byte"
	}
	return ""
}

// jsonbProblem describes why raw is not storable as jsonb, or returns "".
func jsonbProblem(raw []byte) string {
	if len(raw) == 0 {
		return "is empty"
	}
	if !utf8.Valid(raw) {
		return fmt.Sprintf("is not valid UTF-8 at byte %d", firstInvalidUTF8(raw))
	}
	if !json.Valid(raw) {
		var v json.RawMessage
		var se *json.SyntaxError
		if err := json.Unmarshal(raw, &v); errors.As(err, &se) {
			return fmt.Sprintf("encoding/json refused: %s (offset %d)", se, se.Offset)
		}
		return "encoding/json refused"
	}
	// Valid JSON holds a backslash only inside a string, where it starts an
	// escape. Skipping each two-byte escape whole keeps an escaped backslash
	// followed by u0000 from reading as a NUL escape.
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			continue
		}
		r, ok := unicodeEscapeAt(raw, i)
		if !ok {
			i++
			continue
		}
		switch {
		case r == 0:
			return fmt.Sprintf(`holds a \u0000 escape at byte %d`, i)
		case utf16.IsSurrogate(r):
			lo, ok := unicodeEscapeAt(raw, i+6)
			if !ok || utf16.DecodeRune(r, lo) == utf8.RuneError {
				return fmt.Sprintf("holds an unpaired surrogate escape at byte %d", i)
			}
			i += 6
		}
		i += 5
	}
	return ""
}

// firstInvalidUTF8 returns the offset of the first byte that starts no valid UTF-8 sequence.
func firstInvalidUTF8(b []byte) int {
	for i := 0; i < len(b); {
		r, size := utf8.DecodeRune(b[i:])
		if r == utf8.RuneError && size == 1 {
			return i
		}
		i += size
	}
	return len(b)
}

// unicodeEscapeAt reads the six-byte unicode escape that starts at raw[i].
func unicodeEscapeAt(raw []byte, i int) (rune, bool) {
	if i+6 > len(raw) || raw[i] != '\\' || raw[i+1] != 'u' {
		return 0, false
	}
	var b [2]byte
	if _, err := hex.Decode(b[:], raw[i+2:i+6]); err != nil {
		return 0, false
	}
	return rune(b[0])<<8 | rune(b[1]), true
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
func (m *MemoryEventStore) Append(ctx context.Context, stream string, expectedVersion int64, events []NewEvent) error {
	if err := ValidateStreamName(stream); err != nil {
		return err
	}
	if len(events) == 0 {
		return ErrEmptyAppend
	}
	for i, e := range events {
		if err := ValidateNewEvent(e, i); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
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

	ids := make([]string, len(events))
	batch := make(map[string]int, len(events))
	for i, ne := range events {
		id, ok := CanonicalUUID(ne.ID)
		if !ok {
			return fmt.Errorf("eventstore: event at index %d has ID %q, which does not parse as a UUID", i, ne.ID)
		}
		if _, ok := m.ids[id]; ok {
			return fmt.Errorf("eventstore: event at index %d has ID %q, read as %s, which the store already holds", i, ne.ID, id)
		}
		if first, ok := batch[id]; ok {
			return fmt.Errorf("eventstore: event at index %d has ID %q, read as %s, which event %d also reads as", i, ne.ID, id, first)
		}
		batch[id] = i
		ids[i] = id
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
func (m *MemoryEventStore) ReadStream(ctx context.Context, stream string, fromVersion int64, limit int64) ([]Event, error) {
	if err := ValidateStreamName(stream); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
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
func (m *MemoryEventStore) ReadLastEvent(ctx context.Context, stream string) (*Event, error) {
	if err := ValidateStreamName(stream); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
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
func (m *MemoryEventStore) ReadCategory(ctx context.Context, category string, afterPosition int64, limit int64) ([]Event, error) {
	if err := validateCategory(category); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
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
