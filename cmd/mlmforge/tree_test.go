package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mlmforge/mlmforge/internal/networkengine"
	"github.com/stretchr/testify/require"
)

func TestNewTreeCmd_RegistersTheLoadSubcommand(t *testing.T) {
	names := map[string]bool{}
	for _, c := range newTreeCmd().Commands() {
		names[c.Name()] = true
	}

	require.True(t, names["load"])
}

func TestLoadTreeOptions_OnlySetMatrixParamsWhenAsked(t *testing.T) {
	require.Empty(t, loadTreeOptions("unilevel", 0, ""))
	require.Len(t, loadTreeOptions("matrix", 3, "breadth_first"), 1)
}

// recordingLoader captures what the RunE body passed through.
type recordingLoader struct {
	treeID, treeType string
	optCount         int
}

func (r *recordingLoader) LoadTree(_ context.Context, treeID, treeType string, opts ...networkengine.LoadTreeOption) error {
	r.treeID, r.treeType, r.optCount = treeID, treeType, len(opts)
	return nil
}

// Executing the command is the only thing that proves the flags reach the
// loader. Asserting on registered names leaves every RunE body untested.
func TestTreeLoadCmd_PassesItsFlagsToTheLoader(t *testing.T) {
	rec := &recordingLoader{}
	cmd := newTreeCmdWith(
		func(context.Context, string, string) (*treeDeps, error) {
			return &treeDeps{release: func() error { return nil }}, nil
		},
		func(*treeDeps) treeLoader { return rec },
	)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{
		"load", "--db-url", "postgres://x", "--worker", workerStub(t),
		"--tree-id", "t9", "--tree-type", "matrix",
		"--matrix-width", "3", "--matrix-spillover", "breadth_first",
	})

	require.NoError(t, cmd.Execute())
	require.Equal(t, "t9", rec.treeID)
	require.Equal(t, "matrix", rec.treeType)
	require.Equal(t, 1, rec.optCount)
}

// recordingMutator answers only the two calls a root-only matrix load makes.
// The embedded interface is nil, so any other call panics rather than
// returning a zero value a test could pass against.
type recordingMutator struct {
	networkengine.TreeMutator
	width     int
	spillover string
	roots     []string
}

func (m *recordingMutator) CreateMatrixTree(_ context.Context, _ string, width int, spillover string) error {
	m.width, m.spillover = width, spillover
	return nil
}

func (m *recordingMutator) AddRoot(_ context.Context, _, userID string, _ int64) error {
	m.roots = append(m.roots, userID)
	return nil
}

// Counting the options cannot tell WithMatrixParams(3, "breadth_first") from
// WithMatrixParams(0, ""), because loadTreeConfig is unexported and package
// main cannot apply an option and read it back. Driving the real TreeLoader
// puts the values somewhere observable: CreateMatrixTree takes them as
// arguments.
func TestTreeLoadCmd_MatrixFlagValuesReachTheEngine(t *testing.T) {
	store := networkengine.NewMemoryTreeStore()
	require.NoError(t, store.InsertNode(context.Background(), networkengine.TreeNodeRow{
		ID:         "row-1",
		TreeID:     "t9",
		UserID:     "u-root",
		Depth:      0,
		EnrolledAt: time.Unix(1700000000, 0),
	}))
	mut := &recordingMutator{}

	cmd := newTreeCmdWith(
		func(context.Context, string, string) (*treeDeps, error) {
			return &treeDeps{release: func() error { return nil }}, nil
		},
		func(*treeDeps) treeLoader { return networkengine.NewTreeLoader(store, mut) },
	)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{
		"load", "--db-url", "postgres://x", "--worker", workerStub(t),
		"--tree-id", "t9", "--tree-type", "matrix",
		"--matrix-width", "3", "--matrix-spillover", "breadth_first",
	})

	require.NoError(t, cmd.Execute())
	require.Equal(t, 3, mut.width)
	require.Equal(t, "breadth_first", mut.spillover)
	require.Equal(t, []string{"u-root"}, mut.roots)
}

func TestTreeLoadCmd_ReleasesTheDepsAfterRunning(t *testing.T) {
	released := false
	cmd := newTreeCmdWith(
		func(context.Context, string, string) (*treeDeps, error) {
			return &treeDeps{release: func() error { released = true; return nil }}, nil
		},
		func(*treeDeps) treeLoader { return &recordingLoader{} },
	)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{
		"load", "--db-url", "postgres://x", "--worker", workerStub(t),
		"--tree-id", "t9", "--tree-type", "unilevel",
	})

	require.NoError(t, cmd.Execute())
	require.True(t, released)
}

// Supplying both flags and asserting the message is what makes this fail when
// the Args guard is removed. Without them the run still errors, on the unset
// --db-url, and asserting the fact of an error cannot tell the two apart. The
// injected opener keeps that path off the network.
func TestTreeLoadCmd_RejectsStrayPositionalArguments(t *testing.T) {
	cmd := newTreeCmdWith(
		func(context.Context, string, string) (*treeDeps, error) {
			return &treeDeps{release: func() error { return nil }}, nil
		},
		func(*treeDeps) treeLoader { return &recordingLoader{} },
	)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{
		"load", "--db-url", "postgres://x", "--worker", workerStub(t),
		"--tree-id", "t", "--tree-type", "unilevel", "stray",
	})

	require.ErrorContains(t, cmd.Execute(), `unknown command "stray" for "tree load"`)
}

// workerStub writes an executable file so resolveWorkerPath succeeds without a
// real worker. Nothing runs it: the opener is injected in these tests.
func workerStub(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "network-engine-worker")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755))
	return path
}
