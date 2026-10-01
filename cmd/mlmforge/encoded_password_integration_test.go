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

func TestEncodedPassword_ConnectsThroughMigrateAndTree(t *testing.T) {
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}
	const password = "Zm9v@cXV4/d2l0?aHh5#ZXZl" // gitleaks:allow
	const encoded = "Zm9v%40cXV4%2Fd2l0%3FaHh5%23ZXZl"
	n := encodedPasswordSeq.Add(1)
	role := fmt.Sprintf("encoded_role_%d", n)
	dbName := fmt.Sprintf("encoded_db_%d", n)

	admin, err := pgx.Connect(t.Context(), pgContainer.DSN)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	_, err = admin.Exec(t.Context(), "CREATE ROLE "+role+" LOGIN PASSWORD '"+password+"'")
	require.NoError(t, err)
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), "DROP ROLE "+role); err != nil {
			t.Errorf("drop role %s: %v", role, err)
		}
	})
	_, err = admin.Exec(t.Context(), "CREATE DATABASE "+dbName+" OWNER "+role)
	require.NoError(t, err)
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), "DROP DATABASE "+dbName+" WITH (FORCE)"); err != nil {
			t.Errorf("drop database %s: %v", dbName, err)
		}
	})

	u, err := url.Parse(pgContainer.DSN)
	require.NoError(t, err)
	dsn := "postgres://" + role + ":" + encoded + "@" + u.Host + "/" + dbName
	if u.RawQuery != "" {
		dsn += "?" + u.RawQuery
	}

	out, err := runMigrate(t, dsn, "version")
	require.NoError(t, err)
	require.Equal(t, "Version: none, Dirty: false\n", out.stdout.String())

	_, err = runTreeCmd(t, "load", "--db-url", dsn, "--worker", testWorker(t), "--tree-id", "t9", "--tree-type", "unilevel")
	require.Error(t, err)
	require.False(t, strings.HasPrefix(err.Error(), "reach database:"), "the pool did not reach the database: %v", err)
	require.True(t, strings.HasPrefix(err.Error(), "start worker at "), "expected the worker stub to fail after Ping: %v", err)
}
