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
// projected version, and nil otherwise.
func pendingRejection(tree string, last *platform.Event, projected int64, loadErr error) error {
	if last == nil || last.Type != EventTypeEventRejected || last.Version <= projected {
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
	if len(prev) != 1 || prev[0].Version != before {
		return fmt.Errorf("a read of version %d of stream %s, before rejection %s, found no event at that version",
			before, stream, rejection.ID)
	}
	if !sameUUID(prev[0].ID, p.RejectedEventID) || prev[0].Type != p.RejectedType {
		return fmt.Errorf("rejection %s at version %d of stream %s names event %s (%s), and version %d holds event %s (%s)",
			rejection.ID, rejection.Version, stream, p.RejectedEventID, p.RejectedType, before, prev[0].ID, prev[0].Type)
	}
	return nil
}
