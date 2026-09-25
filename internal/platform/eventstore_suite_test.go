package platform

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testReadLastEvent holds both EventStore twins to one ReadLastEvent contract.
func testReadLastEvent(t *testing.T, newStore func(t *testing.T) EventStore) {
	ctx := context.Background()
	appendOne := func(t *testing.T, s EventStore, stream string, expected int64, id string) {
		t.Helper()
		require.NoError(t, s.Append(ctx, stream, expected, []NewEvent{{
			ID: id, Type: "OrderPlaced", Payload: json.RawMessage(`{}`),
		}}))
	}

	t.Run("an empty stream returns nil", func(t *testing.T) {
		s := newStore(t)

		got, err := s.ReadLastEvent(ctx, "order-1")

		require.NoError(t, err)
		assert.Nil(t, got)
	})

	t.Run("a single event is the last event", func(t *testing.T) {
		s := newStore(t)
		appendOne(t, s, "order-1", 0, "00000000-0000-0000-0000-000000000001")

		got, err := s.ReadLastEvent(ctx, "order-1")

		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, "00000000-0000-0000-0000-000000000001", got.ID)
		assert.Equal(t, "order-1", got.Stream)
		assert.Equal(t, int64(1), got.Version)
	})

	t.Run("several events return the highest version", func(t *testing.T) {
		s := newStore(t)
		appendOne(t, s, "order-1", 0, "00000000-0000-0000-0000-000000000001")
		appendOne(t, s, "order-1", 1, "00000000-0000-0000-0000-000000000002")
		appendOne(t, s, "order-1", 2, "00000000-0000-0000-0000-000000000003")

		got, err := s.ReadLastEvent(ctx, "order-1")

		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, "00000000-0000-0000-0000-000000000003", got.ID)
		assert.Equal(t, int64(3), got.Version)
	})

	t.Run("a neighbouring stream is not read", func(t *testing.T) {
		s := newStore(t)
		appendOne(t, s, "order-1", 0, "00000000-0000-0000-0000-000000000001")
		appendOne(t, s, "order-10", 0, "00000000-0000-0000-0000-000000000002")
		appendOne(t, s, "order-10", 1, "00000000-0000-0000-0000-000000000003")

		got, err := s.ReadLastEvent(ctx, "order-1")

		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, "00000000-0000-0000-0000-000000000001", got.ID)
		assert.Equal(t, int64(1), got.Version)
	})

	t.Run("the returned event does not share bytes with the store", func(t *testing.T) {
		s := newStore(t)
		require.NoError(t, s.Append(ctx, "order-1", 0, []NewEvent{{
			ID: "00000000-0000-0000-0000-000000000001", Type: "OrderPlaced",
			Payload: json.RawMessage(`{}`), Metadata: json.RawMessage(`{"a":1}`),
		}}))
		first, err := s.ReadLastEvent(ctx, "order-1")
		require.NoError(t, err)
		require.NotNil(t, first)

		first.Payload[0] = 'X'
		first.Metadata[0] = 'X'
		again, err := s.ReadLastEvent(ctx, "order-1")

		require.NoError(t, err)
		require.NotNil(t, again)
		assert.JSONEq(t, `{}`, string(again.Payload))
		assert.JSONEq(t, `{"a":1}`, string(again.Metadata))
	})

	t.Run("an invalid stream name is refused", func(t *testing.T) {
		s := newStore(t)

		_, err := s.ReadLastEvent(ctx, "orderwithoutaseparator")

		require.ErrorIs(t, err, ErrInvalidStreamName)
	})
}

func TestMemoryEventStore_ReadLastEvent(t *testing.T) {
	testReadLastEvent(t, func(*testing.T) EventStore { return NewMemoryEventStore() })
}

func TestPostgresEventStore_ReadLastEvent(t *testing.T) {
	testReadLastEvent(t, func(t *testing.T) EventStore { return newTestPostgresStore(t) })
}

