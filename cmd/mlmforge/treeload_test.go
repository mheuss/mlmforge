package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mlmforge/mlmforge/internal/networkengine"
	"github.com/mlmforge/mlmforge/internal/platform"
	"github.com/stretchr/testify/require"
)

// stubLoader counts attempts and returns a fixed error.
type stubLoader struct {
	size     int
	err      error
	attempts int
	onCall   func(attempt int)
}

func (s *stubLoader) Load(context.Context, networkengine.LoadRequest) (networkengine.LoadResult, error) {
	s.attempts++
	if s.onCall != nil {
		s.onCall(s.attempts)
	}
	return networkengine.LoadResult{Nodes: s.size}, s.err
}

// unilevelLoad asks for tree as a unilevel tree.
func unilevelLoad(tree string) networkengine.LoadRequest {
	return networkengine.LoadRequest{TreeID: tree, TreeType: "unilevel"}
}

// safeToRetryErr satisfies the interface pgconn.SafeToRetry looks for.
type safeToRetryErr struct{ error }

func (safeToRetryErr) SafeToRetry() bool { return true }

// cancelledButSafeErr is safe to retry and also carries a cancellation.
type cancelledButSafeErr struct{}

func (cancelledButSafeErr) Error() string     { return "context already done" }
func (cancelledButSafeErr) SafeToRetry() bool { return true }
func (cancelledButSafeErr) Unwrap() error     { return context.Canceled }

// retryable is a shape the allowlist admits.
func retryable() error {
	return &networkengine.TreeLoadRejectedError{
		Kind: networkengine.TreeLoadStoreReadFailed,
		Err:  safeToRetryErr{errors.New("wrote no bytes")},
	}
}

func TestTreeLoadRetryable(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil is not a failure",
			err:  nil,
			want: false,
		},
		{
			name: "a write that put no bytes on the wire is retryable",
			err:  retryable(),
			want: true,
		},
		{
			name: "a cancelled context is not retryable",
			err: &networkengine.TreeLoadRejectedError{
				Kind: networkengine.TreeLoadStoreReadFailed,
				Err:  context.Canceled,
			},
			want: false,
		},
		{
			name: "a deadline is not retryable",
			err: &networkengine.TreeLoadRejectedError{
				Kind: networkengine.TreeLoadStoreReadFailed,
				Err:  context.DeadlineExceeded,
			},
			want: false,
		},
		{
			name: "a row that will not decode is not retryable",
			err: &networkengine.TreeLoadRejectedError{
				Kind: networkengine.TreeLoadStoreReadFailed,
				Err:  errors.New("scanning row: cannot scan text into *int"),
			},
			want: false,
		},
		{
			name: "invalid data is not retryable",
			err: &networkengine.TreeLoadRejectedError{
				Kind: networkengine.TreeLoadDataInvalid,
			},
			want: false,
		},
		{
			name: "a config error is not retryable",
			err: &networkengine.TreeLoadRejectedError{
				Kind: networkengine.TreeLoadConfigInvalid,
			},
			want: false,
		},
		{
			name: "a store read carrying no cause is not retryable",
			err: &networkengine.TreeLoadRejectedError{
				Kind: networkengine.TreeLoadStoreReadFailed,
			},
			want: false,
		},
		{
			name: "an incomplete load is never retryable",
			err: &networkengine.TreeLoadIncompleteError{
				Stage: networkengine.TreeLoadStageNodes,
				Err:   safeToRetryErr{errors.New("wrote no bytes")},
			},
			want: false,
		},
		{
			name: "a cancellation reaching the engine stage is not retryable",
			err: &networkengine.TreeLoadIncompleteError{
				Stage: networkengine.TreeLoadStageRoot,
				Err:   context.Canceled,
			},
			want: false,
		},
		{
			// The cancellation check has to run before the allowlist, because
			// this shape passes the allowlist.
			name: "a cancellation that is also safe to retry is not retryable",
			err: &networkengine.TreeLoadRejectedError{
				Kind: networkengine.TreeLoadStoreReadFailed,
				Err:  cancelledButSafeErr{},
			},
			want: false,
		},
		{
			// Retrying this is the outcome the whole policy exists to
			// prevent: the worker may hold a structure nothing can drop.
			name: "an incomplete load wrapping a retryable rejection is not retryable",
			err: &networkengine.TreeLoadIncompleteError{
				Stage: networkengine.TreeLoadStageNodes,
				Err:   retryable(),
			},
			want: false,
		},
		{
			name: "invalid data carrying a retryable cause is not retryable",
			err: &networkengine.TreeLoadRejectedError{
				Kind: networkengine.TreeLoadDataInvalid,
				Err:  safeToRetryErr{errors.New("wrote no bytes")},
			},
			want: false,
		},
		{
			name: "an untyped error is not retryable",
			err:  errors.New("something else"),
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, treeLoadRetryable(tt.err))
		})
	}
}

