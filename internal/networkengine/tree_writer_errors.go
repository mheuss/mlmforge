package networkengine

import "fmt"

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
