package platform

import (
	"context"
	"testing"

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
