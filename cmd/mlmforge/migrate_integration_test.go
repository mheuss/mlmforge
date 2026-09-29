package main

import (
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
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

// insertActiveRoots writes two depth-0 rows with no removed_at into one tree.
func insertActiveRoots(t *testing.T, dsn, tree string) {
	t.Helper()
	conn := connectTo(t, dsn)
	_, err := conn.Exec(t.Context(), `INSERT INTO tree_nodes (tree_id, user_id, depth, enrolled_at, removed_at)
		VALUES ($1, $2, 0, now(), NULL), ($1, $3, 0, now(), NULL)`, tree, testUserID(1), testUserID(2))
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
	require.NotContains(t, stderr, "One active root per tree", "the migration file body must not be echoed")
	require.True(t, strings.HasSuffix(stderr, recovery), "stderr: %s", stderr)
	require.Less(t, strings.Index(stderr, detail), strings.Index(stderr, recovery))
	require.NotContains(t, out.stdout.String()+stderr, "Usage:")
	require.Equal(t, []string{"6,true"}, readRecord(t, dsn))
}

func TestMigrateDown_AFailedRollbackNamesTheRecordBeforeAndAfter(t *testing.T) {
	dsn := newMigrateDatabase(t)
	migrateTo(t, dsn, 8)

	blocker, err := pgx.Connect(t.Context(), dsn)
	require.NoError(t, err)
	tx, err := blocker.Begin(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = tx.Rollback(context.Background())
		_ = blocker.Close(context.Background())
	})
	_, err = tx.Exec(t.Context(), "LOCK TABLE tree_nodes IN ACCESS EXCLUSIVE MODE")
	require.NoError(t, err)

	out, err := runMigrate(t, withParam(t, dsn, "lock_timeout", "500"), "down")

	require.Error(t, err)
	require.True(t, strings.HasPrefix(out.stderr.String(), "Error: rollback migration: "), "stderr: %s", out.stderr.String())
	require.True(t, strings.HasSuffix(out.stderr.String(),
		"This run was `mlmforge migrate down`. The record read 8, clean before this run and now reads 7, dirty.\n"+
			"Do not run `mlmforge migrate reset-dirty`. It would set the record to 6, clean.\n"),
		"stderr: %s", out.stderr.String())
	require.NotContains(t, out.stdout.String()+out.stderr.String(), "Usage:")
	require.Equal(t, []string{"7,true"}, readRecord(t, dsn))
}

func TestMigrateDown_AnEmptyRecordHasNothingToRollBack(t *testing.T) {
	dsn := newMigrateDatabase(t)

	out, err := runMigrate(t, dsn, "down")

	require.NoError(t, err, out.stderr.String())
	require.Equal(t, "No migrations to roll back.\n", out.stdout.String())
	require.Empty(t, out.stderr.String())
}

func TestMigrateResetDirty_MovesADirtyRecordBackOneMigration(t *testing.T) {
	dsn := newMigrateDatabase(t)
	migrateTo(t, dsn, 5)
	setRecord(t, dsn, 6, true)

	out, err := runMigrate(t, dsn, "reset-dirty")

	require.NoError(t, err, out.stderr.String())
	require.Equal(t, "The record read 6, dirty. This run set it to 5, clean.\nRun `mlmforge migrate up` next.\n", out.stdout.String())
	require.Empty(t, out.stderr.String())
	require.Equal(t, []string{"5,false"}, readRecord(t, dsn))
}

func TestMigrateResetDirty_FromTheFirstMigrationLeavesNoVersion(t *testing.T) {
	dsn := newMigrateDatabase(t)
	migrateTo(t, dsn, 1)
	setRecord(t, dsn, 1, true)

	out, err := runMigrate(t, dsn, "reset-dirty")

	require.NoError(t, err, out.stderr.String())
	require.Equal(t, "The record read 1, dirty. This run set it to no version.\nRun `mlmforge migrate up` next.\n", out.stdout.String())
	require.Empty(t, out.stderr.String())
	require.Empty(t, readRecord(t, dsn))
}

