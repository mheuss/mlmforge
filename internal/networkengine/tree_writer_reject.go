package networkengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/mlmforge/mlmforge/internal/platform"
)

// rejectionPayload decodes a rejection's payload.
func rejectionPayload(event platform.Event) (EventRejectedPayload, error) {
	var p EventRejectedPayload
	if err := json.Unmarshal(event.Payload, &p); err != nil {
		return EventRejectedPayload{}, fmt.Errorf("unmarshal event_rejected payload of event %s: %w", event.ID, err)
	}
	return p, nil
}

// pendingRejection returns an error when last is a rejection above the
// projected version and within two versions of it, and nil otherwise.
func pendingRejection(tree string, last *platform.Event, projected int64, loadErr error) error {
	if last == nil || last.Type != EventTypeEventRejected || last.Version <= projected || last.Version-2 > projected {
		return nil
	}
	p, err := rejectionPayload(*last)
	if err != nil {
		return errors.Join(fmt.Errorf("stream %s ends with rejection %s at version %d, above projected version %d; nothing was appended: %w",
			TreeStreamName(tree), last.ID, last.Version, projected, err), loadErr)
	}
	return &RejectionPendingError{
		TreeID: tree, RejectionID: last.ID, Version: last.Version,
		RejectedEventID: p.RejectedEventID, Projected: projected, LoadErr: loadErr,
	}
}

// checkRejectionTarget refuses a rejection that does not name the event
// directly before it in its stream.
func (w *TreeWriter) checkRejectionTarget(ctx context.Context, stream string, rejection platform.Event) error {
	p, err := rejectionPayload(rejection)
	if err != nil {
		return err
	}
	if rejection.Version < 2 {
		return fmt.Errorf("rejection %s is at version %d of stream %s, below 2", rejection.ID, rejection.Version, stream)
	}
	before := rejection.Version - 1
	prev, err := w.events.ReadStream(ctx, stream, before, 1)
	if err != nil {
		return fmt.Errorf("read version %d of stream %s, before rejection %s: %w", before, stream, rejection.ID, err)
	}
	if len(prev) == 0 {
		return fmt.Errorf("a read of version %d of stream %s, before rejection %s, returned no event",
			before, stream, rejection.ID)
	}
	if prev[0].Version != before {
		return fmt.Errorf("a read of version %d of stream %s, before rejection %s, returned event %s at version %d",
			before, stream, rejection.ID, prev[0].ID, prev[0].Version)
	}
	if p.RejectedVersion != before {
		return fmt.Errorf("rejection %s at version %d of stream %s names rejected version %d, not %d",
			rejection.ID, rejection.Version, stream, p.RejectedVersion, before)
	}
	if !sameUUID(prev[0].ID, p.RejectedEventID) || prev[0].Type != p.RejectedType {
		return fmt.Errorf("rejection %s at version %d of stream %s names event %s (%s), and version %d holds event %s (%s)",
			rejection.ID, rejection.Version, stream, p.RejectedEventID, p.RejectedType, before, prev[0].ID, prev[0].Type)
	}
	return nil
}

// rejectionRefusalCodes are the engine codes that count as evidence that an
// event cannot apply.
var rejectionRefusalCodes = map[string]bool{
	"POSITION_OCCUPIED":         true,
	"INVALID_POSITION":          true,
	"HAS_CHILDREN":              true,
	"CANNOT_REMOVE_ROOT":        true,
	"TREE_EMPTY":                true,
	"SPONSOR_NOT_FOUND":         true,
	"SUBTREE_FULL":              true,
	"SPONSORLESS_WITH_RECRUITS": true,
	"SPONSOR_CYCLE":             true,
}

// rejectionSentinels are the store and consumer refusals that count as
// evidence that an event cannot apply.
var rejectionSentinels = []error{
	ErrReplayedPlacement, ErrActiveUserConflict, ErrSlotConflict, ErrRootConflict, ErrUnprojectableEvent,
}

// rejectionEvidence reports whether a failed retry of last shows that last
// cannot apply.
func (w *TreeWriter) rejectionEvidence(ctx context.Context, tree string, shape treeShape, last platform.Event, retryErr error) (bool, error) {
	if errors.Is(retryErr, context.Canceled) || errors.Is(retryErr, context.DeadlineExceeded) {
		return false, nil
	}
	var failed *CatchUpFailedError
	if errors.As(retryErr, &failed) {
		for _, sentinel := range rejectionSentinels {
			if errors.Is(failed.Err, sentinel) {
				return true, nil
			}
		}
		var engineErr *EngineError
		return errors.As(failed.Err, &engineErr) && rejectionRefusalCodes[engineErr.Code], nil
	}
	var incomplete *TreeLoadIncompleteError
	if errors.As(retryErr, &incomplete) {
		return false, nil
	}
	var rejected *TreeLoadRejectedError
	if !errors.As(retryErr, &rejected) || rejected.Kind != TreeLoadDataInvalid {
		return false, nil
	}
	return w.stuckRowStopsTheLoad(ctx, tree, shape, last.ID)
}

// stuckRowStopsTheLoad reports whether the tree holds an active row carrying
// eventID, and its other active rows pass the load's preflight without it.
func (w *TreeWriter) stuckRowStopsTheLoad(ctx context.Context, tree string, shape treeShape, eventID string) (bool, error) {
	rows, err := w.store.GetByTreeDepthOrdered(ctx, tree)
	if err != nil {
		return false, fmt.Errorf("read the active rows of tree %s to preflight them without event %s's row: %w", tree, eventID, err)
	}
	remaining := make([]TreeNodeRow, 0, len(rows))
	held := false
	for _, row := range rows {
		if sameUUID(row.ID, eventID) {
			held = true
			continue
		}
		remaining = append(remaining, row)
	}
	if !held {
		return false, nil
	}
	return preflight(tree, shape.treeType, shape.config(), remaining) == nil, nil
}
