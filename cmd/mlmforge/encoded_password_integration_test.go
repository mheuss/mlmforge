package main

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

var encodedPasswordSeq atomic.Int64

// encodedPasswordTarget is a login role and a database it owns, made for one test.
type encodedPasswordTarget struct {
	role     string
	dbName   string
	host     string
	rawQuery string
}

// newEncodedPasswordTarget creates a login role with password and an empty database it owns, and drops both when the test ends.
func newEncodedPasswordTarget(t *testing.T, password string) encodedPasswordTarget {
	t.Helper()
	n := encodedPasswordSeq.Add(1)
	target := encodedPasswordTarget{role: fmt.Sprintf("encoded_role_%d", n), dbName: fmt.Sprintf("encoded_db_%d", n)}

	admin, err := pgx.Connect(t.Context(), pgContainer.DSN)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	_, err = admin.Exec(t.Context(), "CREATE ROLE "+target.role+" LOGIN PASSWORD '"+password+"'")
	require.NoError(t, err)
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), "DROP ROLE "+target.role); err != nil {
			t.Errorf("drop role %s: %v", target.role, err)
		}
	})
	_, err = admin.Exec(t.Context(), "CREATE DATABASE "+target.dbName+" OWNER "+target.role)
	require.NoError(t, err)
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), "DROP DATABASE "+target.dbName+" WITH (FORCE)"); err != nil {
			t.Errorf("drop database %s: %v", target.dbName, err)
		}
	})

	u, err := url.Parse(pgContainer.DSN)
	require.NoError(t, err)
	target.host, target.rawQuery = u.Host, u.RawQuery
	return target
}

// requireMigrateAndTreeConnect fails the test unless migrate version and tree load both reach the database with dsn.
func requireMigrateAndTreeConnect(t *testing.T, dsn string) {
	t.Helper()
	out, err := runMigrate(t, dsn, "version")
	require.NoError(t, err)
	require.Equal(t, "Version: none, Dirty: false\n", out.stdout.String())

	_, err = runTreeCmd(t, "load", "--db-url", dsn, "--worker", testWorker(t), "--tree-id", "t9", "--tree-type", "unilevel")
	require.Error(t, err)
	require.False(t, strings.HasPrefix(err.Error(), "reach database:"), "the pool did not reach the database: %v", err)
	require.True(t, strings.HasPrefix(err.Error(), "start worker at "), "expected the worker stub to fail after Ping: %v", err)
}

func TestEncodedPassword_ConnectsThroughMigrateAndTree(t *testing.T) {
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}
	for _, tc := range []struct {
		name     string
		password string
		dsn      func(target encodedPasswordTarget) string
	}{
		{"userinfo with @ / ? #", "Zm9v@cXV4/d2l0?aHh5#ZXZl", func(target encodedPasswordTarget) string { // gitleaks:allow
			dsn := "postgres://" + target.role + ":Zm9v%40cXV4%2Fd2l0%3FaHh5%23ZXZl@" + target.host + "/" + target.dbName
			if target.rawQuery != "" {
				dsn += "?" + target.rawQuery
			}
			return dsn
		}},
		{"query password last with & and +", "Zm9v&cXV4+eHl6", func(target encodedPasswordTarget) string { // gitleaks:allow
			return queryPasswordLastDSN(target, "Zm9v%26cXV4%2BeHl6")
		}},
		{"query password last ending in &", "Zm9vcXV4&", func(target encodedPasswordTarget) string { // gitleaks:allow
			return queryPasswordLastDSN(target, "Zm9vcXV4%26")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := newEncodedPasswordTarget(t, tc.password)

			requireMigrateAndTreeConnect(t, tc.dsn(target))
		})
	}
}

// queryPasswordLastDSN returns a connection string for target with encoded as the last query key.
func queryPasswordLastDSN(target encodedPasswordTarget, encoded string) string {
	query := "password=" + encoded
	if target.rawQuery != "" {
		query = target.rawQuery + "&" + query
	}
	return "postgres://" + target.role + "@" + target.host + "/" + target.dbName + "?" + query
}
