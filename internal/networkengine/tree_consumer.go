package networkengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/mlmforge/mlmforge/internal/platform"
)

// TreeEventConsumer projects tree events into the adjacency table and
// Rust engine. It is called synchronously by the placement service
// after appending the event to the EventStore.
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

// HandleEvent processes a single tree event.
func (c *TreeEventConsumer) HandleEvent(ctx context.Context, event platform.Event) error {
	switch event.Type {
	case EventTypeRootAdded:
		return c.handleRootAdded(ctx, event)
	case EventTypeNodePlaced:
		return c.handleNodePlaced(ctx, event)
	case EventTypeNodeRemoved:
		return c.handleNodeRemoved(ctx, event)
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

func (c *TreeEventConsumer) handleRootAdded(ctx context.Context, event platform.Event) error {
	var payload RootAddedPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("unmarshal root_added payload: %w", err)
	}

	if err := checkStream(event, "root_added", payload.TreeID, payload.UserID); err != nil {
		return err
	}

	node := TreeNodeRow{
		ID:         event.ID,
		TreeID:     payload.TreeID,
		UserID:     payload.UserID,
		SponsorID:  &payload.SponsorID,
		Depth:      0,
		EnrolledAt: payload.EnrolledAt,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}

	if err := c.store.InsertNode(ctx, node); err != nil {
		return fmt.Errorf("store root node: %w", err)
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
		// somewhere other than depth 0. Both leave the depth-0 row this call
		// inserted unsupported, and a second active depth-0 row makes
		// validateNodes refuse the whole tree at every later startup.
		held := fmt.Sprintf("get_position reported USER_NOT_FOUND for %s", payload.UserID)
		if perr == nil && pos != nil {
			held = fmt.Sprintf("get_position put %s at depth %d enrolled %d, against %d in this event",
				payload.UserID, pos.Depth, pos.EnrolledAt, payload.EnrolledAt.Unix())
		}
		// Not the caller's context. A cancellation between the inspection and
		// this delete would leave two active depth-0 rows, which is the state
		// this compensation exists to prevent, and no later run repairs it.
		if derr := c.store.DeleteNode(context.WithoutCancel(ctx), payload.TreeID, payload.UserID); derr != nil {
			return reconcileDiverged, fmt.Errorf(
				"engine refused add_root in tree %s, %s, and deleting the row this event inserted failed: %w",
				payload.TreeID, held, derr)
		}
		// Neither store reports rows affected, so the delete returning no
		// error is all that was observed here.
		return reconcileDiverged, fmt.Errorf(
			"engine refused add_root in tree %s, %s; the delete compensating this event's row returned no error",
			payload.TreeID, held)
	}

	return c.withRetry(ctx, "add_root", payload.TreeID, payload.UserID, func() error {
		return c.engine.AddRoot(ctx, payload.TreeID, payload.UserID, payload.EnrolledAt.Unix())
	}, reconcile)
}

func (c *TreeEventConsumer) handleNodePlaced(ctx context.Context, event platform.Event) error {
	var payload NodePlacedPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("unmarshal node_placed payload: %w", err)
	}

	// Reject malformed payloads before either projection. A node_placed that
	// cannot be applied faithfully must not land anywhere: a stored row the
	// engine never honored is the divergence this consumer exists to prevent.
	// Without these checks bad rows would be stored, and for the position
	// rules LoadTree's validation would then refuse the whole tree at the
	// next reload. (The unilevel rule is gate-only: the loader tolerates
	// legacy unilevel positions — HEU-563.)
	if err := checkStream(event, "node_placed", payload.TreeID, payload.UserID); err != nil {
		return err
	}
	if !supportedTreeTypes[payload.TreeType] {
		return fmt.Errorf("node_placed for %s in tree %s has unsupported tree_type %q",
			payload.UserID, payload.TreeID, payload.TreeType)
	}
	if payload.Position != nil && *payload.Position < 0 {
		return fmt.Errorf("node_placed for %s in tree %s has negative position %d",
			payload.UserID, payload.TreeID, *payload.Position)
	}
	switch payload.TreeType {
	case treeTypeMatrix:
		// The width bound needs the tree's configured width, which nothing
		// persists yet (HEU-554). The engine still enforces it at runtime.
		// The u8 ceiling needs no width: no matrix can have a slot above
		// math.MaxUint8, so anything larger is rejected here, mirroring the
		// worker's u8::try_from(position) wire boundary. (The loader's own
		// runtime bound is the width itself, which it gets via
		// WithMatrixParams.)
		if payload.Position == nil {
			return fmt.Errorf("matrix node_placed for %s in tree %s has no position; matrix events must carry explicit placement",
				payload.UserID, payload.TreeID)
		}
		if *payload.Position > math.MaxUint8 {
			return fmt.Errorf("matrix node_placed for %s in tree %s has position %d above the %d slot ceiling",
				payload.UserID, payload.TreeID, *payload.Position, math.MaxUint8)
		}
	case treeTypeBinary:
		if payload.Position == nil || *payload.Position > 1 {
			return fmt.Errorf("binary node_placed for %s in tree %s needs position 0 or 1",
				payload.UserID, payload.TreeID)
		}
	case treeTypeUnilevel:
		if payload.Position != nil {
			return fmt.Errorf("unilevel node_placed for %s in tree %s carries position %d; unilevel trees have no slots",
				payload.UserID, payload.TreeID, *payload.Position)
		}
	default:
		// Unreachable while supportedTreeTypes has three entries, but that
		// map's comment says to expect a fourth. Mirror validateNodes: a new
		// type must fail here loudly until its position rule is decided,
		// not fall through and admit whatever the event carries.
		return fmt.Errorf("node_placed for %s in tree %s has type %q with no position rule (add one to handleNodePlaced)",
			payload.UserID, payload.TreeID, payload.TreeType)
	}

	// Look up parent depth to derive child depth.
	parent, err := c.store.GetNode(ctx, payload.TreeID, payload.ParentID)
	if err != nil {
		return fmt.Errorf("get parent node: %w", err)
	}
	if parent == nil {
		return fmt.Errorf("parent node %s not found in tree %s", payload.ParentID, payload.TreeID)
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
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}

	if err := c.store.InsertNode(ctx, node); err != nil {
		return fmt.Errorf("store placed node: %w", err)
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

	// Matrix placements go through add_node_at so the engine applies exactly
	// the parent and position the event recorded. Matrix add_node would
	// re-derive placement by spillover and diverge from the row just written.
	// The Position deref is safe: the gate above rejects a nil matrix
	// position before anything is written.
	if payload.TreeType == treeTypeMatrix {
		return c.withRetry(ctx, "add_node_at", payload.TreeID, payload.UserID, func() error {
			return c.engine.AddNodeAt(ctx, payload.TreeID, payload.UserID,
				payload.ParentID, payload.SponsorID, *payload.Position, payload.EnrolledAt.Unix())
		}, reconcile)
	}
	return c.withRetry(ctx, "add_node", payload.TreeID, payload.UserID, func() error {
		var opts []AddNodeOption
		if payload.Position != nil {
			opts = append(opts, WithPosition(*payload.Position))
		}
		return c.engine.AddNode(ctx, payload.TreeID, payload.UserID,
			payload.ParentID, payload.SponsorID, payload.EnrolledAt.Unix(), opts...)
	}, reconcile)
}

func (c *TreeEventConsumer) handleNodeRemoved(ctx context.Context, event platform.Event) error {
	var payload NodeRemovedPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("unmarshal node_removed payload: %w", err)
	}

	if err := checkStream(event, "node_removed", payload.TreeID, payload.UserID); err != nil {
		return err
	}

	var moved []Responsored

	// withRetry returning nil cannot say whether this call removed the node or
	// found it already gone, and the two need different work afterwards. The
	// flag carries that out, the way moved already does.
	alreadyProjected := false

	if err := c.withRetry(ctx, "remove_node", payload.TreeID, payload.UserID, func() error {
		m, err := c.engine.RemoveNode(ctx, payload.TreeID, payload.UserID)
		moved = m
		return err
	}, func(ctx context.Context, err error) (reconcileOutcome, error) {
		if !isEngineCode(err, engineCodeUserNotFound) {
			return reconcileNotApplicable, nil
		}
		// This branch turns on one thing: whether an active row is still
		// there. The tombstone-aware read is what the design names, and it
		// leaves the removed row in reach if a later task needs to tell this
		// event's removal from someone else's.
		existing, gerr := c.store.GetNodeIncludingRemoved(ctx, payload.TreeID, payload.UserID)
		if gerr != nil {
			return reconcileInconclusive, gerr
		}
		if existing != nil && existing.RemovedAt == nil {
			// The engine applied the removal and no store write has landed.
			// This handler is engine-first, so that covers a redelivery and
			// equally a first delivery whose reply was lost on an earlier
			// attempt: either way RemoveNode's reply carried the only copy of
			// the moved list and nothing here can rebuild it. HEU-777 owns
			// the repair.
			return reconcileDiverged, &RemovalNotProjectedError{
				TreeID:  payload.TreeID,
				UserID:  payload.UserID,
				EventID: event.ID,
			}
		}
		if existing == nil {
			log.Printf("WARN tree consumer: node_removed for a user the store never held tree_id=%s user_id=%s event_id=%s",
				payload.TreeID, payload.UserID, event.ID)
		}
		alreadyProjected = true
		return reconcileConverged, nil
	}); err != nil {
		return err
	}

	// moved holds whatever the failed call returned, which is nothing. Writing
	// the store with it would soft-delete the node and re-sponsor no one.
	if alreadyProjected {
		return nil
	}

	if err := c.store.DeleteNodeAndResponsor(ctx, payload.TreeID, payload.UserID, moved); err != nil {
		return fmt.Errorf("remove node and re-sponsor recruits: %w", err)
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
// Two sources, because they answer different questions. The event is
// authoritative for what was asked: user, parent, position and enrolment. The
// stored row is authoritative for what is current: sponsor and depth.
//
// Sponsor comes from the row because it is the one compared field a later
// event can change. Removing a user re-sponsors everyone they recruited, and
// the event that placed the node still names the original sponsor. Comparing
// against the event would report divergence for a node the engine placed
// exactly as asked.
//
// Depth comes from the row because the event does not carry one. It is
// derived from the parent row when the placement is projected.
//
// False means "not confirmed equal", which covers a real disagreement and an
// identifier either side fails to parse. The caller treats both the same way,
// which is safe only because no unparseable identifier reaches here: the
// engine emits canonical uuids, and every payload identifier is rejected
// upstream by the worker or by the parent lookup before this is called.
func positionMatchesProjection(pos *EnginePosition, p NodePlacedPayload, stored *TreeNodeRow) bool {
	if pos == nil || stored == nil {
		return false
	}
	if !sameUUID(pos.UserID, p.UserID) {
		return false
	}
	if !samePtrUUID(pos.ParentUserID, &p.ParentID) {
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
	// EnginePosition.Position is an int and always carries a value. Unilevel
	// events must omit position, so comparing it would never match.
	if p.TreeType != treeTypeUnilevel {
		if p.Position == nil || pos.Position != *p.Position {
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
