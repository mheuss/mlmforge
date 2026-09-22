package main

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// groupPaths returns the argument path of every command that holds
// subcommands, starting with the one it is given.
func groupPaths(cmd *cobra.Command, prefix []string) [][]string {
	if !cmd.HasSubCommands() {
		return nil
	}
	paths := [][]string{prefix}
	for _, child := range cmd.Commands() {
		next := append(append([]string{}, prefix...), child.Name())
		paths = append(paths, groupPaths(child, next)...)
	}
	return paths
}

// leafPaths returns the argument path of every command that holds no
// subcommands.
func leafPaths(cmd *cobra.Command, prefix []string) [][]string {
	if !cmd.HasSubCommands() {
		if len(prefix) > 0 {
			return [][]string{prefix}
		}
		return nil
	}
	var paths [][]string
	for _, child := range cmd.Commands() {
		next := append(append([]string{}, prefix...), child.Name())
		paths = append(paths, leafPaths(child, next)...)
	}
	return paths
}

// Holds every leaf to taking no positional arguments.
func TestEveryLeafCommandRejectsAStrayArgument(t *testing.T) {
	paths := leafPaths(newRootCmd(), nil)

	// Without this the test passes when the walk finds nothing.
	require.GreaterOrEqual(t, len(paths), 4, "the walk found %d leaves: %v", len(paths), paths)

	for _, path := range paths {
		t.Run(strings.Join(path, " "), func(t *testing.T) {
			t.Setenv("DATABASE_URL", "")
			t.Setenv(workerPathEnv, "")
			root := newRootCmd()
			root.SetOut(io.Discard)
			root.SetErr(io.Discard)
			root.SetArgs(append(append([]string{}, path...), "definitely-not-an-argument"))

			require.ErrorContains(t, root.Execute(), "definitely-not-an-argument",
				"this command accepted a stray argument, or refused it without naming it")
		})
	}
}

func TestEveryCommandGroupRejectsAnUnknownSubcommand(t *testing.T) {
	paths := groupPaths(newRootCmd(), nil)

	// Without this the test passes when the walk finds nothing.
	require.GreaterOrEqual(t, len(paths), 3, "the walk found %d groups: %v", len(paths), paths)

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

			require.ErrorContains(t, root.Execute(), "definitely-not-a-command",
				"this group accepted an unknown subcommand, or refused it without naming it")
		})
	}
}

func TestEveryCommandGroupStillPrintsHelpWithNoArguments(t *testing.T) {
	paths := groupPaths(newRootCmd(), nil)

	require.GreaterOrEqual(t, len(paths), 3, "the walk found %d groups: %v", len(paths), paths)

	for _, path := range paths {
		name := "root"
		if len(path) > 0 {
			name = strings.Join(path, " ")
		}
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			root := newRootCmd()
			root.SetOut(&out)
			root.SetErr(io.Discard)
			root.SetArgs(append([]string{}, path...))

			require.NoError(t, root.Execute(), "a group invoked with no arguments must not fail")
			require.Contains(t, out.String(), "Available Commands:",
				"a group invoked with no arguments must list its subcommands")
		})
	}
}
