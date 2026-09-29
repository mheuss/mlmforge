package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/mlmforge/mlmforge/internal/platform"
	"github.com/stretchr/testify/require"
)

// requireNoForce fails when authored text says force.
func requireNoForce(t *testing.T, text string) {
	t.Helper()
	require.NotContains(t, strings.ToLower(text), "force", "authored text says force: %q", text)
}

var sixDirty = platform.Record{Version: 6, Dirty: true}

const dirtySixText = "The record reads 6, dirty.\n" +
	"If the command that failed was `mlmforge migrate up`: fix the cause shown in its error, " +
	"run `mlmforge migrate reset-dirty` (it sets the record to 5, clean), then run `mlmforge migrate up`.\n" +
	"If the command that failed was `mlmforge migrate down`: do not run `mlmforge migrate reset-dirty`."

func TestDirtyText_NamesTheRecordAndBothRecoveryPaths(t *testing.T) {
	got := dirtyText(sixDirty, platform.SourceInfo{Path: "/m", InSource: true, Previous: 5, HasPrevious: true})

	require.Equal(t, dirtySixText, got)
	requireNoForce(t, got)
}

func TestDirtyText_AtTheFirstMigrationSaysNoVersionIsLeft(t *testing.T) {
	got := dirtyText(platform.Record{Version: 1, Dirty: true}, platform.SourceInfo{Path: "/m", InSource: true})

	require.Equal(t, "The record reads 1, dirty.\n"+
		"If the command that failed was `mlmforge migrate up`: fix the cause shown in its error, "+
		"run `mlmforge migrate reset-dirty` (afterwards the record holds no version), then run `mlmforge migrate up`.\n"+
		"If the command that failed was `mlmforge migrate down`: do not run `mlmforge migrate reset-dirty`.", got)
	requireNoForce(t, got)
}

func TestDirtyText_AVersionMissingFromTheDirectorySaysResetWillRefuse(t *testing.T) {
	got := dirtyText(sixDirty, platform.SourceInfo{Path: "/m"})

	require.Equal(t, "The record reads 6, dirty.\n"+
		"If the command that failed was `mlmforge migrate up`: The migrations directory /m has no migration 6, "+
		"so `mlmforge migrate reset-dirty` will refuse.\n"+
		"If the command that failed was `mlmforge migrate down`: do not run `mlmforge migrate reset-dirty`.", got)
	requireNoForce(t, got)
}

func TestDirtyText_AnUnreadableDirectoryNamesTheReadError(t *testing.T) {
	got := dirtyText(sixDirty, platform.SourceInfo{Path: "/m", Err: errors.New("permission denied")})

	require.Equal(t, "The record reads 6, dirty.\n"+
		"If the command that failed was `mlmforge migrate up`: The migrations directory /m could not be read for migration 6: permission denied.\n"+
		"If the command that failed was `mlmforge migrate down`: do not run `mlmforge migrate reset-dirty`.", got)
	requireNoForce(t, got)
}

func TestDirtyText_AMinusOneRecordNamesOnlyTheRecord(t *testing.T) {
	got := dirtyText(platform.Record{Version: -1, Dirty: true}, platform.SourceInfo{Path: "/m"})

	require.Equal(t, "The record reads -1, dirty. reset-dirty does not change a record at -1.", got)
	requireNoForce(t, got)
}

func TestVersionText_NamesEachRecordShape(t *testing.T) {
	none := versionText(platform.Status{Record: platform.Record{Version: -1}})
	clean := versionText(platform.Status{Record: platform.Record{Version: 5}})
	require.Equal(t, "Version: none, Dirty: false", none)
	require.Equal(t, "Version: 5, Dirty: false", clean)
	requireNoForce(t, none+clean)
	dirty := versionText(platform.Status{Record: sixDirty,
		Source: platform.SourceInfo{Path: "/m", InSource: true, Previous: 5, HasPrevious: true}})
	require.Equal(t, "Version: 6, Dirty: true\n"+dirtySixText, dirty)
	requireNoForce(t, dirty)
}

func TestWithoutReleaseErrors_AFailedOperationWarnsWithoutClaimingWhatItDid(t *testing.T) {
	op := &platform.NotDirtyError{Record: platform.Record{Version: 5}}
	unlock := &platform.ReleaseError{What: "releasing the migration lock failed", Err: errors.New("u")}
	closing := &platform.ReleaseError{What: "closing the migration drivers failed", Err: errors.New("c")}
	var warnings bytes.Buffer

	rest := withoutReleaseErrors(&warnings, "the record was written", errors.Join(errors.Join(op, unlock), closing))

	require.Same(t, op, rest)
	require.Equal(t, "warning: releasing the migration lock failed: u\n"+
		"warning: closing the migration drivers failed: c\n", warnings.String())
}

