package platform

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/mlmforge/mlmforge/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// twoFastMigrations is a directory with migrations 1 and 2 that run at once.
var twoFastMigrations = map[string]string{
	"1_a.up.sql": "CREATE TABLE fast_a (id int);", "1_a.down.sql": "DROP TABLE fast_a;",
	"2_b.up.sql": "CREATE TABLE fast_b (id int);", "2_b.down.sql": "DROP TABLE fast_b;",
}

// requireOnlyInterrupted fails unless err is exactly one InterruptedError for during, with no release error joined to it.
func requireOnlyInterrupted(t *testing.T, err error, during string) {
	t.Helper()
	releases, rest := SplitRelease(err)
	require.Empty(t, releases, "release errors joined to the interrupt")
	var interrupted *InterruptedError
	require.ErrorAs(t, rest, &interrupted)
	require.Equal(t, &InterruptedError{During: during}, rest)
}

// requireBlockedUntilRelease fails unless done stays empty for 2s and then receives within 500ms of release.
func requireBlockedUntilRelease(t *testing.T, done <-chan error, release func(), whileHeld func()) error {
	t.Helper()
	select {
	case err := <-done:
		t.Fatalf("returned while another session held the lock: %v", err)
	case <-time.After(2 * time.Second):
	}
	whileHeld()
	release()
	select {
	case err := <-done:
		return err
	case <-time.After(500 * time.Millisecond):
		t.Fatal("still waiting 500ms after the lock was released")
		return nil
	}
}

func TestLockMigrations_PollsEveryHundredMilliseconds(t *testing.T) {
	assert.Equal(t, 100*time.Millisecond, migrateLockPollInterval)
}

func TestMigrateCommands_WaitWhileAnotherSessionHoldsTheLock(t *testing.T) {
	cases := map[string]struct {
		record *Record
		call   func(dsn, dir string) error
	}{
		"up": {call: func(dsn, dir string) error { return MigrateUp(context.Background(), dsn, dir, nil) }},
		"down": {record: &Record{Version: 2}, call: func(dsn, dir string) error {
			return MigrateDown(context.Background(), dsn, dir, nil)
		}},
		"version": {call: func(dsn, dir string) error {
			_, err := MigrateVersion(context.Background(), dsn, dir, nil)
			return err
		}},
		"reset-dirty": {record: &Record{Version: 2, Dirty: true}, call: func(dsn, dir string) error {
			_, err := ResetDirty(context.Background(), dsn, dir, nil)
			return err
		}},
		"reset-dirty --after-failed-down": {record: &Record{Version: 1, Dirty: true}, call: func(dsn, dir string) error {
			_, err := ResetAfterFailedDown(context.Background(), dsn, dir, nil)
			return err
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dsn := newResetDatabase(t)
			dir := writeMigrations(t, twoFastMigrations)
			if tc.record != nil {
				require.NoError(t, MigrateUp(context.Background(), dsn, dir, nil))
				storeRecord(t, dsn, dir, tc.record.Version, tc.record.Dirty)
			}
			_, release := testutil.HoldAdvisoryLock(t, dsn, MigrateLockNamespace, 0)
			done := make(chan error, 1)
			go func() { done <- tc.call(dsn, dir) }()

			err := requireBlockedUntilRelease(t, done, release, func() {
				if tc.record != nil {
					version, dirty := testutil.ReadRecord(t, dsn)
					assert.Equal(t, *tc.record, Record{Version: version, Dirty: dirty}, "the record changed while the lock was held")
				}
			})

			require.NoError(t, err)
		})
	}
}

// otherURLForms returns dsn with its database named by ?dbname=, and with no database name, for use with PGDATABASE.
func otherURLForms(t *testing.T, dsn string) (byQuery, bare, name string) {
	t.Helper()
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	name = u.Path[1:]
	u.Path = ""
	bare = u.String()
	q := u.Query()
	q.Set("dbname", name)
	u.RawQuery = q.Encode()
	return u.String(), bare, name
}

func TestMigrateCommands_ExcludeEachOtherWhateverTheURLForm(t *testing.T) {
	for _, form := range []string{"dbname query parameter", "PGDATABASE"} {
		t.Run(form, func(t *testing.T) {
			dsn := newResetDatabase(t)
			dir := testutil.SlowMigrations(t, 2)
			byQuery, bare, name := otherURLForms(t, dsn)
			other := byQuery
			if form == "PGDATABASE" {
				t.Setenv("PGDATABASE", name)
				other = bare
			}
			up := make(chan error, 1)
			go func() { up <- MigrateUp(context.Background(), dsn, dir, nil) }()
			testutil.WaitForActiveQuery(t, pgContainer.DSN, "slow_one")

			version := make(chan Status, 1)
			go func() {
				st, err := MigrateVersion(context.Background(), other, dir, nil)
				assert.NoError(t, err)
				version <- st
			}()
			select {
			case st := <-version:
				t.Fatalf("version read %v while up held the lock", st.Record)
			case <-time.After(2 * time.Second):
			}

			require.NoError(t, <-up)
			assert.Equal(t, Record{Version: 2}, (<-version).Record)
		})
	}
}