func TestRunTreeLoad_DoesNotRetryACancelledContext(t *testing.T) {
	loader := &stubLoader{err: &networkengine.TreeLoadRejectedError{
		Kind: networkengine.TreeLoadStoreReadFailed,
		Err:  context.Canceled,
	}}

	err := runTreeLoad(t.Context(), &bytes.Buffer{}, io.Discard, loader, unilevelLoad("t"))

	require.Error(t, err)
	require.Equal(t, 1, loader.attempts)
}

func TestRunTreeLoad_DoesNotRetryAPermanentFailure(t *testing.T) {
	loader := &stubLoader{err: &networkengine.TreeLoadRejectedError{
		Kind: networkengine.TreeLoadDataInvalid,
	}}

	err := runTreeLoad(t.Context(), &bytes.Buffer{}, io.Discard, loader, unilevelLoad("t"))

	require.Error(t, err)
	require.Equal(t, 1, loader.attempts)
}

// noRetryDelay drops the backoff so the suite does not sleep through it.
func noRetryDelay(t *testing.T) {
	t.Helper()
	original := loadRetryDelay
	loadRetryDelay = 0
	t.Cleanup(func() { loadRetryDelay = original })
}

func TestRunTreeLoad_BoundsRetriesOnARetryableFailure(t *testing.T) {
	noRetryDelay(t)
	loader := &stubLoader{err: retryable()}

	err := runTreeLoad(t.Context(), &bytes.Buffer{}, io.Discard, loader, unilevelLoad("t"))

	require.Error(t, err)
	require.Equal(t, maxLoadAttempts, loader.attempts)
}

// zeroDelayCeiling bounds what a run with the delay set to zero may take.
const zeroDelayCeiling = 100 * time.Millisecond

func TestRunTreeLoad_DoesNotSleepWhenTheDelayIsZero(t *testing.T) {
	noRetryDelay(t)
	loader := &stubLoader{err: retryable()}
	start := time.Now()

	_ = runTreeLoad(t.Context(), &bytes.Buffer{}, io.Discard, loader, unilevelLoad("t"))

	require.Equal(t, maxLoadAttempts, loader.attempts)
	require.Less(t, time.Since(start), zeroDelayCeiling,
		"a full retry run took longer than a zero delay should allow")
}

// A context cancelled between attempts stops the loop where it is, rather than
// sleeping out the backoff it was already told to abandon.
func TestRunTreeLoad_StopsWhenTheContextIsCancelledBetweenAttempts(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	loader := &stubLoader{err: retryable(), onCall: func(int) { cancel() }}

	var out bytes.Buffer
	err := runTreeLoad(ctx, &out, io.Discard, loader, unilevelLoad("t"))

	require.ErrorIs(t, err, context.Canceled, "the cancellation must reach the caller")
	require.ErrorIs(t, err, loader.err, "the load failure must not be dropped")
	require.Equal(t, 1, loader.attempts)
	require.ErrorContains(t, err, "load refused before any engine call (store_read_failed); the engine is unchanged: ")
	// errors.As pulls one member out of the join, so without a separate check
	// the rendered line reports a database fault and never says the run was
	// cut short.
	require.ErrorContains(t, err, "the run was cancelled")
	require.Empty(t, out.String())
}

func TestRunTreeLoad_ReportsARejectionAsLeavingTheEngineUnchanged(t *testing.T) {
	loader := &stubLoader{err: &networkengine.TreeLoadRejectedError{
		Kind: networkengine.TreeLoadDataInvalid,
	}}
	var out bytes.Buffer

	err := runTreeLoad(t.Context(), &out, io.Discard, loader, unilevelLoad("t"))

	require.ErrorContains(t, err, "load refused before any engine call (data_invalid); the engine is unchanged: ")
	require.Empty(t, out.String())
}

