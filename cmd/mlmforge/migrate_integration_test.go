package main

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/jackc/pgx/v5"
	"github.com/mlmforge/mlmforge/internal/platform"
	"github.com/stretchr/testify/require"
)

var migrateDatabaseSeq atomic.Int64

// newMigrateDatabase creates an empty database for one test case and drops it when the case ends.
func newMigrateDatabase(t *testing.T) string {
	t.Helper()
	if pgContainer == nil {
		t.Skip("Postgres container not available")
	}
	admin, err := pgx.Connect(t.Context(), pgContainer.DSN)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close(context.Background()) })

	name := fmt.Sprintf("migrate_case_%d", migrateDatabaseSeq.Add(1))
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

// withParam returns dsn with one query parameter set.
func withParam(t *testing.T, dsn, key, value string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	q := u.Query()
	q.Set(key, value)
	u.RawQuery = q.Encode()
	return u.String()
}

// runMigrate executes one migrate subcommand against dsn and the repository's migrations.
func runMigrate(t *testing.T, dsn string, args ...string) (*cmdOutput, error) {
	t.Helper()
	root := newRootCmd()
	out := &cmdOutput{}
	root.SetOut(&out.stdout)
	root.SetErr(&out.stderr)
	root.SetArgs(append(append([]string{"migrate"}, args...),
		"--db-url", dsn, "--migrations", platform.FindMigrationsDir(t)))
	return out, root.Execute()
}

// migrateTo moves dsn's schema to version with golang-migrate directly.
func migrateTo(t *testing.T, dsn string, version uint) {
	t.Helper()
	m, err := migrate.New("file://"+platform.FindMigrationsDir(t), dsn)
	require.NoError(t, err)
	defer func() { _, _ = m.Close() }()
	require.NoError(t, m.Migrate(version))
}

