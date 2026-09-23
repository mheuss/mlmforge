package platform

import (
	"context"
	"encoding/json"
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
