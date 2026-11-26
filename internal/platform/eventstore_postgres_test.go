package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
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

// appNamedPool returns a pool whose sessions carry a unique application_name, and that name.
func appNamedPool(t *testing.T, prefix string) (*pgxpool.Pool, string) {
	t.Helper()
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}
	app := prefix + "-" + uuid.NewString()
	cfg, err := pgxpool.ParseConfig(pgContainer.DSN)
	require.NoError(t, err)
	cfg.ConnConfig.RuntimeParams["application_name"] = app
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool, app
}

// waitForAppLockWaiters polls until exactly n sessions named app wait on a lock, and returns their pids.
func waitForAppLockWaiters(t *testing.T, pool *pgxpool.Pool, app string, n int) []int32 {
	t.Helper()
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var pids []int32
	reads := 0
	for {
		var seen []int32
		err := pool.QueryRow(ctx,
			`SELECT coalesce(array_agg(pid ORDER BY pid), '{}') FROM pg_stat_activity
			 WHERE application_name = $1 AND wait_event_type = 'Lock'`, app).Scan(&seen)
		if err != nil && reads == 0 {
			t.Fatalf("after %s, no pg_stat_activity read for application_name %s succeeded; want %d session(s) waiting on a lock. The read returned: %v",
				time.Since(start).Round(time.Millisecond), app, n, err)
		}
		if err != nil {
			t.Fatalf("after %s, %d session(s) with application_name %s were waiting on a lock (pids %v); want %d. The last pg_stat_activity read returned: %v",
				time.Since(start).Round(time.Millisecond), len(pids), app, pids, n, err)
		}
		reads++
		pids = seen
		if len(pids) == n {
			return pids
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// receiveWithin returns the next value from ch, or fails the test naming what did not arrive.
func receiveWithin[T any](t *testing.T, ch <-chan T, bound time.Duration, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(bound):
		t.Fatalf("no result from %s arrived within %s", what, bound)
		var zero T
		return zero
	}
}

// appendResult is one racing append's event ID and what the append returned.
type appendResult struct {
	id  string
	err error
}

// String formats the result as its ID and the error's text.
func (r appendResult) String() string {
	return fmt.Sprintf("{%s: %v}", r.id, r.err)
}

func TestPostgresAppend_TwoAppendsPastThePreCheckGiveOneConcurrencyError(t *testing.T) {
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}
	base := pgContainer.NewPool(t)
	racePool, app := appNamedPool(t, "heu1113-append")
	store := NewPostgresEventStore(racePool)
	const stream = "race-1"

	blocker, err := base.Begin(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { _ = blocker.Rollback(context.Background()) })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	_, err = blocker.Exec(ctx,
		`INSERT INTO events (id, stream, type, version, payload) VALUES ($1, $2, 'Blocker', 1, '{}')`,
		uuid.NewString(), stream)
	require.NoError(t, err)

	results := make(chan appendResult, 2)
	for _, id := range []string{"00000000-0000-0000-0000-0000000000a1", "00000000-0000-0000-0000-0000000000a2"} {
		go func() {
			err := store.Append(ctx, stream, 0, []NewEvent{{ID: id, Type: "Raced", Payload: json.RawMessage(`{}`)}})
			results <- appendResult{id: id, err: err}
		}()
	}
	waitForAppLockWaiters(t, base, app, 2)
	require.NoError(t, blocker.Rollback(ctx))

	var won, lost []appendResult
	for range 2 {
		r := receiveWithin(t, results, 10*time.Second,
			fmt.Sprintf("an append at expected version 0 (received so far: %v)", slices.Concat(won, lost)))
		if r.err == nil {
			won = append(won, r)
		} else {
			lost = append(lost, r)
		}
	}
	require.Len(t, won, 1, "appends that returned nil: %v; appends that returned an error: %v", won, lost)
	require.Len(t, lost, 1, "appends that returned nil: %v; appends that returned an error: %v", won, lost)
	var ce *ConcurrencyError
	require.ErrorAs(t, lost[0].err, &ce)
	assert.Equal(t, stream, ce.Stream)
	assert.Equal(t, int64(0), ce.ExpectedVersion)

	events, err := NewPostgresEventStore(base).ReadStream(ctx, stream, 0, 0)
	require.NoError(t, err)
	require.Len(t, events, 1, "events in %s after the race", stream)
	assert.Equal(t, won[0].id, events[0].ID)
	assert.Equal(t, int64(1), events[0].Version)
}
