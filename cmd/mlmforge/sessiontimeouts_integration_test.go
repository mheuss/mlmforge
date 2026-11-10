package main

import (
	"context"
	"testing"

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
	assert.Contains(t, err.Error(), statementTimeoutEnv+`="abc"`)
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
	client := "the client's connection settings (URL, PGOPTIONS or a service file)"
	got, _ := rec.describe("statement_timeout")
	assert.Equal(t, "statement_timeout is 4321ms (set by: "+client+")", got)
	got, _ = rec.describe("lock_timeout")
	assert.Equal(t, "lock_timeout is 1234ms (set by: "+client+")", got)
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
