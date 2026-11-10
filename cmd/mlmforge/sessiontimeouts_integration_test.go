package main

import (
	"context"
	"fmt"
	"net/url"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// showBoth reads both settings on one pooled connection.
func showBoth(t *testing.T, pool *pgxpool.Pool) (statement, lock string) {
	t.Helper()
	require.NoError(t, pool.QueryRow(context.Background(),
		"SELECT current_setting('statement_timeout'), current_setting('lock_timeout')").Scan(&statement, &lock))
	return statement, lock
}

// openTestTreePool opens the tree pool against url and closes it at cleanup.
func openTestTreePool(t *testing.T, url string) (*pgxpool.Pool, *timeoutRecord) {
	t.Helper()
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}
	rec := &timeoutRecord{}
	pool, err := openTreePool(context.Background(), url, rec)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool, rec
}

func TestTreePool_AppliesTheDefaultsWhenNothingIsSet(t *testing.T) {
	t.Setenv(statementTimeoutEnv, "")
	t.Setenv(lockTimeoutEnv, "")
	t.Setenv("PGOPTIONS", "")
	pool, rec := openTestTreePool(t, pgContainer.DSN)

	statement, lock := showBoth(t, pool)

	assert.Equal(t, defaultStatementTimeout, statement)
	assert.Equal(t, defaultLockTimeout, lock)
	got, _ := rec.describe("statement_timeout")
	assert.Equal(t, "statement_timeout is "+defaultStatementTimeout+" (set by: mlmforge default)", got)
	got, _ = rec.describe("lock_timeout")
	assert.Equal(t, "lock_timeout is "+defaultLockTimeout+" (set by: mlmforge default)", got)
}

func TestTreeDeps_ARejectedEnvValueFailsAtConnectAndNamesIt(t *testing.T) {
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}
	worker := requireWorker(t)
	t.Setenv(statementTimeoutEnv, "abc")
	t.Setenv(lockTimeoutEnv, "")

	deps, err := openTreeDeps(context.Background(), pgContainer.DSN, worker)

	if deps != nil {
		_ = deps.release()
	}
	require.Error(t, err)
	t.Logf("observed error: %v", err)
	assert.Contains(t, err.Error(), "writing "+statementTimeoutEnv+`="abc"`)
	assert.Contains(t, err.Error(), "reach database")
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	assert.Equal(t, "22023", pgErr.Code)
}

func TestTreePool_ClientSettingsForBothAreKeptWithNoWrite(t *testing.T) {
	t.Setenv(statementTimeoutEnv, "9s")
	t.Setenv(lockTimeoutEnv, "8s")
	pool, rec := openTestTreePool(t, pgContainer.DSN+"&options=-c%20statement_timeout%3D4321%20-c%20lock_timeout%3D1234")

	statement, lock := showBoth(t, pool)

	assert.Equal(t, "4321ms", statement)
	assert.Equal(t, "1234ms", lock)
	got, _ := rec.describe("statement_timeout")
	assert.Equal(t, "statement_timeout is 4321ms (set by: "+clientSource+")", got)
	got, _ = rec.describe("lock_timeout")
	assert.Equal(t, "lock_timeout is 1234ms (set by: "+clientSource+")", got)
}

const clientSource = "the client's connection settings (URL, PGOPTIONS or a service file)"

func TestTreePool_AClientSettingWinsOverTheEnvVar(t *testing.T) {
	cases := map[string]struct {
		url       func(string) string
		pgoptions string
	}{
		"URL key":     {url: func(d string) string { return d + "&statement_timeout=4321" }},
		"URL options": {url: func(d string) string { return d + "&options=-c%20statement_timeout%3D4321" }},
		"PGOPTIONS":   {url: func(d string) string { return d }, pgoptions: "-c statement_timeout=4321"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv(statementTimeoutEnv, "9s")
			t.Setenv(lockTimeoutEnv, "")
			t.Setenv("PGOPTIONS", tc.pgoptions)
			pool, rec := openTestTreePool(t, tc.url(pgContainer.DSN))

			statement, lock := showBoth(t, pool)

			assert.Equal(t, "4321ms", statement)
			assert.Equal(t, defaultLockTimeout, lock)
			got, _ := rec.describe("statement_timeout")
			assert.Equal(t, "statement_timeout is 4321ms (set by: "+clientSource+")", got)
			got, _ = rec.describe("lock_timeout")
			assert.Equal(t, "lock_timeout is "+defaultLockTimeout+" (set by: mlmforge default)", got)
		})
	}
}

