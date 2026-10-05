package platform

import (
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database"
	"github.com/golang-migrate/migrate/v4/source"
	"github.com/jackc/pgx/v5"
	"github.com/mlmforge/mlmforge/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// endedContext returns a context that has already ended.
func endedContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// stopMigration returns a migration over a two-migration directory with driver as its database.
func stopMigration(t *testing.T, driver *recordingDriver) *migration {
	t.Helper()
	mg := sourceMigration(t, writeMigrations(t, twoFastMigrations))
	mg.db = releaseTagged{Driver: driver, held: new(bool)}
	return mg
}

func TestUpOutcome_StopCases(t *testing.T) {
	none := RecordRead{Record: Record{Version: database.NilVersion}}
	cases := map[string]struct {
		driver  *recordingDriver
		before  RecordRead
		raw     error
		stopped bool
	}{
		"migrations left after the stop": {driver: &recordingDriver{record: Record{Version: 1}}, before: none, stopped: true},
		"the last migration finished":    {driver: &recordingDriver{record: Record{Version: 2}}, before: none},
		"no file ran":                    {driver: &recordingDriver{record: Record{Version: database.NilVersion}}, before: none, stopped: true},
		"the record cannot be read":      {driver: &recordingDriver{versionErr: errors.New("r")}, before: none, stopped: true},
		"nothing to apply":               {driver: &recordingDriver{record: Record{Version: 2}}, before: none, raw: migrate.ErrNoChange},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := stopMigration(t, tc.driver).upOutcome(endedContext(), tc.before, tc.raw)

			var stopped *StoppedError
			assert.Equal(t, tc.stopped, errors.As(err, &stopped), "err: %v", err)
			if !tc.stopped {
				assert.NoError(t, err)
			}
		})
	}
}

func TestUpOutcome_AStopKeepsTheRecordsItRead(t *testing.T) {
	before := RecordRead{Record: Record{Version: database.NilVersion}}

	err := stopMigration(t, &recordingDriver{record: Record{Version: 1}}).upOutcome(endedContext(), before, nil)

	var stopped *StoppedError
	require.ErrorAs(t, err, &stopped)
	assert.Equal(t, "up", stopped.Command)
	assert.Equal(t, before, stopped.Before)
	assert.Equal(t, RecordRead{Record: Record{Version: 1}}, stopped.After)
	assert.True(t, stopped.Source.HasNext)
}

func TestUpOutcome_AFailureDuringAStopKeepsItsClassification(t *testing.T) {
	failed := database.Error{Err: "migration failed: x", OrigErr: serverError{state: "23505"}}

	err := stopMigration(t, &recordingDriver{record: Record{Version: 1, Dirty: true}}).upOutcome(endedContext(), RecordRead{}, failed)

	var apply *ApplyError
	assert.ErrorAs(t, err, &apply)
}

func TestUpOutcome_AStopWithAReleaseFailureKeepsBoth(t *testing.T) {
	err := stopMigration(t, &recordingDriver{record: Record{Version: 1}}).upOutcome(endedContext(), RecordRead{}, errors.Join(nil, unlockFailed))

	releases, rest := SplitRelease(err)
	var stopped *StoppedError
	assert.ErrorAs(t, rest, &stopped)
	assert.Equal(t, []error{unlockFailed}, releases)
}

func TestUpOutcome_WithoutAStopReadsNothing(t *testing.T) {
	driver := &recordingDriver{record: Record{Version: 1}}

	err := stopMigration(t, driver).upOutcome(context.Background(), RecordRead{}, nil)

	assert.NoError(t, err)
	assert.Empty(t, driver.calls)
}

func TestDownOutcome_StopCases(t *testing.T) {
	before := RecordRead{Record: Record{Version: 2}}
	cases := map[string]struct {
		driver  *recordingDriver
		stopped bool
	}{
		"the rollback happened":     {driver: &recordingDriver{record: Record{Version: 1}}},
		"nothing was rolled back":   {driver: &recordingDriver{record: Record{Version: 2}}, stopped: true},
		"the record cannot be read": {driver: &recordingDriver{versionErr: errors.New("r")}, stopped: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := stopMigration(t, tc.driver).downOutcome(endedContext(), before, nil)

			var stopped *StoppedError
			assert.Equal(t, tc.stopped, errors.As(err, &stopped), "err: %v", err)
		})
	}
}