func TestRunTreeLoad_ReportsAnIncompleteLoadWithItsCounts(t *testing.T) {
	loader := &stubLoader{err: &networkengine.TreeLoadIncompleteError{
		Stage: networkengine.TreeLoadStageRoot,
		Total: 47,
	}}
	var out bytes.Buffer

	err := runTreeLoad(t.Context(), &out, io.Discard, loader, unilevelLoad("t"))

	require.ErrorContains(t, err, "load stopped at the root stage; the engine acknowledged 0 of 47 non-root placements: ")
	require.Empty(t, out.String())
}

func TestRunTreeLoad_ReportsAFailedCreateWithoutCounts(t *testing.T) {
	incomplete := &networkengine.TreeLoadIncompleteError{
		Stage: networkengine.TreeLoadStageCreate,
	}
	loader := &stubLoader{err: incomplete}
	var out bytes.Buffer

	err := runTreeLoad(t.Context(), &out, io.Discard, loader, unilevelLoad("t"))

	require.EqualError(t, err, "load stopped at the create stage; the create did not report success: "+incomplete.Error())
	require.NotContains(t, err.Error(), "placements")
	require.Empty(t, out.String())
}

// Reporting this as a rejection would understate what the engine may hold.
func TestRunTreeLoad_ReportsAChainHoldingBothAsIncomplete(t *testing.T) {
	loader := &stubLoader{err: &networkengine.TreeLoadIncompleteError{
		Stage: networkengine.TreeLoadStageNodes,
		Total: 4,
		Err:   retryable(),
	}}
	var out bytes.Buffer

	err := runTreeLoad(t.Context(), &out, io.Discard, loader, unilevelLoad("t"))

	require.ErrorContains(t, err, "load stopped at the nodes stage; the engine acknowledged 0 of 4 non-root placements: ")
	require.Empty(t, out.String())
}

func TestRunTreeLoad_ReportsAnUntypedFailure(t *testing.T) {
	loader := &stubLoader{err: errors.New("something else")}
	var out bytes.Buffer

	err := runTreeLoad(t.Context(), &out, io.Discard, loader, unilevelLoad("t"))

	require.EqualError(t, err, "load failed: something else")
	require.Empty(t, out.String())
}

func TestRunTreeLoad_ReportsSuccess(t *testing.T) {
	loader := &stubLoader{size: 3}
	var out bytes.Buffer

	require.NoError(t, runTreeLoad(t.Context(), &out, io.Discard, loader, unilevelLoad("tree-9")))
	require.Equal(t, 1, loader.attempts)
	require.Equal(t, "loaded tree tree-9 (3 nodes)\n", out.String())
}

// pgconn.SafeToRetry is the allowlist this policy rests on. If it stopped
// recognising the interface, every retryable case above would silently become
// non-retryable and still pass.
func TestSafeToRetryRecognisesTheInterface(t *testing.T) {
	require.True(t, pgconn.SafeToRetry(safeToRetryErr{errors.New("x")}))
	require.False(t, pgconn.SafeToRetry(errors.New("x")))
}

// Zero rows and a tree that is not in the database are the same read, so the
// command must not say it loaded one.
func TestRunTreeLoad_DoesNotClaimALoadForZeroRows(t *testing.T) {
	loader := &stubLoader{size: 0}
	var out bytes.Buffer

	require.NoError(t, runTreeLoad(t.Context(), &out, io.Discard, loader, unilevelLoad("tree-9")))
	require.Equal(t, "tree tree-9 holds no rows; nothing was loaded\n", out.String())
	require.NotContains(t, out.String(), "loaded tree")
}

// The cause is what tells two failures of the same kind apart. Two rejections
// sharing a Kind must not render identically.
//
// Both errors are built through the writer rather than by keyed literal. A
// literal with no msg renders the package fallback, which re-states the Kind
// the outer format already printed, so this case would pass without the fix.
func TestRunTreeLoad_KeepsTheCause(t *testing.T) {
	render := func(req networkengine.LoadRequest) string {
		w := networkengine.NewTreeWriter(platform.NewMemoryEventStore(), networkengine.NewMemoryTreeStore(),
			nil, networkengine.NewMemoryTreeLocker())
		var out bytes.Buffer
		err := runTreeLoad(t.Context(), &out, io.Discard, w, req)
		require.Error(t, err)
		return err.Error()
	}
	tree := testTreeID(50)
	width, spillover := 1, "breadth_first"

	badType := render(networkengine.LoadRequest{TreeID: tree, TreeType: "streamline"})
	badWidth := render(networkengine.LoadRequest{
		TreeID: tree, TreeType: "matrix", MatrixWidth: &width, MatrixSpillover: &spillover,
	})

	require.Contains(t, badType, "config_invalid")
	require.Contains(t, badWidth, "config_invalid")
	require.NotEqual(t, badType, badWidth, "two config_invalid failures must not read alike")
	require.Contains(t, badType, "streamline")
}

