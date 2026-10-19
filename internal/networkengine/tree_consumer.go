package networkengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mlmforge/mlmforge/internal/platform"
)

// TreeEventConsumer projects tree events into the adjacency table and Rust
// engine.
type TreeEventConsumer struct {
	store      TreeStore
	engine     TreeEngine
	maxRetries int
	retryDelay time.Duration
}

// NewTreeEventConsumer creates a consumer that projects events into
// the given store and engine. Default retry count is 2.
func NewTreeEventConsumer(store TreeStore, engine TreeEngine) *TreeEventConsumer {
	return &TreeEventConsumer{
		store:      store,
		engine:     engine,
		maxRetries: 2,
		retryDelay: 50 * time.Millisecond,
	}
}

// HandleEvent processes a single tree event from a stream that records shape.
func (c *TreeEventConsumer) HandleEvent(ctx context.Context, shape treeShape, event platform.Event) error {
	switch event.Type {
	case EventTypeRootAdded:
		return c.handleRootAdded(ctx, event)
	case EventTypeNodePlaced:
		return c.handleNodePlaced(ctx, shape, event)
	case EventTypeNodeRemoved:
		return c.handleNodeRemoved(ctx, shape, event)
	case EventTypeEventRejected:
		return c.handleEventRejected(ctx, event)
	default:
		return nil
	}
}

// checkStream rejects an event that did not arrive on treeID's stream.
func checkStream(event platform.Event, eventName, treeID, userID string) error {
	if want := TreeStreamName(treeID); event.Stream != want {
		return fmt.Errorf("%s for %s in tree %s arrived on stream %q, want %q",
			eventName, userID, treeID, event.Stream, want)
	}
	return nil
}

// checkUserID rejects an event that names no user.
func checkUserID(eventName, treeID, userID string) error {
	if userID == "" {
		return fmt.Errorf("%s in tree %s has an empty user_id", eventName, treeID)
	}
	return nil
}

// checkReplayedInsert decides whether an add whose insert was skipped may
// continue to its engine call.
func (c *TreeEventConsumer) checkReplayedInsert(
	ctx context.Context,
	eventID, treeID, userID string,
	insertErr error,
) error {
	existing, gerr := c.store.GetNodeIncludingRemoved(ctx, treeID, userID)
	if gerr != nil {
		return fmt.Errorf("read existing row for %s in tree %s: %w (%w)", userID, treeID, gerr, insertErr)
	}
	// Only this event's own active row continues. Under anything else the
	// engine call would add a user this event holds no active row for.
	if existing != nil && existing.ID == eventID && existing.RemovedAt == nil {
		return nil
	}
	return fmt.Errorf("%w: the read for %s in tree %s returned %s (%w)",
		ErrReplayedPlacement, userID, treeID, describeExistingRow(existing), insertErr)
}

// describeExistingRow renders what a tombstone-aware read returned.
func describeExistingRow(row *TreeNodeRow) string {
	if row == nil {
		return "no row"
	}
	if row.RemovedAt == nil {
		return fmt.Sprintf("an active row carrying event %s", row.ID)
	}
	return fmt.Sprintf("a row carrying event %s, removed at %s",
		row.ID, row.RemovedAt.UTC().Format(time.RFC3339Nano))
}