func TestMigrateUp_AStopDuringTheFirstFileFinishesItAndStops(t *testing.T) {
	dsn := newResetDatabase(t)
	dir := testutil.SlowMigrations(t, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- MigrateUp(ctx, dsn, dir, nil) }()
	testutil.WaitForActiveQuery(t, pgContainer.DSN, "slow_one")
	cancel()

	var stopped *StoppedError
	require.ErrorAs(t, <-done, &stopped)
	version, dirty := testutil.ReadRecord(t, dsn)
	assert.Equal(t, Record{Version: 1}, Record{Version: version, Dirty: dirty})
	assert.False(t, testutil.TableExists(t, dsn, "slow_two"))
}

func TestMigrateUp_AStopDuringTheLastFileIsSuccess(t *testing.T) {
	dsn := newResetDatabase(t)
	dir := testutil.SlowMigrations(t, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- MigrateUp(ctx, dsn, dir, nil) }()
	testutil.WaitForActiveQuery(t, pgContainer.DSN, "slow_two")
	cancel()

	require.NoError(t, <-done)
	version, dirty := testutil.ReadRecord(t, dsn)
	assert.Equal(t, Record{Version: 2}, Record{Version: version, Dirty: dirty})
}

func TestMigrateUp_AStopBeforeTheFirstFileChangesNothing(t *testing.T) {
	dsn := newResetDatabase(t)
	dir := testutil.SlowMigrations(t, 1)
	mg, err := openMigration(context.Background(), dsn, dir, nil)
	require.NoError(t, err)
	ctx := endedContext()

	before := mg.recordRead()
	err = mg.upOutcome(ctx, before, mg.upInSteps(ctx))
	mg.closeInto(&err)

	var stopped *StoppedError
	require.ErrorAs(t, err, &stopped)
	assert.Equal(t, stopped.Before, stopped.After)
	assert.False(t, testutil.TableExists(t, dsn, "slow_one"))
}

func TestMigrateDown_AStopBeforeItsFileChangesNothing(t *testing.T) {
	dsn := newResetDatabase(t)
	dir := writeMigrations(t, twoFastMigrations)
	require.NoError(t, MigrateUp(context.Background(), dsn, dir, nil))
	mg, err := openMigration(context.Background(), dsn, dir, nil)
	require.NoError(t, err)
	ctx := endedContext()

	before, raw := mg.downStep(ctx)
	err = mg.downOutcome(ctx, before, raw)
	mg.closeInto(&err)

	var stopped *StoppedError
	require.ErrorAs(t, err, &stopped)
	assert.True(t, testutil.TableExists(t, dsn, "fast_b"))
}

func TestUpInSteps_KeepsUpsOutcomes(t *testing.T) {
	cases := map[string]struct {
		files  map[string]string
		record *Record
		check  func(t *testing.T, err error)
		want   *Record
	}{
		"nothing to apply": {
			files: twoFastMigrations, record: &Record{Version: 2},
			check: func(t *testing.T, err error) { assert.ErrorIs(t, err, migrate.ErrNoChange) },
		},
		"a dirty record at the start": {
			files: twoFastMigrations, record: &Record{Version: 1, Dirty: true},
			check: func(t *testing.T, err error) { assert.Equal(t, migrate.ErrDirty{Version: 1}, err) },
		},
		"a record the source does not hold": {
			files: twoFastMigrations, record: &Record{Version: 9},
			check: func(t *testing.T, err error) { assert.ErrorIs(t, err, os.ErrNotExist) },
		},
		"an empty migrations directory": {
			files: map[string]string{},
			check: func(t *testing.T, err error) { assert.ErrorIs(t, err, os.ErrNotExist) },
		},
		"a last version with only a down file": {
			files:  map[string]string{"1_a.up.sql": "SELECT 1;", "1_a.down.sql": "SELECT 1;", "2_b.down.sql": "SELECT 1;"},
			record: &Record{Version: 2},
			check:  func(t *testing.T, err error) { assert.ErrorIs(t, err, migrate.ErrNoChange) },
		},
		"two pending migrations": {
			files: twoFastMigrations,
			check: func(t *testing.T, err error) { assert.NoError(t, err) },
			want:  &Record{Version: 2},
		},
		"a failing file": {
			files: map[string]string{"1_bad.up.sql": "SELEC 1;", "1_bad.down.sql": "SELECT 1;"},
			check: func(t *testing.T, err error) { assert.Error(t, err) },
			want:  &Record{Version: 1, Dirty: true},
		},
		"a second file failing after the first applied": {
			files: map[string]string{"1_a.up.sql": "CREATE TABLE ok_a (id int);", "1_a.down.sql": "DROP TABLE ok_a;", "2_bad.up.sql": "SELEC 1;", "2_bad.down.sql": "SELECT 1;"},
			check: func(t *testing.T, err error) { assert.Error(t, err) },
			want:  &Record{Version: 2, Dirty: true},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dsn := newResetDatabase(t)
			dir := writeMigrations(t, tc.files)
			if tc.record != nil {
				storeRecord(t, dsn, dir, tc.record.Version, tc.record.Dirty)
			}
			mg, err := openMigration(context.Background(), dsn, dir, nil)
			require.NoError(t, err)

			err = mg.upInSteps(context.Background())
			closeErr := error(nil)
			mg.closeInto(&closeErr)

			require.NoError(t, closeErr)
			tc.check(t, err)
			if tc.want != nil {
				version, dirty := testutil.ReadRecord(t, dsn)
				assert.Equal(t, *tc.want, Record{Version: version, Dirty: dirty})
			}
		})
	}
}