func TestMigrateUp_HoldsBothLocksOnOneBackend(t *testing.T) {
	dsn := newResetDatabase(t)
	dir := testutil.SlowMigrations(t, 1)
	done := make(chan error, 1)
	go func() { done <- MigrateUp(context.Background(), dsn, dir, nil) }()
	pid := testutil.WaitForActiveQuery(t, pgContainer.DSN, "slow_one")

	admin, err := pgx.Connect(t.Context(), dsn)
	require.NoError(t, err)
	defer func() { _ = admin.Close(context.Background()) }()
	var ours, theirs int
	require.NoError(t, admin.QueryRow(t.Context(),
		"SELECT count(*) FILTER (WHERE classid = $2 AND objid = 0 AND objsubid = 2), count(*) FILTER (WHERE objsubid = 1) FROM pg_locks WHERE pid = $1 AND locktype = 'advisory' AND granted",
		pid, MigrateLockNamespace).Scan(&ours, &theirs))

	assert.Equal(t, 1, ours, "the mlmforge lock")
	assert.Equal(t, 1, theirs, "golang-migrate's lock")
	require.NoError(t, <-done)
}

func TestMigrateVersion_AnEndedContextStopsTheLockWaitAndNamesTheHolder(t *testing.T) {
	dsn := newResetDatabase(t)
	holder, _ := testutil.HoldAdvisoryLock(t, dsn, MigrateLockNamespace, 0)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(500*time.Millisecond, cancel)
	var told []int

	start := time.Now()
	_, err := MigrateVersion(ctx, dsn, FindMigrationsDir(t), func(pid int) { told = append(told, pid) })

	assert.Less(t, time.Since(start), 2*time.Second)
	requireOnlyInterrupted(t, err, duringLockWait)
	assert.Equal(t, []int{holder}, told)
	assert.False(t, testutil.TableExists(t, dsn, "schema_migrations"), "nothing was written")
}

func TestOpenDriver_AnEndedContextIsAnInterrupt(t *testing.T) {
	dsn := newResetDatabase(t)
	s, err := dialSession(context.Background(), dsn)
	require.NoError(t, err)
	defer func() { _ = s.close() }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = openDriver(ctx, s)

	assert.Equal(t, &InterruptedError{During: duringDriverOpen}, err)
}

func TestMigrateCommands_LeaveNoAdvisoryLockBehind(t *testing.T) {
	cases := map[string]func(t *testing.T, dsn string){
		"a successful up": func(t *testing.T, dsn string) {
			require.NoError(t, MigrateUp(context.Background(), dsn, writeMigrations(t, twoFastMigrations), nil))
		},
		"a failed up": func(t *testing.T, dsn string) {
			dir := writeMigrations(t, map[string]string{"1_bad.up.sql": "SELEC 1;", "1_bad.down.sql": "SELECT 1;"})
			require.Error(t, MigrateUp(context.Background(), dsn, dir, nil))
		},
		"a successful down": func(t *testing.T, dsn string) {
			dir := writeMigrations(t, twoFastMigrations)
			require.NoError(t, MigrateUp(context.Background(), dsn, dir, nil))
			require.NoError(t, MigrateDown(context.Background(), dsn, dir, nil))
		},
		"a failed down": func(t *testing.T, dsn string) {
			dir := writeMigrations(t, map[string]string{"1_a.up.sql": "SELECT 1;", "1_a.down.sql": "SELEC 1;"})
			require.NoError(t, MigrateUp(context.Background(), dsn, dir, nil))
			require.Error(t, MigrateDown(context.Background(), dsn, dir, nil))
		},
		"a successful reset-dirty": func(t *testing.T, dsn string) {
			dir := writeMigrations(t, twoFastMigrations)
			storeRecord(t, dsn, dir, 2, true)
			_, err := ResetDirty(context.Background(), dsn, dir, nil)
			require.NoError(t, err)
		},
		"a refused reset-dirty": func(t *testing.T, dsn string) {
			dir := writeMigrations(t, twoFastMigrations)
			storeRecord(t, dsn, dir, 2, false)
			_, err := ResetDirty(context.Background(), dsn, dir, nil)
			require.Error(t, err)
		},
		"a successful reset-dirty --after-failed-down": func(t *testing.T, dsn string) {
			dir := writeMigrations(t, twoFastMigrations)
			storeRecord(t, dsn, dir, 1, true)
			_, err := ResetAfterFailedDown(context.Background(), dsn, dir, nil)
			require.NoError(t, err)
		},
		"a refused reset-dirty --after-failed-down": func(t *testing.T, dsn string) {
			dir := writeMigrations(t, twoFastMigrations)
			storeRecord(t, dsn, dir, 2, true)
			_, err := ResetAfterFailedDown(context.Background(), dsn, dir, nil)
			require.Error(t, err)
		},
		"a successful version": func(t *testing.T, dsn string) {
			_, err := MigrateVersion(context.Background(), dsn, FindMigrationsDir(t), nil)
			require.NoError(t, err)
		},
		"an interrupted wait": func(t *testing.T, dsn string) {
			_, release := testutil.HoldAdvisoryLock(t, dsn, MigrateLockNamespace, 0)
			defer release()
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			_, err := MigrateVersion(ctx, dsn, FindMigrationsDir(t), nil)
			requireOnlyInterrupted(t, err, duringLockWait)
		},
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			dsn := newResetDatabase(t)

			run(t, dsn)

			testutil.RequireNoAdvisoryLocks(t, dsn)
		})
	}
}
