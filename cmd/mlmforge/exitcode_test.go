package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/mlmforge/mlmforge/internal/networkengine"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
)

func TestExitCode_ReadsTheCodeThroughAWrap(t *testing.T) {
	err := &exitCodeError{code: 3, err: errors.New("appended")}

	assert.Equal(t, 3, exitCode(errors.Join(errors.New("context"), err)))
}

func TestExitCode_IsZeroWithoutAnError(t *testing.T) {
	assert.Equal(t, 0, exitCode(nil))
}

func TestExitCode_IsOneForAnUncodedError(t *testing.T) {
	assert.Equal(t, 1, exitCode(errors.New("anything else")))
}

// rootOver builds the root command with a tree group over w, writing its
// stderr to warn.
func rootOver(t *testing.T, w treeWriter, warn io.Writer) *cobra.Command {
	t.Helper()
	root := &cobra.Command{Use: "mlmforge"}
	root.SetOut(io.Discard)
	root.SetErr(warn)
	root.AddCommand(newTreeCmdWith(
		func(context.Context, string, string) (*treeDeps, error) {
			return &treeDeps{release: func() error { return nil }}, nil
		},
		nil,
		func(*treeDeps) treeWriter { return w },
	))
	return root
}

func TestRunArgs_MapsAWritesOutcomeToItsExitCode(t *testing.T) {
	place := []string{"place", "--tree-id", "t", "--user-id", "u", "--parent-id", "p", "--sponsor-id", "p"}
	behind := projectionFailure(&networkengine.ProjectionObservation{Version: 1, Found: true})
	redelivers := "The next write or tree load of this tree redelivers it."
	cases := []struct {
		name   string
		args   []string
		result networkengine.WriteResult
		err    error
		exit   int
		warn   string
	}{
		{
			name: "add-root with the store observed behind", exit: 3, result: behind, warn: redelivers,
			args: []string{"add-root", "--tree-id", "t", "--user-id", "u", "--sponsor-id", "u", "--tree-type", "unilevel"},
		},
		{name: "place with the store observed behind", exit: 3, result: behind, warn: redelivers, args: place},
		{
			name: "remove with the store observed behind", exit: 3, result: behind, warn: redelivers,
			args: []string{"remove", "--tree-id", "t", "--user-id", "u"},
		},
		{name: "projected", args: place, result: networkengine.WriteResult{Stream: "tree-t", EventID: "e2", Version: 2}, exit: 0},
		{
			name: "nothing appended", args: place, result: networkengine.WriteResult{Stream: "tree-t"},
			err: errors.New("check_mutation refused"), exit: 1, warn: "Error: check_mutation refused",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var warn bytes.Buffer
			root := rootOver(t, &recordingWriter{result: tc.result, err: tc.err}, &warn)
			args := append(append([]string{"tree"}, tc.args...), "--db-url", "postgres://x", "--worker", workerStub(t))

			got := runArgs(root, args)

			assert.Equal(t, tc.exit, got)
			if tc.warn == "" {
				assert.Empty(t, warn.String())
				return
			}
			assert.Contains(t, warn.String(), tc.warn)
		})
	}
}
