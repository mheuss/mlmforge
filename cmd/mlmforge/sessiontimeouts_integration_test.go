package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mlmforge/mlmforge/internal/networkengine"
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

// holdTx opens a transaction for a test to hold locks in.
func holdTx(t *testing.T) pgx.Tx {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, pgContainer.DSN)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close(ctx) })
	tx, err := conn.Begin(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback(ctx) })
	_, err = tx.Exec(ctx, "SET LOCAL idle_in_transaction_session_timeout = '15s'")
	require.NoError(t, err)
	return tx
}

// holdProjectionRow locks the tree's tree_projections row until the test ends.
func holdProjectionRow(t *testing.T, treeID string) {
	t.Helper()
	var v int64
	require.NoError(t, holdTx(t).QueryRow(context.Background(),
		"SELECT projected_version FROM tree_projections WHERE tree_id = $1 FOR UPDATE", treeID).Scan(&v))
}

// holdUserRowInsert inserts an active row for userID in treeID and holds it uncommitted until the test ends.
func holdUserRowInsert(t *testing.T, treeID, userID, parentID string) {
	t.Helper()
	_, err := holdTx(t).Exec(context.Background(), `INSERT INTO tree_nodes (id, tree_id, user_id, parent_id, sponsor_id, depth, enrolled_at)
VALUES (gen_random_uuid(), $1, $2, $3, $3, 1, now())`, treeID, userID, parentID)
	require.NoError(t, err)
}

var placeTimeoutCases = map[string]struct {
	statement, lock, want string
}{
	"statement_timeout": {"500ms", "0", "(the server canceled the statement: statement_timeout is 500ms (set by: " + statementTimeoutEnv + "))"},
	"lock_timeout":      {"0", "500ms", "(the server canceled the statement: lock_timeout is 500ms (set by: " + lockTimeoutEnv + "))"},
}

// placeAgainstAHeldLock runs tree place while hold's lock blocks it.
func placeAgainstAHeldLock(t *testing.T, treeID, statement, lock string, hold func(root string)) (*cmdOutput, error) {
	t.Helper()
	pgContainer.NewPool(t)
	worker := requireWorker(t)
	t.Setenv("PGOPTIONS", "")
	t.Setenv(statementTimeoutEnv, "")
	t.Setenv(lockTimeoutEnv, "")
	seedTree(t, worker, treeID, 0)
	root := testUserID(1)
	hold(root)
	t.Setenv(statementTimeoutEnv, statement)
	t.Setenv(lockTimeoutEnv, lock)
	return runTreeCmd(t, "place", "--db-url", pgContainer.DSN, "--worker", worker, "--tree-id", treeID,
		"--user-id", testUserID(2), "--parent-id", root, "--sponsor-id", root,
		"--enrolled-at", "2026-09-23T13:00:00Z")
}

func TestTreePlace_ARedeliveryTimeoutNamesItsSource(t *testing.T) {
	for name, tc := range placeTimeoutCases {
		t.Run(name, func(t *testing.T) {
			if pgContainer == nil {
				t.Skip("Postgres container not available")
			}
			treeID := testTreeID(801)
			out, err := placeAgainstAHeldLock(t, treeID, tc.statement, tc.lock, func(string) {
				holdProjectionRow(t, treeID)
			})

			require.Error(t, err, "stdout: %s stderr: %s", out.stdout.String(), out.stderr.String())
			var cu *networkengine.CatchUpFailedError
			require.ErrorAs(t, err, &cu)
			assert.Contains(t, err.Error(), tc.want)
			assert.NotContains(t, out.stdout.String(), "appended event")
			assert.NotContains(t, out.stderr.String(), "appended and its projection returned an error")
			ctx := context.Background()
			conn, err := pgx.Connect(ctx, pgContainer.DSN)
			require.NoError(t, err)
			t.Cleanup(func() { _ = conn.Close(ctx) })
			var top int64
			require.NoError(t, conn.QueryRow(ctx,
				"SELECT max(version) FROM events WHERE stream = $1", "tree-"+treeID).Scan(&top))
			assert.Equal(t, int64(1), top)
		})
	}
}

func TestTreePlace_AProjectionTimeoutNamesItsSource(t *testing.T) {
	for name, tc := range placeTimeoutCases {
		t.Run(name, func(t *testing.T) {
			if pgContainer == nil {
				t.Skip("Postgres container not available")
			}
			treeID := testTreeID(802)
			out, err := placeAgainstAHeldLock(t, treeID, tc.statement, tc.lock, func(root string) {
				holdUserRowInsert(t, treeID, testUserID(2), root)
			})

			require.Error(t, err, "stdout: %s stderr: %s", out.stdout.String(), out.stderr.String())
			var ec *exitCodeError
			require.ErrorAs(t, err, &ec)
			assert.Equal(t, exitNotCurrent, ec.code)
			stderr := out.stderr.String()
			_, warning, found := strings.Cut(stderr, "appended and its projection returned an error: ")
			require.True(t, found, "stderr: %s", stderr)
			warning, _, _ = strings.Cut(warning, "\n")
			assert.Contains(t, warning, tc.want)
			assert.NotContains(t, stderr, "could not be read")
		})
	}
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

func TestMeasureTimeoutHookCost(t *testing.T) {
	if os.Getenv("MLMFORGE_MEASURE_TIMEOUTS") != "1" {
		t.Skip("set MLMFORGE_MEASURE_TIMEOUTS=1 to measure the session-timeout hook")
	}
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}
	t.Setenv("PGOPTIONS", "")
	t.Setenv(statementTimeoutEnv, "")
	t.Setenv(lockTimeoutEnv, "")
	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(pgContainer.DSN)
	require.NoError(t, err)
	const runs = 100
	var hook []time.Duration
	for range runs {
		conn, err := pgx.ConnectConfig(ctx, cfg.ConnConfig)
		require.NoError(t, err)
		start := time.Now()
		require.NoError(t, (&timeoutRecord{}).afterConnect(ctx, conn))
		hook = append(hook, time.Since(start))
		require.NoError(t, conn.Close(ctx))
	}
	slices.Sort(hook)
	t.Logf("hook cost over %d new connections: median %s, p95 %s, max %s",
		runs, hook[runs/2], hook[runs*95/100], hook[runs-1])
}
