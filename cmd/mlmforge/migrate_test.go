package main

import (
	"bytes"
	"io"
	"testing"

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
	require.Contains(t, out.String(), "up", "help must still list the subcommands")
}
