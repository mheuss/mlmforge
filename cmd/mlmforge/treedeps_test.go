package main

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// recordingDeps counts release calls so a double release is distinguishable
// from a single one, and a refused release from one that never ran.
type recordingDeps struct {
	releases int
	err      error
}

func (r *recordingDeps) open(context.Context, string, string) (*treeDeps, error) {
	return &treeDeps{release: func() error {
		r.releases++
		return r.err
	}}, nil
}

func TestWithTreeDeps_ReleasesWhenTheOperationFails(t *testing.T) {
	rec := &recordingDeps{}
	boom := errors.New("operation failed")

	err := withTreeDeps(t.Context(), rec.open, "db", "worker",
		func(context.Context, *treeDeps) error { return boom })

	require.ErrorIs(t, err, boom)
	require.Equal(t, 1, rec.releases, "release was not called exactly once")
}

func TestWithTreeDeps_ReleasesWhenTheOperationSucceeds(t *testing.T) {
	rec := &recordingDeps{}

	err := withTreeDeps(t.Context(), rec.open, "db", "worker",
		func(context.Context, *treeDeps) error { return nil })

	require.NoError(t, err)
	require.Equal(t, 1, rec.releases)
}

func TestWithTreeDeps_ReleasesWhenTheOperationPanics(t *testing.T) {
	rec := &recordingDeps{}

	require.Panics(t, func() {
		_ = withTreeDeps(t.Context(), rec.open, "db", "worker",
			func(context.Context, *treeDeps) error { panic("worker wedged") })
	})

	require.Equal(t, 1, rec.releases, "release was not called exactly once")
}

func TestWithTreeDeps_ReportsAReleaseFailureWhenTheOperationSucceeded(t *testing.T) {
	stopErr := errors.New("stop failed")
	rec := &recordingDeps{err: stopErr}

	err := withTreeDeps(t.Context(), rec.open, "db", "worker",
		func(context.Context, *treeDeps) error { return nil })

	require.ErrorIs(t, err, stopErr)
}

func TestWithTreeDeps_ReportsBothWhenReleaseAlsoFails(t *testing.T) {
	boom := errors.New("operation failed")
	stopErr := errors.New("stop failed")
	rec := &recordingDeps{err: stopErr}

	err := withTreeDeps(t.Context(), rec.open, "db", "worker",
		func(context.Context, *treeDeps) error { return boom })

	require.ErrorIs(t, err, boom, "the operation failure must survive")
	require.ErrorIs(t, err, stopErr, "the release error is absent from the chain")
}

func TestWithTreeDeps_ReturnsTheOpenErrorAndRunsNothing(t *testing.T) {
	openErr := errors.New("open failed")
	ran := false

	err := withTreeDeps(t.Context(),
		func(context.Context, string, string) (*treeDeps, error) { return nil, openErr },
		"db", "worker",
		func(context.Context, *treeDeps) error { ran = true; return nil })

	require.ErrorIs(t, err, openErr)
	require.False(t, ran)
}

// countingCloser stands in for the pool. It counts rather than flags, so a
// second close is distinguishable from the one the code owes.
type countingCloser struct{ closes int }

func (c *countingCloser) Close() { c.closes++ }

func TestStartEngine_ClosesThePoolWhenTheWorkerWillNotStart(t *testing.T) {
	pool := &countingCloser{}
	missing := filepath.Join(t.TempDir(), "network-engine-worker")

	engine, err := startEngine(t.Context(), missing, pool)

	require.Error(t, err)
	require.Nil(t, engine)
	require.Equal(t, 1, pool.closes, "close was not called exactly once")
	require.ErrorIs(t, err, fs.ErrNotExist, "the cause must survive the wrapper")
	require.Contains(t, err.Error(), missing, "the message must name the worker it tried")
}

// pgxpool.New does not connect, so a syntactically valid URL reaches the
// engine start without a database. That is what makes this path testable
// without a container.
func TestOpenTreeDeps_ReportsAWorkerThatWillNotStart(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "network-engine-worker")

	deps, err := openTreeDeps(t.Context(), "postgres://user@localhost:1/db", missing)

	require.Error(t, err)
	require.Nil(t, deps)
	require.ErrorIs(t, err, fs.ErrNotExist, "the cause must survive the wrapper")
	require.Contains(t, err.Error(), missing, "the message must name the worker it tried")
}

func TestWithTreeDeps_PassesTheResolvedArgumentsToTheOpener(t *testing.T) {
	var gotURL, gotWorker string
	open := func(_ context.Context, dbURL, workerPath string) (*treeDeps, error) {
		gotURL, gotWorker = dbURL, workerPath
		return &treeDeps{release: func() error { return nil }}, nil
	}

	err := withTreeDeps(t.Context(), open, "postgres://x", "/bin/worker",
		func(context.Context, *treeDeps) error { return nil })

	require.NoError(t, err)
	require.Equal(t, "postgres://x", gotURL)
	require.Equal(t, "/bin/worker", gotWorker)
}
