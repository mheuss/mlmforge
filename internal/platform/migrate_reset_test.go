package platform

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/golang-migrate/migrate/v4/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingDriver records each lock and record call and answers Version with a fixed record.
type recordingDriver struct {
	database.Driver
	calls      []string
	record     Record
	lockErr    error
	setErr     error
	unlockErr  error
	versionErr error
}

func (d *recordingDriver) Lock() error {
	d.calls = append(d.calls, "Lock")
	return d.lockErr
}

func (d *recordingDriver) Unlock() error {
	d.calls = append(d.calls, "Unlock")
	return d.unlockErr
}

func (d *recordingDriver) Version() (int, bool, error) {
	d.calls = append(d.calls, "Version")
	return d.record.Version, d.record.Dirty, d.versionErr
}

func (d *recordingDriver) SetVersion(version int, dirty bool) error {
	d.calls = append(d.calls, fmt.Sprintf("SetVersion(%d, %t)", version, dirty))
	return d.setErr
}

// resetMigration returns a migration over the real migrations directory with driver as its database.
func resetMigration(t *testing.T, driver *recordingDriver) *migration {
	t.Helper()
	mg := sourceOnlyMigration(t)
	mg.db = releaseTagged{Driver: driver}
	return mg
}

func TestResetDirty_ReadsAndWritesTheRecordInsideTheLock(t *testing.T) {
	driver := &recordingDriver{record: Record{Version: 6, Dirty: true}}

	res, err := resetMigration(t, driver).resetDirty()

	require.NoError(t, err)
	assert.Equal(t, ResetResult{From: Record{Version: 6, Dirty: true}, To: Record{Version: 5}}, res)
	assert.Equal(t, []string{"Lock", "Version", "SetVersion(5, false)", "Unlock"}, driver.calls)
}

func TestResetDirty_ARefusalWritesNothingAndStillUnlocks(t *testing.T) {
	driver := &recordingDriver{record: Record{Version: 5}}

	_, err := resetMigration(t, driver).resetDirty()

	var notDirty *NotDirtyError
	require.ErrorAs(t, err, &notDirty)
	assert.Equal(t, []string{"Lock", "Version", "Unlock"}, driver.calls)
}

func TestResetDirty_AnUnlockFailureAfterTheWriteKeepsTheResult(t *testing.T) {
	driver := &recordingDriver{record: Record{Version: 6, Dirty: true}, unlockErr: errors.New("u")}

	res, err := resetMigration(t, driver).resetDirty()

	releases, rest := SplitRelease(err)
	assert.NoError(t, rest)
	assert.Equal(t, []error{&ReleaseError{What: "releasing the migration lock failed", Err: errors.New("u")}}, releases)
	assert.Equal(t, ResetResult{From: Record{Version: 6, Dirty: true}, To: Record{Version: 5}}, res)
}

func TestResetDirty_AFailedLockCallsNothingElse(t *testing.T) {
	driver := &recordingDriver{record: Record{Version: 6, Dirty: true}, lockErr: errors.New("l")}

	_, err := resetMigration(t, driver).resetDirty()

	var notWritten *NotWrittenError
	require.ErrorAs(t, err, &notWritten)
	assert.EqualError(t, err, "take migration lock: l")
	assert.Equal(t, []string{"Lock"}, driver.calls)
}

func TestResetDirty_AFailedWriteReReadsTheRecordInsideTheLock(t *testing.T) {
	driver := &recordingDriver{record: Record{Version: 6, Dirty: true}, setErr: errors.New("s")}

	_, err := resetMigration(t, driver).resetDirty()

	var write *WriteError
	require.ErrorAs(t, err, &write)
	assert.Equal(t, RecordRead{Record: Record{Version: 6, Dirty: true}}, write.After)
	assert.Equal(t, []string{"Lock", "Version", "SetVersion(5, false)", "Version", "Unlock"}, driver.calls)
}

// resetMigrationIn returns a migration over dir with driver as its database.
func resetMigrationIn(t *testing.T, dir string, driver *recordingDriver) *migration {
	t.Helper()
	mg := sourceMigration(t, dir)
	mg.db = releaseTagged{Driver: driver}
	return mg
}