func TestWithoutReleaseErrors_AfterASucceededOperationSaysWhatItDid(t *testing.T) {
	var warnings bytes.Buffer

	rest := withoutReleaseErrors(&warnings, "the record was written", errors.Join(nil,
		&platform.ReleaseError{What: "releasing the migration lock failed", Err: errors.New("u")}))

	require.NoError(t, rest)
	require.Equal(t, "warning: the record was written; releasing the migration lock failed: u\n", warnings.String())
}

func TestApplyFailureText_ADirtyRecordAddsTheRecordAndTheRecovery(t *testing.T) {
	got := applyFailureText(&platform.ApplyError{
		Err:    errors.New("migration failed: detail"),
		After:  platform.RecordRead{Record: sixDirty},
		Source: platform.SourceInfo{Path: "/m", InSource: true, Previous: 5, HasPrevious: true},
	})

	require.Equal(t, "apply migrations: migration failed: detail\n"+
		"This run was `mlmforge migrate up`. The record now reads 6, dirty.\n"+
		"Fix the cause shown above, run `mlmforge migrate reset-dirty` (it sets the record to 5, clean), "+
		"then run `mlmforge migrate up`.", got)
	requireNoForce(t, got)
}

func TestApplyFailureText_AFailedReReadSaysSo(t *testing.T) {
	got := applyFailureText(&platform.ApplyError{
		Err: errors.New("boom"), After: platform.RecordRead{Err: errors.New("connection reset")},
	})

	require.Equal(t, "apply migrations: boom\nThe record could not be read after the failure: connection reset.", got)
	requireNoForce(t, got)
}

func TestApplyFailureText_ACleanRecordAddsNothing(t *testing.T) {
	got := applyFailureText(&platform.ApplyError{
		Err: errors.New("boom"), After: platform.RecordRead{Record: platform.Record{Version: 5}},
	})

	require.Equal(t, "apply migrations: boom", got)
	requireNoForce(t, got)
}

func TestApplyFailureText_AMinusOneRecordSaysResetWillNotChangeIt(t *testing.T) {
	got := applyFailureText(&platform.ApplyError{
		Err: errors.New("boom"), After: platform.RecordRead{Record: platform.Record{Version: -1, Dirty: true}},
	})

	require.Equal(t, "apply migrations: boom\n"+
		"This run was `mlmforge migrate up`. The record now reads -1, dirty.\n"+
		"reset-dirty does not change a record at -1.", got)
	requireNoForce(t, got)
}

func TestApplyFailureText_AVersionMissingFromTheDirectorySaysResetWillRefuse(t *testing.T) {
	got := applyFailureText(&platform.ApplyError{
		Err: errors.New("boom"), After: platform.RecordRead{Record: sixDirty},
		Source: platform.SourceInfo{Path: "/m"},
	})

	require.Equal(t, "apply migrations: boom\n"+
		"This run was `mlmforge migrate up`. The record now reads 6, dirty.\n"+
		"The migrations directory /m has no migration 6, so `mlmforge migrate reset-dirty` will refuse.", got)
	requireNoForce(t, got)
}

func TestMigrateError_ADirtyRecordOnUpReplacesTheLibraryText(t *testing.T) {
	err := migrateError("up", &platform.DirtyError{Record: sixDirty,
		Source: platform.SourceInfo{Path: "/m", InSource: true, Previous: 5, HasPrevious: true}})

	require.EqualError(t, err, "migrate up did not run. "+dirtySixText)
	requireNoForce(t, err.Error())
}

func TestMigrateError_AMultiStatementRefusalNamesTheCommandAndTheValue(t *testing.T) {
	err := migrateError("up", &platform.MultiStatementError{Command: "up", Value: "1"})

	require.EqualError(t, err, "migrate up refused: the database URL sets x-multi-statement=1.")
	requireNoForce(t, err.Error())
}

func TestMigrateError_LeavesAnUntypedErrorAsItIs(t *testing.T) {
	plain := errors.New("open database: dial tcp: connection refused")

	require.Same(t, plain, migrateError("up", plain))
	require.NoError(t, migrateError("up", nil))
}

func TestDirtyText_ARecordBelowMinusOneNamesTheValueItRead(t *testing.T) {
	got := dirtyText(platform.Record{Version: -2, Dirty: true}, platform.SourceInfo{Path: "/m"})

	require.Equal(t, "The record reads -2, dirty. reset-dirty does not change a record at -2.", got)
	requireNoForce(t, got)
}

func TestVersionText_ACleanRecordBelowMinusOnePrintsItsValue(t *testing.T) {
	got := versionText(platform.Status{Record: platform.Record{Version: -2}})

	require.Equal(t, "Version: -2, Dirty: false", got)
	requireNoForce(t, got)
}

func TestApplyFailureText_ARecordBelowMinusOneNamesTheValueItRead(t *testing.T) {
	got := applyFailureText(&platform.ApplyError{
		Err: errors.New("boom"), After: platform.RecordRead{Record: platform.Record{Version: -2, Dirty: true}},
	})

	require.Equal(t, "apply migrations: boom\n"+
		"This run was `mlmforge migrate up`. The record now reads -2, dirty.\n"+
		"reset-dirty does not change a record at -2.", got)
	requireNoForce(t, got)
}

