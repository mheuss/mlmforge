package main

import (
	"bytes"
	"io"
	"testing"

	"github.com/mlmforge/mlmforge/internal/platform"
	"github.com/stretchr/testify/require"
)

func TestRootCmd_MigrateRejectsAnUnknownSubcommand(t *testing.T) {
	root := newRootCmd()
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"migrate", "bogus"})

	err := root.Execute()

	require.Error(t, err, "a mistyped subcommand must not report success")
	require.Contains(t, err.Error(), "bogus", "the message must name the argument it refused")
}

func TestRootCmd_BareMigrateStillPrintsHelpAndSucceeds(t *testing.T) {
	var out bytes.Buffer
	root := newRootCmd()
	root.SetOut(&out)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"migrate"})

	require.NoError(t, root.Execute())
	require.Contains(t, out.String(), "Apply all pending migrations",
		"help must still list the subcommands")
}

// Port 1 on the loopback refuses the connection, so reaching the database would produce a dial error instead.
const refusedDBURL = "postgres://u:p@127.0.0.1:1/db?sslmode=disable"

func TestMigrateUp_RefusesMultiStatementModeBeforeConnecting(t *testing.T) {
	root := newRootCmd()
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"migrate", "up", "--db-url", refusedDBURL + "&x-multi-statement=1"})

	require.EqualError(t, root.Execute(), "migrate up refused: the database URL sets x-multi-statement=1.")
}

func TestMigrateUp_AConnectionFailureAddsNoRecoveryText(t *testing.T) {
	root := newRootCmd()
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"migrate", "up", "--db-url", refusedDBURL, "--migrations", platform.FindMigrationsDir(t)})

	err := root.Execute()

	require.ErrorContains(t, err, "open database")
	require.NotContains(t, err.Error(), "The record")
	require.NotContains(t, err.Error(), "This run was")
}

func TestMigrateUp_AStrayArgumentStillPrintsUsage(t *testing.T) {
	var out bytes.Buffer
	root := newRootCmd()
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"migrate", "up", "stray"})

	require.Error(t, root.Execute())
	require.Contains(t, out.String(), "Usage:")
}

func TestMigrateDown_RefusesMultiStatementModeBeforeConnecting(t *testing.T) {
	root := newRootCmd()
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"migrate", "down", "--db-url", refusedDBURL + "&x-multi-statement=true"})

	require.EqualError(t, root.Execute(), "migrate down refused: the database URL sets x-multi-statement=true.")
}

func TestMigrateResetDirty_RefusesMultiStatementModeBeforeConnecting(t *testing.T) {
	root := newRootCmd()
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"migrate", "reset-dirty", "--db-url", refusedDBURL + "&x-multi-statement=t"})

	require.EqualError(t, root.Execute(), "migrate reset-dirty refused: the database URL sets x-multi-statement=t.")
}

func TestMigrateResetDirtyHelp_NamesTheUpVersusDownRule(t *testing.T) {
	var out bytes.Buffer
	root := newRootCmd()
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"migrate", "reset-dirty", "--help"})

	require.NoError(t, root.Execute())
	require.Contains(t, out.String(),
		"Run it only when the output of the `mlmforge migrate up` that failed tells you to. Do not run it after a failed `mlmforge migrate down`.")
}
