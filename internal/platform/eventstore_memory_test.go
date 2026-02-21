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

func TestMemoryEventStore_AppendAndReadBack(t *testing.T) {
	store := NewMemoryEventStore()
	ctx := context.Background()

	events := []NewEvent{
		{
			ID:      "evt-1",
			Type:    "OrderCompleted",
			Payload: json.RawMessage(`{"order_id":"abc"}`),
		},
	}

	err := store.Append(ctx, "order-abc", 0, events)
	require.NoError(t, err)

	got, err := store.ReadStream(ctx, "order-abc", 1, 0)
	require.NoError(t, err)
	require.Len(t, got, 1)

	assert.Equal(t, "evt-1", got[0].ID)
	assert.Equal(t, "order-abc", got[0].Stream)
	assert.Equal(t, "OrderCompleted", got[0].Type)
	assert.Equal(t, int64(1), got[0].Version)
	assert.Equal(t, int64(1), got[0].GlobalPosition)
	assert.JSONEq(t, `{"order_id":"abc"}`, string(got[0].Payload))
	assert.False(t, got[0].Timestamp.IsZero())
}

func TestMemoryEventStore_AppendMultipleEvents(t *testing.T) {
	store := NewMemoryEventStore()
	ctx := context.Background()

	events := []NewEvent{
		{ID: "evt-1", Type: "OrderCreated", Payload: json.RawMessage(`{}`)},
		{ID: "evt-2", Type: "OrderCompleted", Payload: json.RawMessage(`{}`)},
	}

	err := store.Append(ctx, "order-abc", 0, events)
	require.NoError(t, err)

	got, err := store.ReadStream(ctx, "order-abc", 1, 0)
	require.NoError(t, err)
	require.Len(t, got, 2)

	assert.Equal(t, int64(1), got[0].Version)
	assert.Equal(t, int64(2), got[1].Version)
	assert.Equal(t, int64(1), got[0].GlobalPosition)
	assert.Equal(t, int64(2), got[1].GlobalPosition)
}

func TestMemoryEventStore_AppendWithMetadata(t *testing.T) {
	store := NewMemoryEventStore()
	ctx := context.Background()

	meta := json.RawMessage(`{"actor":"user-123"}`)
	events := []NewEvent{
		{ID: "evt-1", Type: "OrderCompleted", Payload: json.RawMessage(`{}`), Metadata: meta},
	}

	err := store.Append(ctx, "order-abc", 0, events)
	require.NoError(t, err)

	got, err := store.ReadStream(ctx, "order-abc", 1, 0)
	require.NoError(t, err)
	assert.JSONEq(t, `{"actor":"user-123"}`, string(got[0].Metadata))
}

func TestMemoryEventStore_AppendNilMetadata(t *testing.T) {
	store := NewMemoryEventStore()
	ctx := context.Background()

	events := []NewEvent{
		{ID: "evt-1", Type: "OrderCompleted", Payload: json.RawMessage(`{}`)},
	}

	err := store.Append(ctx, "order-abc", 0, events)
	require.NoError(t, err)

	got, err := store.ReadStream(ctx, "order-abc", 1, 0)
	require.NoError(t, err)
	assert.Nil(t, got[0].Metadata)
}

func TestMemoryEventStore_ConcurrencyConflict(t *testing.T) {
	store := NewMemoryEventStore()
	ctx := context.Background()

	err := store.Append(ctx, "order-abc", 0, []NewEvent{
		{ID: "evt-1", Type: "OrderCreated", Payload: json.RawMessage(`{}`)},
	})
	require.NoError(t, err)

	err = store.Append(ctx, "order-abc", 0, []NewEvent{
		{ID: "evt-2", Type: "OrderCompleted", Payload: json.RawMessage(`{}`)},
	})
	require.Error(t, err)

	var concErr *ConcurrencyError
	require.ErrorAs(t, err, &concErr)
	assert.Equal(t, "order-abc", concErr.Stream)
	assert.Equal(t, int64(0), concErr.ExpectedVersion)
	assert.Equal(t, int64(1), concErr.ActualVersion)
}

