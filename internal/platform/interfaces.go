package platform

import "context"

// ConfigStore provides read-only access to system configuration.
// Each context reads its own config namespace. Config is loaded at startup
// from YAML/environment and is not mutated at runtime. Domain-specific
// business rules belong in the owning context's config, not here.
type ConfigStore interface {
	// Get retrieves a config value by key. Returns error if not found.
	Get(ctx context.Context, key string) (string, error)

	// GetWithDefault retrieves a config value, returning the default if not found.
	GetWithDefault(ctx context.Context, key string, defaultVal string) string

	// GetAll retrieves all config values matching a key prefix.
	GetAll(ctx context.Context, prefix string) (map[string]string, error)
}

// AuditWriter records structured audit events. Write-only append.
// Every context writes its own audit events through this interface.
// Operations reads the audit store for reporting via a separate query path.
type AuditWriter interface {
	// Write records a single audit event. Never fails silently.
	Write(ctx context.Context, event AuditEvent) error
}

// JobScheduler manages background job registration and execution.
// Contexts register jobs at startup. The scheduler calls handlers
// at the scheduled time. Jobs are the migration path for legacy
// cron bots (ADR, batch migration Phase 1).
type JobScheduler interface {
	// Register adds a job to the scheduler. Duplicate IDs are rejected.
	Register(ctx context.Context, job JobDefinition) error

	// Cancel removes a scheduled job.
	Cancel(ctx context.Context, jobID string) error
}

// SessionManager handles session lifecycle. Stubbed — full design
// deferred until Portals tier when AuthProvider is also designed.
type SessionManager interface {
	Create(ctx context.Context, userID string, zone string) (Session, error)
	Validate(ctx context.Context, sessionID string) (Session, error)
	Destroy(ctx context.Context, sessionID string) error
}

// EventStore persists and retrieves domain events. Append-only.
// Serves two purposes: event sourcing for the Network Engine (commission
// state rebuilt from event replay) and domain event persistence for all
// bounded contexts (cross-context communication via ADR-010).
//
// The default implementation uses PostgreSQL with JSONB payloads (ADR-005).
// Enterprise deployments can swap in EventStoreDB or Kafka without code
// changes — this interface is the abstraction boundary.
type EventStore interface {
	// Append writes one or more events to a stream atomically.
	// The expectedVersion parameter enforces optimistic concurrency:
	// if the stream's current version doesn't match, Append returns
	// a *ConcurrencyError.
	//
	// Use expectedVersion=0 to assert the stream is new.
	// Use expectedVersion=-1 to skip the version check (append unconditionally).
	Append(ctx context.Context, stream string, expectedVersion int64, events []NewEvent) error

	// ReadStream returns events from a single stream, ordered by version,
	// starting at fromVersion (inclusive). Returns an empty slice if the
	// stream doesn't exist or has no events at or after fromVersion.
	// Pass limit=0 to read all matching events.
	ReadStream(ctx context.Context, stream string, fromVersion int64, limit int64) ([]Event, error)

	// ReadCategory returns events across all streams whose category matches
	// the given prefix. Category is the part of the stream name before the
	// first hyphen (e.g., category "order" matches streams "order-abc",
	// "order-def"). Returns events in global insertion order.
	//
	// Use afterPosition=0 to read from the beginning.
	// Pass limit=0 to read all matching events.
	ReadCategory(ctx context.Context, category string, afterPosition int64, limit int64) ([]Event, error)
}
