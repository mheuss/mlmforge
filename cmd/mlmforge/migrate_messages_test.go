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
	"Run `mlmforge migrate reset-dirty` only if the `mlmforge migrate up` that failed on this record printed \"run `mlmforge migrate reset-dirty`\".\n" +
	"Nothing in this output is that instruction.\n" +
	"In any other case, including a failed `mlmforge migrate down`, do not run it."

func TestDirtyText_NamesTheRecordAndBothRecoveryPaths(t *testing.T) {
	got := dirtyText(sixDirty, platform.SourceInfo{Path: "/m", InSource: true, Previous: 5, HasPrevious: true})

	require.Equal(t, dirtySixText, got)
	requireNoForce(t, got)
}

func TestDirtyText_NeverSpellsOutTheResetSteps(t *testing.T) {
	for _, src := range []platform.SourceInfo{
		{Path: "/m", InSource: true, Previous: 5, HasPrevious: true},
		{Path: "/m", InSource: true},
	} {
		got := dirtyText(sixDirty, src)

		require.Equal(t, dirtySixText, got)
		require.NotContains(t, got, "then run", "a step sequence would satisfy its own condition")
		require.NotContains(t, got, "it sets the record")
		require.NotContains(t, got, "fix the cause")
	}
}

func TestDirtyText_AVersionMissingFromTheDirectorySaysResetWouldRefuse(t *testing.T) {
	got := dirtyText(sixDirty, platform.SourceInfo{Path: "/m"})

	require.Equal(t, "The record reads 6, dirty.\n"+
		"The migrations directory /m has no migration 6, so `mlmforge migrate reset-dirty` would refuse.\n"+
		"Run `mlmforge migrate reset-dirty` only if the `mlmforge migrate up` that failed on this record printed \"run `mlmforge migrate reset-dirty`\".\n"+
		"Nothing in this output is that instruction.\n"+
		"In any other case, including a failed `mlmforge migrate down`, do not run it.", got)
	requireNoForce(t, got)
}

func TestDirtyText_AnUnreadableDirectoryNamesTheReadError(t *testing.T) {
	got := dirtyText(sixDirty, platform.SourceInfo{Path: "/m", Err: errors.New("permission denied")})

	require.Equal(t, "The record reads 6, dirty.\n"+
		"The migrations directory /m could not be read for migration 6: permission denied.\n"+
		"Run `mlmforge migrate reset-dirty` only if the `mlmforge migrate up` that failed on this record printed \"run `mlmforge migrate reset-dirty`\".\n"+
		"Nothing in this output is that instruction.\n"+
		"In any other case, including a failed `mlmforge migrate down`, do not run it.", got)
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
		Err:        errors.New("migration failed: detail"),
		After:      platform.RecordRead{Record: sixDirty},
		Source:     platform.SourceInfo{Path: "/m", InSource: true, Previous: 5, HasPrevious: true},
		BodyFailed: true,
	})

	require.Equal(t, "apply migrations: migration failed: detail\n"+
		"This run was `mlmforge migrate up`. The record now reads 6, dirty.\n"+
		"Fix the cause shown above, run `mlmforge migrate reset-dirty` (it sets the record to 5, clean), "+
		"then run `mlmforge migrate up`.", got)
	requireNoForce(t, got)
}

