package platform

import (
	"errors"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sourceOnlyMigration returns a migration holding only the real migrations directory's source driver.
func sourceOnlyMigration(t *testing.T) *migration {
	t.Helper()
	dir := FindMigrationsDir(t)
	src, err := source.Open("file://" + dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = src.Close() })
	return &migration{source: src, path: dir}
}

var unlockFailed = &ReleaseError{What: "releasing the migration lock failed", Err: errors.New("u")}

func TestUpOutcome_NoChangeWithAnUnlockFailureKeepsOnlyTheRelease(t *testing.T) {
	releases, rest := SplitRelease(sourceOnlyMigration(t).upOutcome(errors.Join(migrate.ErrNoChange, unlockFailed)))

	assert.NoError(t, rest)
	assert.Equal(t, []error{unlockFailed}, releases)
}

func TestUpOutcome_ADirtyRecordWithAnUnlockFailureKeepsBoth(t *testing.T) {
	releases, rest := SplitRelease(sourceOnlyMigration(t).upOutcome(errors.Join(migrate.ErrDirty{Version: 6}, unlockFailed)))

	var dirty *DirtyError
	require.ErrorAs(t, rest, &dirty)
	assert.Equal(t, Record{Version: 6, Dirty: true}, dirty.Record)
	assert.Equal(t, uint(5), dirty.Source.Previous)
	assert.Equal(t, []error{unlockFailed}, releases)
}

func TestUpResult_ALockTimeoutReadsNoRecord(t *testing.T) {
	err := (&migration{}).upResult(migrate.ErrLockTimeout)

	var apply *ApplyError
	assert.False(t, errors.As(err, &apply), "a lock timeout must not carry a record read afterwards")
	assert.ErrorIs(t, err, migrate.ErrLockTimeout)
	assert.EqualError(t, err, "apply migrations: timeout: can't acquire database lock")
}
