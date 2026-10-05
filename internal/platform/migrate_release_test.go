package platform

import (
	"errors"
	"testing"

	"github.com/golang-migrate/migrate/v4/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type unlockFails struct {
	database.Driver
}

func (unlockFails) Unlock() error { return errors.New("u") }

type unlockSucceeds struct {
	database.Driver
}

func (unlockSucceeds) Unlock() error { return nil }

func TestReleaseTagged_AnUnlockFailureIsAReleaseError(t *testing.T) {
	err := releaseTagged{Driver: unlockFails{}}.Unlock()

	assert.Equal(t, &ReleaseError{What: "releasing the migration lock failed", Err: errors.New("u")}, err)
	assert.NoError(t, releaseTagged{Driver: unlockSucceeds{}}.Unlock())
}

type lockFails struct {
	database.Driver
}

func (lockFails) Lock() error { return errors.New("l") }

func TestReleaseTagged_ALockFailureIsALockNotTakenError(t *testing.T) {
	err := releaseTagged{Driver: lockFails{}}.Lock()

	assert.Equal(t, &LockNotTakenError{Err: errors.New("l")}, err)
}

func TestReleaseTagged_AHeldLockMakesEachStepsLockAndUnlockANoOp(t *testing.T) {
	driver := &recordingDriver{}
	tagged := releaseTagged{Driver: driver, held: new(bool)}

	require.NoError(t, tagged.holdLock())
	require.NoError(t, tagged.Lock())
	require.NoError(t, tagged.Unlock())
	require.NoError(t, tagged.releaseHold())

	assert.Equal(t, []string{"Lock", "Unlock"}, driver.calls)
}

func TestReleaseTagged_AFailedHoldIsALockNotTakenError(t *testing.T) {
	tagged := releaseTagged{Driver: lockFails{}, held: new(bool)}

	assert.Equal(t, &LockNotTakenError{Err: errors.New("l")}, tagged.holdLock())
	assert.False(t, *tagged.held)
}

func TestReleaseTagged_AFailedReleaseIsAReleaseError(t *testing.T) {
	tagged := releaseTagged{Driver: &recordingDriver{unlockErr: errors.New("u")}, held: new(bool)}
	require.NoError(t, tagged.holdLock())

	assert.Equal(t, &ReleaseError{What: "releasing the migration lock failed", Err: errors.New("u")}, tagged.releaseHold())
}
