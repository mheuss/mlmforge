package testutil_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mlmforge/mlmforge/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var migrationContainer *testutil.PostgresContainer

func TestMain(m *testing.M) {
	var err error
	migrationContainer, err = testutil.StartPostgres()
	if err != nil {
		// Skip in environments without Docker; the test below will t.Skip.
		fmt.Fprintf(os.Stderr, "Postgres container unavailable: %v\n", err)
	}
	code := m.Run()
	if migrationContainer != nil {
		migrationContainer.Terminate()
	}
	os.Exit(code)
}

func TestMigrations_QualificationHistoryTableExists(t *testing.T) {
	if migrationContainer == nil {
		t.Skip("Postgres container not available")
	}
	pool, err := pgxpool.New(context.Background(), migrationContainer.DSN)
	require.NoError(t, err)
	defer pool.Close()

	var exists bool
	err = pool.QueryRow(context.Background(),
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables
                        WHERE table_name = 'qualification_history')`,
	).Scan(&exists)
	require.NoError(t, err)
	assert.True(t, exists, "qualification_history table should exist after migrations")
}
