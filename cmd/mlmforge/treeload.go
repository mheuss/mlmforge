package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mlmforge/mlmforge/internal/networkengine"
)

// maxLoadAttempts bounds the retry loop.
const maxLoadAttempts = 3

// loadRetryDelay is the pause between attempts.
var loadRetryDelay = 250 * time.Millisecond

// treeLoader is the surface runTreeLoad drives.
type treeLoader interface {
	LoadTree(ctx context.Context, treeID, treeType string, opts ...networkengine.LoadTreeOption) (int, error)
}

// treeLoadRetryable reports whether a load failure can succeed on a retry.
func treeLoadRetryable(err error) bool {
	if err == nil {
		return false
	}
	// Checked first, because either error type can carry it.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var incomplete *networkengine.TreeLoadIncompleteError
	if errors.As(err, &incomplete) {
		return false
	}
	var rejected *networkengine.TreeLoadRejectedError
	if !errors.As(err, &rejected) {
		return false
	}
	if rejected.Kind != networkengine.TreeLoadStoreReadFailed {
		return false
	}
	return pgconn.SafeToRetry(rejected.Err)
}

// runTreeLoad replays one tree, retrying only what treeLoadRetryable allows.
func runTreeLoad(ctx context.Context, out io.Writer, loader treeLoader, treeID, treeType string, opts []networkengine.LoadTreeOption) error {
	var err error
	for attempt := 1; attempt <= maxLoadAttempts; attempt++ {
		var size int
		size, err = loader.LoadTree(ctx, treeID, treeType, opts...)
		if err == nil {
			// Zero rows and a tree that is not in the database are the same
			// read, so this must not claim a load that did not happen.
			if size == 0 {
				_, _ = fmt.Fprintf(out, "tree %s holds no rows; nothing was loaded\n", treeID)
				return nil
			}
			_, _ = fmt.Fprintf(out, "loaded tree %s (%d nodes)\n", treeID, size)
			return nil
		}
		if !treeLoadRetryable(err) {
			break
		}
		if attempt < maxLoadAttempts {
			// Checked before the select. Once the backoff timer is also
			// ready a select picks at random, so losing that draw would start
			// another attempt on a context already cancelled.
			if ctxErr := ctx.Err(); ctxErr != nil {
				// The load error goes out too. A bare cancellation tells the
				// operator nothing about what was failing.
				return newTreeLoadFailure(errors.Join(err, ctxErr))
			}
			select {
			case <-ctx.Done():
				return newTreeLoadFailure(errors.Join(err, ctx.Err()))
			case <-time.After(loadRetryDelay):
			}
		}
	}
	return newTreeLoadFailure(err)
}

// treeLoadFailure carries the operator message while keeping the cause
// reachable through errors.Is and errors.As.
type treeLoadFailure struct {
	msg string
	err error
}

func newTreeLoadFailure(err error) *treeLoadFailure {
	return &treeLoadFailure{msg: treeLoadFailureMessage(err), err: err}
}

func (e *treeLoadFailure) Error() string { return e.msg }
func (e *treeLoadFailure) Unwrap() error { return e.err }

// treeLoadFailureMessage states what the load left behind. The two typed errors
// are told apart with errors.As, never by matching on the message.
func treeLoadFailureMessage(err error) string {
	// Incomplete is checked first, matching treeLoadRetryable. A chain holding
	// both must not be reported as leaving the engine unchanged.
	var incomplete *networkengine.TreeLoadIncompleteError
	if errors.As(err, &incomplete) {
		return fmt.Sprintf("load stopped at the %s stage; the engine acknowledged %d of %d placements: %s%s",
			incomplete.Stage, incomplete.Confirmed, incomplete.Total, incomplete, interrupted(err))
	}
	var rejected *networkengine.TreeLoadRejectedError
	if errors.As(err, &rejected) {
		return fmt.Sprintf("load refused before any engine call (%s); the engine is unchanged: %s%s",
			rejected.Kind, rejected, interrupted(err))
	}
	return fmt.Sprintf("load failed: %s", err)
}

// interrupted names a cancellation that errors.As would otherwise drop.
func interrupted(err error) string {
	// errors.As pulls one member out of a join, so a cancellation joined
	// beside a typed error is invisible to the branches above.
	switch {
	case errors.Is(err, context.Canceled):
		return " (the run was cancelled)"
	case errors.Is(err, context.DeadlineExceeded):
		return " (the run exceeded its deadline)"
	default:
		return ""
	}
}