func TestRunTreeLoad_ReportsARedeliveryAheadOfTheLoadedLine(t *testing.T) {
	loader := &resultLoader{res: networkengine.LoadResult{
		CaughtUp:       &networkengine.CaughtUpEvent{EventID: "e2", Version: 2, Type: networkengine.EventTypeNodePlaced},
		ProjectedAfter: &networkengine.ProjectionObservation{Version: 2, Found: true}, Nodes: 2,
	}}
	var out bytes.Buffer

	require.NoError(t, runTreeLoad(t.Context(), &out, io.Discard, loader, unilevelLoad("tree-9")))
	require.Equal(t, "redelivered event e2 at version 2\nloaded tree tree-9 (2 nodes)\n", out.String())
}

func TestRunTreeLoad_ReportsARedeliveryThatLeftTheVersionBehind(t *testing.T) {
	loader := &resultLoader{res: networkengine.LoadResult{
		CaughtUp:       &networkengine.CaughtUpEvent{EventID: "e2", Version: 2, Type: networkengine.EventTypeNodeRemoved},
		ProjectedAfter: &networkengine.ProjectionObservation{Version: 1, Found: true}, Nodes: 1,
	}}
	var out bytes.Buffer

	require.NoError(t, runTreeLoad(t.Context(), &out, io.Discard, loader, unilevelLoad("tree-9")))
	require.Equal(t, "redelivered event e2 at version 2; the projected version is still 1\n"+
		"loaded tree tree-9 (1 nodes)\n", out.String())
}

func TestRunTreeLoad_ReportsARedeliveryThatLeftNoProjectionRow(t *testing.T) {
	loader := &resultLoader{res: networkengine.LoadResult{
		CaughtUp:       &networkengine.CaughtUpEvent{EventID: "e1", Version: 1, Type: networkengine.EventTypeRootAdded},
		ProjectedAfter: &networkengine.ProjectionObservation{}, Nodes: 1,
	}}
	var out bytes.Buffer

	require.NoError(t, runTreeLoad(t.Context(), &out, io.Discard, loader, unilevelLoad("tree-9")))
	require.Equal(t, "redelivered event e1 at version 1; the tree has no projection row\n"+
		"loaded tree tree-9 (1 nodes)\n", out.String())
}

func TestRunTreeLoad_ReportsARedeliveryAheadOfALaterFailure(t *testing.T) {
	noRetryDelay(t)
	loader := &resultLoader{
		res: networkengine.LoadResult{
			CaughtUp: &networkengine.CaughtUpEvent{EventID: "e2", Version: 2, Type: networkengine.EventTypeNodePlaced},
		},
		err: errors.New("read the active rows of tree t after redelivering version 2: connection reset"),
	}
	var out bytes.Buffer

	err := runTreeLoad(t.Context(), &out, io.Discard, loader, unilevelLoad("t"))

	require.EqualError(t, err, "load failed: read the active rows of tree t after redelivering version 2: connection reset")
	require.Equal(t, 1, exitCode(err))
	require.Equal(t, "redelivered event e2 at version 2\n", out.String())
	require.Equal(t, 1, loader.attempts)
}

func TestRunTreeLoad_WarnsOnAReleaseFailureAlongsideAnError(t *testing.T) {
	loader := &resultLoader{
		res: networkengine.LoadResult{
			CaughtUp:   &networkengine.CaughtUpEvent{EventID: "e2", Version: 2, Type: networkengine.EventTypeNodePlaced},
			ReleaseErr: errors.New("pg_advisory_unlock for tree t returned false"),
		},
		err: errors.New("read the active rows of tree t after redelivering version 2: connection reset"),
	}
	var out, warn bytes.Buffer

	err := runTreeLoad(t.Context(), &out, &warn, loader, unilevelLoad("t"))

	require.Equal(t, 1, exitCode(err))
	require.Equal(t, "redelivered event e2 at version 2\n", out.String())
	require.Equal(t, "warning: releasing the tree lock reported: pg_advisory_unlock for tree t returned false\n",
		warn.String())
}

