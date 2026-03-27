package platform

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestPostgresStore(t *testing.T) *PostgresEventStore {
	t.Helper()

	dsn := os.Getenv("EVENTSTORE_TEST_DSN")
	if dsn == "" {
		t.Skip("EVENTSTORE_TEST_DSN not set, skipping PostgreSQL integration tests")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(func() { pool.Close() })

	// Drop existing tables and apply migrations for a clean slate.
	_, _ = pool.Exec(ctx, "DROP TABLE IF EXISTS schema_migrations, tree_nodes, events")
	cleanup := runMigrationsForTest(t, dsn)
	t.Cleanup(cleanup)

	// Truncate events for test isolation (migrations create the table,
	// but previous test data may linger within the same migration cycle).
	_, err = pool.Exec(ctx, "TRUNCATE events RESTART IDENTITY")
	require.NoError(t, err)

	return NewPostgresEventStore(pool)
}

func TestPostgresEventStore_AppendAndReadBack(t *testing.T) {
	store := newTestPostgresStore(t)
	ctx := context.Background()

	events := []NewEvent{
		{
			ID:      "00000000-0000-0000-0000-000000000001",
			Type:    "OrderCompleted",
			Payload: json.RawMessage(`{"order_id":"abc"}`),
		},
	}

	err := store.Append(ctx, "order-abc", 0, events)
	require.NoError(t, err)

	got, err := store.ReadStream(ctx, "order-abc", 1, 0)
	require.NoError(t, err)
	require.Len(t, got, 1)

	assert.Equal(t, "00000000-0000-0000-0000-000000000001", got[0].ID)
	assert.Equal(t, "order-abc", got[0].Stream)
	assert.Equal(t, "OrderCompleted", got[0].Type)
	assert.Equal(t, int64(1), got[0].Version)
	assert.True(t, got[0].GlobalPosition > 0)
	assert.JSONEq(t, `{"order_id":"abc"}`, string(got[0].Payload))
	assert.False(t, got[0].Timestamp.IsZero())
}

func TestPostgresEventStore_ConcurrencyConflict(t *testing.T) {
	store := newTestPostgresStore(t)
	ctx := context.Background()

	err := store.Append(ctx, "order-abc", 0, []NewEvent{
		{ID: "00000000-0000-0000-0000-000000000001", Type: "OrderCreated", Payload: json.RawMessage(`{}`)},
	})
	require.NoError(t, err)

	err = store.Append(ctx, "order-abc", 0, []NewEvent{
		{ID: "00000000-0000-0000-0000-000000000002", Type: "OrderCompleted", Payload: json.RawMessage(`{}`)},
	})
	require.Error(t, err)

	var concErr *ConcurrencyError
	require.ErrorAs(t, err, &concErr)
	assert.Equal(t, "order-abc", concErr.Stream)
}

func TestPostgresEventStore_SkipVersionCheck(t *testing.T) {
	store := newTestPostgresStore(t)
	ctx := context.Background()

	err := store.Append(ctx, "order-abc", 0, []NewEvent{
		{ID: "00000000-0000-0000-0000-000000000001", Type: "OrderCreated", Payload: json.RawMessage(`{}`)},
	})
	require.NoError(t, err)

	err = store.Append(ctx, "order-abc", -1, []NewEvent{
		{ID: "00000000-0000-0000-0000-000000000002", Type: "OrderCompleted", Payload: json.RawMessage(`{}`)},
	})
	require.NoError(t, err)

	got, err := store.ReadStream(ctx, "order-abc", 1, 0)
	require.NoError(t, err)
	assert.Len(t, got, 2)
}

func TestPostgresEventStore_ReadCategory(t *testing.T) {
	store := newTestPostgresStore(t)
	ctx := context.Background()

	_ = store.Append(ctx, "order-abc", 0, []NewEvent{
		{ID: "00000000-0000-0000-0000-000000000001", Type: "OrderCreated", Payload: json.RawMessage(`{}`)},
	})
	_ = store.Append(ctx, "order-def", 0, []NewEvent{
		{ID: "00000000-0000-0000-0000-000000000002", Type: "OrderCreated", Payload: json.RawMessage(`{}`)},
	})
	_ = store.Append(ctx, "autoship-xyz", 0, []NewEvent{
		{ID: "00000000-0000-0000-0000-000000000003", Type: "AutoshipCreated", Payload: json.RawMessage(`{}`)},
	})

	got, err := store.ReadCategory(ctx, "order", 0, 0)
	require.NoError(t, err)
	require.Len(t, got, 2)
}

func TestPostgresEventStore_ReadStreamFromVersion(t *testing.T) {
	store := newTestPostgresStore(t)
	ctx := context.Background()

	_ = store.Append(ctx, "order-abc", 0, []NewEvent{
		{ID: "00000000-0000-0000-0000-000000000001", Type: "OrderCreated", Payload: json.RawMessage(`{}`)},
		{ID: "00000000-0000-0000-0000-000000000002", Type: "OrderUpdated", Payload: json.RawMessage(`{}`)},
		{ID: "00000000-0000-0000-0000-000000000003", Type: "OrderCompleted", Payload: json.RawMessage(`{}`)},
	})

	got, err := store.ReadStream(ctx, "order-abc", 2, 0)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, int64(2), got[0].Version)
	assert.Equal(t, int64(3), got[1].Version)
}

func TestPostgresEventStore_MultipleEventsAtomicAppend(t *testing.T) {
	store := newTestPostgresStore(t)
	ctx := context.Background()

	events := []NewEvent{
		{ID: "00000000-0000-0000-0000-000000000001", Type: "OrderCreated", Payload: json.RawMessage(`{}`)},
		{ID: "00000000-0000-0000-0000-000000000002", Type: "OrderCompleted", Payload: json.RawMessage(`{}`)},
	}

	err := store.Append(ctx, "order-abc", 0, events)
	require.NoError(t, err)

	got, err := store.ReadStream(ctx, "order-abc", 1, 0)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, int64(1), got[0].Version)
	assert.Equal(t, int64(2), got[1].Version)
}

func TestMigrateUpIdempotent(t *testing.T) {
	dsn := os.Getenv("EVENTSTORE_TEST_DSN")
	if dsn == "" {
		t.Skip("EVENTSTORE_TEST_DSN not set, skipping migration tests")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(func() { pool.Close() })

	_, _ = pool.Exec(ctx, "DROP TABLE IF EXISTS schema_migrations, tree_nodes, events")

	cleanup := runMigrationsForTest(t, dsn)
	defer cleanup()

	// Running MigrateUp again should not error (ErrNoChange is swallowed).
	migrationsPath := findMigrationsDir(t)
	err = MigrateUp(dsn, migrationsPath)
	require.NoError(t, err)
}
