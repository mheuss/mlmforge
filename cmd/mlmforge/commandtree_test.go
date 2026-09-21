package main

import (
	"io"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// groupPaths returns the argument path of every command that holds
// subcommands, starting with the one it is given. Cobra generates help and
// completion, whose argument handling is cobra's contract rather than this
// repository's, so they are skipped.
func groupPaths(cmd *cobra.Command, prefix []string) [][]string {
	if !cmd.HasSubCommands() {
		return nil
	}
	paths := [][]string{prefix}
	for _, child := range cmd.Commands() {
		switch child.Name() {
		case "help", "completion":
			continue
		}
		next := append(append([]string{}, prefix...), child.Name())
		paths = append(paths, groupPaths(child, next)...)
	}
	return paths
}

func TestEveryCommandGroupRejectsAnUnknownSubcommand(t *testing.T) {
	paths := groupPaths(newRootCmd(), nil)

	// Without this the test passes when the walk finds nothing.
	require.GreaterOrEqual(t, len(paths), 3, "expected the root and both groups")

	for _, path := range paths {
		name := "root"
		if len(path) > 0 {
			name = strings.Join(path, " ")
		}
		t.Run(name, func(t *testing.T) {
			// A fresh root per case. Cobra keeps state from a previous Execute.
			root := newRootCmd()
			root.SetOut(io.Discard)
			root.SetErr(io.Discard)
			root.SetArgs(append(append([]string{}, path...), "definitely-not-a-command"))

			require.Error(t, root.Execute(), "this group accepted an unknown subcommand")
		})
	}
}