func (c *TreeEventConsumer) handleRootAdded(ctx context.Context, event platform.Event) error {
	var payload RootAddedPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return unprojectable(fmt.Errorf("unmarshal root_added payload: %w", err))
	}

	if err := checkStream(event, "root_added", payload.TreeID, payload.UserID); err != nil {
		return unprojectable(err)
	}
	if err := checkUserID("root_added", payload.TreeID, payload.UserID); err != nil {
		return unprojectable(err)
	}

	node := TreeNodeRow{
		ID:         event.ID,
		TreeID:     payload.TreeID,
		UserID:     payload.UserID,
		SponsorID:  &payload.SponsorID,
		Depth:      0,
		EnrolledAt: payload.EnrolledAt,
	}

	inserted := true
	if err := c.store.ProjectInsert(ctx, node, event.Version); err != nil {
		if !errors.Is(err, ErrNodeAlreadyProjected) {
			return fmt.Errorf("store root node: %w", err)
		}
		if rerr := c.checkReplayedInsert(ctx, event.ID, payload.TreeID, payload.UserID, err); rerr != nil {
			return rerr
		}
		inserted = false
	}

	reconcile := func(ctx context.Context, err error) (reconcileOutcome, error) {
		if !isEngineCode(err, engineCodeUserAlreadyExists) &&
			!isEngineCode(err, engineCodeRootAlreadyExists) {
			return reconcileNotApplicable, nil
		}
		pos, perr := c.engine.GetPosition(ctx, payload.TreeID, payload.UserID)

		if perr == nil && pos != nil && pos.Depth == 0 &&
			sameUUID(pos.UserID, payload.UserID) &&
			pos.EnrolledAt == payload.EnrolledAt.Unix() {
			return reconcileConverged, nil
		}

		// USER_NOT_FOUND is an answer, not a failed inspection: the engine
		// does not hold this user, so the root is someone else. Every other
		// inspection failure learned nothing, and deleting on one would
		// destroy a root over a timeout.
		if perr != nil && !isEngineCode(perr, engineCodeUserNotFound) {
			return reconcileInconclusive, perr
		}

		// Either the engine does not hold this user, or it holds them
		// somewhere other than depth 0. Both leave the depth-0 row
		// unsupported.
		held := fmt.Sprintf("get_position reported USER_NOT_FOUND for %s", payload.UserID)
		if perr == nil && pos != nil {
			held = fmt.Sprintf("get_position put %s at depth %d enrolled %d, against %d in this event",
				payload.UserID, pos.Depth, pos.EnrolledAt, payload.EnrolledAt.Unix())
		}
		// The delete below undoes this call's own insert.
		if !inserted {
			return reconcileDiverged, fmt.Errorf(
				"engine refused add_root in tree %s, %s; this delivery's insert was skipped, so the stored row was left alone",
				payload.TreeID, held)
		}
		// Not the caller's context. A cancellation between the inspection and
		// this delete would leave two active depth-0 rows, which is the state
		// this compensation exists to prevent, and no later run repairs it.
		writeCtx, cancelWrite := detachedWrite(ctx)
		defer cancelWrite()
		if derr := c.store.UndoRootProjection(writeCtx, payload.TreeID, payload.UserID, event.Version); derr != nil {
			return reconcileDiverged, fmt.Errorf(
				"engine refused add_root in tree %s, %s, and deleting the row this event inserted failed: %w",
				payload.TreeID, held, derr)
		}
		// A nil error is all this line observed. It does not say a row was
		// deleted.
		return reconcileDiverged, fmt.Errorf(
			"engine refused add_root in tree %s, %s; the delete compensating this event's row returned no error",
			payload.TreeID, held)
	}

	call := rootAddedCall(payload)
	err := c.withRetry(ctx, call.op, payload.TreeID, payload.UserID, func() error {
		_, err := call.apply(ctx, c.engine)
		return err
	}, reconcile)
	if err == nil || !inserted {
		return err
	}
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}

	// The caller's context is cancelled, so this read needs one of its own.
	readCtx, cancelRead := detachedRead(ctx)
	defer cancelRead()
	row, rerr := c.store.GetNodeIncludingRemoved(readCtx, payload.TreeID, payload.UserID)
	if rerr != nil {
		return fmt.Errorf("%w; reading back the row for %s in tree %s failed: %v",
			err, payload.UserID, payload.TreeID, rerr)
	}
	// Not "the row this delivery inserted is": the read is keyed on tree and
	// user and prefers any active row, so both ids are named and the reader
	// compares them.
	return fmt.Errorf("%w; this delivery inserted event %s, and reading %s in tree %s returned %s",
		err, event.ID, payload.UserID, payload.TreeID, describeExistingRow(row))
}