func TestMemoryEventStore_ConcurrencyConflictDoesNotMutateStream(t *testing.T) {
	store := NewMemoryEventStore()
	ctx := context.Background()

	err := store.Append(ctx, "order-abc", 0, []NewEvent{
		{ID: "evt-1", Type: "OrderCreated", Payload: json.RawMessage(`{}`)},
	})
	require.NoError(t, err)

	_ = store.Append(ctx, "order-abc", 0, []NewEvent{
		{ID: "evt-2", Type: "OrderCompleted", Payload: json.RawMessage(`{}`)},
	})

	got, err := store.ReadStream(ctx, "order-abc", 1, 0)
	require.NoError(t, err)
	assert.Len(t, got, 1, "stream should still have exactly 1 event")
}

func TestMemoryEventStore_SkipVersionCheck(t *testing.T) {
	store := NewMemoryEventStore()
	ctx := context.Background()

	err := store.Append(ctx, "order-abc", 0, []NewEvent{
		{ID: "evt-1", Type: "OrderCreated", Payload: json.RawMessage(`{}`)},
	})
	require.NoError(t, err)

	err = store.Append(ctx, "order-abc", -1, []NewEvent{
		{ID: "evt-2", Type: "OrderCompleted", Payload: json.RawMessage(`{}`)},
	})
	require.NoError(t, err)

	got, err := store.ReadStream(ctx, "order-abc", 1, 0)
	require.NoError(t, err)
	assert.Len(t, got, 2)
}

func TestMemoryEventStore_NewStreamExpectedVersionZero(t *testing.T) {
	store := NewMemoryEventStore()
	ctx := context.Background()

	err := store.Append(ctx, "order-new", 0, []NewEvent{
		{ID: "evt-1", Type: "OrderCreated", Payload: json.RawMessage(`{}`)},
	})
	require.NoError(t, err)

	got, err := store.ReadStream(ctx, "order-new", 1, 0)
	require.NoError(t, err)
	assert.Len(t, got, 1)
}

func TestMemoryEventStore_CorrectExpectedVersionSucceeds(t *testing.T) {
	store := NewMemoryEventStore()
	ctx := context.Background()

	err := store.Append(ctx, "order-abc", 0, []NewEvent{
		{ID: "evt-1", Type: "OrderCreated", Payload: json.RawMessage(`{}`)},
	})
	require.NoError(t, err)

	err = store.Append(ctx, "order-abc", 1, []NewEvent{
		{ID: "evt-2", Type: "OrderCompleted", Payload: json.RawMessage(`{}`)},
	})
	require.NoError(t, err)

	got, err := store.ReadStream(ctx, "order-abc", 1, 0)
	require.NoError(t, err)
	assert.Len(t, got, 2)
}

func TestMemoryEventStore_ReadStreamEmpty(t *testing.T) {
	store := NewMemoryEventStore()
	ctx := context.Background()

	got, err := store.ReadStream(ctx, "nonexistent-stream", 1, 0)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestMemoryEventStore_ReadStreamFromVersion(t *testing.T) {
	store := NewMemoryEventStore()
	ctx := context.Background()

	err := store.Append(ctx, "order-abc", 0, []NewEvent{
		{ID: "evt-1", Type: "OrderCreated", Payload: json.RawMessage(`{}`)},
		{ID: "evt-2", Type: "OrderUpdated", Payload: json.RawMessage(`{}`)},
		{ID: "evt-3", Type: "OrderCompleted", Payload: json.RawMessage(`{}`)},
	})
	require.NoError(t, err)

	got, err := store.ReadStream(ctx, "order-abc", 2, 0)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, int64(2), got[0].Version)
	assert.Equal(t, int64(3), got[1].Version)
}

func TestMemoryEventStore_ReadCategoryMatchesPrefix(t *testing.T) {
	store := NewMemoryEventStore()
	ctx := context.Background()

	_ = store.Append(ctx, "order-abc", 0, []NewEvent{
		{ID: "evt-1", Type: "OrderCreated", Payload: json.RawMessage(`{}`)},
	})
	_ = store.Append(ctx, "order-def", 0, []NewEvent{
		{ID: "evt-2", Type: "OrderCreated", Payload: json.RawMessage(`{}`)},
	})
	_ = store.Append(ctx, "autoship-xyz", 0, []NewEvent{
		{ID: "evt-3", Type: "AutoshipCreated", Payload: json.RawMessage(`{}`)},
	})

	got, err := store.ReadCategory(ctx, "order", 0, 0)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "evt-1", got[0].ID)
	assert.Equal(t, "evt-2", got[1].ID)
}

