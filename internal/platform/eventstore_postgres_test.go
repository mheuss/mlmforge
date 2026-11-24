package platform

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
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

// TestPostgresRefusesUnstorableValuesDirectly checks that Postgres itself refuses each listed value, bypassing the store's validators.
func TestPostgresRefusesUnstorableValuesDirectly(t *testing.T) {
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}
	ctx := context.Background()
	insert := func(t *testing.T, stream string, e NewEvent) {
		t.Helper()
		pool := pgContainer.NewPool(t)

		_, err := pool.Exec(ctx, `INSERT INTO events (id, stream, type, version, payload, metadata) VALUES ($1, $2, $3, 1, $4, $5)`,
			e.ID, stream, e.Type, e.Payload, e.Metadata)

		require.ErrorContains(t, err, "(SQLSTATE 22")
		var n int
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM events`).Scan(&n))
		assert.Zero(t, n, "rows in events after a refused insert")
	}

	t.Run("the unedited event is stored", func(t *testing.T) {
		pool := pgContainer.NewPool(t)

		_, err := pool.Exec(ctx, `INSERT INTO events (id, stream, type, version, payload, metadata) VALUES ($1, $2, $3, 1, $4, $5)`,
			"00000000-0000-0000-0000-000000000001", "order-abc", "OrderCreated", json.RawMessage(`{}`), json.RawMessage(nil))

		require.NoError(t, err)
	})
	for _, tc := range unstorableEvents() {
		t.Run(tc.name+" is refused by Postgres itself", func(t *testing.T) {
			insert(t, tc.stream, tc.event)
		})
	}
	t.Run("a clean stream name and category are read", func(t *testing.T) {
		pool := pgContainer.NewPool(t)
		var n int

		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE stream = $1`, "order-abc").Scan(&n))
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE split_part(stream, '-', 1) = $1`, "order").Scan(&n))
	})
	for _, tc := range unstorableNames() {
		t.Run("a stream name holding "+tc.name+" is refused by Postgres itself", func(t *testing.T) {
			pool := pgContainer.NewPool(t)
			var n int

			err := pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE stream = $1`, tc.stream).Scan(&n)

			require.ErrorContains(t, err, "(SQLSTATE 22")
		})
		t.Run("a category holding "+tc.name+" is refused by Postgres itself", func(t *testing.T) {
			pool := pgContainer.NewPool(t)
			var n int

			err := pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE split_part(stream, '-', 1) = $1`, tc.category).Scan(&n)

			require.ErrorContains(t, err, "(SQLSTATE 22")
		})
	}
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
