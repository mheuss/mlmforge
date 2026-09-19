package main

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mlmforge/mlmforge/internal/networkengine"
	"github.com/stretchr/testify/require"
)

// stubLoader counts attempts and returns a fixed error.
type stubLoader struct {
	err      error
	attempts int
	onCall   func(attempt int)
}

func (s *stubLoader) LoadTree(context.Context, string, string, ...networkengine.LoadTreeOption) error {
	s.attempts++
	if s.onCall != nil {
		s.onCall(s.attempts)
	}
	return s.err
}

// safeToRetryErr satisfies the interface pgconn.SafeToRetry looks for.
type safeToRetryErr struct{ error }

func (safeToRetryErr) SafeToRetry() bool { return true }

// retryable is the one shape the allowlist admits.
func retryable() error {
	return &networkengine.TreeLoadRejectedError{
		Kind: networkengine.TreeLoadStoreReadFailed,
		Err:  safeToRetryErr{errors.New("connection refused")},
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
			name: "a connection failure the query never left is retryable",
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
				Err:   safeToRetryErr{errors.New("connection refused")},
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

	err := runTreeLoad(t.Context(), &bytes.Buffer{}, loader, "t", "unilevel", nil)

	require.Error(t, err)
	require.Equal(t, 1, loader.attempts)
}

func TestRunTreeLoad_DoesNotRetryAPermanentFailure(t *testing.T) {
	loader := &stubLoader{err: &networkengine.TreeLoadRejectedError{
		Kind: networkengine.TreeLoadDataInvalid,
	}}

	err := runTreeLoad(t.Context(), &bytes.Buffer{}, loader, "t", "unilevel", nil)

	require.Error(t, err)
	require.Equal(t, 1, loader.attempts)
}

func TestRunTreeLoad_BoundsRetriesOnARetryableFailure(t *testing.T) {
	loader := &stubLoader{err: retryable()}

	err := runTreeLoad(t.Context(), &bytes.Buffer{}, loader, "t", "unilevel", nil)

	require.Error(t, err)
	require.Equal(t, maxLoadAttempts, loader.attempts)
}

// A context cancelled between attempts stops the loop where it is, rather than
// sleeping out the backoff it was already told to abandon.
func TestRunTreeLoad_StopsWhenTheContextIsCancelledBetweenAttempts(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	loader := &stubLoader{err: retryable(), onCall: func(int) { cancel() }}

	err := runTreeLoad(ctx, &bytes.Buffer{}, loader, "t", "unilevel", nil)

	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, loader.attempts)
}

func TestRunTreeLoad_ReportsARejectionAsLeavingTheEngineUnchanged(t *testing.T) {
	loader := &stubLoader{err: &networkengine.TreeLoadRejectedError{
		Kind: networkengine.TreeLoadDataInvalid,
	}}
	var out bytes.Buffer

	_ = runTreeLoad(t.Context(), &out, loader, "t", "unilevel", nil)

	require.Equal(t,
		"load refused before any engine call (data_invalid); the engine is unchanged\n",
		out.String())
}

func TestRunTreeLoad_ReportsAnIncompleteLoadWithItsCounts(t *testing.T) {
	loader := &stubLoader{err: &networkengine.TreeLoadIncompleteError{
		Stage: networkengine.TreeLoadStageRoot,
		Total: 47,
	}}
	var out bytes.Buffer

	_ = runTreeLoad(t.Context(), &out, loader, "t", "unilevel", nil)

	require.Equal(t,
		"load stopped at the root stage; the engine acknowledged 0 of 47 placements\n",
		out.String())
}

func TestRunTreeLoad_ReportsAnUntypedFailure(t *testing.T) {
	loader := &stubLoader{err: errors.New("something else")}
	var out bytes.Buffer

	_ = runTreeLoad(t.Context(), &out, loader, "t", "unilevel", nil)

	require.Equal(t, "load failed: something else\n", out.String())
}

func TestRunTreeLoad_ReportsSuccess(t *testing.T) {
	loader := &stubLoader{}
	var out bytes.Buffer

	require.NoError(t, runTreeLoad(t.Context(), &out, loader, "tree-9", "unilevel", nil))
	require.Equal(t, 1, loader.attempts)
	require.Equal(t, "loaded tree tree-9\n", out.String())
}

// pgconn.SafeToRetry is the allowlist this policy rests on. If it stopped
// recognising the interface, every retryable case above would silently become
// non-retryable and still pass.
func TestSafeToRetryRecognisesTheInterface(t *testing.T) {
	require.True(t, pgconn.SafeToRetry(safeToRetryErr{errors.New("x")}))
	require.False(t, pgconn.SafeToRetry(errors.New("x")))
}