func TestMemoryEventStore_ReadCategoryAfterPosition(t *testing.T) {
	store := NewMemoryEventStore()
	ctx := context.Background()

	_ = store.Append(ctx, "order-abc", 0, []NewEvent{
		{ID: "evt-1", Type: "OrderCreated", Payload: json.RawMessage(`{}`)},
	})
	_ = store.Append(ctx, "order-def", 0, []NewEvent{
		{ID: "evt-2", Type: "OrderCreated", Payload: json.RawMessage(`{}`)},
	})

	got, err := store.ReadCategory(ctx, "order", 1, 0)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "evt-2", got[0].ID)
}

func TestMemoryEventStore_ReadCategoryEmpty(t *testing.T) {
	store := NewMemoryEventStore()
	ctx := context.Background()

	got, err := store.ReadCategory(ctx, "nonexistent", 0, 0)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestMemoryEventStore_ReadCategoryGlobalOrder(t *testing.T) {
	store := NewMemoryEventStore()
	ctx := context.Background()

	_ = store.Append(ctx, "order-abc", 0, []NewEvent{
		{ID: "evt-1", Type: "OrderCreated", Payload: json.RawMessage(`{}`)},
	})
	_ = store.Append(ctx, "autoship-xyz", 0, []NewEvent{
		{ID: "evt-2", Type: "AutoshipCreated", Payload: json.RawMessage(`{}`)},
	})
	_ = store.Append(ctx, "order-def", 0, []NewEvent{
		{ID: "evt-3", Type: "OrderCreated", Payload: json.RawMessage(`{}`)},
	})

	got, err := store.ReadCategory(ctx, "order", 0, 0)
	require.NoError(t, err)
	require.Len(t, got, 2)

	assert.Equal(t, int64(1), got[0].GlobalPosition)
	assert.Equal(t, int64(3), got[1].GlobalPosition)
}

func TestMemoryEventStore_ReadCategoryMultiHyphenStream(t *testing.T) {
	store := NewMemoryEventStore()
	ctx := context.Background()

	// "commission-period-2026-01" has category "commission" (before first hyphen).
	_ = store.Append(ctx, "commission-period-2026-01", 0, []NewEvent{
		{ID: "evt-1", Type: "PeriodOpened", Payload: json.RawMessage(`{}`)},
	})
	_ = store.Append(ctx, "order-abc", 0, []NewEvent{
		{ID: "evt-2", Type: "OrderCreated", Payload: json.RawMessage(`{}`)},
	})

	got, err := store.ReadCategory(ctx, "commission", 0, 0)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "evt-1", got[0].ID)
}

func TestMemoryEventStore_AppendRejectsStreamWithoutHyphen(t *testing.T) {
	store := NewMemoryEventStore()
	ctx := context.Background()

	// Stream names must follow {category}-{id} format. A name without a
	// hyphen is rejected because there's no id part.
	err := store.Append(ctx, "singleton", 0, []NewEvent{
		{ID: "evt-1", Type: "SystemStarted", Payload: json.RawMessage(`{}`)},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidStreamName)
}

func TestMemoryEventStore_VersionsAcrossStreamsIndependent(t *testing.T) {
	store := NewMemoryEventStore()
	ctx := context.Background()

	_ = store.Append(ctx, "order-abc", 0, []NewEvent{
		{ID: "evt-1", Type: "OrderCreated", Payload: json.RawMessage(`{}`)},
		{ID: "evt-2", Type: "OrderCompleted", Payload: json.RawMessage(`{}`)},
	})
	_ = store.Append(ctx, "order-def", 0, []NewEvent{
		{ID: "evt-3", Type: "OrderCreated", Payload: json.RawMessage(`{}`)},
	})

	abc, err := store.ReadStream(ctx, "order-abc", 1, 0)
	require.NoError(t, err)
	assert.Equal(t, int64(1), abc[0].Version)
	assert.Equal(t, int64(2), abc[1].Version)

	def, err := store.ReadStream(ctx, "order-def", 1, 0)
	require.NoError(t, err)
	assert.Equal(t, int64(1), def[0].Version)
}

func TestMemoryEventStore_ReadStream_WithLimit(t *testing.T) {
	store := NewMemoryEventStore()
	ctx := context.Background()

	err := store.Append(ctx, "order-abc", 0, []NewEvent{
		{ID: "evt-1", Type: "OrderCreated", Payload: json.RawMessage(`{}`)},
		{ID: "evt-2", Type: "OrderUpdated", Payload: json.RawMessage(`{}`)},
		{ID: "evt-3", Type: "OrderShipped", Payload: json.RawMessage(`{}`)},
		{ID: "evt-4", Type: "OrderDelivered", Payload: json.RawMessage(`{}`)},
		{ID: "evt-5", Type: "OrderCompleted", Payload: json.RawMessage(`{}`)},
	})
	require.NoError(t, err)

	got, err := store.ReadStream(ctx, "order-abc", 1, 3)
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, "evt-1", got[0].ID)
	assert.Equal(t, "evt-2", got[1].ID)
	assert.Equal(t, "evt-3", got[2].ID)
}

