package main

import (
	"bytes"
	"context"
	"errors"
	"io"
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

// recordingLoader captures the request the RunE body sent.
type recordingLoader struct {
	req networkengine.LoadRequest
}

func (r *recordingLoader) Load(_ context.Context, req networkengine.LoadRequest) (networkengine.LoadResult, error) {
	r.req = req
	return networkengine.LoadResult{}, nil
}

// Executing the command is the only thing that proves the flags reach the
// loader. Asserting on registered names leaves every RunE body untested.
func TestTreeLoadCmd_PassesItsFlagsToTheLoader(t *testing.T) {
	rec := &recordingLoader{}
	cmd := newTreeCmdWith(
		func(context.Context, string, string) (*treeDeps, error) {
			return &treeDeps{release: func() error { return nil }}, nil
		},
		func(*treeDeps) treeLoadWriter { return rec },
		nil,
	)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{
		"load", "--db-url", "postgres://x", "--worker", workerStub(t),
		"--tree-id", "t9", "--tree-type", "matrix",
		"--matrix-width", "3", "--matrix-spillover", "breadth_first",
	})

	require.NoError(t, cmd.Execute())
	width, spillover := 3, "breadth_first"
	require.Equal(t, networkengine.LoadRequest{
		TreeID: "t9", TreeType: "matrix", MatrixWidth: &width, MatrixSpillover: &spillover,
	}, rec.req)
}

// A matrix flag left off the command line must reach the writer as absent.
func TestTreeLoadCmd_LeavesMatrixParametersUnsetWhenAbsent(t *testing.T) {
	rec := &recordingLoader{}
	cmd := newTreeCmdWith(
		func(context.Context, string, string) (*treeDeps, error) {
			return &treeDeps{release: func() error { return nil }}, nil
		},
		func(*treeDeps) treeLoadWriter { return rec },
		nil,
	)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{
		"load", "--db-url", "postgres://x", "--worker", workerStub(t),
		"--tree-id", "t9", "--tree-type", "matrix",
	})

	require.NoError(t, cmd.Execute())
	require.Nil(t, rec.req.MatrixWidth)
	require.Nil(t, rec.req.MatrixSpillover)
}

func TestTreeLoadCmd_ReleasesTheDepsAfterRunning(t *testing.T) {
	released := false
	cmd := newTreeCmdWith(
		func(context.Context, string, string) (*treeDeps, error) {
			return &treeDeps{release: func() error { released = true; return nil }}, nil
		},
		func(*treeDeps) treeLoadWriter { return &recordingLoader{} },
		nil,
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
		func(*treeDeps) treeLoadWriter { return &recordingLoader{} },
		nil,
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

func (f *failingLoader) Load(context.Context, networkengine.LoadRequest) (networkengine.LoadResult, error) {
	return networkengine.LoadResult{}, f.err
}

// The failure reaches the operator exactly once, and brings no usage dump.
func TestTreeLoadCmd_ReportsAFailureOnceWithoutUsage(t *testing.T) {
	cmd := newTreeCmdWith(
		func(context.Context, string, string) (*treeDeps, error) {
			return &treeDeps{release: func() error { return nil }}, nil
		},
		func(*treeDeps) treeLoadWriter { return &failingLoader{err: errors.New("boom")} },
		nil,
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

func (l *sigLoader) Load(ctx context.Context, _ networkengine.LoadRequest) (networkengine.LoadResult, error) {
	if err := syscall.Kill(syscall.Getpid(), l.sig); err != nil {
		return networkengine.LoadResult{}, err
	}
	select {
	case <-ctx.Done():
		l.sawDone = true
	case <-time.After(2 * time.Second):
	}
	return networkengine.LoadResult{}, ctx.Err()
}

// One case per signal. A single case would leave the other registration
// unpinned.
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
				func(*treeDeps) treeLoadWriter { return loader },
				nil,
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

func TestRootCmd_TreeRejectsAnUnknownSubcommand(t *testing.T) {
	root := newRootCmd()
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"tree", "bogus"})

	err := root.Execute()

	require.Error(t, err, "a mistyped subcommand must not report success")
	require.Contains(t, err.Error(), "bogus", "the message must name the argument it refused")
}

func TestRootCmd_BareTreeStillPrintsHelpAndSucceeds(t *testing.T) {
	var out bytes.Buffer
	root := newRootCmd()
	root.SetOut(&out)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"tree"})

	require.NoError(t, root.Execute())
	require.Contains(t, out.String(), "Replay a stored tree into the engine",
		"help must still list the subcommands")
}

// workerStub writes an executable file for the resolver to find. Nothing runs
// it: the opener is injected in these tests.
func workerStub(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "network-engine-worker")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755))
	return path
}
