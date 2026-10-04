package networkengine

import (
	"fmt"
	"time"
)

// appendConflictError reports an append refused on its expected version.
type appendConflictError struct {
	stream   string
	expected int64
	err      error
}

func (e *appendConflictError) Error() string {
	return fmt.Sprintf("append to stream %s at expected version %d was refused with a concurrency conflict",
		e.stream, e.expected)
}

func (e *appendConflictError) Unwrap() error { return e.err }

// CatchUpFailedError reports a redelivery of a stream's last event that
// returned an error.
type CatchUpFailedError struct {
	TreeID  string
	EventID string
	Version int64
	Type    string
	Err     error
}

func (e *CatchUpFailedError) Error() string {
	return fmt.Sprintf("redelivering event %s (%s) at version %d in stream %s returned: %v; nothing was appended",
		e.EventID, e.Type, e.Version, TreeStreamName(e.TreeID), e.Err)
}

func (e *CatchUpFailedError) Unwrap() error { return e.Err }

// AppendOutcomeUnknownError reports an append that returned an error, whose
// version could not be read to learn whether it landed.
type AppendOutcomeUnknownError struct {
	Stream    string
	Version   int64
	EventID   string
	AppendErr error
	ReadErr   error
}

func (e *AppendOutcomeUnknownError) Error() string {
	return fmt.Sprintf("append of event %s to stream %s at version %d returned: %v; "+
		"reading version %d to confirm it returned: %v; whether the event was appended is unknown",
		e.EventID, e.Stream, e.Version, e.AppendErr, e.Version, e.ReadErr)
}

func (e *AppendOutcomeUnknownError) Unwrap() []error { return []error{e.AppendErr, e.ReadErr} }

// TreeLockWaitError reports a tree lock the writer waited for and did not
// acquire.
type TreeLockWaitError struct {
	TreeID string
	Waited time.Duration
	Err    error
}

func (e *TreeLockWaitError) Error() string {
	return fmt.Sprintf("waited %s for the lock on tree %s and did not acquire it; the locker returned: %v",
		e.Waited, e.TreeID, e.Err)
}

func (e *TreeLockWaitError) Unwrap() error { return e.Err }

// ProjectionMissingError reports a tree with no projection row whose stream
// ends past version 1.
type ProjectionMissingError struct {
	TreeID      string
	LastVersion int64
}

func (e *ProjectionMissingError) Error() string {
	return fmt.Sprintf("tree %s has no projection row and stream %s ends at version %d; nothing was appended",
		e.TreeID, TreeStreamName(e.TreeID), e.LastVersion)
}

// StreamMovedError reports a stream whose last version is neither the tree's
// projected version nor one past it. The stream can be ahead of that version or
// behind it. LoadedVersion is the projected version read. NoLoad is set when no
// load followed that read.
type StreamMovedError struct {
	TreeID        string
	LoadedVersion int64
	LastVersion   int64
	NoLoad        bool
}

func (e *StreamMovedError) Error() string {
	if e.NoLoad {
		return fmt.Sprintf("tree %s has projected version %d, and stream %s ends at version %d; nothing was appended",
			e.TreeID, e.LoadedVersion, TreeStreamName(e.TreeID), e.LastVersion)
	}
	return fmt.Sprintf("tree %s had projected version %d before its load, and stream %s ends at version %d; nothing was appended",
		e.TreeID, e.LoadedVersion, TreeStreamName(e.TreeID), e.LastVersion)
}

// RejectionPendingError reports a stream that ends with a rejection the store
// has not applied.
type RejectionPendingError struct {
	TreeID          string
	RejectionID     string
	Version         int64
	RejectedEventID string
	Projected       int64
	Found           bool  // the tree has a projection row
	LoadErr         error // the load's error, when the load also failed
}

func (e *RejectionPendingError) Error() string {
	seen := fmt.Sprintf("has projected version %d", e.Projected)
	if !e.Found {
		seen = "has no projection row"
	}
	msg := fmt.Sprintf("stream %s ends with rejection %s at version %d of event %s, and tree %s %s; "+
		"nothing was appended. Run mlmforge tree reject-event --tree-id %s --event-id %s --reason <text> again to project it",
		TreeStreamName(e.TreeID), e.RejectionID, e.Version, e.RejectedEventID, e.TreeID, seen, e.TreeID, e.RejectedEventID)
	if e.LoadErr != nil {
		msg += ". The load before this check returned: " + e.LoadErr.Error()
	}
	return msg
}

func (e *RejectionPendingError) Unwrap() error { return e.LoadErr }
