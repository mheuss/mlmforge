package main

import (
	"bytes"
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
	ran := 0
	for _, sub := range []string{"up", "down", "version", "reset-dirty"} {
		for _, tc := range testutil.ConnStringCases() {
			if tc.MigrateStage == "" {
				continue
			}
			t.Run(sub+"/"+tc.Name, func(t *testing.T) {
				testutil.ClearTimeoutEnv(t)
				want := "open database: " + testutil.RefusalText(t, "golang-migrate", tc.MigrateStage)

				stdout, stderr, err := executeRootCaptured("migrate", sub,
					"--db-url", tc.ConnString, "--migrations", platform.FindMigrationsDir(t))

				requireRefusalPrinted(t, tc, want, stdout, stderr, err)
				ran++
			})
		}
	}
	require.Equal(t, 28, ran, "migrate subtests that ran to the end")
}

func TestTreeCommands_ARefusedConnStringPrintsNoPassword(t *testing.T) {
	commands := []struct {
		name  string
		flags []string
	}{
		{"load", []string{"--tree-id", "t9", "--tree-type", "unilevel"}},
		{"add-root", []string{"--tree-id", "t9", "--user-id", "u", "--sponsor-id", "u", "--tree-type", "unilevel"}},
		{"place", []string{"--tree-id", "t9", "--user-id", "u", "--parent-id", "p", "--sponsor-id", "u"}},
		{"remove", []string{"--tree-id", "t9", "--user-id", "u"}},
	}
	ran := 0
	for _, command := range commands {
		for _, tc := range testutil.ConnStringCases() {
			if tc.Name != "slash" && tc.Name != "bad-sslmode" {
				continue
			}
			t.Run(command.name+"/"+tc.Name, func(t *testing.T) {
				testutil.IsolatePgxEnv(t)
				want := "open database pool: " + testutil.RefusalText(t, "pgx", tc.PgxStage)
				args := append([]string{"tree", command.name, "--db-url", tc.ConnString, "--worker", testWorker(t)}, command.flags...)

				stdout, stderr, err := executeRootCaptured(args...)

				requireRefusalPrinted(t, tc, want, stdout, stderr, err)
				ran++
			})
		}
	}
	require.Equal(t, 8, ran, "tree subtests that ran to the end")
}
