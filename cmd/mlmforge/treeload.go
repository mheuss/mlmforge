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
	LoadTree(ctx context.Context, treeID, treeType string, opts ...networkengine.LoadTreeOption) error
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

// nodeCounter reports how many active nodes a tree holds.
type nodeCounter func(ctx context.Context) (int, error)

// runTreeLoad replays one tree, retrying only what treeLoadRetryable allows.
//
// The size is read before the load so a success can say how much was replayed.
// Reading it first also means an unreadable store fails here rather than
// reporting a load whose size is unknown.
func runTreeLoad(ctx context.Context, out io.Writer, loader treeLoader, count nodeCounter, treeID, treeType string, opts []networkengine.LoadTreeOption) error {
	size, err := count(ctx)
	if err != nil {
		return err
	}
	for attempt := 1; attempt <= maxLoadAttempts; attempt++ {
		err = loader.LoadTree(ctx, treeID, treeType, opts...)
		if err == nil {
			_, _ = fmt.Fprintf(out, "loaded tree %s (%d nodes)\n", treeID, size)
			return nil
		}
		if !treeLoadRetryable(err) {
			break
		}
		if attempt < maxLoadAttempts {
			select {
			case <-ctx.Done():
				// The load error goes out too. A bare cancellation tells the
				// operator nothing about what was failing.
				err = errors.Join(err, ctx.Err())
				return newTreeLoadFailure(err)
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
		return fmt.Sprintf("load stopped at the %s stage; the engine acknowledged %d of %d placements",
			incomplete.Stage, incomplete.Confirmed, incomplete.Total)
	}
	var rejected *networkengine.TreeLoadRejectedError
	if errors.As(err, &rejected) {
		return fmt.Sprintf("load refused before any engine call (%s); the engine is unchanged", rejected.Kind)
	}
	return fmt.Sprintf("load failed: %s", err)
}
