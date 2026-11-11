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

// testDuplicateEventIDs checks that an EventStore refuses a reused event ID.
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

	t.Run("a batch with an ID the stream holds is refused and nothing is written", func(t *testing.T) {
		s := newStore(t)
		require.NoError(t, s.Append(ctx, "order-1", 0, []NewEvent{event(id1)}))

		err := s.Append(ctx, "order-1", 1, []NewEvent{event(id2), event(id1)})

		requireNotConcurrencyError(t, err)
		got, err := s.ReadStream(ctx, "order-1", 1, 0)
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, id1, got[0].ID)
	})

	t.Run("a batch with an ID another stream holds is refused and nothing is written", func(t *testing.T) {
		s := newStore(t)
		require.NoError(t, s.Append(ctx, "order-1", 0, []NewEvent{event(id1)}))

		err := s.Append(ctx, "order-2", 0, []NewEvent{event(id2), event(id1)})

		requireNotConcurrencyError(t, err)
		stream, err := s.ReadStream(ctx, "order-2", 1, 0)
		require.NoError(t, err)
		assert.Empty(t, stream)
		category, err := s.ReadCategory(ctx, "order", 0, 0)
		require.NoError(t, err)
		require.Len(t, category, 1)
		assert.Equal(t, "order-1", category[0].Stream)
	})

	t.Run("a batch with another spelling of an ID the stream holds is refused and nothing is written", func(t *testing.T) {
		s := newStore(t)
		require.NoError(t, s.Append(ctx, "order-1", 0, []NewEvent{event("a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11")}))

		err := s.Append(ctx, "order-1", 1, []NewEvent{event(id2), event("A0EEBC99-9C0B-4EF8-BB6D-6BB9BD380A11")})

		requireNotConcurrencyError(t, err)
		got, err := s.ReadStream(ctx, "order-1", 1, 0)
		require.NoError(t, err)
		require.Len(t, got, 1)
	})

	t.Run("two spellings of one ID within one batch refuse the whole batch", func(t *testing.T) {
		s := newStore(t)

		err := s.Append(ctx, "order-1", 0, []NewEvent{
			event("a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11"), event("{a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11}"),
		})

		requireNotConcurrencyError(t, err)
		got, err := s.ReadStream(ctx, "order-1", 1, 0)
		require.NoError(t, err)
		assert.Empty(t, got)
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

// testByteIsolation checks that an EventStore keeps its own copy of Payload
// and Metadata bytes.
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

// testEventIDForms checks which spellings of an event ID an EventStore
// accepts, and the form it reads them back in.
func testEventIDForms(t *testing.T, newStore func(t *testing.T) EventStore) {
	ctx := context.Background()
	const canonical = "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11"
	appendID := func(s EventStore, id string) error {
		return s.Append(ctx, "order-1", 0, []NewEvent{{ID: id, Type: "OrderPlaced", Payload: json.RawMessage(`{}`)}})
	}

	accepted := []string{
		"a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11",
		"A0EEBC99-9C0B-4EF8-BB6D-6BB9BD380A11",
		"{a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11}",
		"a0eebc999c0b4ef8bb6d6bb9bd380a11",
		"a0ee-bc99-9c0b-4ef8-bb6d-6bb9-bd38-0a11",
		"a0eebc99-9c0b4ef8-bb6d6bb9-bd380a11",
		"{a0eebc999c0b4ef8bb6d6bb9bd380a11}",
	}
	for _, id := range accepted {
		t.Run("accepts "+id, func(t *testing.T) {
			s := newStore(t)

			require.NoError(t, appendID(s, id))

			got, err := s.ReadStream(ctx, "order-1", 1, 0)
			require.NoError(t, err)
			require.Len(t, got, 1)
			assert.Equal(t, canonical, got[0].ID)
		})
	}

	refused := []string{
		"evt-1",
		"urn:uuid:a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11",
		"a0eebc99x9c0bx4ef8xbb6dx6bb9bd380a11",
		" a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11",
		"{a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11",
		"a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11}",
		"a0eebc99--9c0b-4ef8-bb6d-6bb9bd380a11",
		"a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11-",
		"a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a1",
		"a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11a",
		"a0-eebc99-9c0b-4ef8-bb6d-6bb9bd380a11",
	}
	for _, id := range refused {
		t.Run("refuses "+id, func(t *testing.T) {
			s := newStore(t)

			require.Error(t, appendID(s, id))

			got, err := s.ReadStream(ctx, "order-1", 1, 0)
			require.NoError(t, err)
			assert.Empty(t, got)
		})
	}

	t.Run("a version conflict is reported ahead of an ID that does not parse", func(t *testing.T) {
		s := newStore(t)
		require.NoError(t, appendID(s, canonical))

		err := appendID(s, "evt-2")

		var ce *ConcurrencyError
		require.ErrorAs(t, err, &ce)
	})

	t.Run("a missing Type later in the batch is reported ahead of an ID that does not parse", func(t *testing.T) {
		s := newStore(t)

		err := s.Append(ctx, "order-1", 0, []NewEvent{
			{ID: "evt-1", Type: "OrderPlaced", Payload: json.RawMessage(`{}`)},
			{ID: canonical, Payload: json.RawMessage(`{}`)},
		})

		require.ErrorContains(t, err, "event at index 1 has empty Type")
	})
}

func TestMemoryEventStore_EventIDForms(t *testing.T) {
	testEventIDForms(t, func(*testing.T) EventStore { return NewMemoryEventStore() })
}

func TestPostgresEventStore_EventIDForms(t *testing.T) {
	testEventIDForms(t, func(t *testing.T) EventStore { return newTestPostgresStore(t) })
}

// testAppendAndRead checks what an EventStore stores on append and returns on read.
func testAppendAndRead(t *testing.T, newStore func(t *testing.T) EventStore) {
	ctx := context.Background()

	t.Run("one event reads back with the fields the store assigns", func(t *testing.T) {
		s := newStore(t)
		require.NoError(t, s.Append(ctx, "order-abc", 0, []NewEvent{{
			ID: "00000000-0000-0000-0000-000000000001", Type: "OrderCompleted",
			Payload: json.RawMessage(`{"order_id":"abc"}`),
		}}))

		got, err := s.ReadStream(ctx, "order-abc", 1, 0)

		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, "00000000-0000-0000-0000-000000000001", got[0].ID)
		assert.Equal(t, "order-abc", got[0].Stream)
		assert.Equal(t, "OrderCompleted", got[0].Type)
		assert.Equal(t, int64(1), got[0].Version)
		assert.Positive(t, got[0].GlobalPosition)
		assert.JSONEq(t, `{"order_id":"abc"}`, string(got[0].Payload))
		assert.False(t, got[0].Timestamp.IsZero())
	})

	t.Run("several events in one append get consecutive versions and increasing positions", func(t *testing.T) {
		s := newStore(t)
		require.NoError(t, s.Append(ctx, "order-abc", 0, []NewEvent{
			{ID: "00000000-0000-0000-0000-000000000001", Type: "OrderCreated", Payload: json.RawMessage(`{}`)},
			{ID: "00000000-0000-0000-0000-000000000002", Type: "OrderCompleted", Payload: json.RawMessage(`{}`)},
		}))

		got, err := s.ReadStream(ctx, "order-abc", 1, 0)

		require.NoError(t, err)
		require.Len(t, got, 2)
		assert.Equal(t, int64(1), got[0].Version)
		assert.Equal(t, int64(2), got[1].Version)
		assert.Positive(t, got[0].GlobalPosition)
		assert.Greater(t, got[1].GlobalPosition, got[0].GlobalPosition)
	})

	t.Run("metadata reads back", func(t *testing.T) {
		s := newStore(t)
		require.NoError(t, s.Append(ctx, "order-abc", 0, []NewEvent{{
			ID: "00000000-0000-0000-0000-000000000001", Type: "OrderCompleted",
			Payload: json.RawMessage(`{}`), Metadata: json.RawMessage(`{"actor":"user-123"}`),
		}}))

		got, err := s.ReadStream(ctx, "order-abc", 1, 0)

		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.JSONEq(t, `{"actor":"user-123"}`, string(got[0].Metadata))
	})

	t.Run("nil metadata reads back as nil", func(t *testing.T) {
		s := newStore(t)
		require.NoError(t, s.Append(ctx, "order-abc", 0, []NewEvent{{
			ID: "00000000-0000-0000-0000-000000000001", Type: "OrderCompleted", Payload: json.RawMessage(`{}`),
		}}))

		got, err := s.ReadStream(ctx, "order-abc", 1, 0)

		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Nil(t, got[0].Metadata)
	})
}

func TestMemoryEventStore_AppendAndRead(t *testing.T) {
	testAppendAndRead(t, func(*testing.T) EventStore { return NewMemoryEventStore() })
}

func TestPostgresEventStore_AppendAndRead(t *testing.T) {
	testAppendAndRead(t, func(t *testing.T) EventStore { return newTestPostgresStore(t) })
}

// testOptimisticConcurrency checks an EventStore's expected-version rules.
func testOptimisticConcurrency(t *testing.T, newStore func(t *testing.T) EventStore) {
	ctx := context.Background()
	event := func(id, typ string) []NewEvent {
		return []NewEvent{{ID: id, Type: typ, Payload: json.RawMessage(`{}`)}}
	}

	t.Run("a stale expected version is refused with its versions", func(t *testing.T) {
		s := newStore(t)
		require.NoError(t, s.Append(ctx, "order-abc", 0, event("00000000-0000-0000-0000-000000000001", "OrderCreated")))

		err := s.Append(ctx, "order-abc", 0, event("00000000-0000-0000-0000-000000000002", "OrderCompleted"))

		var concErr *ConcurrencyError
		require.ErrorAs(t, err, &concErr)
		assert.Equal(t, "order-abc", concErr.Stream)
		assert.Equal(t, int64(0), concErr.ExpectedVersion)
		assert.Equal(t, int64(1), concErr.ActualVersion)
	})

	t.Run("a refused append leaves the stream unchanged", func(t *testing.T) {
		s := newStore(t)
		require.NoError(t, s.Append(ctx, "order-abc", 0, event("00000000-0000-0000-0000-000000000001", "OrderCreated")))
		var concErr *ConcurrencyError
		require.ErrorAs(t, s.Append(ctx, "order-abc", 0, event("00000000-0000-0000-0000-000000000002", "OrderCompleted")), &concErr)

		got, err := s.ReadStream(ctx, "order-abc", 1, 0)

		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, "00000000-0000-0000-0000-000000000001", got[0].ID)
	})

	t.Run("expected version 0 creates a new stream", func(t *testing.T) {
		s := newStore(t)
		require.NoError(t, s.Append(ctx, "order-new", 0, event("00000000-0000-0000-0000-000000000001", "OrderCreated")))

		got, err := s.ReadStream(ctx, "order-new", 1, 0)

		require.NoError(t, err)
		assert.Len(t, got, 1)
	})

	t.Run("the current version as expected version appends", func(t *testing.T) {
		s := newStore(t)
		require.NoError(t, s.Append(ctx, "order-abc", 0, event("00000000-0000-0000-0000-000000000001", "OrderCreated")))
		require.NoError(t, s.Append(ctx, "order-abc", 1, event("00000000-0000-0000-0000-000000000002", "OrderCompleted")))

		got, err := s.ReadStream(ctx, "order-abc", 1, 0)

		require.NoError(t, err)
		assert.Len(t, got, 2)
	})

	t.Run("expected version -1 skips the check", func(t *testing.T) {
		s := newStore(t)
		require.NoError(t, s.Append(ctx, "order-abc", 0, event("00000000-0000-0000-0000-000000000001", "OrderCreated")))
		require.NoError(t, s.Append(ctx, "order-abc", -1, event("00000000-0000-0000-0000-000000000002", "OrderCompleted")))

		got, err := s.ReadStream(ctx, "order-abc", 1, 0)

		require.NoError(t, err)
		assert.Len(t, got, 2)
	})

	t.Run("versions count per stream", func(t *testing.T) {
		s := newStore(t)
		require.NoError(t, s.Append(ctx, "order-abc", 0, []NewEvent{
			{ID: "00000000-0000-0000-0000-000000000001", Type: "OrderCreated", Payload: json.RawMessage(`{}`)},
			{ID: "00000000-0000-0000-0000-000000000002", Type: "OrderCompleted", Payload: json.RawMessage(`{}`)},
		}))
		require.NoError(t, s.Append(ctx, "order-def", 0, event("00000000-0000-0000-0000-000000000003", "OrderCreated")))

		abc, err := s.ReadStream(ctx, "order-abc", 1, 0)
		require.NoError(t, err)
		def, err := s.ReadStream(ctx, "order-def", 1, 0)
		require.NoError(t, err)

		require.Len(t, abc, 2)
		assert.Equal(t, int64(1), abc[0].Version)
		assert.Equal(t, int64(2), abc[1].Version)
		require.Len(t, def, 1)
		assert.Equal(t, int64(1), def[0].Version)
	})
}

func TestMemoryEventStore_OptimisticConcurrency(t *testing.T) {
	testOptimisticConcurrency(t, func(*testing.T) EventStore { return NewMemoryEventStore() })
}

func TestPostgresEventStore_OptimisticConcurrency(t *testing.T) {
	testOptimisticConcurrency(t, func(t *testing.T) EventStore { return newTestPostgresStore(t) })
}
