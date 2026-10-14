package networkengine

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mlmforge/mlmforge/internal/platform"
)

// replayPageSize is how many events one stream read asks for during replay.
const replayPageSize = 500

// replayResult is how far a replay got.
type replayResult struct {
	reached int64  // the last version applied or skipped
	root    string // the root the replay left in the engine, empty when none
}

// replayStream applies events from through to of tree's stream to engine. It
// skips an event when the next event in that range is a rejection naming it.
func replayStream(ctx context.Context, events platform.EventStore, engine TreeMutator,
	tree string, shape treeShape, from, to int64) (replayResult, error) {
	stream := TreeStreamName(tree)
	r := &replayer{engine: engine, tree: tree, shape: shape, to: to, res: replayResult{reached: from - 1}}
	next := from
	for next <= to {
		page, err := events.ReadStream(ctx, stream, next, min(int64(replayPageSize), to-next+1))
		if err != nil {
			return r.res, r.failRead(fmt.Errorf("read stream %s from version %d: %w", stream, next, err))
		}
		if len(page) == 0 {
			break
		}
		for _, ev := range page {
			if ev.Version > to {
				return r.res, r.invariant(fmt.Sprintf("events through version %d", to),
					fmt.Sprintf("event at version %d", ev.Version))
			}
			if ev.Version != next {
				return r.res, r.invariant(fmt.Sprintf("version %d", next), fmt.Sprintf("version %d", ev.Version))
			}
			if err := r.accept(ctx, ev); err != nil {
				return r.res, err
			}
			next++
		}
	}
	if next != to+1 {
		return r.res, r.invariant(fmt.Sprintf("events through version %d", to),
			fmt.Sprintf("events through version %d", next-1))
	}
	if err := r.flush(ctx); err != nil {
		return r.res, err
	}
	return r.res, nil
}

// replayer holds one event back until the next shows whether it was rejected.
type replayer struct {
	engine  TreeMutator
	tree    string
	shape   treeShape
	to      int64
	pending *platform.Event
	res     replayResult
}

// accept takes the next event in version order.
func (r *replayer) accept(ctx context.Context, ev platform.Event) error {
	if ev.Type == EventTypeEventRejected {
		if err := r.checkRejection(ev); err != nil {
			return err
		}
		r.pending = nil
		r.res.reached = ev.Version
		return nil
	}
	// The payload checks wait for flush. Run here, they would refuse a stream
	// whose bad event a later rejection skips.
	if !rejectableEventTypes[ev.Type] {
		return r.invariantAt(ev, "a tree event type", fmt.Sprintf("type %q", ev.Type))
	}
	if err := r.flush(ctx); err != nil {
		return err
	}
	held := ev
	r.pending = &held
	return nil
}

// flush checks the held event against the stream and makes its engine call.
func (r *replayer) flush(ctx context.Context) error {
	if r.pending == nil {
		return nil
	}
	ev := *r.pending
	r.pending = nil
	call, root, err := r.call(ev)
	if err != nil {
		return err
	}
	if _, err := call.apply(ctx, r.engine); err != nil {
		return r.failEvent(ev, fmt.Errorf("engine %s for %s: %w", call.op, call.userID, err))
	}
	switch {
	case ev.Type == EventTypeRootAdded:
		r.res.root = root
	case ev.Type == EventTypeNodeRemoved && sameUUID(call.userID, r.res.root):
		r.res.root = ""
	}
	r.res.reached = ev.Version
	return nil
}

