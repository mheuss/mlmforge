package platform

import (
	"errors"
	"testing"

	"github.com/golang-migrate/migrate/v4/database"
	"github.com/stretchr/testify/assert"
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
