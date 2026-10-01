package main

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/mlmforge/mlmforge/internal/platform"
	"github.com/mlmforge/mlmforge/internal/testutil"
	"github.com/stretchr/testify/require"
)

// executeRootCaptured runs one command line through the real command tree and returns what it printed.
func executeRootCaptured(args ...string) (stdout, stderr string, err error) {
	root := newRootCmd()
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(args)
	err = root.Execute()
	return out.String(), errOut.String(), err
}

// requireRefusalPrinted checks the returned error, stdout and stderr of one refused run.
func requireRefusalPrinted(t *testing.T, tc testutil.ConnStringCase, want, stdout, stderr string, err error) {
	t.Helper()
	require.Error(t, err)
	testutil.RequireNoPasswordWindow(t, stdout+"\n"+stderr+"\n"+err.Error(), tc.Password, want, tc.WithoutPassword())
	testutil.RequireNoDriverParseError(t, err)
	require.EqualError(t, err, want)
	require.Equal(t, "Error: "+want+"\n", stderr)
	require.Empty(t, stdout)
}

func TestMigrateCommands_ARefusedConnStringPrintsNoPassword(t *testing.T) {
	selected := 0
	for _, sub := range []string{"up", "down", "version", "reset-dirty"} {
		for _, tc := range testutil.ConnStringCases() {
			if tc.MigrateStage == "" {
				continue
			}
			selected++
			t.Run(sub+"/"+tc.Name, func(t *testing.T) {
				testutil.ClearTimeoutEnv(t)
				testutil.ClearLibPQEnv(t)
				want := "open database: " + testutil.RefusalText(t, "golang-migrate", tc.MigrateStage)
				if tc.ResolveStage != "" {
					want = testutil.PreDriverText(t, tc.ResolveStage, tc.ResolvePart)
				}

				stdout, stderr, err := executeRootCaptured("migrate", sub,
					"--db-url", tc.ConnString, "--migrations", platform.FindMigrationsDir(t))

				requireRefusalPrinted(t, tc, want, stdout, stderr, err)
			})
		}
	}
	require.Equal(t, 36, selected, "migrate cases selected")
}

// treeRefusalCommands are the tree subcommands with the flags each needs to reach its database.
var treeRefusalCommands = []struct {
	name  string
	flags []string
}{
	{"load", []string{"--tree-id", "t9", "--tree-type", "unilevel"}},
	{"add-root", []string{"--tree-id", "t9", "--user-id", "u", "--sponsor-id", "u", "--tree-type", "unilevel"}},
	{"place", []string{"--tree-id", "t9", "--user-id", "u", "--parent-id", "p", "--sponsor-id", "u"}},
	{"remove", []string{"--tree-id", "t9", "--user-id", "u"}},
}

func TestTreeCommands_ARefusedConnStringPrintsNoPassword(t *testing.T) {
	selected := 0
	for _, command := range treeRefusalCommands {
		for _, tc := range testutil.ConnStringCases() {
			if tc.Name != "slash" && tc.Name != "bad-sslmode" {
				continue
			}
			selected++
			t.Run(command.name+"/"+tc.Name, func(t *testing.T) {
				testutil.IsolatePgxEnv(t)
				want := "open database pool: " + testutil.RefusalText(t, "pgx", tc.PgxStage)
				if tc.ResolveStage != "" {
					want = testutil.PreDriverText(t, tc.ResolveStage, tc.ResolvePart)
				}
				args := append([]string{"tree", command.name, "--db-url", tc.ConnString, "--worker", testWorker(t)}, command.flags...)

				stdout, stderr, err := executeRootCaptured(args...)

				requireRefusalPrinted(t, tc, want, stdout, stderr, err)
			})
		}
	}
	require.Equal(t, 8, selected, "tree cases selected")
}

// failingOpener fails the test if a tree command opens its dependencies.
func failingOpener(t *testing.T) depsOpener {
	return func(context.Context, string, string) (*treeDeps, error) {
		t.Errorf("the tree command opened its dependencies")
		return nil, errors.New("opener called")
	}
}

// executeTreeCaptured runs one tree command line over open and returns what it printed.
func executeTreeCaptured(open depsOpener, args ...string) (stdout, stderr string, err error) {
	cmd := newTreeCmdWith(open, nil, nil)
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err = cmd.Execute()
	return out.String(), errOut.String(), err
}

// requirePreDriverRefusal checks the returned error, stdout and stderr of one run refused before any driver.
func requirePreDriverRefusal(t *testing.T, tc testutil.ResolveRefusedCase, stdout, stderr string, err error) {
	t.Helper()
	want := testutil.PreDriverText(t, tc.Stage, tc.Part)
	require.EqualError(t, err, want)
	require.Equal(t, "Error: "+want+"\n", stderr)
	require.Empty(t, stdout)
	if tc.WiringPassword != "" {
		testutil.RequireNoPasswordWindow(t, stdout+"\n"+stderr+"\n"+err.Error(), tc.WiringPassword, want, tc.WiringWithoutPassword())
	}
	testutil.RequireNoDriverParseError(t, err)
	var cse *platform.ConnStringError
	require.ErrorAs(t, err, &cse)
	require.Equal(t, tc.Stage, cse.Stage())
	require.Equal(t, tc.Part, cse.Part())
}

func TestMigrateCommands_RefuseBeforeTheDriver(t *testing.T) {
	selected := 0
	for _, sub := range []string{"up", "down", "version", "reset-dirty"} {
		for _, tc := range testutil.ResolveRefusedCases() {
			selected++
			t.Run(sub+"/"+tc.Name, func(t *testing.T) {
				testutil.ClearTimeoutEnv(t)
				testutil.ClearLibPQEnv(t)

				stdout, stderr, err := executeRootCaptured("migrate", sub,
					"--db-url", tc.Wiring, "--migrations", platform.FindMigrationsDir(t))

				requirePreDriverRefusal(t, tc, stdout, stderr, err)
			})
		}
	}
	require.Equal(t, 44, selected, "migrate runs")
}

func TestTreeCommands_RefuseBeforeTheDriver(t *testing.T) {
	selected := 0
	for _, command := range treeRefusalCommands {
		for _, tc := range testutil.ResolveRefusedCases() {
			selected++
			t.Run(command.name+"/"+tc.Name, func(t *testing.T) {
				testutil.IsolatePgxEnv(t)
				args := append([]string{command.name, "--db-url", tc.Wiring, "--worker", testWorker(t)}, command.flags...)

				stdout, stderr, err := executeTreeCaptured(failingOpener(t), args...)

				requirePreDriverRefusal(t, tc, stdout, stderr, err)
			})
		}
	}
	require.Equal(t, 44, selected, "tree runs")
}