func TestMemoryEventStore_ReadCategory_WithLimit(t *testing.T) {
	store := NewMemoryEventStore()
	ctx := context.Background()

	_ = store.Append(ctx, "order-abc", 0, []NewEvent{
		{ID: "evt-1", Type: "OrderCreated", Payload: json.RawMessage(`{}`)},
	})
	_ = store.Append(ctx, "order-def", 0, []NewEvent{
		{ID: "evt-2", Type: "OrderCreated", Payload: json.RawMessage(`{}`)},
	})
	_ = store.Append(ctx, "order-ghi", 0, []NewEvent{
		{ID: "evt-3", Type: "OrderCreated", Payload: json.RawMessage(`{}`)},
	})
	_ = store.Append(ctx, "order-jkl", 0, []NewEvent{
		{ID: "evt-4", Type: "OrderCreated", Payload: json.RawMessage(`{}`)},
	})
	_ = store.Append(ctx, "order-mno", 0, []NewEvent{
		{ID: "evt-5", Type: "OrderCreated", Payload: json.RawMessage(`{}`)},
	})

	got, err := store.ReadCategory(ctx, "order", 0, 3)
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, "evt-1", got[0].ID)
	assert.Equal(t, "evt-2", got[1].ID)
	assert.Equal(t, "evt-3", got[2].ID)
}

func TestMemoryEventStore_ReadStream_FromVersionZero(t *testing.T) {
	store := NewMemoryEventStore()
	ctx := context.Background()

	err := store.Append(ctx, "order-abc", 0, []NewEvent{
		{ID: "evt-1", Type: "OrderCreated", Payload: json.RawMessage(`{}`)},
		{ID: "evt-2", Type: "OrderCompleted", Payload: json.RawMessage(`{}`)},
	})
	require.NoError(t, err)

	// fromVersion=0 should be clamped to 1 and return all events.
	got, err := store.ReadStream(ctx, "order-abc", 0, 0)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, int64(1), got[0].Version)
	assert.Equal(t, int64(2), got[1].Version)
}

func TestMemoryEventStore_AppendRejectsEmptyStreamName(t *testing.T) {
	store := NewMemoryEventStore()
	ctx := context.Background()

	events := []NewEvent{
		{ID: "evt-1", Type: "OrderCreated", Payload: json.RawMessage(`{}`)},
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
		{ID: "evt-1", Type: "", Payload: json.RawMessage(`{}`)},
	}
	err := store.Append(ctx, "order-abc", 0, events)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty Type")
}

func TestMemoryEventStore_AppendRejectsNilPayload(t *testing.T) {
	store := NewMemoryEventStore()
	ctx := context.Background()

	events := []NewEvent{
		{ID: "evt-1", Type: "OrderCreated", Payload: nil},
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
		{ID: "evt-1", Type: "OrderCreated", Payload: json.RawMessage(`{}`)},
		{ID: "evt-2", Type: "", Payload: json.RawMessage(`{}`)},
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
						ID:      fmt.Sprintf("evt-%d-%d", w, i),
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

func TestMemoryEventStore_ReadStreamRejectsInvalidStreamName(t *testing.T) {
	store := NewMemoryEventStore()
	ctx := context.Background()

	got, err := store.ReadStream(ctx, "nohyphen", 1, 0)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidStreamName)
	assert.Nil(t, got)
}

func TestMemoryEventStore_AppendRejectsEmptyID_StreamName(t *testing.T) {
	store := NewMemoryEventStore()
	ctx := context.Background()

	events := []NewEvent{
		{ID: "evt-1", Type: "OrderCreated", Payload: json.RawMessage(`{}`)},
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
