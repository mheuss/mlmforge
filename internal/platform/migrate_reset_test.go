package platform

import (
	"errors"
	"fmt"
	"testing"

	"github.com/golang-migrate/migrate/v4/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingDriver records each lock and record call and answers Version with a fixed record.
type recordingDriver struct {
	database.Driver
	calls     []string
	record    Record
	lockErr   error
	setErr    error
	unlockErr error
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
	return d.record.Version, d.record.Dirty, nil
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
