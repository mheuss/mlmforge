package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMemoryEventStore_AppendRejectsStreamWithoutHyphen(t *testing.T) {
	store := NewMemoryEventStore()
	ctx := context.Background()

	// Stream names must follow {category}-{id} format. A name without a
	// hyphen is rejected because there's no id part.
	err := store.Append(ctx, "singleton", 0, []NewEvent{
		{ID: "00000000-0000-0000-0000-000000000001", Type: "SystemStarted", Payload: json.RawMessage(`{}`)},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidStreamName)
}

func TestMemoryEventStore_AppendRejectsEmptyStreamName(t *testing.T) {
	store := NewMemoryEventStore()
	ctx := context.Background()

	events := []NewEvent{
		{ID: "00000000-0000-0000-0000-000000000001", Type: "OrderCreated", Payload: json.RawMessage(`{}`)},
	}
	err := store.Append(ctx, "", 0, events)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidStreamName)
}

func TestMemoryEventStore_AppendRejectsEmptySlice(t *testing.T) {
	store := NewMemoryEventStore()
	ctx := context.Background()

	err := store.Append(ctx, "order-abc", 0, []NewEvent{})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrEmptyAppend)
}

func TestMemoryEventStore_AppendRejectsNilSlice(t *testing.T) {
	store := NewMemoryEventStore()
	ctx := context.Background()

	err := store.Append(ctx, "order-abc", 0, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrEmptyAppend)
}

func TestMemoryEventStore_AppendRejectsEmptyID(t *testing.T) {
	store := NewMemoryEventStore()
	ctx := context.Background()

	events := []NewEvent{
		{ID: "", Type: "OrderCreated", Payload: json.RawMessage(`{}`)},
	}
	err := store.Append(ctx, "order-abc", 0, events)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty ID")
}

func TestMemoryEventStore_AppendRejectsEmptyType(t *testing.T) {
	store := NewMemoryEventStore()
	ctx := context.Background()

	events := []NewEvent{
		{ID: "00000000-0000-0000-0000-000000000001", Type: "", Payload: json.RawMessage(`{}`)},
	}
	err := store.Append(ctx, "order-abc", 0, events)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty Type")
}

func TestMemoryEventStore_AppendRejectsNilPayload(t *testing.T) {
	store := NewMemoryEventStore()
	ctx := context.Background()

	events := []NewEvent{
		{ID: "00000000-0000-0000-0000-000000000001", Type: "OrderCreated", Payload: nil},
	}
	err := store.Append(ctx, "order-abc", 0, events)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil Payload")
}

func TestMemoryEventStore_AppendValidatesAllEvents(t *testing.T) {
	store := NewMemoryEventStore()
	ctx := context.Background()

	// First event is valid, second has empty Type.
	events := []NewEvent{
		{ID: "00000000-0000-0000-0000-000000000001", Type: "OrderCreated", Payload: json.RawMessage(`{}`)},
		{ID: "00000000-0000-0000-0000-000000000002", Type: "", Payload: json.RawMessage(`{}`)},
	}
	err := store.Append(ctx, "order-abc", 0, events)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "index 1")

	// Stream should be unchanged because validation failed before writing.
	got, err := store.ReadStream(ctx, "order-abc", 1, 0)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestMemoryEventStore_ConcurrentAppendAndRead(t *testing.T) {
	store := NewMemoryEventStore()
	ctx := context.Background()

	const (
		numWriters     = 10
		eventsPerWrite = 5
	)

	// Each writer appends to its own stream using unconditional append (-1).
	// This avoids concurrency errors while exercising the mutex.
	var wg sync.WaitGroup

	for w := range numWriters {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stream := fmt.Sprintf("stream-%d", w)
			for i := range eventsPerWrite {
				evt := []NewEvent{
					{
						ID:      fmt.Sprintf("00000000-0000-0000-%04d-%012d", w, i),
						Type:    "TestEvent",
						Payload: json.RawMessage(`{}`),
					},
				}
				err := store.Append(ctx, stream, -1, evt)
				assert.NoError(t, err)
			}
		}()
	}

	// Concurrent readers reading while writes are in progress.
	for r := range numWriters {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stream := fmt.Sprintf("stream-%d", r)
			for range eventsPerWrite {
				_, err := store.ReadStream(ctx, stream, 1, 0)
				assert.NoError(t, err)
			}
		}()
	}

	// Also read by category concurrently.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range numWriters * eventsPerWrite {
			_, err := store.ReadCategory(ctx, "stream", 0, 0)
			assert.NoError(t, err)
		}
	}()

	wg.Wait()

	// Verify no events were lost.
	totalEvents := 0
	for w := range numWriters {
		stream := fmt.Sprintf("stream-%d", w)
		got, err := store.ReadStream(ctx, stream, 1, 0)
		require.NoError(t, err)
		assert.Len(t, got, eventsPerWrite, "stream-%d should have all events", w)
		totalEvents += len(got)
	}
	assert.Equal(t, numWriters*eventsPerWrite, totalEvents)

	// Verify category read returns all events across all streams.
	allEvents, err := store.ReadCategory(ctx, "stream", 0, 0)
	require.NoError(t, err)
	assert.Len(t, allEvents, numWriters*eventsPerWrite)
}

func TestValidateStreamName(t *testing.T) {
	tests := []struct {
		name    string
		stream  string
		wantErr bool
	}{
		{name: "valid simple", stream: "order-abc", wantErr: false},
		{name: "valid multi-hyphen", stream: "commission-period-2026-01", wantErr: false},
		{name: "valid uuid id", stream: "order-550e8400-e29b-41d4-a716-446655440000", wantErr: false},
		{name: "empty string", stream: "", wantErr: true},
		{name: "no hyphen", stream: "singleton", wantErr: true},
		{name: "hyphen only", stream: "-", wantErr: true},
		{name: "empty category", stream: "-abc", wantErr: true},
		{name: "empty id", stream: "order-", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateStreamName(tt.stream)
			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, ErrInvalidStreamName)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestMemoryEventStore_AppendRejectsEmptyID_StreamName(t *testing.T) {
	store := NewMemoryEventStore()
	ctx := context.Background()

	events := []NewEvent{
		{ID: "00000000-0000-0000-0000-000000000001", Type: "OrderCreated", Payload: json.RawMessage(`{}`)},
	}

	// Empty category part.
	err := store.Append(ctx, "-abc", 0, events)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidStreamName)

	// Empty id part.
	err = store.Append(ctx, "order-", 0, events)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidStreamName)
}