func (c *TreeEventConsumer) handleNodePlaced(ctx context.Context, shape treeShape, event platform.Event) error {
	var payload NodePlacedPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return unprojectable(fmt.Errorf("unmarshal node_placed payload: %w", err))
	}

	// Reject malformed payloads before either projection. A node_placed that
	// cannot be applied faithfully must not land anywhere: a stored row the
	// engine never honored is the divergence this consumer exists to prevent.
	if err := checkStream(event, "node_placed", payload.TreeID, payload.UserID); err != nil {
		return unprojectable(err)
	}
	if err := checkUserID("node_placed", payload.TreeID, payload.UserID); err != nil {
		return unprojectable(err)
	}
	if err := checkNodePlacedOnStream(payload, shape.treeType); err != nil {
		return unprojectable(err)
	}

	// Look up parent depth to derive child depth.
	parent, err := c.store.GetNode(ctx, payload.TreeID, payload.ParentID)
	if err != nil {
		return fmt.Errorf("get parent node: %w", err)
	}
	if parent == nil {
		return unprojectable(fmt.Errorf("parent node %s not found in tree %s", payload.ParentID, payload.TreeID))
	}
	depth := parent.Depth + 1

	node := TreeNodeRow{
		ID:         event.ID,
		TreeID:     payload.TreeID,
		UserID:     payload.UserID,
		ParentID:   &payload.ParentID,
		SponsorID:  &payload.SponsorID,
		Position:   payload.Position,
		Depth:      depth,
		EnrolledAt: payload.EnrolledAt,
	}

	if err := c.store.ProjectInsert(ctx, node, event.Version); err != nil {
		if !errors.Is(err, ErrNodeAlreadyProjected) {
			return fmt.Errorf("store placed node: %w", err)
		}
		if rerr := c.checkReplayedInsert(ctx, event.ID, payload.TreeID, payload.UserID, err); rerr != nil {
			return rerr
		}
	}

	// One closure for both engine calls below. USER_ALREADY_EXISTS after a
	// store insert that reported the row already present is the lost-reply
	// case: the mutation landed and the reply did not.
	reconcile := func(ctx context.Context, err error) (reconcileOutcome, error) {
		if !isEngineCode(err, engineCodeUserAlreadyExists) {
			return reconcileNotApplicable, nil
		}
		// The row is read rather than reused from the insert above, because a
		// redelivery leaves the original row in place and a later removal may
		// have re-sponsored it since.
		stored, serr := c.store.GetNode(ctx, payload.TreeID, payload.UserID)
		if serr != nil {
			return reconcileInconclusive, fmt.Errorf("read the projected row: %w", serr)
		}
		// No active row to compare against, so report that rather than a
		// position mismatch.
		if stored == nil {
			return reconcileDiverged, fmt.Errorf(
				"engine holds %s in tree %s and the store has no active row for them; event %s",
				payload.UserID, payload.TreeID, event.ID)
		}
		pos, perr := c.engine.GetPosition(ctx, payload.TreeID, payload.UserID)
		if perr != nil {
			return reconcileInconclusive, perr
		}
		if !positionMatchesProjection(pos, payload, stored) {
			return reconcileDiverged, fmt.Errorf(
				"engine holds %s in tree %s at a position the projected row does not match; event %s",
				payload.UserID, payload.TreeID, event.ID)
		}
		return reconcileConverged, nil
	}

	call := nodePlacedCall(payload)
	return c.withRetry(ctx, call.op, payload.TreeID, payload.UserID, func() error {
		_, err := call.apply(ctx, c.engine)
		return err
	}, reconcile)
}