func TestMigrateUp_AFailingFileKeepsItsApplyError(t *testing.T) {
	dsn := newResetDatabase(t)
	dir := writeMigrations(t, map[string]string{"1_bad.up.sql": "SELEC 1;", "1_bad.down.sql": "SELECT 1;"})

	err := MigrateUp(context.Background(), dsn, dir, nil)

	var apply *ApplyError
	require.ErrorAs(t, err, &apply)
	assert.True(t, apply.BodyFailed)
	assert.Equal(t, RecordRead{Record: Record{Version: 1, Dirty: true}}, apply.After)
}

// A release failure alone must not read as a stop.
func TestUpOutcome_AReleaseFailureWithoutAStopIsSuccessWithTheFailure(t *testing.T) {
	err := stopMigration(t, &recordingDriver{record: Record{Version: 1}}).upOutcome(context.Background(), RecordRead{}, errors.Join(nil, unlockFailed))

	releases, rest := SplitRelease(err)
	assert.NoError(t, rest)
	assert.Equal(t, []error{unlockFailed}, releases)
}

func TestMigrateUp_ASecondFailingFileKeepsItsApplyError(t *testing.T) {
	dsn := newResetDatabase(t)
	dir := writeMigrations(t, map[string]string{"1_a.up.sql": "CREATE TABLE ok_a (id int);", "1_a.down.sql": "DROP TABLE ok_a;", "2_bad.up.sql": "SELEC 1;", "2_bad.down.sql": "SELECT 1;"})

	err := MigrateUp(context.Background(), dsn, dir, nil)

	var apply *ApplyError
	require.ErrorAs(t, err, &apply)
	assert.True(t, apply.BodyFailed)
	assert.Equal(t, RecordRead{Record: Record{Version: 2, Dirty: true}}, apply.After)
	assert.True(t, testutil.TableExists(t, dsn, "ok_a"))
}

// foreignDriverLockGranted reports whether another session ever got golang-migrate's lock for dsn's database before a backend ran a query containing until.
func foreignDriverLockGranted(t *testing.T, dsn, until string) bool {
	t.Helper()
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	key, err := database.GenerateAdvisoryLockId(u.Path, "public", "schema_migrations")
	require.NoError(t, err)
	conn, err := pgx.Connect(context.Background(), dsn)
	require.NoError(t, err)
	defer func() { _ = conn.Close(context.Background()) }()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var running bool
		require.NoError(t, conn.QueryRow(context.Background(),
			"SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE state = 'active' AND pid <> pg_backend_pid() AND strpos(query, $1) > 0)", until).Scan(&running))
		if running {
			return false
		}
		var granted bool
		require.NoError(t, conn.QueryRow(context.Background(), "SELECT pg_try_advisory_lock($1::bigint)", key).Scan(&granted))
		if granted {
			_, _ = conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1::bigint)", key)
			return true
		}
	}
	t.Fatalf("no backend ran a query containing %q within 10s", until)
	return false
}

// Polling runs from file 1 starting until file 2 starts, so it spans the gap between them.
func TestMigrateUp_HoldsGolangMigratesLockBetweenFiles(t *testing.T) {
	dsn := newResetDatabase(t)
	dir := testutil.SlowMigrations(t, 1)
	result := make(chan error, 1)
	go func() { result <- MigrateUp(context.Background(), dsn, dir, nil) }()
	testutil.WaitForActiveQuery(t, pgContainer.DSN, "slow_one")

	granted := foreignDriverLockGranted(t, dsn, "CREATE TABLE slow_two")

	require.NoError(t, <-result)
	assert.False(t, granted, "another session got golang-migrate's lock between files")
}

