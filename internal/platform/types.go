package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// AuditEvent represents a single auditable action in the system.
type AuditEvent struct {
	ContextName string            // Which bounded context generated this event
	ActorID     string            // Who performed the action
	ActorType   string            // "user", "admin", or "system"
	Action      string            // What happened ("status_changed", "order_placed")
	EntityType  string            // What was acted upon ("user", "order", "commission")
	EntityID    string            // ID of the entity
	Detail      map[string]string // Additional context, free-form
	Timestamp   time.Time
}

// JobDefinition describes a background job to be scheduled.
type JobDefinition struct {
	ID          string                      // Unique job identifier
	Name        string                      // Human-readable name
	Schedule    string                      // Cron expression
	Handler     func(context.Context) error // The function to execute
	ContextName string                      // Owning bounded context
}

// Session represents an active user session.
type Session struct {
	ID        string
	UserID    string
	Zone      string // "admin" or "backoffice"
	CreatedAt time.Time
	ExpiresAt time.Time
}

// Event is the storage envelope for all domain events. The payload
// is a JSON-encoded domain event struct (OrderCompleted, etc.).
// The store assigns Version, GlobalPosition, and Timestamp on write.
type Event struct {
	ID             string          // Unique event ID (UUID)
	Stream         string          // Stream name, e.g. "order-abc123"
	Type           string          // Event type, e.g. "OrderCompleted"
	Version        int64           // Position within the stream (1-based)
	GlobalPosition int64           // Position across all streams
	Payload        json.RawMessage // JSON-encoded domain event
	Metadata       json.RawMessage // Optional context (actor, correlation ID)
	Timestamp      time.Time       // When the event occurred
}

// NewEvent is the input to EventStore.Append. The caller provides the
// event identity and payload. The store assigns Version, GlobalPosition,
// and Timestamp.
type NewEvent struct {
	ID       string          // Caller-provided UUID
	Type     string          // Event type name, e.g. "OrderCompleted"
	Payload  json.RawMessage // JSON-encoded domain event
	Metadata json.RawMessage // Optional, may be nil
}

// ConcurrencyError is returned by EventStore.Append when the stream's
// current version doesn't match the expected version.
type ConcurrencyError struct {
	Stream          string
	ExpectedVersion int64
	ActualVersion   int64
}

// Error returns a human-readable description of the version conflict.
func (e *ConcurrencyError) Error() string {
	return fmt.Sprintf(
		"concurrency conflict on stream %q: expected version %d, actual version %d",
		e.Stream, e.ExpectedVersion, e.ActualVersion,
	)
}
