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