// testDuplicateEventIDs holds both EventStore twins to one contract for a
// reused event ID.
func testDuplicateEventIDs(t *testing.T, newStore func(t *testing.T) EventStore) {
	ctx := context.Background()
	const (
		id1 = "00000000-0000-0000-0000-000000000001"
		id2 = "00000000-0000-0000-0000-000000000002"
	)
	event := func(id string) NewEvent {
		return NewEvent{ID: id, Type: "OrderPlaced", Payload: json.RawMessage(`{}`)}
	}
	requireNotConcurrencyError := func(t *testing.T, err error) {
		t.Helper()
		require.Error(t, err)
		var ce *ConcurrencyError
		require.False(t, errors.As(err, &ce), "expected a duplicate ID refusal, got %v", err)
	}

	t.Run("a reused ID in the same stream is refused and nothing is written", func(t *testing.T) {
		s := newStore(t)
		require.NoError(t, s.Append(ctx, "order-1", 0, []NewEvent{event(id1)}))

		err := s.Append(ctx, "order-1", 1, []NewEvent{event(id1)})

		requireNotConcurrencyError(t, err)
		got, err := s.ReadStream(ctx, "order-1", 1, 0)
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, id1, got[0].ID)
	})

	t.Run("a reused ID in another stream is refused and nothing is written", func(t *testing.T) {
		s := newStore(t)
		require.NoError(t, s.Append(ctx, "order-1", 0, []NewEvent{event(id1)}))

		err := s.Append(ctx, "order-2", 0, []NewEvent{event(id1)})

		requireNotConcurrencyError(t, err)
		stream, err := s.ReadStream(ctx, "order-2", 1, 0)
		require.NoError(t, err)
		assert.Empty(t, stream)
		category, err := s.ReadCategory(ctx, "order", 0, 0)
		require.NoError(t, err)
		require.Len(t, category, 1)
		assert.Equal(t, "order-1", category[0].Stream)
	})

	t.Run("a reused ID within one batch refuses the whole batch", func(t *testing.T) {
		s := newStore(t)

		err := s.Append(ctx, "order-1", 0, []NewEvent{event(id1), event(id2), event(id1)})

		requireNotConcurrencyError(t, err)
		got, err := s.ReadStream(ctx, "order-1", 1, 0)
		require.NoError(t, err)
		assert.Empty(t, got)
	})
}

func TestMemoryEventStore_DuplicateEventIDs(t *testing.T) {
	testDuplicateEventIDs(t, func(*testing.T) EventStore { return NewMemoryEventStore() })
}

func TestPostgresEventStore_DuplicateEventIDs(t *testing.T) {
	testDuplicateEventIDs(t, func(t *testing.T) EventStore { return newTestPostgresStore(t) })
}

// testByteIsolation holds both EventStore twins to one contract for who owns
// the Payload and Metadata bytes.
func testByteIsolation(t *testing.T, newStore func(t *testing.T) EventStore) {
	ctx := context.Background()
	appendOne := func(t *testing.T, s EventStore) {
		t.Helper()
		require.NoError(t, s.Append(ctx, "order-1", 0, []NewEvent{{
			ID: "00000000-0000-0000-0000-000000000001", Type: "OrderPlaced",
			Payload: json.RawMessage(`{}`), Metadata: json.RawMessage(`{"a":1}`),
		}}))
	}

	t.Run("the caller's slices do not share bytes with the store", func(t *testing.T) {
		s := newStore(t)
		payload := json.RawMessage(`{}`)
		metadata := json.RawMessage(`{"a":1}`)
		require.NoError(t, s.Append(ctx, "order-1", 0, []NewEvent{{
			ID: "00000000-0000-0000-0000-000000000001", Type: "OrderPlaced",
			Payload: payload, Metadata: metadata,
		}}))

		payload[0] = 'X'
		metadata[0] = 'X'
		got, err := s.ReadLastEvent(ctx, "order-1")

		require.NoError(t, err)
		require.NotNil(t, got)
		assert.JSONEq(t, `{}`, string(got.Payload))
		assert.JSONEq(t, `{"a":1}`, string(got.Metadata))
	})

	t.Run("events from ReadStream do not share bytes with the store", func(t *testing.T) {
		s := newStore(t)
		appendOne(t, s)
		first, err := s.ReadStream(ctx, "order-1", 1, 0)
		require.NoError(t, err)
		require.Len(t, first, 1)

		first[0].Payload[0] = 'X'
		first[0].Metadata[0] = 'X'
		again, err := s.ReadStream(ctx, "order-1", 1, 0)

		require.NoError(t, err)
		require.Len(t, again, 1)
		assert.JSONEq(t, `{}`, string(again[0].Payload))
		assert.JSONEq(t, `{"a":1}`, string(again[0].Metadata))
	})

	t.Run("events from ReadCategory do not share bytes with the store", func(t *testing.T) {
		s := newStore(t)
		appendOne(t, s)
		first, err := s.ReadCategory(ctx, "order", 0, 0)
		require.NoError(t, err)
		require.Len(t, first, 1)

		first[0].Payload[0] = 'X'
		first[0].Metadata[0] = 'X'
		again, err := s.ReadCategory(ctx, "order", 0, 0)

		require.NoError(t, err)
		require.Len(t, again, 1)
		assert.JSONEq(t, `{}`, string(again[0].Payload))
		assert.JSONEq(t, `{"a":1}`, string(again[0].Metadata))
	})
}

func TestMemoryEventStore_ByteIsolation(t *testing.T) {
	testByteIsolation(t, func(*testing.T) EventStore { return NewMemoryEventStore() })
}

func TestPostgresEventStore_ByteIsolation(t *testing.T) {
	testByteIsolation(t, func(t *testing.T) EventStore { return newTestPostgresStore(t) })
}