func TestMigrateResetDirty_RefusesARecordItCannotActOnAndLeavesItUnchanged(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(t *testing.T, dsn string)
		stderr string
		record []string
	}{
		{
			name:   "clean",
			setup:  func(t *testing.T, dsn string) { migrateTo(t, dsn, 5) },
			stderr: "Error: reset-dirty changes only a dirty record. The record reads 5, clean. The record was not changed.\n",
			record: []string{"5,false"},
		},
		{
			name:   "no version",
			setup:  func(t *testing.T, dsn string) {},
			stderr: "Error: reset-dirty changes only a dirty record. The record holds no version. The record was not changed.\n",
			record: nil,
		},
		{
			name:   "minus one dirty",
			setup:  func(t *testing.T, dsn string) { migrateTo(t, dsn, 1); setRecord(t, dsn, -1, true) },
			stderr: "Error: The record reads -1, dirty. reset-dirty does not change a record at -1. The record was not changed.\n",
			record: []string{"-1,true"},
		},
		{
			name:  "not in the directory",
			setup: func(t *testing.T, dsn string) { migrateTo(t, dsn, 5); setRecord(t, dsn, 99, true) },
			stderr: "Error: The record reads 99, dirty. The migrations directory " + platform.FindMigrationsDir(t) +
				" has no migration 99. The record was not changed.\n",
			record: []string{"99,true"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dsn := newMigrateDatabase(t)
			tc.setup(t, dsn)

			out, err := runMigrate(t, dsn, "reset-dirty")

			require.Error(t, err)
			require.Equal(t, tc.stderr, out.stderr.String())
			require.Empty(t, out.stdout.String())
			require.Equal(t, tc.record, readRecord(t, dsn))
		})
	}
}

func TestMigrateDown_AVersionWithNoFileIsAnErrorNotNothingToRollBack(t *testing.T) {
	dsn := newMigrateDatabase(t)
	migrateTo(t, dsn, 5)
	setRecord(t, dsn, 99, false)

	out, err := runMigrate(t, dsn, "down")

	require.Error(t, err)
	require.True(t, strings.HasPrefix(out.stderr.String(), "Error: rollback migration: "), "stderr: %s", out.stderr.String())
	require.Empty(t, out.stdout.String())
	require.Equal(t, []string{"99,false"}, readRecord(t, dsn))
}

// latestMigration returns the highest version among the up migration files.
func latestMigration(t *testing.T) int {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(platform.FindMigrationsDir(t), "*.up.sql"))
	require.NoError(t, err)
	latest := -1
	for _, file := range files {
		n, err := strconv.Atoi(strings.SplitN(filepath.Base(file), "_", 2)[0])
		require.NoError(t, err, file)
		latest = max(latest, n)
	}
	require.GreaterOrEqual(t, latest, 6, "found no migration at or past 6 in %v", files)
	return latest
}

func TestMigrate_RecoversFromTheRootIndexFailureWithResetDirty(t *testing.T) {
	dsn := newMigrateDatabase(t)
	migrateTo(t, dsn, 5)
	tree := testTreeID(831)
	insertActiveRoots(t, dsn, tree)

	out, err := runMigrate(t, dsn, "up")
	require.Error(t, err)
	require.Contains(t, out.stderr.String(), "Key (tree_id)=("+tree+") is duplicated")
	require.Equal(t, []string{"6,true"}, readRecord(t, dsn))

	conn := connectTo(t, dsn)
	tag, err := conn.Exec(t.Context(), "UPDATE tree_nodes SET removed_at = now() WHERE tree_id = $1 AND user_id = $2", tree, testUserID(2))
	require.NoError(t, err)
	require.EqualValues(t, 1, tag.RowsAffected())

	out, err = runMigrate(t, dsn, "reset-dirty")
	require.NoError(t, err, out.stderr.String())
	require.Equal(t, "The record read 6, dirty. This run set it to 5, clean.\nRun `mlmforge migrate up` next.\n", out.stdout.String())

	out, err = runMigrate(t, dsn, "up")
	require.NoError(t, err, out.stderr.String())
	require.Equal(t, []string{fmt.Sprintf("%d,false", latestMigration(t))}, readRecord(t, dsn))
}

func TestMigrateUp_AFailedFileIsNotEchoedAndNeverSaysForce(t *testing.T) {
	dsn := newMigrateDatabase(t)
	migrateTo(t, dsn, 4)
	conn := connectTo(t, dsn)
	_, err := conn.Exec(t.Context(), "CREATE TABLE commission_runs (id int)")
	require.NoError(t, err)

	out, err := runMigrate(t, dsn, "up")

	require.Error(t, err)
	stderr := out.stderr.String()
	require.Contains(t, stderr, `relation "commission_runs" already exists`)
	require.NotContains(t, stderr, "CREATE TABLE", "the migration file body must not be echoed")
	requireNoForce(t, out.stdout.String()+stderr)
}
