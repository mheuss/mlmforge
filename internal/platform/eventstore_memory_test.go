package platform

import (
	"context"
	"encoding/json"
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

	got, err := store.ReadStream(ctx, "order-abc", 1)
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

	got, err := store.ReadStream(ctx, "order-abc", 1)
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

	got, err := store.ReadStream(ctx, "order-abc", 1)
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

	got, err := store.ReadStream(ctx, "order-abc", 1)
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

	got, err := store.ReadStream(ctx, "order-abc", 1)
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

	got, err := store.ReadStream(ctx, "order-abc", 1)
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

	got, err := store.ReadStream(ctx, "order-new", 1)
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

	got, err := store.ReadStream(ctx, "order-abc", 1)
	require.NoError(t, err)
	assert.Len(t, got, 2)
}

func TestMemoryEventStore_ReadStreamEmpty(t *testing.T) {
	store := NewMemoryEventStore()
	ctx := context.Background()

	got, err := store.ReadStream(ctx, "nonexistent-stream", 1)
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

	got, err := store.ReadStream(ctx, "order-abc", 2)
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

	got, err := store.ReadCategory(ctx, "order", 0)
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

	got, err := store.ReadCategory(ctx, "order", 1)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "evt-2", got[0].ID)
}

func TestMemoryEventStore_ReadCategoryEmpty(t *testing.T) {
	store := NewMemoryEventStore()
	ctx := context.Background()

	got, err := store.ReadCategory(ctx, "nonexistent", 0)
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

	got, err := store.ReadCategory(ctx, "order", 0)
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

	got, err := store.ReadCategory(ctx, "commission", 0)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "evt-1", got[0].ID)
}

func TestMemoryEventStore_ReadCategoryStreamWithoutHyphen(t *testing.T) {
	store := NewMemoryEventStore()
	ctx := context.Background()

	// Stream with no hyphen: category equals the whole stream name.
	_ = store.Append(ctx, "singleton", 0, []NewEvent{
		{ID: "evt-1", Type: "SystemStarted", Payload: json.RawMessage(`{}`)},
	})

	got, err := store.ReadCategory(ctx, "singleton", 0)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "evt-1", got[0].ID)
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

	abc, err := store.ReadStream(ctx, "order-abc", 1)
	require.NoError(t, err)
	assert.Equal(t, int64(1), abc[0].Version)
	assert.Equal(t, int64(2), abc[1].Version)

	def, err := store.ReadStream(ctx, "order-def", 1)
	require.NoError(t, err)
	assert.Equal(t, int64(1), def[0].Version)
}
