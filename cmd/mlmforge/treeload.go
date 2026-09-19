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
const loadRetryDelay = 250 * time.Millisecond

// treeLoader is the surface runTreeLoad drives.
type treeLoader interface {
	LoadTree(ctx context.Context, treeID, treeType string, opts ...networkengine.LoadTreeOption) error
}

// treeLoadRetryable reports whether a load failure can succeed on a retry.
//
// The set is an allowlist. Only a store read that pgx reports as safe to retry
// qualifies. Every other cause is refused, so one nobody has enumerated
// defaults to stopping rather than to looping.
func treeLoadRetryable(err error) bool {
	if err == nil {
		return false
	}
	// Checked before the type switch, because either type can carry it.
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
		err = loader.LoadTree(ctx, treeID, treeType, opts...)
		if err == nil {
			fmt.Fprintf(out, "loaded tree %s\n", treeID)
			return nil
		}
		if !treeLoadRetryable(err) {
			break
		}
		if attempt < maxLoadAttempts {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(loadRetryDelay):
			}
		}
	}
	reportLoadFailure(out, err)
	return err
}

// reportLoadFailure states what the load left behind. The two typed errors are
// told apart with errors.As, never by matching on the message.
func reportLoadFailure(out io.Writer, err error) {
	var rejected *networkengine.TreeLoadRejectedError
	if errors.As(err, &rejected) {
		fmt.Fprintf(out, "load refused before any engine call (%s); the engine is unchanged\n", rejected.Kind)
		return
	}
	var incomplete *networkengine.TreeLoadIncompleteError
	if errors.As(err, &incomplete) {
		fmt.Fprintf(out, "load stopped at the %s stage; the engine acknowledged %d of %d placements\n",
			incomplete.Stage, incomplete.Confirmed, incomplete.Total)
		return
	}
	fmt.Fprintf(out, "load failed: %s\n", err)
}