// checkRejection refuses a rejection that does not name the held event.
func (r *replayer) checkRejection(ev platform.Event) error {
	var p EventRejectedPayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return r.failEvent(ev, fmt.Errorf("unmarshal event_rejected payload: %w", err))
	}
	if err := r.checkTree(ev, p.TreeID); err != nil {
		return err
	}
	if r.pending == nil {
		return r.invariantAt(ev, "a held event for the rejection to name",
			fmt.Sprintf("rejection at version %d names version %d; no event is held", ev.Version, p.RejectedVersion))
	}
	prev := *r.pending
	if p.RejectedVersion != prev.Version || !sameUUID(p.RejectedEventID, prev.ID) || p.RejectedType != prev.Type {
		return r.invariantAt(ev,
			fmt.Sprintf("event %s (%s) at version %d", prev.ID, prev.Type, prev.Version),
			fmt.Sprintf("event %s (%s) at version %d", p.RejectedEventID, p.RejectedType, p.RejectedVersion))
	}
	return nil
}

// call decodes ev, checks it against the stream, and returns its engine call
// and, for a root_added, the user it names.
func (r *replayer) call(ev platform.Event) (engineCall, string, error) {
	switch ev.Type {
	case EventTypeRootAdded:
		var p RootAddedPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			return engineCall{}, "", r.failEvent(ev, fmt.Errorf("unmarshal root_added payload: %w", err))
		}
		if err := r.checkTree(ev, p.TreeID); err != nil {
			return engineCall{}, "", err
		}
		return rootAddedCall(p), p.UserID, nil
	case EventTypeNodePlaced:
		var p NodePlacedPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			return engineCall{}, "", r.failEvent(ev, fmt.Errorf("unmarshal node_placed payload: %w", err))
		}
		if err := r.checkTree(ev, p.TreeID); err != nil {
			return engineCall{}, "", err
		}
		if p.TreeType != r.shape.treeType {
			return engineCall{}, "", r.invariantAt(ev, "tree type "+r.shape.treeType, "tree type "+p.TreeType)
		}
		if err := checkNodePlacedShape(p); err != nil {
			return engineCall{}, "", r.failEvent(ev, err)
		}
		return nodePlacedCall(p), "", nil
	case EventTypeNodeRemoved:
		var p NodeRemovedPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			return engineCall{}, "", r.failEvent(ev, fmt.Errorf("unmarshal node_removed payload: %w", err))
		}
		if err := r.checkTree(ev, p.TreeID); err != nil {
			return engineCall{}, "", err
		}
		call, err := nodeRemovedCall(p, r.shape.treeType)
		if err != nil {
			return engineCall{}, "", r.failEvent(ev, err)
		}
		return call, "", nil
	default:
		return engineCall{}, "", r.invariantAt(ev, "a tree event type", fmt.Sprintf("type %q", ev.Type))
	}
}

// checkTree refuses a payload that names a tree other than the one replayed.
func (r *replayer) checkTree(ev platform.Event, payloadTree string) error {
	if !sameUUID(payloadTree, r.tree) {
		return r.invariantAt(ev, "tree_id "+r.tree, "tree_id "+payloadTree)
	}
	return nil
}

func (r *replayer) failRead(err error) error {
	return &TreeReplayError{TreeID: r.tree, Stage: TreeReplayStageReplay,
		ReachedVersion: r.res.reached, ProjectedVersion: r.to, Err: err}
}

func (r *replayer) failEvent(ev platform.Event, err error) error {
	return &TreeReplayError{TreeID: r.tree, Stage: TreeReplayStageReplay, ReachedVersion: r.res.reached,
		ProjectedVersion: r.to, FailedVersion: ev.Version, FailedType: ev.Type, Err: err}
}

func (r *replayer) invariant(expected, observed string) error {
	return &TreeReplayError{TreeID: r.tree, Stage: TreeReplayStageReplay, ReachedVersion: r.res.reached,
		ProjectedVersion: r.to, Expected: expected, Observed: observed}
}

func (r *replayer) invariantAt(ev platform.Event, expected, observed string) error {
	return &TreeReplayError{TreeID: r.tree, Stage: TreeReplayStageReplay, ReachedVersion: r.res.reached,
		ProjectedVersion: r.to, FailedVersion: ev.Version, FailedType: ev.Type, Expected: expected, Observed: observed}
}