func (c *TreeEventConsumer) handleNodeRemoved(ctx context.Context, shape treeShape, event platform.Event) error {
	var payload NodeRemovedPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return unprojectable(fmt.Errorf("unmarshal node_removed payload: %w", err))
	}

	if err := checkStream(event, "node_removed", payload.TreeID, payload.UserID); err != nil {
		return unprojectable(err)
	}
	if err := checkNodeRemovedOnStream(payload, shape.treeType); err != nil {
		return unprojectable(err)
	}
	call, callErr := nodeRemovedCall(payload, shape.treeType)
	if callErr != nil {
		return unprojectable(callErr)
	}

	var outcome engineOutcome

	// withRetry returning nil cannot say whether this call removed the node or
	// found it already gone, and the two need different work afterwards. The
	// flag carries that out, the way outcome already does.
	alreadyProjected := false

	if err := c.withRetry(ctx, call.op, payload.TreeID, payload.UserID, func() error {
		out, err := call.apply(ctx, c.engine)
		outcome = out
		return err
	}, func(ctx context.Context, err error) (reconcileOutcome, error) {
		if !isEngineCode(err, engineCodeUserNotFound) {
			return reconcileNotApplicable, nil
		}
		// The stamp read answers whether an earlier delivery of this event
		// already projected. The tree-and-user read below it separates an active
		// row, a tombstone this event stamped, and any other tombstone or no row.
		stamped, serr := c.store.GetNodeByRemovalEvent(ctx, payload.TreeID, event.ID)
		if serr != nil {
			return reconcileInconclusive, fmt.Errorf("read the row this removal stamped: %w", serr)
		}
		if stamped != nil {
			alreadyProjected = true
			return reconcileConverged, nil
		}
		existing, gerr := c.store.GetNodeIncludingRemoved(ctx, payload.TreeID, payload.UserID)
		if gerr != nil {
			return reconcileInconclusive, fmt.Errorf("read the user's row: %w", gerr)
		}
		if existing != nil && existing.RemovedAt == nil {
			// The engine applied the removal and no store write has landed.
			// Converging here would skip the store write for that removal.
			return reconcileDiverged, &RemovalNotProjectedError{
				TreeID:  payload.TreeID,
				UserID:  payload.UserID,
				EventID: event.ID,
			}
		}
		// The user read runs after the stamp read, so it can see a stamp another
		// writer committed in between. When it does, trust the later read.
		if existing != nil && existing.RemovedByEventID != nil && sameUUID(*existing.RemovedByEventID, event.ID) {
			alreadyProjected = true
			return reconcileConverged, nil
		}
		stamp := ""
		if existing != nil {
			stamp = ", with no removal stamp"
			if existing.RemovedByEventID != nil {
				stamp = ", stamped by removal event " + *existing.RemovedByEventID
			}
		}
		return reconcileDiverged, unprojectable(fmt.Errorf(
			"engine %s for %s in tree %s returned %s; no row carries removal event %s, and the read for that user returned %s%s",
			call.op, payload.UserID, payload.TreeID, engineCodeUserNotFound, event.ID, describeExistingRow(existing), stamp))
	}); err != nil {
		return err
	}

	// A converged reconcile leaves no store write to make.
	if alreadyProjected {
		return nil
	}

	// The outcome cannot be rebuilt here, so giving up is not a clean abort.
	writeCtx, cancelWrite := detachedWrite(ctx)
	defer cancelWrite()
	projection := RemovalProjection{Users: outcome.removed, Responsored: outcome.responsored, Relocated: outcome.relocated}
	if err := c.store.ProjectRemoval(writeCtx, payload.TreeID, projection, event.ID, event.Version); err != nil {
		return fmt.Errorf("remove node and re-sponsor recruits: %w", err)
	}
	return nil
}

