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