// roleURL creates a login role with statement_timeout set on it, and returns a URL for it.
func roleURL(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, pgContainer.DSN)
	require.NoError(t, err)
	defer func() { _ = admin.Close(ctx) }()
	_, err = admin.Exec(ctx, "CREATE ROLE heu801_timeouts LOGIN PASSWORD 'heu801pw'")
	require.NoError(t, err)
	t.Cleanup(func() {
		c, err := pgx.Connect(ctx, pgContainer.DSN)
		if err != nil {
			t.Errorf("drop role heu801_timeouts: connect: %v", err)
			return
		}
		defer func() { _ = c.Close(ctx) }()
		if _, err := c.Exec(ctx, "DROP ROLE heu801_timeouts"); err != nil {
			t.Errorf("drop role heu801_timeouts: %v", err)
		}
	})
	_, err = admin.Exec(ctx, "ALTER ROLE heu801_timeouts SET statement_timeout = 4444")
	require.NoError(t, err)
	u, err := url.Parse(pgContainer.DSN)
	require.NoError(t, err)
	u.User = url.UserPassword("heu801_timeouts", "heu801pw")
	return u.String()
}

func TestTreePool_TheEnvVarWinsOverARoleSetting(t *testing.T) {
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}
	t.Setenv(statementTimeoutEnv, "7s")
	t.Setenv(lockTimeoutEnv, "")
	t.Setenv("PGOPTIONS", "")
	pool, rec := openTestTreePool(t, roleURL(t))

	var user string
	require.NoError(t, pool.QueryRow(context.Background(), "SELECT current_user").Scan(&user))
	require.Equal(t, "heu801_timeouts", user)
	statement, lock := showBoth(t, pool)

	assert.Equal(t, "7s", statement)
	assert.Equal(t, defaultLockTimeout, lock)
	got, _ := rec.describe("statement_timeout")
	assert.Equal(t, "statement_timeout is 7s (set by: "+statementTimeoutEnv+")", got)
}

func TestTreePool_ARoleSettingWinsOverTheDefault(t *testing.T) {
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}
	t.Setenv(statementTimeoutEnv, "")
	t.Setenv(lockTimeoutEnv, "")
	t.Setenv("PGOPTIONS", "")
	pool, rec := openTestTreePool(t, roleURL(t))

	statement, lock := showBoth(t, pool)

	assert.Equal(t, "4444ms", statement)
	assert.Equal(t, defaultLockTimeout, lock)
	got, _ := rec.describe("statement_timeout")
	assert.Equal(t, "statement_timeout is 4444ms (set by: the database role or database settings)", got)
}

func TestTreePool_EveryConnectionCarriesTheSettings(t *testing.T) {
	t.Setenv(statementTimeoutEnv, "")
	t.Setenv(lockTimeoutEnv, "")
	t.Setenv("PGOPTIONS", "")
	pool, _ := openTestTreePool(t, pgContainer.DSN)
	ctx := context.Background()

	check := func(c *pgxpool.Conn, label string) {
		var statement, lock string
		require.NoError(t, c.QueryRow(ctx,
			"SELECT current_setting('statement_timeout'), current_setting('lock_timeout')").Scan(&statement, &lock))
		assert.Equal(t, defaultStatementTimeout, statement, label)
		assert.Equal(t, defaultLockTimeout, lock, label)
	}

	a, err := pool.Acquire(ctx)
	require.NoError(t, err)
	b, err := pool.Acquire(ctx)
	require.NoError(t, err)
	check(a, "first of two held at once")
	check(b, "second of two held at once")
	a.Release()
	b.Release()

	a, err = pool.Acquire(ctx)
	require.NoError(t, err)
	check(a, "reacquired")
	require.NoError(t, a.Conn().Close(ctx))
	a.Release()

	before := pool.Stat().NewConnsCount()
	var held []*pgxpool.Conn
	for range 2 {
		c, err := pool.Acquire(ctx)
		require.NoError(t, err)
		held = append(held, c)
	}
	require.Greater(t, pool.Stat().NewConnsCount(), before, "no replacement connection was opened")
	for i, c := range held {
		check(c, fmt.Sprintf("after the close, connection %d", i))
		c.Release()
	}
}

func TestTreePool_AnEnvVarZeroTurnsTheTimeoutOff(t *testing.T) {
	t.Setenv(statementTimeoutEnv, "0")
	t.Setenv(lockTimeoutEnv, "")
	pool, rec := openTestTreePool(t, pgContainer.DSN)

	statement, lock := showBoth(t, pool)

	assert.Equal(t, "0", statement)
	assert.Equal(t, defaultLockTimeout, lock)
	got, _ := rec.describe("statement_timeout")
	assert.Equal(t, "statement_timeout is 0 (set by: "+statementTimeoutEnv+")", got)
}
