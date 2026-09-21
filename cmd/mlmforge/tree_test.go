package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
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
		func(*treeDeps, string) (treeLoader, nodeCounter) { return rec, countingTo(0) },
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

// recordingMutator answers only CreateMatrixTree and AddRoot. The embedded
// interface is nil, so any other call panics rather than returning a zero
// value a test could pass against.
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

// Counting the options cannot tell two matrix configurations apart. Driving
// the real loader puts the values somewhere observable: CreateMatrixTree takes
// them as arguments.
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
		func(*treeDeps, string) (treeLoader, nodeCounter) {
			return networkengine.NewTreeLoader(store, mut), countingTo(1)
		},
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
		func(*treeDeps, string) (treeLoader, nodeCounter) { return &recordingLoader{}, countingTo(0) },
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
		func(*treeDeps, string) (treeLoader, nodeCounter) { return &recordingLoader{}, countingTo(0) },
	)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{
		"load", "--db-url", "postgres://x", "--worker", workerStub(t),
		"--tree-id", "t", "--tree-type", "unilevel", "stray",
	})

	require.ErrorContains(t, cmd.Execute(), `unknown command "stray" for "tree load"`)
}

// failingLoader drives runTreeLoad down its reporting path.
type failingLoader struct{ err error }

func (f *failingLoader) LoadTree(context.Context, string, string, ...networkengine.LoadTreeOption) error {
	return f.err
}

// The failure reaches the operator exactly once, and brings no usage dump.
func TestTreeLoadCmd_ReportsAFailureOnceWithoutUsage(t *testing.T) {
	cmd := newTreeCmdWith(
		func(context.Context, string, string) (*treeDeps, error) {
			return &treeDeps{release: func() error { return nil }}, nil
		},
		func(*treeDeps, string) (treeLoader, nodeCounter) {
			return &failingLoader{err: errors.New("boom")}, countingTo(0)
		},
	)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"load", "--db-url", "postgres://x", "--worker", workerStub(t),
		"--tree-id", "t9", "--tree-type", "unilevel",
	})

	require.Error(t, cmd.Execute())
	require.Equal(t, 1, strings.Count(out.String(), "load failed: boom"))
	require.Contains(t, out.String(), "Error:")
	require.NotContains(t, out.String(), "Usage:")
}

// Driving the root command is what distinguishes SilenceUsage on load from
// SilenceUsage on the group. A test that drives the group alone passes either
// way.
func TestRootCmd_SilenceUsageIsSetWhereARealInvocationReadsIt(t *testing.T) {
	// newRootCmd wires the real opener, so the failure has to land before it.
	// An unresolvable --db-url would dial whatever the host resolves to.
	t.Setenv("DATABASE_URL", "")
	t.Setenv(workerPathEnv, "")

	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{
		"tree", "load", "--tree-id", "t9", "--tree-type", "unilevel",
	})

	err := root.Execute()

	require.ErrorContains(t, err, "--db-url flag or DATABASE_URL env var is required")
	require.Contains(t, out.String(), "--db-url flag or DATABASE_URL env var is required")
	require.NotContains(t, out.String(), "Usage:")
}

// sigLoader raises one signal at this process and reports whether the context
// it was handed saw the cancellation.
type sigLoader struct {
	sig     syscall.Signal
	sawDone bool
}

func (l *sigLoader) LoadTree(ctx context.Context, _, _ string, _ ...networkengine.LoadTreeOption) error {
	if err := syscall.Kill(syscall.Getpid(), l.sig); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		l.sawDone = true
	case <-time.After(2 * time.Second):
	}
	return ctx.Err()
}

// One case per signal. A single case would leave the other registration
// unpinned, because dropping either one from the command leaves the other
// green.
//
// The keep-alive takes the signal off its default disposition for this
// process, so a missing registration fails the case instead of killing the
// test binary. Registrations multiplex, so it does not mask the one under
// test.
//
// This covers the load command only. Whether other commands keep their default
// disposition needs a subprocess and is not covered here.
func TestTreeLoadCmd_SignalsCancelTheLoad(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			keepAlive := make(chan os.Signal, 1)
			signal.Notify(keepAlive, sig)
			defer signal.Stop(keepAlive)

			loader := &sigLoader{sig: sig}
			cmd := newTreeCmdWith(
				func(context.Context, string, string) (*treeDeps, error) {
					return &treeDeps{release: func() error { return nil }}, nil
				},
				func(*treeDeps, string) (treeLoader, nodeCounter) { return loader, countingTo(0) },
			)
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetArgs([]string{
				"load", "--db-url", "postgres://x", "--worker", workerStub(t),
				"--tree-id", "t9", "--tree-type", "unilevel",
			})

			err := cmd.Execute()

			// Asserted before the error, so a missing registration reports the
			// signal rather than a bare nil.
			require.True(t, loader.sawDone, "%s must reach the loader's context", sig)
			require.Error(t, err)
		})
	}
}

// workerStub writes an executable file so resolveWorkerPath succeeds without a
// real worker. Nothing runs it: the opener is injected in these tests.
func workerStub(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "network-engine-worker")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755))
	return path
}