func TestResetAfterFailedDown_MovesTheRecordForwardInsideTheLock(t *testing.T) {
	driver := &recordingDriver{record: Record{Version: 2, Dirty: true}}
	mg := resetMigrationIn(t, writeMigrations(t, sparseMigrations), driver)

	res, err := mg.resetLocked(mg.nextTarget)

	require.NoError(t, err)
	assert.Equal(t, ResetResult{From: Record{Version: 2, Dirty: true}, To: Record{Version: 7}}, res)
	assert.Equal(t, []string{"Lock", "Version", "SetVersion(7, false)", "Unlock"}, driver.calls)
}

func TestResetAfterFailedDown_FromMinusOneTakesTheFirstMigration(t *testing.T) {
	driver := &recordingDriver{record: Record{Version: -1, Dirty: true}}
	mg := resetMigrationIn(t, writeMigrations(t, sparseMigrations), driver)

	res, err := mg.resetLocked(mg.nextTarget)

	require.NoError(t, err)
	assert.Equal(t, ResetResult{From: Record{Version: -1, Dirty: true}, To: Record{Version: 2}}, res)
}

func TestResetAfterFailedDown_ARefusalWritesNothingAndStillUnlocks(t *testing.T) {
	dir := writeMigrations(t, sparseMigrations)
	cases := map[string]struct {
		record Record
		want   error
	}{
		"the last migration":   {Record{Version: 7, Dirty: true}, &NoNextMigrationError{Record: Record{Version: 7, Dirty: true}, Path: dir}},
		"not in the directory": {Record{Version: 5, Dirty: true}, &VersionNotInSourceError{Record: Record{Version: 5, Dirty: true}, Path: dir}},
		"below minus one":      {Record{Version: -2, Dirty: true}, &NegativeVersionError{Record: Record{Version: -2, Dirty: true}}},
		"clean":                {Record{Version: 2}, &NotDirtyError{Record: Record{Version: 2}}},
		"no version":           {Record{Version: -1}, &NotDirtyError{Record: Record{Version: -1}}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			driver := &recordingDriver{record: tc.record}
			mg := resetMigrationIn(t, dir, driver)

			_, err := mg.resetLocked(mg.nextTarget)

			assert.Equal(t, tc.want, err)
			assert.Equal(t, []string{"Lock", "Version", "Unlock"}, driver.calls)
		})
	}
}

func TestResetAfterFailedDown_AnEmptyDirectoryHasNothingAfterMinusOne(t *testing.T) {
	dir := t.TempDir()
	driver := &recordingDriver{record: Record{Version: -1, Dirty: true}}
	mg := resetMigrationIn(t, dir, driver)

	_, err := mg.resetLocked(mg.nextTarget)

	assert.Equal(t, &NoNextMigrationError{Record: Record{Version: -1, Dirty: true}, Path: dir}, err)
	assert.Equal(t, []string{"Lock", "Version", "Unlock"}, driver.calls)
}

func TestResetAfterFailedDown_ANextMigrationWithNoDownFileWritesNothing(t *testing.T) {
	dir := writeMigrations(t, map[string]string{"2_a.up.sql": "SELECT 1;", "2_a.down.sql": "SELECT 1;", "7_b.up.sql": "SELECT 1;"})
	driver := &recordingDriver{record: Record{Version: 2, Dirty: true}}
	mg := resetMigrationIn(t, dir, driver)

	_, err := mg.resetLocked(mg.nextTarget)

	assert.Equal(t, &NoDownFileError{Record: Record{Version: 2, Dirty: true}, Version: 7, Path: dir}, err)
	assert.Equal(t, []string{"Lock", "Version", "Unlock"}, driver.calls)
}

func TestResetAfterFailedDown_AnUnreadableFileRefusesWithoutWriting(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a file with mode 000")
	}
	cases := map[string]string{
		"the record's up file": "2_a.up.sql",
		"the next down file":   "7_b.down.sql",
	}
	for name, file := range cases {
		t.Run(name, func(t *testing.T) {
			dir := writeMigrations(t, sparseMigrations)
			require.NoError(t, os.Chmod(filepath.Join(dir, file), 0))
			driver := &recordingDriver{record: Record{Version: 2, Dirty: true}}
			mg := resetMigrationIn(t, dir, driver)

			_, err := mg.resetLocked(mg.nextTarget)

			var notWritten *NotWrittenError
			require.ErrorAs(t, err, &notWritten)
			assert.Equal(t, []string{"Lock", "Version", "Unlock"}, driver.calls)
		})
	}
}