// handleEventRejected applies an operator's rejection of the event before it.
func (c *TreeEventConsumer) handleEventRejected(ctx context.Context, event platform.Event) error {
	var p EventRejectedPayload
	if err := json.Unmarshal(event.Payload, &p); err != nil {
		return fmt.Errorf("unmarshal event_rejected payload: %w", err)
	}
	for _, field := range []struct{ name, value string }{
		{"tree_id", p.TreeID},
		{"rejected_event_id", p.RejectedEventID},
		{"rejected_type", p.RejectedType},
		{"reason", p.Reason},
	} {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("event_rejected %s has an empty %s", event.ID, field.name)
		}
	}
	if want := TreeStreamName(p.TreeID); event.Stream != want {
		return fmt.Errorf("event_rejected %s for tree %s arrived on stream %q, want %q", event.ID, p.TreeID, event.Stream, want)
	}
	if event.Version < 2 {
		return fmt.Errorf("event_rejected %s is at version %d, below 2", event.ID, event.Version)
	}
	if p.RejectedVersion != event.Version-1 {
		return fmt.Errorf("event_rejected %s at version %d names rejected version %d, not %d",
			event.ID, event.Version, p.RejectedVersion, event.Version-1)
	}
	if !rejectableEventTypes[p.RejectedType] {
		return fmt.Errorf("event_rejected %s names rejected type %q, which is not a rejectable tree event type",
			event.ID, p.RejectedType)
	}
	rejected, err := uuid.Parse(p.RejectedEventID)
	if err != nil {
		return fmt.Errorf("event_rejected %s names rejected_event_id %q, which is not a UUID: %w", event.ID, p.RejectedEventID, err)
	}
	if err := c.store.ProjectRejection(ctx, p.TreeID, rejected.String(), event.Version); err != nil {
		return fmt.Errorf("project event_rejected %s: %w", event.ID, err)
	}
	return nil
}

// Worker error codes. The Rust worker maps its TreeError variants to these
// strings, so they are a contract with it rather than values chosen here.
const (
	engineCodeUserAlreadyExists = "USER_ALREADY_EXISTS"
	engineCodeRootAlreadyExists = "ROOT_ALREADY_EXISTS"
	engineCodeUserNotFound      = "USER_NOT_FOUND"
)

// isEngineCode reports whether err carries the given worker error code.
//
// errors.As rather than a type assertion, so a caller does not depend on how
// deeply the transport's error is wrapped.
func isEngineCode(err error, code string) bool {
	var e *EngineError
	return errors.As(err, &e) && e.Code == code
}

// positionMatchesProjection reports whether the engine's view of a placed node
// agrees with what was projected for it.
//
// False means "not confirmed equal", which covers a real disagreement and an
// identifier either side fails to parse.
func positionMatchesProjection(pos *EnginePosition, p NodePlacedPayload, stored *TreeNodeRow) bool {
	if pos == nil || stored == nil {
		return false
	}
	if !sameUUID(pos.UserID, p.UserID) {
		return false
	}
	if !samePtrUUID(pos.ParentUserID, stored.ParentID) {
		return false
	}
	if !samePtrUUID(pos.SponsorUserID, stored.SponsorID) {
		return false
	}
	if int(pos.Depth) != stored.Depth {
		return false
	}
	if pos.EnrolledAt != p.EnrolledAt.Unix() {
		return false
	}
	// A unilevel row has no position to compare.
	if p.TreeType != treeTypeUnilevel {
		if stored.Position == nil || pos.Position != *stored.Position {
			return false
		}
	}
	return true
}

// sameUUID compares two identifiers by value rather than by spelling, so case
// and any other valid textual variation do not read as a disagreement. An
// identifier that does not parse cannot be confirmed equal to anything.
func sameUUID(a, b string) bool {
	ua, err := uuid.Parse(a)
	if err != nil {
		return false
	}
	ub, err := uuid.Parse(b)
	if err != nil {
		return false
	}
	return ua == ub
}

func samePtrUUID(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return sameUUID(*a, *b)
}

// storeWriteTimeout bounds a store write detached from the caller's context.
// WithoutCancel strips the deadline along with the cancellation, so without
// this the write has no bound at all.
const storeWriteTimeout = 5 * time.Second

