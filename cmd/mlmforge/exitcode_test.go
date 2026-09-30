package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
	refused := networkengine.WriteResult{
		Stream: "tree-t", EventID: "e2", Version: 2,
		ProjectionErr: fmt.Errorf("project event e2 at version 2 in stream tree-t: %w",
			&networkengine.ProjectionRefusedError{TreeID: "t", EventVersion: 2, ProjectedVersion: 3}),
		Observed: &networkengine.ProjectionObservation{Version: 3, Found: true},
	}
	place := []string{"place", "--tree-id", "t", "--user-id", "u", "--parent-id", "p", "--sponsor-id", "p"}
	behind := projectionFailure(&networkengine.ProjectionObservation{Version: 1, Found: true})
	cases := []struct {
		name   string
		args   []string
		result networkengine.WriteResult
		err    error
		exit   int
		warn   string
	}{
		{
			name: "add-root with the store observed behind", exit: 3, result: behind,
			args: []string{"add-root", "--tree-id", "t", "--user-id", "u", "--sponsor-id", "u", "--tree-type", "unilevel"},
			warn: "The next write or tree load of this tree redelivers it.",
		},
		{
			name: "remove with the store observed behind", exit: 3, result: behind,
			args: []string{"remove", "--tree-id", "t", "--user-id", "u"},
			warn: "The next write or tree load of this tree redelivers it.",
		},
		{name: "projected", result: networkengine.WriteResult{Stream: "tree-t", EventID: "e2", Version: 2}, exit: 0},
		{
			name: "store observed behind", exit: 3,
			result: projectionFailure(&networkengine.ProjectionObservation{Version: 1, Found: true}),
			warn:   "The next write or tree load of this tree redelivers it.",
		},
		{
			name: "store observed current", exit: 0,
			result: projectionFailure(&networkengine.ProjectionObservation{Version: 2, Found: true}),
			warn:   "The tree's projected version is 2.",
		},
		{
			name: "store not observed", exit: 3,
			result: projectionFailure(&networkengine.ProjectionObservation{Err: errors.New("reset")}),
			warn:   "The tree's projected version could not be read: reset.",
		},
		{name: "projection refused", result: refused, exit: 0, warn: "The tree's projected version is 3."},
		{name: "nothing appended", result: networkengine.WriteResult{Stream: "tree-t"}, err: errors.New("check_mutation refused"), exit: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var warn bytes.Buffer
			root := rootOver(t, &recordingWriter{result: tc.result, err: tc.err}, &warn)

			args := tc.args
			if args == nil {
				args = place
			}
			args = append(append([]string{"tree"}, args...), "--db-url", "postgres://x", "--worker", workerStub(t))

			got := runArgs(root, args)

			assert.Equal(t, tc.exit, got)
			assert.Contains(t, warn.String(), tc.warn)
		})
	}
}