// connectTo opens a connection to dsn that closes when the case ends.
func connectTo(t *testing.T, dsn string) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(t.Context(), dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

// setRecord replaces the migration record with one row.
func setRecord(t *testing.T, dsn string, version int, dirty bool) {
	t.Helper()
	conn := connectTo(t, dsn)
	_, err := conn.Exec(t.Context(), "TRUNCATE schema_migrations")
	require.NoError(t, err)
	_, err = conn.Exec(t.Context(), "INSERT INTO schema_migrations (version, dirty) VALUES ($1, $2)", version, dirty)
	require.NoError(t, err)
}

const dirtySixText = "The record reads 6, dirty.\n" +
	"If the command that failed was `mlmforge migrate up`: fix the cause shown in its error, " +
	"run `mlmforge migrate reset-dirty` (it sets the record to 5, clean), then run `mlmforge migrate up`.\n" +
	"If the command that failed was `mlmforge migrate down`: do not run `mlmforge migrate reset-dirty`."

func TestMigrateVersion_ADirtyRecordPrintsItAndBothRecoveryPaths(t *testing.T) {
	dsn := newMigrateDatabase(t)
	migrateTo(t, dsn, 5)
	setRecord(t, dsn, 6, true)

	out, err := runMigrate(t, dsn, "version")

	require.NoError(t, err, out.stderr.String())
	require.Equal(t, "Version: 6, Dirty: true\n"+dirtySixText+"\n", out.stdout.String())
}

func TestMigrateVersion_AStoredMinusOneDirtyRecordPrintsAsStored(t *testing.T) {
	dsn := newMigrateDatabase(t)
	migrateTo(t, dsn, 1)
	setRecord(t, dsn, -1, true)

	out, err := runMigrate(t, dsn, "version")

	require.NoError(t, err, out.stderr.String())
	require.Equal(t, "Version: -1, Dirty: true\n"+
		"The record reads -1, dirty. reset-dirty does not change a record at -1.\n", out.stdout.String())
}

func TestMigrateVersion_ATableWithNoRowPrintsNone(t *testing.T) {
	dsn := newMigrateDatabase(t)

	out, err := runMigrate(t, dsn, "version")

	require.NoError(t, err, out.stderr.String())
	require.Equal(t, "Version: none, Dirty: false\n", out.stdout.String())
}

func TestMigrateVersion_ACleanRecordPrintsOnlyTheVersionLine(t *testing.T) {
	dsn := newMigrateDatabase(t)
	migrateTo(t, dsn, 5)

	out, err := runMigrate(t, dsn, "version")

	require.NoError(t, err, out.stderr.String())
	require.Equal(t, "Version: 5, Dirty: false\n", out.stdout.String())
}

func TestMigrateVersion_RunsWithMultiStatementModeOn(t *testing.T) {
	dsn := newMigrateDatabase(t)
	migrateTo(t, dsn, 5)

	out, err := runMigrate(t, withParam(t, dsn, "x-multi-statement", "true"), "version")

	require.NoError(t, err, out.stderr.String())
	require.Equal(t, "Version: 5, Dirty: false\n", out.stdout.String())
}

// readRecord returns each row of schema_migrations as "version,dirty".
func readRecord(t *testing.T, dsn string) []string {
	t.Helper()
	conn := connectTo(t, dsn)
	rows, err := conn.Query(t.Context(), "SELECT version, dirty FROM schema_migrations")
	require.NoError(t, err)
	defer rows.Close()
	var got []string
	for rows.Next() {
		var version int64
		var dirty bool
		require.NoError(t, rows.Scan(&version, &dirty))
		got = append(got, fmt.Sprintf("%d,%t", version, dirty))
	}
	require.NoError(t, rows.Err())
	return got
}

// insertActiveRoots writes two active depth-0 rows into one tree.
func insertActiveRoots(t *testing.T, dsn, tree string) {
	t.Helper()
	conn := connectTo(t, dsn)
	_, err := conn.Exec(t.Context(), `INSERT INTO tree_nodes (tree_id, user_id, depth, enrolled_at)
		VALUES ($1, $2, 0, now()), ($1, $3, 0, now())`, tree, testUserID(1), testUserID(2))
	require.NoError(t, err)
}

func TestMigrateUp_ADirtyRecordNamesBothRecoveryPathsAndNeverSaysForce(t *testing.T) {
	dsn := newMigrateDatabase(t)
	migrateTo(t, dsn, 5)
	setRecord(t, dsn, 6, true)

	out, err := runMigrate(t, dsn, "up")

	require.Error(t, err)
	require.Equal(t, "Error: migrate up did not run. "+dirtySixText+"\n", out.stderr.String())
	require.NotContains(t, strings.ToLower(out.stdout.String()+out.stderr.String()), "force")
	require.Equal(t, []string{"6,true"}, readRecord(t, dsn))
}

func TestMigrateUp_AFailedApplyShowsThePostgresDetailThenTheRecord(t *testing.T) {
	dsn := newMigrateDatabase(t)
	migrateTo(t, dsn, 5)
	tree := testTreeID(830)
	insertActiveRoots(t, dsn, tree)

	out, err := runMigrate(t, dsn, "up")

	require.Error(t, err)
	stderr := out.stderr.String()
	detail := "Key (tree_id)=(" + tree + ") is duplicated"
	recovery := "This run was `mlmforge migrate up`. The record now reads 6, dirty.\n" +
		"Fix the cause shown above, run `mlmforge migrate reset-dirty` (it sets the record to 5, clean), " +
		"then run `mlmforge migrate up`.\n"
	require.Contains(t, stderr, detail)
	require.True(t, strings.HasSuffix(stderr, recovery), "stderr: %s", stderr)
	require.Less(t, strings.Index(stderr, detail), strings.Index(stderr, recovery))
	require.NotContains(t, out.stdout.String()+stderr, "Usage:")
	require.Equal(t, []string{"6,true"}, readRecord(t, dsn))
}
