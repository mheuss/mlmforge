package platform

import (
	"context"
	"fmt"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var resetDatabaseSeq atomic.Int64

// newResetDatabase creates an empty database for one test and drops it when the test ends.
func newResetDatabase(t *testing.T) string {
	t.Helper()
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}
	admin, err := pgx.Connect(t.Context(), pgContainer.DSN)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close(context.Background()) })

	name := fmt.Sprintf("reset_down_case_%d", resetDatabaseSeq.Add(1))
	_, err = admin.Exec(t.Context(), "CREATE DATABASE "+name)
	require.NoError(t, err)
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("drop database %s: %v", name, err)
		}
	})

	u, err := url.Parse(pgContainer.DSN)
	require.NoError(t, err)
	u.Path = "/" + name
	return u.String()
}

// storeRecord creates the version table through MigrateVersion and replaces its row.
func storeRecord(t *testing.T, dsn, dir string, version int, dirty bool) {
	t.Helper()
	_, err := MigrateVersion(context.Background(), dsn, dir, nil)
	require.NoError(t, err)
	conn, err := pgx.Connect(t.Context(), dsn)
	require.NoError(t, err)
	defer func() { _ = conn.Close(context.Background()) }()
	_, err = conn.Exec(t.Context(), "TRUNCATE schema_migrations")
	require.NoError(t, err)
	_, err = conn.Exec(t.Context(), "INSERT INTO schema_migrations (version, dirty) VALUES ($1, $2)", version, dirty)
	require.NoError(t, err)
}

// storedRecord reads the version table through MigrateVersion.
func storedRecord(t *testing.T, dsn, dir string) Record {
	t.Helper()
	st, err := MigrateVersion(context.Background(), dsn, dir, nil)
	require.NoError(t, err)
	return st.Record
}

func TestResetAfterFailedDown_TakesTheNextMigrationFromTheDirectory(t *testing.T) {
	dir := writeMigrations(t, sparseMigrations)
	cases := map[string]struct {
		from int
		want Record
	}{
		"from 2":         {from: 2, want: Record{Version: 7}},
		"from minus one": {from: -1, want: Record{Version: 2}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dsn := newResetDatabase(t)
			storeRecord(t, dsn, dir, tc.from, true)

			res, err := ResetAfterFailedDown(context.Background(), dsn, dir, nil)

			require.NoError(t, err)
			assert.Equal(t, ResetResult{From: Record{Version: tc.from, Dirty: true}, To: tc.want}, res)
			assert.Equal(t, tc.want, storedRecord(t, dsn, dir))
		})
	}
}

func TestResetAfterFailedDown_TheLastMigrationHasNothingAfterIt(t *testing.T) {
	dir := writeMigrations(t, sparseMigrations)
	dsn := newResetDatabase(t)
	storeRecord(t, dsn, dir, 7, true)

	_, err := ResetAfterFailedDown(context.Background(), dsn, dir, nil)

	var noNext *NoNextMigrationError
	require.ErrorAs(t, err, &noNext)
	assert.Equal(t, Record{Version: 7, Dirty: true}, storedRecord(t, dsn, dir))
}

func TestResetAfterFailedDown_RefusesANextMigrationWithNoDownFile(t *testing.T) {
	dir := writeMigrations(t, map[string]string{"2_a.up.sql": "SELECT 1;", "2_a.down.sql": "SELECT 1;", "7_b.up.sql": "SELECT 1;"})
	dsn := newResetDatabase(t)
	storeRecord(t, dsn, dir, 2, true)

	_, err := ResetAfterFailedDown(context.Background(), dsn, dir, nil)

	var noDown *NoDownFileError
	require.ErrorAs(t, err, &noDown)
	assert.Equal(t, NoDownFileError{Record: Record{Version: 2, Dirty: true}, Version: 7, Path: dir}, *noDown)
	assert.Equal(t, Record{Version: 2, Dirty: true}, storedRecord(t, dsn, dir))
}
