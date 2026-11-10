package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
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

// sameDSN builds the test container's URL unchanged.
func sameDSN(dsn string) string { return dsn }

// openTestTreePool opens the tree pool against the URL url builds and closes it at cleanup.
func openTestTreePool(t *testing.T, url func(dsn string) string) (*pgxpool.Pool, *timeoutRecord) {
	t.Helper()
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}
	rec := &timeoutRecord{}
	pool, err := openTreePool(context.Background(), url(pgContainer.DSN), rec)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool, rec
}

func TestTreePool_AppliesTheDefaultsWhenNothingIsSet(t *testing.T) {
	t.Setenv(statementTimeoutEnv, "")
	t.Setenv(lockTimeoutEnv, "")
	t.Setenv("PGOPTIONS", "")
	pool, rec := openTestTreePool(t, sameDSN)

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
	pool, rec := openTestTreePool(t, func(d string) string {
		return d + "&options=-c%20statement_timeout%3D4321%20-c%20lock_timeout%3D1234"
	})

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
			pool, rec := openTestTreePool(t, tc.url)

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
	t.Setenv(statementTimeoutEnv, "7s")
	t.Setenv(lockTimeoutEnv, "")
	t.Setenv("PGOPTIONS", "")
	pool, rec := openTestTreePool(t, func(string) string { return roleURL(t) })

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
	t.Setenv(statementTimeoutEnv, "")
	t.Setenv(lockTimeoutEnv, "")
	t.Setenv("PGOPTIONS", "")
	pool, rec := openTestTreePool(t, func(string) string { return roleURL(t) })

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
	pool, _ := openTestTreePool(t, sameDSN)
	ctx := context.Background()

	check := func(c *pgxpool.Conn, label string) {
		var statement, lock string
		require.NoError(t, c.QueryRow(ctx,
			"SELECT current_setting('statement_timeout'), current_setting('lock_timeout')").Scan(&statement, &lock))
		assert.Equal(t, defaultStatementTimeout, statement, label)
		assert.Equal(t, defaultLockTimeout, lock, label)
	}

	// Released at cleanup too, so a failed require still lets the pool close.
	acquire := func() *pgxpool.Conn {
		c, err := pool.Acquire(ctx)
		require.NoError(t, err)
		t.Cleanup(c.Release)
		return c
	}

	a := acquire()
	b := acquire()
	check(a, "first of two held at once")
	check(b, "second of two held at once")
	a.Release()
	b.Release()

	reused := pool.Stat().NewConnsCount()
	a = acquire()
	require.Equal(t, reused, pool.Stat().NewConnsCount(), "NewConnsCount changed on the reacquire")
	check(a, "reacquired")
	require.NoError(t, a.Conn().Close(ctx))
	a.Release()

	before := pool.Stat().NewConnsCount()
	var held []*pgxpool.Conn
	for range 2 {
		held = append(held, acquire())
	}
	require.Greater(t, pool.Stat().NewConnsCount(), before,
		"NewConnsCount stayed at %d after acquiring two connections post-close", before)
	for i, c := range held {
		check(c, fmt.Sprintf("after the close, connection %d", i))
		c.Release()
	}
}

func TestMigrateUp_RunsWithoutTheTreeTimeouts(t *testing.T) {
	dsn := newMigrateDatabase(t)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "000001_seen.up.sql"), []byte(
		"CREATE TABLE heu801_seen AS SELECT current_setting('statement_timeout') AS s, current_setting('lock_timeout') AS l;\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "000001_seen.down.sql"), []byte("DROP TABLE heu801_seen;\n"), 0o600))
	t.Setenv(statementTimeoutEnv, "9s")
	t.Setenv(lockTimeoutEnv, "8s")
	t.Setenv("PGOPTIONS", "")

	root := newRootCmd()
	out := &cmdOutput{}
	root.SetOut(&out.stdout)
	root.SetErr(&out.stderr)
	root.SetArgs([]string{"migrate", "up", "--db-url", dsn, "--migrations", dir})
	require.NoError(t, root.Execute(), out.stderr.String())

	var statement, lock string
	require.NoError(t, connectTo(t, dsn).QueryRow(context.Background(),
		"SELECT s, l FROM heu801_seen").Scan(&statement, &lock))
	assert.Equal(t, "0", statement)
	assert.Equal(t, "0", lock)
}

func TestTreePool_AnEnvVarZeroTurnsTheTimeoutOff(t *testing.T) {
	t.Setenv(statementTimeoutEnv, "0")
	t.Setenv(lockTimeoutEnv, "")
	pool, rec := openTestTreePool(t, sameDSN)

	statement, lock := showBoth(t, pool)

	assert.Equal(t, "0", statement)
	assert.Equal(t, defaultLockTimeout, lock)
	got, _ := rec.describe("statement_timeout")
	assert.Equal(t, "statement_timeout is 0 (set by: "+statementTimeoutEnv+")", got)
}
