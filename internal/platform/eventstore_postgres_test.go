package platform

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func newTestPostgresStore(t *testing.T) *PostgresEventStore {
	t.Helper()
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}
	pool := pgContainer.NewPool(t)
	return NewPostgresEventStore(pool)
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

func TestMigrateUpIdempotent(t *testing.T) {
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}

	// The container already has migrations applied by StartPostgres.
	// Running MigrateUp again should not error (ErrNoChange is swallowed).
	migrationsPath := FindMigrationsDir(t)
	err := MigrateUp(context.Background(), pgContainer.DSN, migrationsPath, nil)
	require.NoError(t, err)
}
