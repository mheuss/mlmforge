package networkengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

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

// rejectionSentinels are the errors that count as evidence that an event
// cannot apply.
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
		if !sameUUID(failed.EventID, last.ID) {
			return false, nil
		}
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

// RejectRequest names the tree event to reject and why.
type RejectRequest struct {
	TreeID  string
	EventID string
	Reason  string
}

// RejectOutcome names what a Reject did.
type RejectOutcome string

const (
	// RejectOutcomeRejected appended a rejection.
	RejectOutcomeRejected RejectOutcome = "rejected"
	// RejectOutcomeResumed projected a rejection already in the stream.
	RejectOutcomeResumed RejectOutcome = "resumed"
	// RejectOutcomeAlreadyApplied found the rejection already projected.
	RejectOutcomeAlreadyApplied RejectOutcome = "already_applied"
)

// RejectResult describes one Reject.
type RejectResult struct {
	Stream          string
	Outcome         RejectOutcome
	RejectedEventID string                 // the event the rejection names
	RejectedVersion int64                  // that event's version
	RetryErr        error                  // what the retry returned, when it failed
	EventID         string                 // the rejection's ID, once appended or found
	Version         int64                  // the rejection's version
	ProjectionErr   error                  // any failure projecting the rejection
	Observed        *ProjectionObservation // the projected version, re-read after a projection error
	ReleaseErr      error                  // the unlock failed
}

// Reject appends a rejection of the stream's last event, or projects one
// already there, under the tree's lock.
func (w *TreeWriter) Reject(ctx context.Context, r RejectRequest) (result RejectResult, err error) {
	treeID, err := canonicalID("tree_id", r.TreeID)
	if err != nil {
		return result, err
	}
	eventID, err := canonicalID("event_id", r.EventID)
	if err != nil {
		return result, err
	}
	tree := treeID.String()
	stream := TreeStreamName(tree)
	result.Stream = stream
	reason := strings.TrimSpace(r.Reason)
	if reason == "" {
		return result, fmt.Errorf("reject event %s in tree %s: the reason is empty after trimming whitespace; nothing was appended",
			eventID, tree)
	}
	shape, found, err := w.readShape(ctx, stream)
	if err != nil {
		return result, err
	}
	if !found {
		return result, fmt.Errorf("reject event %s in tree %s: stream %s has no events", eventID, tree, stream)
	}

	unlock, err := w.lock(ctx, treeID)
	if err != nil {
		return result, err
	}
	defer func() {
		result.ReleaseErr = unlock()
	}()

	projected, projectedFound, err := w.store.ProjectedVersion(ctx, tree)
	if err != nil {
		return result, fmt.Errorf("read the projected version of tree %s; nothing was appended: %w", tree, err)
	}
	last, err := w.events.ReadLastEvent(ctx, stream)
	if err != nil {
		return result, fmt.Errorf("read the last event of stream %s; nothing was appended: %w", stream, err)
	}
	if last == nil {
		return result, fmt.Errorf("reject event %s in tree %s: a read of the last event of stream %s returned none", eventID, tree, stream)
	}
	if last.Type == EventTypeEventRejected {
		return w.resumeRejection(ctx, result, tree, eventID.String(), *last, projected, projectedFound)
	}
	if !sameUUID(last.ID, eventID.String()) {
		return result, fmt.Errorf("stream %s ends with event %s at version %d, not event %s; nothing was appended",
			stream, last.ID, last.Version, eventID)
	}
	if !rejectableEventTypes[last.Type] {
		return result, fmt.Errorf("stream %s ends with event %s at version %d of type %q, which reject-event does not reject; nothing was appended",
			stream, last.ID, last.Version, last.Type)
	}
	result.RejectedEventID, result.RejectedVersion = last.ID, last.Version

	retryErr := w.retry(ctx, tree, stream, shape, *last)
	if retryErr == nil {
		return result, fmt.Errorf("retrying event %s at version %d in stream %s returned no error; nothing was appended",
			last.ID, last.Version, stream)
	}
	result.RetryErr = retryErr
	evidence, err := w.rejectionEvidence(ctx, tree, shape, *last, retryErr)
	if err != nil {
		return result, fmt.Errorf("%w; nothing was appended", err)
	}
	if !evidence {
		return result, fmt.Errorf("retrying event %s at version %d in stream %s returned an error that is not one reject-event accepts as evidence; nothing was appended: %w",
			last.ID, last.Version, stream, retryErr)
	}

	event, err := newTreeEvent(EventTypeEventRejected, EventRejectedPayload{
		TreeID: tree, RejectedEventID: last.ID, RejectedVersion: last.Version, RejectedType: last.Type, Reason: reason,
	})
	if err != nil {
		return result, err
	}
	version, err := w.append(ctx, stream, last.Version, event)
	if err != nil {
		return result, err
	}
	result.Outcome, result.EventID, result.Version = RejectOutcomeRejected, event.ID, version
	result.ProjectionErr = w.project(ctx, stream, event.ID, version)
	if result.ProjectionErr != nil {
		result.Observed = w.observe(ctx, tree)
	}
	return result, nil
}

// retry loads the tree and redelivers expected, the stream's last event.
func (w *TreeWriter) retry(ctx context.Context, tree, stream string, shape treeShape, expected platform.Event) error {
	loaded, last, _, err := w.prepare(ctx, tree, shape)
	if err != nil {
		return err
	}
	if last == nil {
		return fmt.Errorf("a read of stream %s for the retry returned no last event, where event %s was expected", stream, expected.ID)
	}
	if !sameUUID(last.ID, expected.ID) {
		return fmt.Errorf("a read of stream %s for the retry returned last event %s, where event %s was expected", stream, last.ID, expected.ID)
	}
	_, err = w.catchUp(ctx, tree, stream, *last, loaded)
	return err
}

// resumeRejection handles a stream that already ends with a rejection. A
// missing projection row reads as projected version 0.
func (w *TreeWriter) resumeRejection(ctx context.Context, result RejectResult, tree, eventID string,
	last platform.Event, projected int64, projectedFound bool) (RejectResult, error) {
	stream := result.Stream
	p, err := rejectionPayload(last)
	if err != nil {
		return result, fmt.Errorf("%w; nothing was appended", err)
	}
	if !sameUUID(p.RejectedEventID, eventID) {
		return result, fmt.Errorf("stream %s ends with rejection %s of event %s, not event %s; nothing was appended",
			stream, last.ID, p.RejectedEventID, eventID)
	}
	result.RejectedEventID, result.RejectedVersion = p.RejectedEventID, p.RejectedVersion
	result.EventID, result.Version = last.ID, last.Version
	switch projected {
	case last.Version:
		result.Outcome = RejectOutcomeAlreadyApplied
		return result, nil
	case last.Version - 1, last.Version - 2:
		if err := w.checkRejectionTarget(ctx, stream, last); err != nil {
			return result, fmt.Errorf("%w; nothing was appended or projected", err)
		}
		result.Outcome = RejectOutcomeResumed
		result.ProjectionErr = w.project(ctx, stream, last.ID, last.Version)
		if result.ProjectionErr != nil {
			result.Observed = w.observe(ctx, tree)
		}
		return result, nil
	}
	if !projectedFound {
		return result, &ProjectionMissingError{TreeID: tree, LastVersion: last.Version}
	}
	return result, &StreamMovedError{TreeID: tree, LoadedVersion: projected, LastVersion: last.Version}
}
