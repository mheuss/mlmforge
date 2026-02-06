package platform

import (
	"context"
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