// fakeSource is a source.Driver whose reads return the errors it is given.
type fakeSource struct {
	source.Driver
	upErr, downErr, nextErr error
}

func (f fakeSource) ReadUp(uint) (io.ReadCloser, string, error) {
	if f.upErr != nil {
		return nil, "", f.upErr
	}
	return io.NopCloser(strings.NewReader("")), "", nil
}

func (f fakeSource) ReadDown(uint) (io.ReadCloser, string, error) {
	if f.downErr != nil {
		return nil, "", f.downErr
	}
	return io.NopCloser(strings.NewReader("")), "", nil
}

func (f fakeSource) Next(uint) (uint, error) { return 0, f.nextErr }

func TestIsLast_StopsAtAnUpReadThatFailsForAnotherReason(t *testing.T) {
	cases := map[string]struct {
		src  fakeSource
		want bool
	}{
		"an up file":                     {src: fakeSource{nextErr: os.ErrNotExist}, want: true},
		"only a down file":               {src: fakeSource{upErr: os.ErrNotExist, nextErr: os.ErrNotExist}, want: true},
		"an up file that cannot be read": {src: fakeSource{upErr: os.ErrPermission, nextErr: os.ErrNotExist}, want: false},
		"neither file":                   {src: fakeSource{upErr: os.ErrNotExist, downErr: os.ErrNotExist, nextErr: os.ErrNotExist}, want: false},
		"a migration after it":           {src: fakeSource{}, want: false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			mg := &migration{source: tc.src}

			assert.Equal(t, tc.want, mg.isLast(Record{Version: 2}))
		})
	}
}

func TestUpStopped_JudgesTheLastMigrationLikeTheLoop(t *testing.T) {
	cases := map[string]struct {
		files   map[string]string
		record  Record
		stopped bool
	}{
		"minus one with an empty directory":    {files: map[string]string{}, record: Record{Version: -1}, stopped: true},
		"a last version with only a down file": {files: map[string]string{"1_a.up.sql": "SELECT 1;", "1_a.down.sql": "SELECT 1;", "2_b.down.sql": "SELECT 1;"}, record: Record{Version: 2}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			mg := sourceMigration(t, writeMigrations(t, tc.files))
			mg.db = releaseTagged{Driver: &recordingDriver{record: tc.record}, held: new(bool)}

			err := mg.upStopped(RecordRead{Record: Record{Version: -1}})

			var stopped *StoppedError
			assert.Equal(t, tc.stopped, errors.As(err, &stopped), "err: %v", err)
		})
	}
}

func TestHoldingDriverLock_AFailedHoldRunsNothing(t *testing.T) {
	mg := &migration{db: releaseTagged{Driver: lockFails{}, held: new(bool)}}
	ran := false

	err := mg.holdingDriverLock(func() error { ran = true; return nil })

	assert.False(t, ran)
	assert.Equal(t, &LockNotTakenError{Err: errors.New("l")}, err)
}

func TestMigrateDown_AStopWhileWaitingForGolangMigratesLockChangesNothing(t *testing.T) {
	dsn := newResetDatabase(t)
	dir := writeMigrations(t, twoFastMigrations)
	require.NoError(t, MigrateUp(context.Background(), dsn, dir, nil))
	mg, err := openMigration(context.Background(), dsn, dir, nil)
	require.NoError(t, err)
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	key, err := database.GenerateAdvisoryLockId(u.Path, "public", "schema_migrations")
	require.NoError(t, err)
	holder, err := pgx.Connect(context.Background(), dsn)
	require.NoError(t, err)
	defer func() { _ = holder.Close(context.Background()) }()
	_, err = holder.Exec(context.Background(), "SELECT pg_advisory_lock($1::bigint)", key)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		before, raw := mg.downStep(ctx)
		result <- mg.downOutcome(ctx, before, raw)
	}()
	testutil.WaitForActiveQuery(t, pgContainer.DSN, "pg_advisory_lock")

	cancel()
	_, err = holder.Exec(context.Background(), "SELECT pg_advisory_unlock($1::bigint)", key)
	require.NoError(t, err)
	err = <-result
	mg.closeInto(&err)

	var stopped *StoppedError
	require.ErrorAs(t, err, &stopped)
	assert.True(t, testutil.TableExists(t, dsn, "fast_b"))
}
