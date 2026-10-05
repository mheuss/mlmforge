package platform

import (
	"context"
	"database/sql/driver"
	"errors"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database"
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
	releases, rest := SplitRelease(sourceOnlyMigration(t).upOutcome(context.Background(), RecordRead{}, errors.Join(migrate.ErrNoChange, unlockFailed)))

	assert.NoError(t, rest)
	assert.Equal(t, []error{unlockFailed}, releases)
}

func TestUpOutcome_ADirtyRecordWithAnUnlockFailureKeepsBoth(t *testing.T) {
	releases, rest := SplitRelease(sourceOnlyMigration(t).upOutcome(context.Background(), RecordRead{}, errors.Join(migrate.ErrDirty{Version: 6}, unlockFailed)))

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

func TestDownOutcome_NoChangeWithAnUnlockFailureKeepsOnlyTheRelease(t *testing.T) {
	releases, rest := SplitRelease(sourceOnlyMigration(t).downOutcome(context.Background(), RecordRead{}, errors.Join(migrate.ErrNoChange, unlockFailed)))

	assert.ErrorIs(t, rest, ErrNoChange)
	assert.Equal(t, []error{unlockFailed}, releases)
}

func TestDownOutcome_ADirtyRecordWithAnUnlockFailureKeepsBoth(t *testing.T) {
	releases, rest := SplitRelease(sourceOnlyMigration(t).downOutcome(context.Background(), RecordRead{}, errors.Join(migrate.ErrDirty{Version: 6}, unlockFailed)))

	var dirty *DirtyError
	require.ErrorAs(t, rest, &dirty)
	assert.Equal(t, Record{Version: 6, Dirty: true}, dirty.Record)
	assert.Equal(t, []error{unlockFailed}, releases)
}

func TestDownResult_ALockTimeoutReadsNoRecord(t *testing.T) {
	err := (&migration{}).downResult(RecordRead{}, migrate.ErrLockTimeout)

	var rollback *RollbackError
	assert.False(t, errors.As(err, &rollback), "a lock timeout must not carry a record read afterwards")
	assert.ErrorIs(t, err, migrate.ErrLockTimeout)
	assert.EqualError(t, err, "rollback migration: timeout: can't acquire database lock")
}

func TestDownResult_SetsBodyFailedForTheShapesIsBodyFailureIsTestedWith(t *testing.T) {
	cases := map[string]struct {
		err  error
		want bool
	}{
		"unique violation in the file": {database.Error{Err: "migration failed: duplicate key", OrigErr: serverError{state: "23505"}}, true},
		"lock timeout in the file":     {database.Error{Err: "migration failed: x", OrigErr: serverError{state: "55P03"}}, true},
		"dropped connection":           {database.Error{Err: "migration failed", OrigErr: driver.ErrBadConn}, false},
		"no SQLSTATE":                  {database.Error{Err: "migration failed", OrigErr: errors.New("connection reset")}, false},
		"query canceled":               {database.Error{Err: "migration failed: x", OrigErr: serverError{state: "57014"}}, false},
		"failed commit":                {database.Error{Err: "transaction commit failed", OrigErr: serverError{state: "23505"}}, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			mg := resetMigration(t, &recordingDriver{record: Record{Version: 7, Dirty: true}})

			var rollback *RollbackError
			require.ErrorAs(t, mg.downResult(RecordRead{Record: Record{Version: 8}}, tc.err), &rollback)
			assert.Equal(t, tc.want, rollback.BodyFailed)
			assert.Equal(t, RecordRead{Record: Record{Version: 7, Dirty: true}}, rollback.After)
		})
	}
}

func TestUpAndDownResults_AnUnknownStatementOutcomeIsNotBodyFailed(t *testing.T) {
	unknown := database.Error{Err: "migration failed: x", OrigErr: serverError{state: "40003"}}
	mg := resetMigration(t, &recordingDriver{record: Record{Version: 6, Dirty: true}})

	var apply *ApplyError
	require.ErrorAs(t, mg.upResult(unknown), &apply)
	assert.False(t, apply.BodyFailed)
	var rollback *RollbackError
	require.ErrorAs(t, mg.downResult(RecordRead{Record: Record{Version: 7}}, unknown), &rollback)
	assert.False(t, rollback.BodyFailed)
}

// tryLockFailed is a LockNotTakenError carrying a lock-timeout failure.
var tryLockFailed = &LockNotTakenError{Err: &database.Error{
	OrigErr: errors.New("pq: canceling statement due to lock timeout"),
	Err:     "try lock failed",
	Query:   []byte("SELECT pg_advisory_lock($1)"),
}}

func TestUpResult_ALockNotTakenReadsNoRecord(t *testing.T) {
	driver := &recordingDriver{record: Record{Version: 6, Dirty: true}}

	err := resetMigration(t, driver).upResult(tryLockFailed)

	var apply *ApplyError
	assert.False(t, errors.As(err, &apply))
	assert.Empty(t, driver.calls)
	assert.EqualError(t, err, "apply migrations: try lock failed (details: pq: canceling statement due to lock timeout)")
}

func TestDownResult_ALockNotTakenReadsNoRecord(t *testing.T) {
	driver := &recordingDriver{record: Record{Version: 6, Dirty: true}}

	err := resetMigration(t, driver).downResult(RecordRead{Record: Record{Version: 7}}, tryLockFailed)

	var rollback *RollbackError
	assert.False(t, errors.As(err, &rollback))
	assert.Empty(t, driver.calls)
	assert.EqualError(t, err, "rollback migration: try lock failed (details: pq: canceling statement due to lock timeout)")
}
