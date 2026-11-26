package networkengine

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// repeatableReadStore returns a tree store whose pool defaults to REPEATABLE READ, and the pool's application_name.
func repeatableReadStore(t *testing.T) (*PostgresTreeStore, string) {
	t.Helper()
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}
	// Removing this call leaves earlier tests' rows in the tables.
	pgContainer.NewPool(t)
	app := "heu1113-rr-" + uuid.NewString()
	cfg, err := pgxpool.ParseConfig(pgContainer.DSN)
	require.NoError(t, err)
	cfg.ConnConfig.RuntimeParams["default_transaction_isolation"] = "repeatable read"
	cfg.ConnConfig.RuntimeParams["application_name"] = app
	cfg.MaxConns = 8
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var def string
	require.NoError(t, pool.QueryRow(ctx, "SHOW default_transaction_isolation").Scan(&def))
	require.Equal(t, "repeatable read", def,
		"SHOW default_transaction_isolation on the pool built with default_transaction_isolation=repeatable read")
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	var level string
	require.NoError(t, tx.QueryRow(ctx, "SHOW transaction_isolation").Scan(&level))
	require.Equal(t, "repeatable read", level,
		"SHOW transaction_isolation in a transaction begun with no options on the pool built with default_transaction_isolation=repeatable read")
	require.NoError(t, tx.Rollback(ctx))
	return NewPostgresTreeStore(pool), app
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

func TestRepeatableReadStore_RunsRepeatableRead(t *testing.T) {
	repeatableReadStore(t)
}