// detachedWrite returns a context a store write can finish on after the caller
// has given up, still bounded so it cannot hang.
//
// Two reasons to detach. The write is the second half of an operation whose
// first half already committed, so abandoning it leaves a divergence no later
// run repairs. And a cancelled context makes pgx destroy the connection
// instead of returning it to the pool, so cancellation load leaks capacity.
func detachedWrite(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), storeWriteTimeout)
}

// detachedRead returns a context a read can finish on after the caller has
// given up, still bounded so it cannot hang.
func detachedRead(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), storeWriteTimeout)
}

// reconcileOutcome is what an inspection concluded about a mutation the
// engine refused.
type reconcileOutcome int

const (
	// reconcileNotApplicable: the error is not one reconcile can speak to.
	reconcileNotApplicable reconcileOutcome = iota
	// reconcileConverged: the engine already holds what this event asked for.
	reconcileConverged
	// reconcileDiverged: the engine holds something else. Not retryable.
	reconcileDiverged
	// reconcileInconclusive: the inspection itself failed, so nothing was
	// learned. Kept apart from diverged so a timed-out query is retried
	// rather than reported as the engine disagreeing.
	reconcileInconclusive
)

// withRetry executes fn with retries. Respects context cancellation between
// attempts. Logs at ERROR level when all retries are exhausted (interim
// notification path until HEU-296).
//
// reconcile may be nil. When set, it is consulted on each failed attempt and
// decides whether the failure is really a failure. It is not consulted once
// the context is done.
//
// A converged outcome returns nil without fn having succeeded, so anything
// the closure was meant to assign is still unset. A non-nil error alongside
// converged or notApplicable is logged and discarded; only diverged and
// inconclusive read it.
func (c *TreeEventConsumer) withRetry(
	ctx context.Context, op, treeID, userID string,
	fn func() error,
	reconcile func(ctx context.Context, err error) (reconcileOutcome, error),
) error {
	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if err := fn(); err != nil {
			lastErr = err

			// Inspecting through a cancelled context cannot answer, and the
			// failure would be logged as an inspection fault on every clean
			// shutdown. The select below reports the cancellation instead.
			if reconcile != nil && ctx.Err() == nil {
				outcome, rerr := reconcile(ctx, err)
				switch outcome {
				case reconcileConverged:
					if rerr != nil {
						log.Printf("INFO tree consumer: reconcile converged with a non-nil error, discarding it op=%s tree_id=%s user_id=%s err=%v",
							op, treeID, userID, rerr)
					}
					return nil
				case reconcileNotApplicable:
					if rerr != nil {
						log.Printf("INFO tree consumer: reconcile not applicable with a non-nil error, discarding it op=%s tree_id=%s user_id=%s err=%v",
							op, treeID, userID, rerr)
					}
				case reconcileDiverged:
					// A caller returning diverged with no error would
					// otherwise report the one outcome that means the store
					// and the engine disagree as success. The engine error is
					// the only concrete evidence there is, so it is carried.
					if rerr == nil {
						rerr = fmt.Errorf(
							"engine %s: reconcile reported divergence and returned no error, tree_id=%s user_id=%s, engine error: %w",
							op, treeID, userID, err)
					}
					return rerr
				case reconcileInconclusive:
					log.Printf("ERROR tree consumer: reconcile inspection failed op=%s tree_id=%s user_id=%s err=%v",
						op, treeID, userID, rerr)
				default:
					log.Printf("ERROR tree consumer: reconcile returned an unrecognised outcome %d, retrying op=%s tree_id=%s user_id=%s",
						outcome, op, treeID, userID)
				}
			}

			if attempt < c.maxRetries {
				select {
				case <-ctx.Done():
					return fmt.Errorf("engine %s cancelled during retry: %w", op, ctx.Err())
				case <-time.After(c.retryDelay):
				}
			}
			continue
		}
		return nil
	}

	log.Printf("ERROR tree consumer: engine %s failed after %d retries tree_id=%s user_id=%s err=%v",
		op, c.maxRetries, treeID, userID, lastErr)
	return fmt.Errorf("engine %s failed after %d retries: %w", op, c.maxRetries, lastErr)
}