var sevenDirtyAfterDown = platform.RecordRead{Record: platform.Record{Version: 7, Dirty: true}}

func TestRollbackFailureText_NamesTheRecordBeforeAndAfter(t *testing.T) {
	got := rollbackFailureText(&platform.RollbackError{
		Err:    errors.New("lock timeout"),
		Before: platform.RecordRead{Record: platform.Record{Version: 8}},
		After:  sevenDirtyAfterDown,
		Source: platform.SourceInfo{Path: "/m", InSource: true, Previous: 6, HasPrevious: true},
	})

	require.Equal(t, "rollback migration: lock timeout\n"+
		"This run was `mlmforge migrate down`. The record read 8, clean before this run and now reads 7, dirty.\n"+
		"Do not run `mlmforge migrate reset-dirty`. It would set the record to 6, clean.", got)
	requireNoForce(t, got)
}

func TestRollbackFailureText_AFailedBeforeReadNamesOnlyTheRecordAfter(t *testing.T) {
	got := rollbackFailureText(&platform.RollbackError{
		Err:    errors.New("boom"),
		Before: platform.RecordRead{Err: errors.New("x")},
		After:  sevenDirtyAfterDown,
		Source: platform.SourceInfo{Path: "/m", InSource: true, Previous: 6, HasPrevious: true},
	})

	require.Equal(t, "rollback migration: boom\n"+
		"This run was `mlmforge migrate down`. The record now reads 7, dirty.\n"+
		"Do not run `mlmforge migrate reset-dirty`. It would set the record to 6, clean.", got)
	requireNoForce(t, got)
}

func TestRollbackFailureText_AtTheFirstMigrationSaysNoVersionWouldBeLeft(t *testing.T) {
	got := rollbackFailureText(&platform.RollbackError{
		Err:    errors.New("boom"),
		Before: platform.RecordRead{Record: platform.Record{Version: 2}},
		After:  platform.RecordRead{Record: platform.Record{Version: 1, Dirty: true}},
		Source: platform.SourceInfo{Path: "/m", InSource: true},
	})

	require.Equal(t, "rollback migration: boom\n"+
		"This run was `mlmforge migrate down`. The record read 2, clean before this run and now reads 1, dirty.\n"+
		"Do not run `mlmforge migrate reset-dirty`. Afterwards the record would hold no version.", got)
	requireNoForce(t, got)
}

func TestRollbackFailureText_AMinusOneRecordSaysResetWillNotChangeIt(t *testing.T) {
	got := rollbackFailureText(&platform.RollbackError{
		Err:    errors.New("boom"),
		Before: platform.RecordRead{Record: platform.Record{Version: 1}},
		After:  platform.RecordRead{Record: platform.Record{Version: -1, Dirty: true}},
		Source: platform.SourceInfo{Path: "/m"},
	})

	require.Equal(t, "rollback migration: boom\n"+
		"This run was `mlmforge migrate down`. The record read 1, clean before this run and now reads -1, dirty.\n"+
		"reset-dirty does not change a record at -1.", got)
	requireNoForce(t, got)
}

func TestRollbackFailureText_AVersionMissingFromTheDirectoryDropsTheEffect(t *testing.T) {
	got := rollbackFailureText(&platform.RollbackError{
		Err:    errors.New("boom"),
		Before: platform.RecordRead{Record: platform.Record{Version: 8}},
		After:  sevenDirtyAfterDown,
		Source: platform.SourceInfo{Path: "/m"},
	})

	require.Equal(t, "rollback migration: boom\n"+
		"This run was `mlmforge migrate down`. The record read 8, clean before this run and now reads 7, dirty.\n"+
		"Do not run `mlmforge migrate reset-dirty`.", got)
	requireNoForce(t, got)
}

func TestRollbackFailureText_ACleanOrUnreadableRecordAfter(t *testing.T) {
	clean := rollbackFailureText(&platform.RollbackError{
		Err: errors.New("boom"), After: platform.RecordRead{Record: platform.Record{Version: 8}},
	})
	unread := rollbackFailureText(&platform.RollbackError{
		Err: errors.New("boom"), After: platform.RecordRead{Err: errors.New("reset")},
	})

	require.Equal(t, "rollback migration: boom", clean)
	require.Equal(t, "rollback migration: boom\nThe record could not be read after the failure: reset.", unread)
	requireNoForce(t, clean+unread)
}

func TestMigrateError_ADirtyRecordOnDownNamesTheCommand(t *testing.T) {
	err := migrateError("down", &platform.DirtyError{Record: sixDirty,
		Source: platform.SourceInfo{Path: "/m", InSource: true, Previous: 5, HasPrevious: true}})

	require.EqualError(t, err, "migrate down did not run. "+dirtySixText)
	requireNoForce(t, err.Error())
}