func TestApplyFailureText_AFailedReReadWithoutABodyFailureSaysNotToReset(t *testing.T) {
	got := applyFailureText(&platform.ApplyError{
		Err: errors.New("boom"), After: platform.RecordRead{Err: errors.New("connection reset")},
	})

	require.Equal(t, "apply migrations: boom\nThe record could not be read after the failure: connection reset.\n"+
		"The error does not show that Postgres refused the migration file. Do not run `mlmforge migrate reset-dirty`.", got)
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
		Source: platform.SourceInfo{Path: "/m"}, BodyFailed: true,
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

func TestResetText_NamesTheRecordBeforeAndAfter(t *testing.T) {
	back := resetText(platform.ResetResult{From: sixDirty, To: platform.Record{Version: 5}})
	none := resetText(platform.ResetResult{From: platform.Record{Version: 1, Dirty: true}, To: platform.Record{Version: -1}})

	require.Equal(t, "The record read 6, dirty. This run set it to 5, clean.\nRun `mlmforge migrate up` next.", back)
	require.Equal(t, "The record read 1, dirty. This run set it to no version.\nRun `mlmforge migrate up` next.", none)
	requireNoForce(t, back+none)
}

func TestMigrateError_EachResetRefusalNamesTheRecordAndSaysNothingChanged(t *testing.T) {
	cases := map[string]error{
		"reset-dirty changes only a dirty record. The record reads 5, clean. The record was not changed.":        &platform.NotDirtyError{Record: platform.Record{Version: 5}},
		"reset-dirty changes only a dirty record. The record holds no version. The record was not changed.":      &platform.NotDirtyError{Record: platform.Record{Version: -1}},
		"The record reads -1, dirty. reset-dirty does not change a record at -1. The record was not changed.":    &platform.NegativeVersionError{Record: platform.Record{Version: -1, Dirty: true}},
		"The record reads 9, dirty. The migrations directory /m has no migration 9. The record was not changed.": &platform.VersionNotInSourceError{Record: platform.Record{Version: 9, Dirty: true}, Path: "/m"},
	}
	for want, err := range cases {
		got := migrateError("reset-dirty", err)
		require.EqualError(t, got, want)
		requireNoForce(t, got.Error())
	}
}

func TestMigrateError_AResetThatFailedBeforeWritingSaysSo(t *testing.T) {
	err := migrateError("reset-dirty", &platform.NotWrittenError{Err: errors.New("take migration lock: l")})

	require.EqualError(t, err, "take migration lock: l\nThis run did not write the record.")
	requireNoForce(t, err.Error())
}

func TestWriteFailureText_NamesTheRecordReadAfterTheFailure(t *testing.T) {
	read := writeFailureText(&platform.WriteError{Err: errors.New("s"), After: platform.RecordRead{Record: sixDirty}})
	unread := writeFailureText(&platform.WriteError{Err: errors.New("s"), After: platform.RecordRead{Err: errors.New("r")}})

	require.Equal(t, "write migration record: s\nThe record now reads 6, dirty.", read)
	require.Equal(t, "write migration record: s\nThe record could not be read after the failure: r.", unread)
	requireNoForce(t, read+unread)
}

func TestApplyFailureText_WithoutAPositiveBodyFailureNeverOffersTheReset(t *testing.T) {
	got := applyFailureText(&platform.ApplyError{
		Err:    errors.New("driver: bad connection"),
		After:  platform.RecordRead{Record: sixDirty},
		Source: platform.SourceInfo{Path: "/m", InSource: true, Previous: 5, HasPrevious: true},
	})

	require.Equal(t, "apply migrations: driver: bad connection\n"+
		"This run was `mlmforge migrate up`. The record now reads 6, dirty.\n"+
		"The error does not show that Postgres refused the migration file. "+
		"Do not run `mlmforge migrate reset-dirty`.", got)
	requireNoForce(t, got)
}

func TestApplyFailureText_AFailedReReadAfterABodyFailurePointsAtVersion(t *testing.T) {
	got := applyFailureText(&platform.ApplyError{
		Err: errors.New("boom"), After: platform.RecordRead{Err: errors.New("connection reset")}, BodyFailed: true,
	})

	require.Equal(t, "apply migrations: boom\nThe record could not be read after the failure: connection reset.\n"+
		"The error shows Postgres refused the migration file. If `mlmforge migrate version` then shows the record dirty, "+
		"fix the cause shown above, run `mlmforge migrate reset-dirty`, then run `mlmforge migrate up`.", got)
	requireNoForce(t, got)
}
