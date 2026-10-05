package platform

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database"
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
	mg.db = releaseTagged{Driver: driver}
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

	before := mg.recordRead()
	err = mg.downOutcome(ctx, before, mg.downOnce(ctx))
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