func TestRunTreeLoad_WarnsWhenTheLockReleaseFails(t *testing.T) {
	loader := &resultLoader{res: networkengine.LoadResult{
		Nodes: 1, ReleaseErr: errors.New("pg_advisory_unlock for tree t returned false"),
	}}
	var out, warn bytes.Buffer

	require.NoError(t, runTreeLoad(t.Context(), &out, &warn, loader, unilevelLoad("t")))
	require.Equal(t, "loaded tree t (1 nodes)\n", out.String())
	require.Equal(t, "warning: releasing the tree lock reported: pg_advisory_unlock for tree t returned false\n",
		warn.String())
}

// resultLoader returns one fixed result and error, and counts attempts.
type resultLoader struct {
	res      networkengine.LoadResult
	err      error
	attempts int
}

func (r *resultLoader) Load(context.Context, networkengine.LoadRequest) (networkengine.LoadResult, error) {
	r.attempts++
	return r.res, r.err
}

func TestRunTreeLoad_ReportsAFailedRedeliveryWithoutALoadedLine(t *testing.T) {
	loader := &resultLoader{err: &networkengine.CatchUpFailedError{
		TreeID: "t", EventID: "e2", Version: 2, Type: networkengine.EventTypeNodePlaced,
		Err: errors.New("parent node p not found in tree t"),
	}}
	var out bytes.Buffer

	err := runTreeLoad(t.Context(), &out, io.Discard, loader, unilevelLoad("t"))

	var failed *networkengine.CatchUpFailedError
	require.ErrorAs(t, err, &failed)
	require.Equal(t, 1, exitCode(err))
	require.Empty(t, out.String())
	require.Equal(t, 1, loader.attempts)
}

// createOnlyEngine answers CreateTree. The embedded interface is nil, so any
// other call panics rather than returning a value a test could pass against.
type createOnlyEngine struct {
	networkengine.TreeEngineChecker
}

func (createOnlyEngine) CreateTree(context.Context, string, string) error { return nil }

// flakyVersionStore fails its first ProjectedVersion with a cause that is safe
// to retry.
type flakyVersionStore struct {
	networkengine.TreeStore
	reads int
}

func (s *flakyVersionStore) ProjectedVersion(ctx context.Context, treeID string) (int64, bool, error) {
	s.reads++
	if s.reads == 1 {
		return 0, false, safeToRetryErr{errors.New("wrote no bytes")}
	}
	return s.TreeStore.ProjectedVersion(ctx, treeID)
}

// flakyFirstReadEvents fails its first ReadStream with a cause that is safe to
// retry.
type flakyFirstReadEvents struct {
	*platform.MemoryEventStore
	reads int
}

func (e *flakyFirstReadEvents) ReadStream(ctx context.Context, stream string, from, limit int64) ([]platform.Event, error) {
	e.reads++
	if e.reads == 1 {
		return nil, safeToRetryErr{errors.New("wrote no bytes")}
	}
	return e.MemoryEventStore.ReadStream(ctx, stream, from, limit)
}

func TestRunTreeLoad_RetriesASafeProjectedVersionFailureOverTheWriter(t *testing.T) {
	noRetryDelay(t)
	store := &flakyVersionStore{TreeStore: networkengine.NewMemoryTreeStore()}
	w := networkengine.NewTreeWriter(platform.NewMemoryEventStore(), store, createOnlyEngine{},
		networkengine.NewMemoryTreeLocker())
	tree := testTreeID(51)
	var out bytes.Buffer

	require.NoError(t, runTreeLoad(t.Context(), &out, io.Discard, w, unilevelLoad(tree)))
	require.Equal(t, 2, store.reads, "the failed version read was not retried")
	require.Equal(t, "tree "+tree+" holds no rows; nothing was loaded\n", out.String())
}

func TestRunTreeLoad_RetriesASafeVersion1ReadFailureOverTheWriter(t *testing.T) {
	noRetryDelay(t)
	events := &flakyFirstReadEvents{MemoryEventStore: platform.NewMemoryEventStore()}
	w := networkengine.NewTreeWriter(events, networkengine.NewMemoryTreeStore(), createOnlyEngine{},
		networkengine.NewMemoryTreeLocker())
	tree := testTreeID(52)
	var out bytes.Buffer

	require.NoError(t, runTreeLoad(t.Context(), &out, io.Discard, w, unilevelLoad(tree)))
	require.Greater(t, events.reads, 1, "the failed version-1 read was not retried")
	require.Equal(t, "tree "+tree+" holds no rows; nothing was loaded\n", out.String())
}
